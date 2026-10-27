//go:build !darwin && !linux

package pool

// parentPID is unknown here: -1.
func parentPID(int) int { return -1 }

// procTreeReadable: it cannot be read here, so a request is judged by what it names alone.
const procTreeReadable = false
