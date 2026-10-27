package musehost

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Live MSP round-trip against the echo provider: no Meta account needed.
func TestEchoHostTurn(t *testing.T) {
	if _, err := exec.LookPath("muse"); err != nil {
		t.Skip("muse not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var text strings.Builder
	host, err := Start(ctx, Options{Provider: "echo", Dir: t.TempDir()}, Handlers{
		OnText: func(d string) { text.WriteString(d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	sid, err := host.StartSession(ctx, "denyUnmatched", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if sid == "" {
		t.Fatal("empty session id")
	}
	status, err := host.RunTurn(ctx, sid, "hello muse")
	if err != nil {
		t.Fatal(err)
	}
	if status == "" {
		t.Fatal("empty turn status")
	}
	t.Logf("status=%q text=%q", status, text.String())
}

func TestExecEcho(t *testing.T) {
	if _, err := exec.LookPath("muse"); err != nil {
		t.Skip("muse not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := Exec(ctx, "", "ping", "--provider", "echo")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exec out: %q", out)
}
