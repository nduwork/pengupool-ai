// Package voice turns the owner's spoken words into the Compass loop.
//
// No microphone capture of our own: the clip comes from the Muse app (or
// any recorder). `muse voice transcribe` turns it into text; ingest
// generalizes and proposes; the owner confirms. Mirrors pengu/voice.py.
package voice

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// LookPath and run are stubbed in tests.
var (
	LookPath = exec.LookPath
	Run      = func(argv []string, timeout time.Duration) (string, string, error) {
		cmd := exec.Command(argv[0], argv[1:]...)
		out, errOut := &strings.Builder{}, &strings.Builder{}
		cmd.Stdout, cmd.Stderr = out, errOut
		done := make(chan error, 1)
		go func() { done <- cmd.Run() }()
		select {
		case err := <-done:
			return out.String(), errOut.String(), err
		case <-time.After(timeout):
			_ = cmd.Process.Kill()
			return "", "", errors.New("timeout")
		}
	}
)

// Transcribe returns the text of an audio clip via `muse voice transcribe`.
func Transcribe(clip string) (string, error) {
	if strings.TrimSpace(clip) == "" {
		return "", errors.New("no clip")
	}
	if _, err := LookPath("muse"); err != nil {
		return "", errors.New("muse CLI not found")
	}
	stdout, stderr, err := Run([]string{"muse", "voice", "transcribe", clip}, 120*time.Second)
	if err != nil {
		return "", fmt.Errorf("muse voice failed: %w", err)
	}
	text := strings.TrimSpace(stdout)
	if text == "" {
		return "", fmt.Errorf("muse voice returned no text: %.200s", strings.TrimSpace(stderr))
	}
	return text, nil
}
