// Package pool is PenguPool's domain: work repositories, harness sessions, the Skill library with
// its memory and logs, tasks, team proposals and approvals, teams and their messages, and group
// access. All of it lives in state.json and plain files; there is no database.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/hub"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Options configure a Pool.
type Options struct {
	Store *store.Store
	Hub   *hub.Hub
	Exe   string // this executable, for hooks
	Logf  func(string, ...any)
	// Env is the environment agents start with (the daemon's own by default).
	Env []string
	// Nodes reaches remote hosts; nil means local only.
	Nodes NodeManager
	// Version is the running PenguPool version, for the update check; empty: no check.
	Version string
	// RequireOwnerInput: a prompt is the owner's only if the owner pressed Enter in that session
	// through the app or the owner's terminal (the gateway's OwnerInput). Set by the daemon; tests and
	// the offline evaluation, which have no gateway, leave it off.
	RequireOwnerInput bool
}

// NodeManager is the SSH node layer as the pool sees it.
type NodeManager interface {
	Status() []protocol.NodeView
	Add(ctx context.Context, in protocol.NodeAdd) (protocol.NodeView, error)
	Remove(id string) error
	Check(ctx context.Context, id string) protocol.NodeView
	Unlock(ctx context.Context, id, password string, save bool) (protocol.NodeView, error)
	ForgetPassword(id string) error
	Keys() []protocol.SSHKey
	KeySetup(ctx context.Context, id, key string) ([]protocol.KeySetupStep, error)
	Connect(ctx context.Context, id string) (protocol.HostConnection, error)
}

// Pool is the domain service behind every pool request.
type Pool struct {
	st    *store.Store
	hub   *hub.Hub
	exe   string
	env   []string
	logf  func(string, ...any)
	nodes NodeManager

	mu                sync.Mutex
	achvLocks         map[string]*sync.Mutex       // one achievement job per skill, under mu
	achvAgain         map[string]bool              // a job was asked while one ran, under mu
	msgSock           map[string]claudeInbox       // session id → Claude inbox socket
	crossOK           map[sessionPair]time.Time    // channels the owner opened with @, until when
	injected          map[string]map[string]string // session id → digest → heading of each context section it was given
	lastInput         map[string]time.Time         // session id → last desktop input (unused by native paths)
	ownerEnter        map[string][]credit          // session id → the prompts the owner submitted in it, not yet matched (under mu)
	askingOwner       map[string]bool              // sessions with a question or plan tool open (under mu)
	questionUse       map[string]string            // the tool_use_id a session's pending question is about (under mu)
	askWaitedAt       map[string]time.Time         // when an agent last waited on its ask (under mu)
	answersLater      map[string][]laterAnswer     // owner answers a busy member gets typed at its next Stop (under mu)
	deciding          map[string]bool              // items a Compass decision is being made on (percompass.go, under mu)
	hookAt            map[string]time.Time         // session id → its newest hook event (under mu; hookless.go)
	promptAt          map[string]time.Time         // session id → the owner's first prompt in its launch (under mu)
	routed            map[string]string            // LEAD session id → the teammate triage named for this turn (under mu)
	answering         map[int64]time.Time          // check-ins being answered by the owner, so a double click types once (under mu)
	requireOwnerInput bool
	pending           map[string][]protocol.TeamMessage
	turns             map[string]*turn     // session id → its open turn (guarded by mu)
	wording           map[string]*wording  // session id → the owner's words it is asked to word for the Compass (under mu)
	conflicts         conflictCache        // the Compass review's conflict items, for one set of lines (under mu)
	prevReply         map[string]string    // session id → the reply of its last placed turn: the options a choice is made from (guarded by mu)
	prevReplyAt       map[string]time.Time // session id → when that reply was placed (guarded by mu)
	nativeTo          map[string]time.Time // session id → the last native SendMessage to it the guard let through (guarded by mu)
	sstat             syncStatus           // the last sync round (guarded by mu)
	roundMu           sync.Mutex           // one sync at a time
	renames           map[string]string    // session id → a name to type into Claude at its next idle point (guarded by mu)
	syncMu            sync.Mutex           // one Skill library sync at a time
	logged            sync.Map             // catalog warnings already logged, so a list or refresh does not repeat them
	triaging          sync.Map             // ask id → true while triage weighs it, so a sweep starts it once
	// cloneMu: clones being registered hold it shared; the orphan clean-up takes it alone, so it
	// never removes a clone between its clone and its registration.
	cloneMu sync.RWMutex
	// clock replaces the wall clock for Compass signals and beliefs: the offline evaluation replays
	// the owner's time (eval.go). Nil: the wall clock.
	clock func() time.Time
	// testDecider stands in for the configured decision provider in tests.
	testDecider decision.Provider
	triageErr   string // the triage model's last call failed with this; "" after one that answered; triageUnchecked at start (guarded by mu)
	// testSpawn stands in for starting a worker in tests.
	testSpawn func(spawnReq) (store.Session, error)
	// testScreen stands in for the hub's followed screens in tests.
	testScreen func(id string) (string, bool)
	// testSend stands in for delivering text to a session in tests, testKeys for typing into one.
	testSend        func(id, text string) error
	testNotice      func(id, text string) error // stands in for a wording notice's delivery (wordingAtIdle)
	testKeys        func(id, keys string) error
	museMu          sync.Mutex                // guards the muse pairing/answer caches below
	museCodes       map[string]*musePairCode  // one-time pairing codes
	musePairFails   int                       // wrong codes since the last success: throttles guessing
	musePairBlocked time.Time                 // pairing refused until then, after too many wrong codes
	museAnswered    map[string]museAnswerDone // device\x00key -> first answer: replays return it
	museInflight    map[string]bool           // device\x00key -> an answer still running
	// museToolWarned: a Muse PreToolUse without tool_name was allowed and noted (under mu).
	museToolWarned bool
	db             *agentdb.DB // agents.db: the Compass and logs; nil if it could not open
	formMu         sync.Mutex  // formation changes apply one at a time

	reposCache atomic.Pointer[ghRepos] // the GitHub account's repositories
	// syncFor is the sync repository checked this run (private, writable) and the account's
	// login, which names this environment's folder in a shared repository (guarded by roundMu).
	syncFor, syncLogin string

	ownerName atomic.Value // string: the owner the Compass belongs to (compass.go)

	version  string
	updMu    sync.Mutex       // one update check at a time; guards upd and updAt
	upd      *protocol.Update // the last successful check
	updAt    time.Time
	dlMu     sync.Mutex              // one update download at a time
	testDisk func(path string) error // stands in for hdiutil's disk image check in tests
	asks     map[string][]time.Time  // session id (or "owner") → its decision.ask calls this hour (guarded by mu)
	gateMu   sync.Mutex              // guards gateSess and keyMu (the maps only)
	keyMu    map[string]*sync.Mutex  // one decision at a time per scope or permission key (lockKey)
	gateSess map[string]*gateSession // session id → its turn count, go-ahead breaker and open permission prompt
}

type claudeInbox struct{ Socket, Token string }

// New returns a pool over the given store and hub.
func New(o Options) *Pool {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	env := o.Env
	if env == nil {
		env = withUserPath(agentEnv(os.Environ()), os.Getenv("HOME"))
	}
	p := &Pool{
		triageErr: triageUnchecked,
		st:        o.Store, hub: o.Hub, exe: o.Exe, env: env, logf: logf, nodes: o.Nodes, version: o.Version,
		msgSock: map[string]claudeInbox{}, crossOK: map[sessionPair]time.Time{}, injected: map[string]map[string]string{},
		lastInput: map[string]time.Time{}, pending: map[string][]protocol.TeamMessage{},
		ownerEnter: map[string][]credit{}, requireOwnerInput: o.RequireOwnerInput,
		museCodes: map[string]*musePairCode{}, museAnswered: map[string]museAnswerDone{}, museInflight: map[string]bool{},
	}
	p.loadInboxes()
	p.expireStaleApprovals()
	// Off the start path: a slow `muse plugins install` must not hold the daemon up.
	go harness.EnsureMusePlugin(o.Store.Dir(), o.Exe, env, logf)
	p.dropRetiredProviders()
	if db, err := agentdb.Open(filepath.Join(o.Store.Dir(), "agents.db")); err != nil {
		logf("agent db: %v", err)
	} else {
		p.db = db
		p.keyHelpers()
		if dropped, err := db.Upgrade(p.owner()); err != nil {
			logf("agent db: upgrade: %v", err)
		} else if dropped != nil {
			logf("agent db: upgraded; rows no longer kept (a specialist's notes start empty): %v; the file before the upgrade is %s",
				dropped, agentdb.PreUpgradePath(filepath.Join(o.Store.Dir(), "agents.db")))
		}
		ls, _ := db.Lines(p.owner(), "", "", 0)
		p.sweepDropped(ls) // lines dropped before the Compass stopped keeping them
	}
	return p
}

// keyHelpers gives every helper registered without a repository key the key of its checkout's
// origin: its identity in the record on every machine.
func (p *Pool) keyHelpers() {
	keys := map[string]string{}
	for _, sk := range p.st.State().Domain.Skills {
		if sk.Key == "" {
			if k := agentKey(context.Background(), sk.Path); k != "" {
				keys[sk.ID] = k
			}
		}
	}
	if len(keys) == 0 {
		return
	}
	_ = p.update(func(st *store.State) error {
		for i := range st.Domain.Skills {
			if k := keys[st.Domain.Skills[i].ID]; k != "" {
				st.Domain.Skills[i].Key = k
			}
		}
		return nil
	})
}

// sessionMarkers are set by a running Claude Code for its own children. A host started from inside
// a Claude session would pass them on, and each agent would think it is a sub-session (no
// transcript, no resume). User settings such as CLAUDE_CODE_USE_BEDROCK pass through. CI makes
// agents (Gemini CLI, Ink TUIs) run non-interactively, and a PenguPool session is interactive.
var sessionMarkers = map[string]bool{
	"CI": true, "CLAUDECODE": true, "CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_CHILD_SESSION": true, "CLAUDE_CODE_SSE_PORT": true,
	"CLAUDE_CODE_MESSAGING_SOCKET": true, "CLAUDE_CODE_MESSAGING_TOKEN": true, "CLAUDE_CODE_SESSION_ID": true,
}

func agentEnv(env []string) []string {
	out := env[:0:0]
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); !sessionMarkers[k] {
			out = append(out, e)
		}
	}
	return out
}

// userBins are where agent CLIs and their runtimes (node, bun) install. A host started without
// the owner's login-shell PATH (launchd gives /usr/bin:/bin:/usr/sbin:/sbin) would find none of
// them, so the ones that exist are added after PATH's own entries.
// ponytail: fixed list; a version manager's bin (nvm, asdf) still needs the login shell's PATH.
var userBins = []string{"~/.local/bin", "~/.bun/bin", "~/.npm-global/bin", "~/.cargo/bin", "~/go/bin",
	"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"}

func withUserPath(env []string, home string) []string {
	i := slices.IndexFunc(env, func(e string) bool { return strings.HasPrefix(e, "PATH=") })
	path := ""
	if i >= 0 {
		path = env[i][len("PATH="):]
	}
	have := filepath.SplitList(path)
	for _, d := range userBins {
		if rest, ok := strings.CutPrefix(d, "~/"); ok {
			if home == "" {
				continue
			}
			d = filepath.Join(home, rest)
		}
		if st, err := os.Stat(d); err == nil && st.IsDir() && !slices.Contains(have, d) {
			have = append(have, d)
		}
	}
	joined := "PATH=" + strings.Join(have, string(filepath.ListSeparator))
	if i >= 0 {
		env[i] = joined
		return env
	}
	return append(env, joined)
}

type handler func(ctx context.Context, c gateway.Caller, params json.RawMessage) (any, error)

// RPC answers one pool request.
func (p *Pool) RPC(ctx context.Context, c gateway.Caller, typ string, params json.RawMessage) (any, error) {
	h, ok := p.handlers()[typ]
	if !ok {
		return nil, protocol.Errorf(protocol.CodeUnsupported, "unsupported message type %q", typ)
	}
	// Default deny: everything is the owner's (the app, or the phone for its few requests) except
	// what an agent does for itself. Agents' CLI calls arrive on the unix socket, which never counts
	// as the owner, so no agent can decide an approval, change the Compass or the team, or reconfigure
	// the host, whatever it sends.
	// A paired device is never an agent: its own requests only.
	if !c.Owner && !(c.Phone && (typ == "muse.pair" || c.Device != "" && museAllowed[typ])) && !(c.Terminal && terminalAllowed[typ]) && !(agentAllowed[typ] && !c.Phone) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "%s is for the owner, in the PenguPool app", typ)
	}
	if err := p.bindSession(c, params); err != nil {
		return nil, err
	}
	return h(ctx, c, params)
}

// terminalAllowed are the owner's terminal requests (the CLI outside any agent session, with the
// host's token): `pengupool new` and the desktop app's environment menu,
// which lists and opens the owner's SSH hosts through the CLI, and `pengupool experience import` and `export`. Never an approval, Compass or team
// decision: those stay in the app.
var terminalAllowed = map[string]bool{"session.launch": true, "node.list": true, "node.connect": true, "experience.import": true, "experience.export": true,
	"doctor.record": true, "doctor.removed": true}

// agentAllowed are the requests an agent makes for itself, through the CLI. Each one checks the
// session it acts for, and none of them decides anything for the owner: a team change is only
// requested, and waits for the owner's approval. Agents never write the Compass or logs, except that a
// session PenguPool asked words the owner's preference once (compass.word).
var agentAllowed = map[string]bool{
	"hook.event": true, "message.send": true, "team.request": true, "search": true, "memory.search": true, "experience.search": true, "experience.show": true,
	"compass.word":         true, // an asked session words the owner's preference once (compassWord)
	"decision.ask":         true, // asks the owner's decision model typed questions; it decides nothing for the owner
	"ask.create":           true, // puts a question to the owner; only the owner or a paired device answers it
	"ask.wait":             true, // waits for the answer to the session's own ask
	"ask.answer_for_owner": true, // carries the owner's own words in this session to a child's ask (askAnswerFor)
	"ask.per_compass":      true, // asks the host to decide a subtree item from the owner's Compass; the host decides, not the agent
}

func (p *Pool) handlers() map[string]handler {
	return map[string]handler{
		"team.delete":           bind(p.teamDelete),
		"experience.import":     bind(p.experienceImport),
		"experience.export":     bind(p.experienceExport),
		"doctor.record":         bind(p.doctorRecord),
		"doctor.removed":        bind(p.doctorRemoved),
		"experience.search":     bind(p.experienceSearch),
		"experience.show":       bind(p.experienceShow),
		"experience.list":       bind(p.experienceList),
		"experience.get":        bind(p.experienceGet),
		"experience.delete":     bind(p.experienceDelete),
		"session.launch":        bind(p.launch),
		"shell.open":            bind(p.shellOpen),
		"session.resume":        bind(p.resume),
		"run.restorable":        bind(p.runRestorable),
		"run.restore":           bind(p.runRestore),
		"run.fresh":             bind(p.runFresh),
		"run.dismiss":           bind(p.runDismiss),
		"session.open_terminal": bind(p.openTerminal),
		"session.forget":        bind(p.forget),
		"session.rename":        bind(p.sessionRename),
		"session.describe":      bind(p.sessionDescribe),
		"session.steps":         bind(p.sessionSteps),
		"session.slash":         bind(p.sessionSlash),
		"session.restart":       bind(p.sessionRestart),
		"session.close":         bind(p.sessionClose),
		"session.previous":      bind(p.sessionPrevious),
		"session.adopt":         bind(p.sessionAdopt),
		"team.group":            bind(p.teamGroup),
		"team.rename":           bind(p.teamRename),
		"repo.worktrees":        bind(p.repoWorktrees),
		"repo.branches":         bind(p.repoBranches),
		"repo.pull":             bind(p.repoPull),
		"worktree.remove":       bind(p.worktreeRemove),
		"history.read":          p.historyRead,
		"runlog.read":           bind(p.runlogRead),
		"runlog.delete":         bind(p.runlogDelete),
		"repo.add":              bind(p.repoAdd),
		"repo.remove":           bind(p.repoRemove),
		"skill.add":             bind(p.skillAdd),
		"skill.update":          bind(p.skillUpdate),
		"skill.delete":          bind(p.skillDelete),
		"skill.check":           bind(p.skillCheck),
		"compass.list":          bind(p.compassList),
		"compass.save":          bind(p.compassSave),
		"compass.delete":        bind(p.compassDelete),
		"compass.merge":         bind(p.compassMerge),
		"compass.review":        bind(p.compassReview),
		"compass.apply_merge":   bind(p.compassApplyMerge),
		"compass.keep_one":      bind(p.compassKeepOne),
		"compass.spot_checks":   bind(p.compassSpotChecks),
		"compass.answer_replay": bind(p.compassAnswerReplay),
		"gates.open":            bind(p.gatesOpen),
		"checkin.answer":        bind(p.checkinAnswer),
		"compass.spot_check":    bind(p.compassSpotCheck),
		"compass.wrong":         bind(p.compassWrong),
		"gates.lines":           bind(p.gatesLines),
		"search":                bind(p.search),
		"memory.search":         bind(p.search), // the name before 0.11, kept one release
		"compass.word":          bind(p.compassWord),
		"fs.dirs":               bind(p.fsDirs),
		"fs.create":             bind(p.fsCreate),
		"fs.rename":             bind(p.fsRename),
		"fs.trash":              bind(p.fsTrash),
		"team.messages":         bind(p.teamMessages),
		"marketplace.list":      bind(p.marketplaceList),
		"marketplace.refresh":   bind(p.marketplaceRefresh),
		"marketplace.set":       bind(p.marketplaceSet),
		"marketplace.install":   bind(p.marketplaceInstall),
		"task.create":           bind(p.taskCreate),
		"task.repropose":        bind(p.taskRepropose),
		"task.complete":         bind(p.taskComplete),
		"task.cancel":           bind(p.taskCancel),
		"task.fail":             bind(p.taskFail),
		"team.budget":           bind(p.teamBudget),
		"team.goal":             bind(p.teamGoal),
		"proposal.edit":         bind(p.proposalEdit),
		"approval.decide":       p.approvalDecide,
		"ask.create":            bind(p.agentAsk),
		"ask.wait":              bind(p.askWait),
		"ask.answer_for_owner":  p.askAnswerForRPC,
		"ask.per_compass":       p.askPerCompassRPC,
		"answer.wrong":          bind(p.answerWrong),       // the owner's "That was wrong" on a Compass decision
		"answer.per_compass":    bind(p.perCompassHistory), // the Compass decisions and refusals, for the app
		"team.lead_compass":     bind(p.teamLeadCompass),   // the owner's per-team toggle
		"ask.answer":            p.askAnswer,
		"team.place_helper":     bind(p.placeHelperRPC),
		"team.formation":        bind(p.teamFormation),
		"team.request":          bind(p.teamRequest),
		"team.close_preview":    bind(p.closePreview),
		"team.close_helper":     bind(p.closeHelperRPC),
		"team.close":            bind(p.teamClose),
		"team.create":           bind(p.teamCreate),
		"team.add_session":      bind(p.teamAddSession),
		"team.policy":           bind(p.teamPolicy),
		"message.send":          bind(p.messageSend),
		"ask.settle":            bind(p.askSettle), // the owner's, from Needs you
		"hook.event":            bindCaller(p.hookEventFrom),
		"decision.providers":    bind(p.providers),
		"decision.ask":          bindCaller(p.decisionAsk),
		"provider.save":         bind(p.providerSave),
		"provider.check":        bind(p.providerCheck),
		"provider.models":       bind(p.providerModels),
		"provider.activate":     bind(p.providerActivate),
		"settings.tailscale":    bind(p.settingsTailscale),
		"settings.get":          bind(p.settingsGet),
		"triage.status":         bind(p.triageStatus),
		"settings.save":         bind(p.settingsSave),
		"sync.get":              bind(p.syncGet),
		"sync.set":              bind(p.syncSet),
		"sync.now":              bind(p.syncNow),
		"sync.repos":            bind(p.syncRepos),
		"sync.compact":          bind(p.syncCompact),
		"muse.next":             p.museNext,
		"muse.answer":           p.museAnswer,
		"muse.say":              p.museSay,
		"muse.sessions":         p.museSessions,
		"muse.compass":          p.museCompass,
		"muse.action":           p.museAction,
		"muse.member":           p.museMember,
		"muse.pair":             p.musePair,
		"muse.pairCode":         p.musePairCode,
		"muse.devices":          p.museDevices,
		"muse.revoke":           p.museRevoke,
		"backup.get":            bind(p.syncGet), // the old names, kept one release
		"backup.set":            bind(p.syncSet),
		"backup.now":            bind(p.syncNow),
		"backup.repos":          bind(p.syncRepos),
		"backup.compact":        bind(p.syncCompact),
		"update.check":          bind(p.updateCheck),
		"update.download":       bind(p.updateDownload),
		"harness.probe":         bind(p.harnessProbe),
		"node.add":              bind(p.nodeAdd),
		"node.remove":           bind(p.nodeRemove),
		"node.check":            bind(p.nodeCheck),
		"node.unlock":           bind(p.nodeUnlock),
		"node.forget_password":  bind(p.nodeForgetPassword),
		"node.keys":             bind(p.nodeKeys),
		"node.key_setup":        bind(p.nodeKeySetup),
		"node.connect":          bind(p.nodeConnect),
		"node.list":             bind(p.nodeList),
		"github.login":          bind(p.githubLogin),
		"github.status":         bind(p.githubStatusRPC),
		"github.logout":         bind(p.githubLogout),
		"github.cancel":         bind(p.githubCancel),
		"github.switch":         bind(p.githubSwitch),
		"github.repos":          bind(p.githubRepos),
		"github.propose":        bind(p.githubPropose),
	}
}

// bind adapts a typed handler: params decode into In, unknown fields are rejected.
func bind[In any](fn func(ctx context.Context, in In) (any, error)) handler {
	return bindCaller(func(ctx context.Context, _ gateway.Caller, in In) (any, error) { return fn(ctx, in) })
}

func bindCaller[In any](fn func(ctx context.Context, c gateway.Caller, in In) (any, error)) handler {
	return func(ctx context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
		var in In
		if len(raw) > 0 {
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidArgument, "params: %v", err)
			}
		}
		return fn(ctx, c, in)
	}
}

// update changes the domain under the store's single writer.
func (p *Pool) update(fn func(st *store.State) error) error {
	return p.st.Update(fn)
}

func now() string { return store.RFC3339(time.Now()) }

// at is when the Compass is written and read: now, or the replayed time of an offline evaluation.
func (p *Pool) at() time.Time {
	if p.clock != nil {
		return p.clock()
	}
	return time.Now()
}

func invalid(format string, a ...any) error {
	return protocol.Errorf(protocol.CodeInvalidArgument, format, a...)
}

func notFound(format string, a ...any) error {
	return protocol.Errorf(protocol.CodeNotFound, format, a...)
}

func conflict(format string, a ...any) error {
	return protocol.Errorf(protocol.CodeConflict, format, a...)
}

func sessionIndex(st *store.State, id string) int {
	for i := range st.Sessions {
		if st.Sessions[i].ID == id {
			return i
		}
	}
	return -1
}

func (p *Pool) session(id string) (store.Session, bool) {
	for _, s := range p.st.State().Sessions {
		if s.ID == id {
			return s, true
		}
	}
	return store.Session{}, false
}

func findTeam(d *protocol.Domain, id string) *protocol.Team {
	for i := range d.Teams {
		if d.Teams[i].ID == id {
			return &d.Teams[i]
		}
	}
	return nil
}

func findTask(d *protocol.Domain, id string) *protocol.Task {
	for i := range d.Tasks {
		if d.Tasks[i].ID == id {
			return &d.Tasks[i]
		}
	}
	return nil
}

func findSkill(d *protocol.Domain, id string) *protocol.Skill {
	for i := range d.Skills {
		if d.Skills[i].ID == id {
			return &d.Skills[i]
		}
	}
	return nil
}

func findRepo(d *protocol.Domain, id string) *protocol.Repo {
	for i := range d.Repos {
		if d.Repos[i].ID == id {
			return &d.Repos[i]
		}
	}
	return nil
}

// teamOf returns the active team a session belongs to.
func teamOf(d *protocol.Domain, sessionID string) *protocol.Team {
	for i := range d.Teams {
		t := &d.Teams[i]
		if t.State != "active" {
			continue
		}
		for _, m := range t.Members {
			if m.SessionID == sessionID && m.State == "active" {
				return t
			}
		}
	}
	return nil
}

func (p *Pool) decider() decision.Provider {
	p.mu.Lock()
	td := p.testDecider
	p.mu.Unlock()
	prov := td
	if prov == nil {
		prov = decision.FromConfig(p.st.Config().Decision, p.logf)
	}
	if prov == nil {
		return nil
	}
	return watched{prov, p}
}

const triageUnchecked = "\x00unchecked"

// watched records whether the triage model answered its last call: the app's "set up a triage
// model" prompt shows while it is missing or unreachable (triageStatus).
type watched struct {
	decision.Provider
	p *Pool
}

// clientWait is how long the provider's own client waits for the model (decision.New's 30 s): a
// call that ran that long says the model is not answering, whoever's deadline ended it. Tests change it.
var clientWait = 30 * time.Second

func (w watched) Decide(ctx context.Context, req decision.Request) (decision.Response, error) {
	start := time.Now()
	res, err := w.Provider.Decide(ctx, req)
	// A call its caller cancelled or cut short (a hook's budget) says nothing about the model; one that
	// ran the client's whole wait does, even when the caller's own deadline ended it first. The bar is
	// a second short of the wait (1/30 of it), so a caller whose deadline equals the wait clears it
	// with a real margin rather than by timer lag.
	if ctx.Err() == nil || time.Since(start) >= clientWait-clientWait/30 {
		w.p.setTriage(err)
	}
	return res, err
}

// setTriage keeps the triage model's last outcome: "" after it answered, else why it did not.
func (p *Pool) setTriage(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.triageErr = ""
	if err != nil {
		p.triageErr = err.Error()
	}
}

// checkTriage runs the active model's connection check (at start, and when the active one is saved
// again), so a model that is broken shows as unreachable before any work asks it.
func (p *Pool) checkTriage(ctx context.Context) {
	c := p.st.Config().Decision
	pc, ok := c.Providers[c.Active]
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, items, err := decision.Check(ctx, pc)
	if err == nil && slices.ContainsFunc(items, func(i decision.CheckItem) bool { return !i.OK }) {
		err = errors.New("it answered plain questions wrong")
	}
	p.setTriage(err)
}

// triageStatus is "missing" (no triage model set), "unchecked" (set, not asked yet since the host
// started), "unreachable" (its last call or check failed) or "ok".
func (p *Pool) triageStatus(_ context.Context, _ struct{}) (any, error) {
	if p.decider() == nil {
		out := map[string]any{"status": "missing"}
		for _, r := range p.st.Config().Decision.Removed {
			if r.Active { // the active model was removed: the app says why triage has none
				out["removed"] = r
			}
		}
		return out, nil
	}
	p.mu.Lock()
	e := p.triageErr
	p.mu.Unlock()
	if e == triageUnchecked {
		return map[string]any{"status": "unchecked"}, nil
	}
	if e != "" {
		return map[string]any{"status": "unreachable", "error": e}, nil
	}
	return map[string]any{"status": "ok"}, nil
}

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }
