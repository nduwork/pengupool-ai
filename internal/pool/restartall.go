package pool

import (
	"fmt"
	"time"
)

// watchRelaunch watches a session just started again for restoreGap, looking every restoreGap/10.
// It returns "" when the session is still running at the end, else why it is not.
func (p *Pool) watchRelaunch(id string) string {
	for end := time.Now().Add(restoreGap); time.Now().Before(end); time.Sleep(restoreGap / 10) {
		if s, ok := p.session(id); ok && s.State != "running" {
			if s.ExitCode != nil {
				return fmt.Sprintf("it exited right after restarting (exit code %d)", *s.ExitCode)
			}
			return "it stopped right after restarting"
		}
	}
	return ""
}
