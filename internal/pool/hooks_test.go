package pool

import "testing"

// The watermark: hooks that arrived before the latest processed turn end are stale, except the
// enders themselves, which always process.
func TestStaleHookWatermark(t *testing.T) {
	p := &Pool{}
	p.noteTurnEnd("s", 7)
	for _, c := range []struct {
		event string
		seq   uint64
		want  bool
	}{
		{"PostToolUse", 6, true},
		{"PreToolUse", 6, true},
		{"UserPromptSubmit", 6, true},
		{"ToolFailed", 6, true},
		{"PermissionRequest", 6, true},
		{"Notification", 6, true},
		{"SessionStart", 6, true},
		{"PostToolUse", 7, false},
		{"PostToolUse", 8, false},
		{"Stop", 6, false},
		{"Interrupted", 6, false},
		{"SessionEnd", 6, false},
		{"Delivered", 6, false},
	} {
		if got := p.staleTurnHook("s", c.event, c.seq); got != c.want {
			t.Errorf("staleTurnHook(%s, %d) = %v, want %v", c.event, c.seq, got, c.want)
		}
	}
	if p.staleTurnHook("other", "PostToolUse", 1) {
		t.Error("unknown session has no watermark")
	}
	// A late duplicate end never moves the watermark back.
	p.noteTurnEnd("s", 5)
	if !p.staleTurnHook("s", "PostToolUse", 6) {
		t.Error("watermark regressed on an older end")
	}
}
