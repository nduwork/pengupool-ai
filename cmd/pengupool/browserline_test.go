package main

import (
	"strings"
	"testing"
)

// The daemon log names the browser app's address, never the owner token.
func TestBrowserAppLineHasNoToken(t *testing.T) {
	line := browserAppLine("127.0.0.1:7787")
	if strings.Contains(line, "token") || !strings.Contains(line, "http://127.0.0.1:7787/") {
		t.Fatalf("line = %q", line)
	}
}
