package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteHostVersion(t *testing.T) {
	dir := t.TempDir()
	if err := writeHostVersion(dir, "pengupool 0.8.0+abc (protocol v1)"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "run", "host.version"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "pengupool 0.8.0+abc (protocol v1)\n" {
		t.Fatalf("host.version = %q", b)
	}
}
