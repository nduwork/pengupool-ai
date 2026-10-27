//go:build !linux && !darwin

package gateway

import "net"

// checkPeerUID has no platform support here, so the peer check is best-effort: the socket's
// directory permissions are what keep other users out.
func checkPeerUID(net.Conn) error { return nil }

// peerPID is unknown here: 0.
func peerPID(net.Conn) int { return 0 }
