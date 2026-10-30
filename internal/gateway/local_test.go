package gateway

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// The app and the owner's routes answer only on this machine; the network gets the device API.
func TestNetworkGetsDeviceAPIOnly(t *testing.T) {
	srv, _ := newTestServerOpts(t, Options{Sessions: &fakeSessions{},
		Static:     fstest.MapFS{"index.html": {Data: []byte("app")}},
		PhoneHosts: func() []string { return []string{"box.tail1.ts.net"} }})
	h := srv.handler()
	addr := srv.HTTPAddr()
	_, port, _ := net.SplitHostPort(addr)
	get := func(peer, host, path string) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr, r.Host = peer, host
		if fwd, ok := strings.CutPrefix(r.RemoteAddr, "serve:"); ok { // through Tailscale Serve
			r.RemoteAddr = fwd
			r.Header.Set("X-Forwarded-For", "100.64.0.9")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, c := range []struct {
		name, peer, host, path string
		want                   int
	}{
		{"local app", "127.0.0.1:5000", addr, "/", 200},
		{"local ws without token", "127.0.0.1:5000", addr, "/ws", 401},
		{"ssh forward", "127.0.0.1:5000", "127.0.0.1:9999", "/", 200},
		{"local ipv6", "[::1]:5000", addr, "/", 200},
		{"mapped loopback", "[::ffff:127.0.0.1]:5000", addr, "/", 200},
		{"mapped lan", "[::ffff:192.168.1.20]:5000", addr, "/", 404},
		{"lan traversal", "192.168.1.20:5000", addr, "/api/muse/v1/../../ws", 404},
		{"lan app", "192.168.1.20:5000", addr, "/", 404},
		{"lan ws", "192.168.1.20:5000", addr, "/ws", 404},
		{"lan files", "192.168.1.20:5000", addr, "/api/files", 404},
		{"lan health", "192.168.1.20:5000", addr, "/api/healthz", 200},
		{"tailnet app", "100.64.0.9:5000", "box.tail1.ts.net", "/", 404},
		{"serve app", "127.0.0.1:5000", "box.tail1.ts.net", "/", 404},
		{"serve ws", "127.0.0.1:5000", "box.tail1.ts.net", "/ws", 404},
		{"serve with a loopback host", "serve:127.0.0.1:5000", "127.0.0.1:1", "/", 404},
		{"serve with localhost", "serve:127.0.0.1:5000", "localhost:" + port, "/ws", 404},
		{"serve to the device API", "serve:127.0.0.1:5000", "box.tail1.ts.net", "/api/muse/v1/openapi.json", 200},
		{"tailnet openapi", "100.64.0.9:5000", "box.tail1.ts.net", "/api/muse/v1/openapi.json", 200},
		{"tailnet sessions without token", "100.64.0.9:5000", "box.tail1.ts.net", "/api/muse/v1/sessions", 401},
	} {
		if got := get(c.peer, c.host, c.path); got != c.want {
			t.Errorf("%s: %s from %s = %d, want %d", c.name, c.path, c.peer, got, c.want)
		}
	}
}
