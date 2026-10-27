// Package store owns the on-disk home path. Home is $PENGUPOOL_AI_HOME,
// default ~/.pengupool-ai. Only the path helper remains: the Compass
// layer that wrote here is gone, imported once into agentdb.
package store

import (
	"os"
	"path/filepath"
)

// Home returns the store directory, creating nothing.
func Home() string {
	if h := os.Getenv("PENGUPOOL_AI_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pengupool-ai"
	}
	return filepath.Join(home, ".pengupool-ai")
}
