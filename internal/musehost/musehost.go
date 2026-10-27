// Package musehost drives Muse Code two ways: an MSP session host
// (`muse serve`, JSON-RPC 2.0 over stdio — initialize, session/start,
// turn/start, approvals decided on the wire) and one-shot headless runs
// (`muse exec --json`). The wire contract is `muse schema` (MSP v1);
// approval is chosen on the wire, never by a serve flag.
package musehost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Approval is one pending wire approval. The client picks a ChoiceID from
// Choices; it never invents one.
type Approval struct {
	ID      string
	Session string
	Choices []string
	Raw     json.RawMessage
}

// UserInput is one pending wire question from the agent.
type UserInput struct {
	ID      string
	Session string
	Raw     json.RawMessage
}

// Handlers decide wire requests. A nil Approve denies; a nil Answer
// cancels the input.
type Handlers struct {
	OnEvent func(method string, params json.RawMessage)
	Approve func(a Approval) (choiceID string, err error)
	Answer  func(u UserInput) (answers json.RawMessage, err error)
	OnText  func(delta string)
}

type Options struct {
	Bin      string // muse binary; empty = look up "muse" on PATH
	Provider string // echo|meta|local; empty = muse default
	Model    string
	Dir      string // working directory of the serve process
}

type Host struct {
	cmd        *exec.Cmd
	w          *bufio.Writer
	mu         sync.Mutex
	nextID     int
	pend       map[int]chan rpcMsg
	h          Handlers
	done       chan struct{}
	turnWaiter chan string
}

type rpcMsg struct {
	ID     *int            `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// newCmdID mints a UUIDv7 command id; MSP rejects other versions.
func newCmdID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

// Start launches `muse serve` and does the initialize handshake.
func Start(ctx context.Context, opts Options, h Handlers) (*Host, error) {
	bin := opts.Bin
	if bin == "" {
		b, err := exec.LookPath("muse")
		if err != nil {
			return nil, fmt.Errorf("muse not on PATH: %w", err)
		}
		bin = b
	}
	args := []string{"serve"}
	if opts.Provider != "" {
		args = append(args, "--provider", opts.Provider)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = opts.Dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	host := &Host{cmd: cmd, w: bufio.NewWriter(stdin), pend: map[int]chan rpcMsg{}, h: h, done: make(chan struct{})}
	go host.readLoop(bufio.NewScanner(stdout))
	var init struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	err = host.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "pengupool_ai", "version": "0.3.0"},
		"capabilities": map[string]any{"requestedCapabilities": []string{"sessionMcp", "sessionListStream"}},
	}, &init)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("muse initialize: %w", err)
	}
	if init.ServerInfo.Name != "muse" {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("unexpected MSP server %q", init.ServerInfo.Name)
	}
	// MSP handshake completes with the initialized notification.
	nb, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	host.mu.Lock()
	_, _ = host.w.Write(append(nb, '\n'))
	_ = host.w.Flush()
	host.mu.Unlock()
	return host, nil
}

func (h *Host) readLoop(sc *bufio.Scanner) {
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	defer close(h.done)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m rpcMsg
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		switch {
		case m.ID != nil && m.Method != "":
			go h.serveRequest(m)
		case m.ID != nil:
			h.mu.Lock()
			ch := h.pend[*m.ID]
			delete(h.pend, *m.ID)
			h.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		default:
			if m.Method == "turn/completed" {
				h.mu.Lock()
				w := h.turnWaiter
				h.mu.Unlock()
				if w != nil {
					var p struct {
						Status string `json:"status"`
						State  string `json:"state"`
					}
					_ = json.Unmarshal(m.Params, &p)
					s := p.Status + p.State
					if s == "" {
						s = "completed"
					}
					select {
					case w <- s:
					default:
					}
				}
			}
			if m.Method == "item/delta" && h.h.OnText != nil {
				var d struct {
					Delta string `json:"delta"`
					Text  string `json:"text"`
				}
				if json.Unmarshal(m.Params, &d) == nil {
					if t := d.Delta + d.Text; t != "" {
						h.h.OnText(t)
					}
				}
			}
			if h.h.OnEvent != nil {
				h.h.OnEvent(m.Method, m.Params)
			}
		}
	}
}

// serveRequest answers a server-initiated must-answer request: the
// presentation receipt goes back at once, the decision travels as its
// own client call (approval/decide, userInput/answer).
func (h *Host) serveRequest(m rpcMsg) {
	h.respond(*m.ID, map[string]any{})
	ctx := context.Background()
	switch m.Method {
	case "approval/request":
		var p struct {
			ApprovalID       string `json:"approvalId"`
			SessionID        string `json:"sessionId"`
			AvailableChoices []struct {
				ChoiceID string `json:"choiceId"`
			} `json:"availableChoices"`
		}
		_ = json.Unmarshal(m.Params, &p)
		a := Approval{ID: p.ApprovalID, Session: p.SessionID, Raw: m.Params}
		for _, c := range p.AvailableChoices {
			a.Choices = append(a.Choices, c.ChoiceID)
		}
		choice := ""
		if h.h.Approve != nil {
			choice, _ = h.h.Approve(a)
		}
		if choice == "" {
			// Deny: prefer an explicit deny-like choice when offered.
			for _, c := range a.Choices {
				if strings.Contains(strings.ToLower(c), "deny") || strings.Contains(strings.ToLower(c), "reject") {
					choice = c
					break
				}
			}
		}
		if choice == "" && len(a.Choices) > 0 {
			choice = a.Choices[len(a.Choices)-1]
		}
		if choice != "" {
			_ = h.Call(ctx, "approval/decide", map[string]any{
				"approvalId": a.ID, "choiceId": choice, "commandId": newCmdID(),
			}, nil)
		}
	case "userInput/request":
		var p struct {
			UserInputID string `json:"userInputId"`
			SessionID   string `json:"sessionId"`
		}
		_ = json.Unmarshal(m.Params, &p)
		u := UserInput{ID: p.UserInputID, Session: p.SessionID, Raw: m.Params}
		if h.h.Answer != nil {
			if answers, err := h.h.Answer(u); err == nil && answers != nil {
				_ = h.Call(ctx, "userInput/answer", map[string]any{
					"userInputId": u.ID, "sessionId": u.Session, "answers": json.RawMessage(answers), "commandId": newCmdID(),
				}, nil)
				return
			}
		}
		_ = h.Call(ctx, "userInput/cancel", map[string]any{
			"userInputId": u.ID, "sessionId": u.Session, "commandId": newCmdID(),
		}, nil)
	}
}

func (h *Host) respond(id int, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = h.w.Write(append(b, '\n'))
	_ = h.w.Flush()
}

// Call sends one request and waits for its result.
func (h *Host) Call(ctx context.Context, method string, params any, out any) error {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	ch := make(chan rpcMsg, 1)
	h.pend[id] = ch
	pb, err := json.Marshal(params)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(pb)})
	if _, err := h.w.Write(append(b, '\n')); err != nil {
		h.mu.Unlock()
		return err
	}
	if err := h.w.Flush(); err != nil {
		h.mu.Unlock()
		return err
	}
	h.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.done:
		return fmt.Errorf("muse host closed during %s", method)
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("%s: [%d] %s", method, m.Error.Code, m.Error.Message)
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	}
}

// StartSession opens a session. approvalMode is wire-chosen:
// allowAll|promptUnmatched|onRequest|denyUnmatched ("" = server default).
// config may carry mcpServers etc.; unknown keys are ignored on the wire.
func (h *Host) StartSession(ctx context.Context, approvalMode, workspaceRoot string, config map[string]any) (string, error) {
	params := map[string]any{"commandId": newCmdID()}
	if approvalMode != "" {
		params["approvalMode"] = approvalMode
	}
	if workspaceRoot != "" {
		params["workspaceRoot"] = workspaceRoot
	}
	if config != nil {
		params["config"] = config
	}
	var res struct {
		Session struct {
			SessionID string `json:"sessionId"`
		} `json:"session"`
	}
	if err := h.Call(ctx, "session/start", params, &res); err != nil {
		return "", err
	}
	return res.Session.SessionID, nil
}

// RunTurn sends one prompt and waits for turn/completed, returning the
// terminal status. Streaming text goes to Handlers.OnText.
func (h *Host) RunTurn(ctx context.Context, sessionID, text string) (string, error) {
	waiter := make(chan string, 1)
	h.setTurnWaiter(waiter)
	defer h.setTurnWaiter(nil)
	var res struct {
		TurnID string `json:"turnId"`
		Status string `json:"status"`
	}
	err := h.Call(ctx, "turn/start", map[string]any{
		"commandId": newCmdID(),
		"sessionId": sessionID,
		"input":     []any{map[string]string{"type": "text", "text": text}},
	}, &res)
	if err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-h.done:
		return "", fmt.Errorf("muse host closed during turn")
	case s := <-waiter:
		return s, nil
	}
}

func (h *Host) setTurnWaiter(ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.turnWaiter = ch
}

// Close kills the serve process.
func (h *Host) Close() error {
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	return h.cmd.Wait()
}

// Exec runs one headless prompt (`muse exec --json`) and returns the
// concatenated assistant text from the JSONL event stream.
func Exec(ctx context.Context, bin, prompt string, extraArgs ...string) (string, error) {
	if bin == "" {
		b, err := exec.LookPath("muse")
		if err != nil {
			return "", fmt.Errorf("muse not on PATH: %w", err)
		}
		bin = b
	}
	args := append([]string{"exec", "--json"}, extraArgs...)
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("muse exec: %w", err)
	}
	var sb strings.Builder
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev struct {
			PayloadType string `json:"payload_type"`
			Payload     struct {
				Text string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.PayloadType == "run.output.delta" {
			sb.WriteString(ev.Payload.Text)
		}
	}
	return sb.String(), nil
}
