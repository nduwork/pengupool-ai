package pool

import (
	"strings"
	"time"
)

// Tasks: a task is labelled by its consolidated title, else its first instruction.

const taskIdle = 72 * time.Hour

// isInstruction: the owner's prompt sets the team's task. An answer the host typed in for the owner
// (Delivered) answers one question and sets no task.
func isInstruction(t turn) bool {
	return t.fromOwner() && t.Delivered == "" && !t.fromNotice() && len(strings.Fields(t.Prompt)) >= 3 && !detectSecret(t.Prompt)
}
