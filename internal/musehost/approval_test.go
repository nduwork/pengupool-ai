package musehost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The wire approval path, against a fake muse that speaks MSP: the host
// must receipt approval/request and answer with approval/decide carrying
// the handler's choice.
const fakeMuse = `#!/usr/bin/env python3
import sys, json, os

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

decided = []
for line in sys.stdin:
    try:
        m = json.loads(line)
    except Exception:
        continue
    method = m.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": m["id"], "result": {"serverInfo": {"name": "muse", "version": "fake"}}})
    elif method == "session/start":
        send({"jsonrpc": "2.0", "id": m["id"], "result": {"session": {"sessionId": "s1"}, "viewCursor": "c"}})
        send({"jsonrpc": "2.0", "id": 99, "method": "approval/request", "params": {
            "approvalId": "a1", "sessionId": "s1",
            "availableChoices": [{"choiceId": "deny"}, {"choiceId": "allow"}]}})
    elif method == "approval/decide":
        decided.append(m["params"]["choiceId"])
        send({"jsonrpc": "2.0", "id": m["id"], "result": {"approvalId": "a1", "commandId": m["params"]["commandId"], "status": "decided", "terminal": True}})
        with open(os.environ["FAKE_MARKER"], "w") as f:
            f.write(",".join(decided))
    elif method == "turn/start":
        send({"jsonrpc": "2.0", "method": "turn/completed", "params": {"status": "completed"}})
        send({"jsonrpc": "2.0", "id": m["id"], "result": {"turnId": "t1", "status": "ok", "startedNewTurn": True, "commandId": m["params"]["commandId"]}})
`

func TestWireApprovalDecide(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "decided.txt")
	t.Setenv("FAKE_MARKER", marker)
	bin := filepath.Join(dir, "muse")
	if err := os.WriteFile(bin, []byte(fakeMuse), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, err := Start(ctx, Options{Bin: bin}, Handlers{
		Approve: func(a Approval) (string, error) { return "allow", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	sid, err := host.StartSession(ctx, "onRequest", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.RunTurn(ctx, sid, "do it"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil && string(b) == "allow" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("approval/decide with choice allow not observed")
}
