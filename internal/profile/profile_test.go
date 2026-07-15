package profile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport/serial"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := Profile{
		Name: "board-a",
		UART: &UARTConfig{
			Identity: transport.Identity{Kind: "usb-serial-by-id", Key: "/dev/serial/by-id/usb-FTDI-if00"},
			Line:     serial.LineSettings{BaudRate: 115200, DataBits: 8, Parity: serial.ParityNone, StopBits: 1, Flow: serial.FlowNone},
		},
	}
	if err := Save(dir, p); err != nil {
		t.Fatal(err)
	}

	got, err := Load(dir, "board-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, got) {
		t.Fatalf("got %+v, want %+v", got, p)
	}

	info, err := os.Stat(filepath.Join(dir, "board-a.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("got mode %v, want 0600", info.Mode().Perm())
	}
}

func TestSaveRejectsInvalidLineSettings(t *testing.T) {
	dir := t.TempDir()
	p := Profile{
		Name: "board-b",
		UART: &UARTConfig{
			Identity: transport.Identity{Kind: "usb-serial-by-id", Key: "/dev/serial/by-id/usb-x"},
			Line:     serial.LineSettings{BaudRate: 0, DataBits: 8, Parity: serial.ParityNone, StopBits: 1},
		},
	}
	if err := Save(dir, p); err == nil {
		t.Fatal("expected Save to reject invalid line settings")
	}
}

func TestSSHConfigSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := Profile{
		Name: "board-c",
		SSH: &SSHConfig{
			Host:           "board.example",
			Port:           22,
			User:           "root",
			IdentityFiles:  []string{"/home/op/.ssh/id_ed25519"},
			UseAgent:       true,
			KnownHostsFile: "/home/op/.config/ai-debug-gateway/known_hosts",
		},
	}
	if err := Save(dir, p); err != nil {
		t.Fatal(err)
	}

	got, err := Load(dir, "board-c")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, got) {
		t.Fatalf("got %+v, want %+v", got, p)
	}
}

func TestProfileTypeHasNoSecretFields(t *testing.T) {
	assertNoSecretFields(t, reflect.TypeOf(Profile{}))
	assertNoSecretFields(t, reflect.TypeOf(UARTConfig{}))
	assertNoSecretFields(t, reflect.TypeOf(SSHConfig{}))
}

func assertNoSecretFields(t *testing.T, typ reflect.Type) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "password") || strings.Contains(name, "passphrase") || strings.Contains(name, "secret") {
			t.Fatalf("%s must never have a secret-shaped field, found %q", typ.Name(), typ.Field(i).Name)
		}
	}
}
