package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// Read-only workspace views for the desktop owner: the selected session's file tree, its Git
// changes and diffs, and the tail of its worker log. The browser names a session and a path
// relative to that session's cwd; every file read goes through os.Root, so neither ".." nor a
// symbolic link can leave the workspace.

const (
	maxFileBytes = 256 << 10
	maxDirItems  = 2000
	maxLogBytes  = 64 << 10
	gitTimeout   = 5 * time.Second
)

func (s *Server) workspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/files", s.owner(s.handleFiles))
	mux.HandleFunc("/api/changes", s.owner(s.handleChanges))
	mux.HandleFunc("/api/diff", s.owner(s.handleDiff))
	mux.HandleFunc("/api/log", s.owner(s.handleLog))
	mux.HandleFunc("/api/raw/", s.handleRaw)
}

const maxRawBytes = 16 << 20

// rawCap is the capability in a preview URL: read-only access to one session's workspace files,
// derived from the host token so it is never the token itself. A rendered preview (Markdown, HTML,
// an image) loads its relative assets through it.
func (s *Server) rawCap(sessionID string) string {
	m := hmac.New(sha256.New, []byte(s.token))
	m.Write([]byte("raw:" + sessionID))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// handleRaw serves one workspace file's bytes at /api/raw/<session>/<cap>/<path>: read through
// os.Root like every workspace read, never a directory, sandboxed when it is a document.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/raw/"), "/", 3)
	if len(parts) != 3 || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.rawCap(parts[0]))) != 1 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var sess *store.Session
	for _, x := range s.opts.Store.State().Sessions {
		if x.ID == parts[0] {
			sess = &x
		}
	}
	rel, ok := relPath(parts[2])
	if sess == nil || !ok || rel == "." {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	root, err := os.OpenRoot(sess.Cwd)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() || info.Size() > maxRawBytes {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ct := mime.TypeByExtension(path.Ext(rel))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	// A document opened from a preview cannot reach the app and runs no scripts, except an HTML page
	// the owner turned scripts on for (?run=1): it runs them in an opaque origin, which cannot read
	// the host's answers (CORS allows only the app). A PDF is left to the browser's viewer, which a
	// sandbox would block; nosniff keeps anything else from being read as one.
	switch {
	case ct == "application/pdf":
	case r.URL.Query().Get("run") == "1" && strings.HasPrefix(ct, "text/html"):
		h.Set("Content-Security-Policy", "sandbox allow-scripts")
	default:
		h.Set("Content-Security-Policy", "sandbox; script-src 'none'; object-src 'none'")
	}
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// owner gates a workspace route on the bootstrap token (header or query) and the app's Origin,
// and resolves the session it names.
func (s *Server) owner(next func(http.ResponseWriter, *http.Request, store.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tok := r.Header.Get("X-PenguPool-Token")
		if tok == "" {
			tok = r.URL.Query().Get(TokenQuery)
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !originAllowed(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		id := r.URL.Query().Get("session")
		for _, sess := range s.opts.Store.State().Sessions {
			if sess.ID == id {
				next(w, r, sess)
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "message": "no such session"})
	}
}

// relPath cleans a browser-supplied relative path; "" and "." name the workspace root.
func relPath(p string) (string, bool) {
	p = path.Clean("/" + strings.TrimPrefix(p, "/"))[1:]
	if p == "" {
		return ".", true
	}
	return p, fs.ValidPath(p)
}

type fileEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size,omitempty"`
	Link  bool   `json:"link,omitempty"`
	Mtime string `json:"mtime,omitempty"` // last modified, RFC 3339
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request, sess store.Session) {
	rel, ok := relPath(r.URL.Query().Get("path"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_argument", "message": "bad path"})
		return
	}
	root, err := os.OpenRoot(sess.Cwd)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeFSError(w, err)
		return
	}
	if info.IsDir() {
		ents, err := f.ReadDir(maxDirItems)
		if err != nil && !errors.Is(err, io.EOF) {
			writeFSError(w, err)
			return
		}
		out := make([]fileEntry, 0, len(ents))
		for _, e := range ents {
			fe := fileEntry{Name: e.Name(), Dir: e.IsDir(), Link: e.Type()&fs.ModeSymlink != 0}
			if fe.Link {
				// Follow the link only inside the root; an escaping link is shown but not opened.
				if st, err := root.Stat(path.Join(rel, e.Name())); err == nil {
					fe.Dir = st.IsDir()
				}
			}
			if fi, err := e.Info(); err == nil {
				fe.Mtime = fi.ModTime().UTC().Format(time.RFC3339)
				if !fe.Dir {
					fe.Size = fi.Size()
				}
			}
			out = append(out, fe)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Dir != out[j].Dir {
				return out[i].Dir
			}
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		})
		writeJSON(w, http.StatusOK, map[string]any{"path": rel, "dir": true, "entries": out, "truncated": len(ents) == maxDirItems})
		return
	}
	buf, _ := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	resp := map[string]any{"path": rel, "dir": false, "size": info.Size(), "truncated": len(buf) > maxFileBytes,
		"mtime": info.ModTime().UTC().Format(time.RFC3339),
		"raw":   "/api/raw/" + sess.ID + "/" + s.rawCap(sess.ID) + "/" + rel}
	if len(buf) > maxFileBytes {
		buf = buf[:maxFileBytes]
	}
	if bytes.IndexByte(buf, 0) >= 0 || !utf8.Valid(trimPartialRune(buf)) {
		resp["binary"] = true
	} else {
		resp["content"] = string(buf)
	}
	writeJSON(w, http.StatusOK, resp)
}

// trimPartialRune drops an incomplete UTF-8 sequence cut by the read limit.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return b
}

type change struct {
	Path   string `json:"path"`
	Status string `json:"status"` // two-letter porcelain XY
	From   string `json:"from,omitempty"`
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request, sess store.Session) {
	branch, err := git(r.Context(), sess.Cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"git": false, "message": strings.TrimSpace(err.Error())})
		return
	}
	top, _ := git(r.Context(), sess.Cwd, "rev-parse", "--show-toplevel")
	out, err := git(r.Context(), sess.Cwd, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ".")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"git": false, "message": err.Error()})
		return
	}
	var changes []change
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if len(rec) < 4 {
			continue
		}
		c := change{Status: rec[:2], Path: rec[3:]}
		if c.Status[0] == 'R' || c.Status[0] == 'C' {
			if i+1 < len(recs) {
				c.From = recs[i+1]
				i++
			}
		}
		changes = append(changes, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"git": true, "branch": strings.TrimSpace(branch), "toplevel": strings.TrimSpace(top), "changes": changes,
	})
}

// handleDiff returns one path's diff: staged=1 compares the index with HEAD, otherwise the
// working file with the index (an untracked file diffs against /dev/null). Paths are relative to
// the repository top level, as status reports them. If HEAD or the index moved while the diff was
// read, the answer says it is stale.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request, sess store.Session) {
	rel, ok := relPath(r.URL.Query().Get("path"))
	if !ok || rel == "." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_argument", "message": "bad path"})
		return
	}
	top, err := git(r.Context(), sess.Cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"diff": "", "message": err.Error()})
		return
	}
	top = strings.TrimSpace(top)
	before := repoRevision(r.Context(), top)
	base := []string{"-c", "core.quotepath=off", "diff", "--no-ext-diff", "--no-textconv", "--no-color"}
	var out string
	if r.URL.Query().Get("staged") == "1" {
		out, _ = git(r.Context(), top, append(base, "--cached", "--", ":(literal)"+rel)...)
	} else {
		out, _ = git(r.Context(), top, append(base, "--", ":(literal)"+rel)...)
		if out == "" {
			if st, _ := git(r.Context(), top, "status", "--porcelain", "--", ":(literal)"+rel); strings.HasPrefix(st, "??") {
				// --no-index exits 1 when the files differ; the output is still the diff.
				out, _ = git(r.Context(), top, append(base, "--no-index", "--", "/dev/null", rel)...)
			}
		}
	}
	if len(out) > maxFileBytes {
		out = out[:maxFileBytes] + "\n… diff truncated\n"
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "diff": out, "stale": before != repoRevision(r.Context(), top)})
}

// repoRevision identifies HEAD and the index, so a diff read across a change can say so.
func repoRevision(ctx context.Context, top string) string {
	head, _ := git(ctx, top, "rev-parse", "HEAD")
	gitDir, _ := git(ctx, top, "rev-parse", "--absolute-git-dir")
	st, err := os.Stat(strings.TrimSpace(gitDir) + "/index")
	if err != nil {
		return head
	}
	return head + st.ModTime().String() + fmt.Sprint(st.Size())
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request, sess store.Session) {
	f, err := os.Open(s.opts.Store.Path(store.LogsDir, sess.ID+".log"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"log": ""})
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > maxLogBytes {
		_, _ = f.Seek(-maxLogBytes, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	writeJSON(w, http.StatusOK, map[string]any{"log": string(b), "path": f.Name()})
}

// git runs git with structured arguments, no pager, no prompts, and external helpers disabled.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil && stdout.Len() == 0 {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

func writeFSError(w http.ResponseWriter, err error) {
	code, status := "unavailable", http.StatusInternalServerError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code, status = "not_found", http.StatusNotFound
	case errors.Is(err, fs.ErrPermission):
		code, status = "forbidden", http.StatusForbidden
	case strings.Contains(err.Error(), "escapes"):
		code, status = "forbidden", http.StatusForbidden
	}
	writeJSON(w, status, map[string]string{"error": code, "message": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
