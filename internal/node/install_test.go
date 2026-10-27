package node

import "testing"

// The macOS host binary is arm64 only: an Intel Mac SSH host is refused plainly.
func TestBinaryForMac(t *testing.T) {
	m := &Manager{bin: "/app/bin/pengupool"}
	if p, err := m.binaryFor("Darwin arm64"); err != nil || p != m.bin {
		t.Fatalf("Apple Silicon: %q %v", p, err)
	}
	if _, err := m.binaryFor("Darwin x86_64"); err == nil || err.Error() != "PenguPool's macOS host runs on Apple Silicon only" {
		t.Fatalf("Intel: %v", err)
	}
}
