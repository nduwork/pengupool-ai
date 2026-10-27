package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestTunneled(t *testing.T) {
	for _, c := range []struct {
		remote, host string
		want         bool
	}{
		{"127.0.0.1:50000", "127.0.0.1:27345", true}, // the app's forward, through sshd
		{"[::1]:50000", "[::1]:27345", true},
		{"127.0.0.1:50000", "evil.example:27345", false}, // DNS rebinding
		{"127.0.0.1:50000", "localhost:27345", false},
		{"192.168.1.9:50000", "127.0.0.1:27345", false}, // not from this host
	} {
		r := httptest.NewRequest("GET", "/api/healthz", nil)
		r.RemoteAddr, r.Host = c.remote, c.host
		if got := tunneled(r); got != c.want {
			t.Errorf("tunneled(%s from %s) = %v", c.host, c.remote, got)
		}
	}
}
