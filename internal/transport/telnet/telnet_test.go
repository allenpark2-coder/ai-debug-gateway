package telnet

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
)

// filterCase feeds input through a fresh Stream's filter and checks
// the kept data and the refusals sent back.
func runFilter(t *testing.T, chunks [][]byte) (data, refusals []byte) {
	t.Helper()
	s := &Stream{}
	for _, chunk := range chunks {
		buf := append([]byte(nil), chunk...)
		kept, ref := s.filter(buf)
		data = append(data, buf[:kept]...)
		refusals = append(refusals, ref...)
	}
	return data, refusals
}

func TestFilterPlainDataPassesThrough(t *testing.T) {
	data, refusals := runFilter(t, [][]byte{[]byte("login: ")})
	if string(data) != "login: " {
		t.Fatalf("got %q", data)
	}
	if len(refusals) != 0 {
		t.Fatalf("unexpected refusals % x", refusals)
	}
}

func TestFilterEscapedIACYieldsSingleDataByte(t *testing.T) {
	data, _ := runFilter(t, [][]byte{{'a', cmdIAC, cmdIAC, 'b'}})
	if !bytes.Equal(data, []byte{'a', 0xFF, 'b'}) {
		t.Fatalf("got % x", data)
	}
}

func TestFilterRefusesEveryOffer(t *testing.T) {
	// Server demands ECHO (DO 1) and offers SGA (WILL 3): the client
	// must refuse both and keep none of it as data.
	data, refusals := runFilter(t, [][]byte{{cmdIAC, cmdDO, 1, cmdIAC, cmdWILL, 3}})
	if len(data) != 0 {
		t.Fatalf("negotiation leaked into data: % x", data)
	}
	want := []byte{cmdIAC, cmdWONT, 1, cmdIAC, cmdDONT, 3}
	if !bytes.Equal(refusals, want) {
		t.Fatalf("got % x, want % x", refusals, want)
	}
}

func TestFilterIgnoresServerRefusals(t *testing.T) {
	_, refusals := runFilter(t, [][]byte{{cmdIAC, cmdWONT, 1, cmdIAC, cmdDONT, 3}})
	if len(refusals) != 0 {
		t.Fatalf("a WONT/DONT must not be answered, got % x", refusals)
	}
}

func TestFilterSkipsSubnegotiation(t *testing.T) {
	in := []byte{'x'}
	in = append(in, cmdIAC, cmdSB, 24, 1, cmdIAC, cmdIAC, 0, cmdIAC, cmdSE)
	in = append(in, 'y')
	data, _ := runFilter(t, [][]byte{in})
	if string(data) != "xy" {
		t.Fatalf("got %q", data)
	}
}

func TestFilterSurvivesSequencesSplitAcrossReads(t *testing.T) {
	// The same stream, one byte per read: IAC DO 1 then data.
	var chunks [][]byte
	for _, b := range []byte{cmdIAC, cmdDO, 1, 'o', 'k'} {
		chunks = append(chunks, []byte{b})
	}
	data, refusals := runFilter(t, chunks)
	if string(data) != "ok" {
		t.Fatalf("got %q", data)
	}
	if !bytes.Equal(refusals, []byte{cmdIAC, cmdWONT, 1}) {
		t.Fatalf("got refusals % x", refusals)
	}
}

func TestWriteEscapesIAC(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	s := newStream(client, "test:23")
	defer s.Close()

	go s.Write([]byte{'a', 0xFF, 'b'})

	buf := make([]byte, 8)
	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := server.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], []byte{'a', cmdIAC, cmdIAC, 'b'}) {
		t.Fatalf("got % x", buf[:n])
	}
}

func TestOpenNegotiatesAndDeliversLoginPrompt(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverGot := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// A BusyBox-style opening: demand ECHO, then the login prompt.
		conn.Write([]byte{cmdIAC, cmdDO, 1})
		conn.Write([]byte("board login: "))
		// The refusal and the login answer may arrive in separate
		// segments; keep reading until both are here or the deadline
		// hits.
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var raw []byte
		for !bytes.Contains(raw, []byte("root\n")) {
			buf := make([]byte, 64)
			n, err := conn.Read(buf)
			raw = append(raw, buf[:n]...)
			if err != nil {
				break
			}
		}
		serverGot <- raw
	}()

	addr := ln.Addr().(*net.TCPAddr)
	s, err := Open(context.Background(), &profile.TelnetConfig{Host: "127.0.0.1", Port: addr.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.Kind() != "telnet" {
		t.Fatalf("kind %q", s.Kind())
	}
	if id := s.Identity(); id.Kind != "telnet-host" || !id.Known() {
		t.Fatalf("identity %+v", id)
	}

	var got []byte
	for !bytes.Contains(got, []byte("login: ")) {
		buf := make([]byte, 64)
		n, err := s.Read(buf)
		if err != nil {
			t.Fatalf("read: %v (so far %q)", err, got)
		}
		got = append(got, buf[:n]...)
	}
	if bytes.Contains(got, []byte{cmdIAC}) {
		t.Fatalf("protocol bytes leaked into data: % x", got)
	}

	if _, err := s.Write([]byte("root\n")); err != nil {
		t.Fatal(err)
	}

	raw := <-serverGot
	// The server sees our WONT 1 refusal followed by the login answer.
	if !bytes.Contains(raw, []byte{cmdIAC, cmdWONT, 1}) {
		t.Fatalf("server never saw the ECHO refusal: % x", raw)
	}
	if !bytes.Contains(raw, []byte("root\n")) {
		t.Fatalf("server never saw the login answer: % x", raw)
	}
}

func TestOpenDefaultsToPort23(t *testing.T) {
	// Dial an address that must fail fast either way; the point is
	// only that a zero port resolves to :23, which we can observe in
	// the error text.
	_, err := Open(context.Background(), &profile.TelnetConfig{Host: "127.0.0.1", Port: 0})
	if err == nil {
		t.Skip("a local telnetd is actually listening on :23")
	}
	if !bytes.Contains([]byte(err.Error()), []byte(":23")) {
		t.Fatalf("expected the default port in the dial error, got %v", err)
	}
}
