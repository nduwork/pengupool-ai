package pool

import (
	"strings"
	"time"
)

// Tasks: a task is labelled by its consolidated title, else its first instruction.

const taskIdle = 72 * time.Hour

func isInstruction(t turn) bool {
	return t.fromOwner() && !t.fromNotice() && len(strings.Fields(t.Prompt)) >= 3 && !detectSecret(t.Prompt)
}
