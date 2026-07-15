package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

func decodeJSON(data json.RawMessage, v any) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// AttachOptions configures one interactive attach session.
type AttachOptions struct {
	// EscapePrefix is the local command-mode trigger byte, initially
	// Ctrl-] (0x1d).
	EscapePrefix byte
	// PollInterval bounds how often Attach polls output.read for new
	// target output. The wire protocol is request/response only, so
	// this polling loop is how the attach terminal approximates a live
	// view; it never blocks keystroke forwarding.
	PollInterval time.Duration
}

// DefaultAttachOptions returns Ctrl-] with a responsive poll interval.
func DefaultAttachOptions() AttachOptions {
	return AttachOptions{EscapePrefix: 0x1d, PollInterval: 30 * time.Millisecond}
}

// Attach runs the interactive terminal loop against an already-dialed
// attach connection: raw bytes from in are forwarded to the target
// except for a local escape prefix, which enters command mode for one
// line read from in and echoed to out. It returns when in reaches EOF
// or a "detach"/"end" command is run. Callers are responsible for
// putting the terminal underlying in/out into raw mode and restoring
// it on every exit path, including signals; Attach itself does not
// touch terminal mode.
func Attach(client *Client, in io.Reader, out io.Writer, opts AttachOptions) error {
	reader := bufio.NewReader(in)
	filter := NewEscapeFilter(opts.EscapePrefix)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		pollOutput(client, out, opts.PollInterval, stop)
	}()
	defer func() {
		close(stop)
		<-done
	}()

	for {
		b, err := reader.ReadByte()
		if err != nil {
			return err
		}

		res := filter.Feed(b)
		if res.ShouldForward {
			if werr := client.TransportWrite([]byte{res.Forward}); werr != nil {
				fmt.Fprintf(out, "\r\n[gateway] %v\r\n", werr)
			}
			continue
		}
		if !res.EnterCommandMode {
			continue // a lone, not-yet-resolved escape prefix byte
		}

		line, rerr := readCommandLine(reader, res.CommandModeFirstByte, out)
		if rerr != nil {
			return rerr
		}
		end, cerr := runCommandLine(client, line, out)
		if cerr != nil {
			fmt.Fprintf(out, "\r\n[gateway] %v\r\n", cerr)
		}
		if end {
			return nil
		}
	}
}

// readCommandLine echoes firstByte plus the rest of the line typed
// locally (never forwarded to the target) until Enter, and returns the
// full line.
func readCommandLine(reader *bufio.Reader, firstByte byte, out io.Writer) (string, error) {
	fmt.Fprint(out, "\r\ngateway> ")
	var line []byte
	if firstByte != '\r' && firstByte != '\n' {
		line = append(line, firstByte)
		out.Write([]byte{firstByte})
	}
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\r' || b == '\n' {
			fmt.Fprint(out, "\r\n")
			return string(line), nil
		}
		line = append(line, b)
		out.Write([]byte{b})
	}
}

// runCommandLine executes one parsed local command against client. It
// reports whether the attach loop should end: true for both "detach"
// (the daemon session keeps running) and "end" (which also asks the
// daemon to disconnect first).
func runCommandLine(client *Client, line string, out io.Writer) (endLoop bool, err error) {
	cmd, perr := ParseCommand(line)
	if perr != nil {
		return false, nil // a blank line is a no-op, not an error
	}

	switch cmd.Name {
	case "approve":
		if len(cmd.Args) != 1 {
			return false, fmt.Errorf("usage: approve <id>")
		}
		_, err := client.CommandApprove(cmd.Args[0])
		return false, err
	case "reject":
		if len(cmd.Args) != 1 {
			return false, fmt.Errorf("usage: reject <id>")
		}
		_, err := client.CommandReject(cmd.Args[0])
		return false, err
	case "edit":
		if len(cmd.Args) < 2 {
			return false, fmt.Errorf("usage: edit <id> <new command text>")
		}
		_, err := client.CommandEdit(cmd.Args[0], strings.Join(cmd.Args[1:], " "), "")
		return false, err
	case "secret":
		return false, client.SecretBegin()
	case "secret-done":
		return false, client.SecretDone()
	case "retry uart":
		_, err := client.RetryUART()
		return false, err
	case "takeover":
		_, err := client.Takeover()
		return false, err
	case "detach":
		return true, nil
	case "end":
		_, err := client.SessionEnd()
		return true, err
	default:
		return false, fmt.Errorf("unknown command %q", cmd.Name)
	}
}

// pollOutput repeatedly polls output.read and writes newly arrived
// target bytes to out, until stop is closed.
func pollOutput(client *Client, out io.Writer, interval time.Duration, stop <-chan struct{}) {
	var after uint64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			result, err := client.OutputRead(after, 64*1024)
			if err != nil {
				continue
			}
			var decoded struct {
				Next uint64 `json:"next"`
				Data []byte `json:"data"`
			}
			if jerr := decodeJSON(result, &decoded); jerr != nil {
				continue
			}
			if len(decoded.Data) > 0 {
				out.Write(decoded.Data)
			}
			after = decoded.Next
		}
	}
}
