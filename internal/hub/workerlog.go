package hub

import (
	"os"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// workerLogPath is where a worker's stderr goes: <store>/logs/<session-id>.log.
//
// A worker owns that file. The daemon opens it, hands it to the child and closes its own handle
// right after the fork, which is the whole point: a worker outlives its daemon, and a worker whose
// next log line hit a pipe whose reader died with the daemon was killed by SIGPIPE, taking the agent
// with it.
func (h *Hub) workerLogPath(id string) string {
	return h.store.Path(store.LogsDir, id+".log")
}

// openWorkerLog opens a worker's log for appending. The returned file must be handed to the child as
// its *os.File: os/exec turns any other io.Writer into a pipe with the read end in the daemon, which
// is the shape that killed workers with SIGPIPE. The offset is where this daemon's own lines begin,
// so that forwarding them into the daemon log never repeats what another daemon already said about
// the same session.
func openWorkerLog(path string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// forwardWorkerLog copies what a worker wrote to its log during this daemon's run into the daemon
// log, tagged with its session. A worker that dies before its handshake has nowhere else to say why,
// and its log file is not the daemon's terminal.
func forwardWorkerLog(path string, from int64, id string, logf func(string, ...any)) {
	raw, err := os.ReadFile(path)
	if err != nil {
		logf("worker %s: read log %s: %v", id, path, err)
		return
	}
	if from < 0 || from > int64(len(raw)) {
		from = 0
	}
	for _, line := range strings.Split(string(raw[from:]), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			logf("worker %s: %s", id, line)
		}
	}
}
