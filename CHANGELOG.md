# Changelog

Every PenguPool AI release, newest first. Each entry describes that release as it shipped; later releases may have changed or removed what it describes. Downloads are on [GitHub Releases](https://github.com/nduwork/pengupool-ai/releases).

## 0.11.3

A Muse Code session now shows **Muse Spark · Meta** and its model on its map card and session header, read from Muse's own status line; the session log records the model each turn. When Muse does not show its model, the label reads **Muse Code · Meta**.

Muse Spark, through the Meta Model API, is the default check on tasks: when a member sends a task to one of its children, Muse Spark weighs the team's layout and picks who should do it. When it picks another child, the task goes there, marked rerouted, and the sender is told why; the sender can send it back with `pengupool send --override "<reason>"`, and the reason is recorded. Each checked task carries Muse Spark's model, pick and reasoning on the message, in both sessions' logs and as a **Muse Spark · Meta** badge in Agent messages. Set it up in Settings → Decision model → **Meta · Muse Spark** with a Meta API key; until a key is stored, nothing changes. Muse Spark answers with minimal reasoning. When it cannot be reached, the task goes as addressed, marked triage unavailable, so the team never waits on it. A `-contributor` model shows a note that Meta may train on the tasks sent.

Whether a helper's finished job is kept in its Experience is now decided by the triage model; a job is skipped only on a confident no, and kept otherwise. A helper with no entries yet is told its Experience exists and how to search it.

With the Tailscale app installed but no `tailscale` command on PATH, Settings showed "invalid character 'T'" under Logged in. The host now runs the app's own binary as the CLI, and when it prints something other than JSON, Settings shows the CLI's own words.

## 0.11.2

A Muse session no longer stays Active after its turn ends. Muse runs subagents, such as its memory reminder, in the session after a turn; their tool calls were taken for a new turn, so the session read Active until its next real turn and messages sent to it waited. Now the session reads Waiting when its turn ends, and messages reach it. A subagent's tool calls are still checked, as before. A Muse session stuck this way before updating recovers at its next turn.

The map now shows a session as **? Need input** while it has a pending ask: an approval or question that the bell and the phone already showed. This includes Muse sessions.

A terminal glitch after a resize can no longer crash a session's worker and take its agent down. Two open panes of the same session no longer fight over its size.

The team roles read **Helper** again, in place of Specialist (the Helper hub, the Helpers page, the Helper pool), and **Builder** in place of SME. This covers the app, the team context agents read, the CLI and the docs. A team member's `role` in the Muse app API reads `BUILDER` or `HELPER` where it read `SME` or `SPECIALIST`. Nothing stored changes.

A helper's page no longer shows the "Does not pass the checklist yet" box. Its checks flagged harmless lines as possible secrets; a helper is checked in its pull request to the hub instead.

0.11.1 was tagged but never published; this release carries its changes, described below.

## 0.11.1

A Specialist hub entry no longer needs a `sha`. An entry without one installs at the latest commit of its `ref` (the repository's default branch when it names none), and each library sync moves the helper to that branch's latest commit, waiting while a session of it runs or its folder has local changes. A renamed default branch is followed. An entry with a `sha` stays pinned to that commit as before. To control what a helper runs, fork its repository and point the entry at the fork. Builds before 0.11.1 skip entries without a `sha`.

A hub entry can credit the work it adapts: its optional `credits` show as a muted *Adapted from* line, with links, in the Specialist hub, the Specialists page and an installed specialist's description. Credits are display-only and never reach an agent's context. A malformed `credits` value drops only that entry's credits.

One wrongly typed field in a hub entry, such as `capabilities` given as a string, no longer empties the whole Specialist hub. Only that entry is skipped, with the reason in the log, and the others load. A helper already installed from it stays as it is.

## 0.11.0

The app is signed with a Developer ID and notarized by Apple. Download the disk image, drag PenguPool AI.app onto Applications and open it: macOS opens it after its usual confirmation for downloaded apps, with no unidentified-developer warning and no Gatekeeper workaround. The app and the disk image each carry their notarization ticket, so the first launch works without the network too. Installing from the disk image is now the one documented way; `make install` stays the development build (CONTRIBUTING.md). Coming from an earlier build, macOS may ask once more for permissions it granted the old one, such as controlling Finder for Move to Trash and local network access.

The in-app updater takes only an update signed by the same Developer ID team as the running app. Before copying the new app out of the disk image it verifies the signature and checks the team against Apple's certificate chain; an update that fails is refused, the installed app reopens, and the update log says why. An ad-hoc development build skips this check.

## 0.10.24

Text an agent copies in the app's terminal now reaches the clipboard. Claude Code copies with the OSC 52 escape sequence, which the terminal dropped, so Cmd/Ctrl+V pasted whatever was copied before. The copy lands on the clipboard of the device viewing the session: in the app window and in Chrome. Safari (including iPhone), Firefox and the plain-HTTP connection refuse a clipboard write outside a click or key press; there a notice says the copy didn't reach the clipboard. A program in a session can never read the clipboard through the terminal. Switching back to a session or reconnecting never puts an old copy back on the clipboard. Other agents and tools that copy the same way (Codex, Gemini CLI, tmux or Neovim with OSC 52 on) work too.

A specialist can join a group with no work repository, such as one made by grouping sessions on the map. Before, adding one failed with "the team has no work repository (a manual group); specialists need one". It now works in a scratch folder of its own. A specialist dropped on a card goes under that card's session instead of the group's main member.

Welcome is off by default: an empty Workspace shows the map, with the Setup card in the sidebar. Welcome is still there: open it from the empty map's *show Welcome* link or Settings → General → **Open Welcome**, or turn on *Show Welcome when Workspace is empty*. A window where that switch was turned on keeps it.

## 0.10.23

Settings → Tailscale switches Serve between HTTP and HTTPS. *Serve over HTTP* turns off PenguPool's own HTTPS route once HTTP serves, so the two no longer both serve. *Serve over HTTPS* now works from HTTP: it serves HTTPS, then turns off PenguPool's own HTTP route. The old mode is turned off only after the new address answers; a 502, a refused connection or a TLS error keeps the old mode up. Another app's route on either port is never touched. If HTTPS cannot start (no certificates on the tailnet), HTTP stays up with its address, and the error is shown.

Tailscale Serve no longer answers 502 after a login or restart until the next click. At start-up the daemon keeps retrying, with backoff for about 2 minutes, until its listener is bound and tailscaled answers. Then it re-points PenguPool's own stale route. Before, it gave up after 2 s, or after one try when tailscaled was not up yet.

Switching Serve between HTTP and HTTPS finishes even if the page reloads mid-switch: turning the old mode off, or turning a new mode that did not answer back off, now runs to the end instead of leaving both modes up. Both Serve buttons are disabled while a switch runs, so a second click cannot queue behind it.

Agent permissions cover the team commands agents are told to run: besides `send`, `search`, `experience search` and `experience show`, now also `ask`, `answer`, `decide`, `team request add`, `team request remove` and `compass word`. PenguPool still checks every call: an answer only from the owner's own prompt, a team change only as a request the owner approves, a Compass line only from the session PenguPool asked. If you turned this on before, the new commands keep asking until you turn the switch off and on; Settings shows them as new.

Text in the app's terminal can now be selected while an agent such as Claude Code has mouse reporting on: hold Option and drag. Option still works as Meta for keys, so Option+Enter types a new line as before. Option-drag now selects lines; column selection is no longer available.

## 0.10.22

A specialist can join a team more than once, each copy in its own session. A further copy gets
its own name (pr-review-agent-2) and its own folder, fixed when it joins, so renaming a copy never
makes two share one. Messages reach each copy by its session. Triage offers one option per
specialist and picks the copy that is idle; the owner may name any copy, and handing the work to
any copy counts. The Add member sheet lists every specialist, including ones already on the team.

Re-proposing a task whose work repository was removed reports that the repository is gone instead of crashing the host.

`scripts/uninstall.sh --purge` deletes only this version's data folder (`~/.pengupool-ai/v2`, or `v2` under PENGUPOOL_HOME) and the parent only when it is then empty, never the whole PENGUPOOL_HOME.

A restarted or restored team specialist keeps the model it was started with and its read access to the team's repositories.

The daemon log no longer records the owner token: it names only the browser app's address (`pengupool web` prints the signed-in link). The store, its logs folder and daemon.log are now private to the owner (folders 0700, the log 0600), including stores made by earlier versions. A daemon.log written before this release still holds the token; delete it to remove the token from disk.

The README opens with an at-a-glance section: what PenguPool is, how to run it, and an architecture sketch that links CONTRIBUTING's package map.

Internal: a dead variable in team proposals is removed (no behaviour change).

## 0.10.21

The map's Compact context and Clear actions work on Muse sessions (Muse /compact and /new).

Muse sessions idle reliably at turn end. A tool hook processed after its turn's Stop no longer
flips the session back to working (it arrived before the Stop, so it is ignored as stale), so
queued team messages deliver at turn end instead of waiting for hours. Status changes to working
are logged like idle ones.

Tailscale Serve follows the daemon instead of guessing it. Enabling Serve and the start-up heal
wait for the daemon's bound port rather than assuming 7787, so a restart onto a new port no longer
leaves Serve pointing at nothing. Both Serve modes are first-class: a stale HTTPS setup is
re-pointed over HTTPS and a stale plain-HTTP setup over HTTP, never switched; a fresh setup gets
HTTPS, and on a tailnet without HTTPS certificates Serve reports that instead of silently
falling back to plain HTTP. PenguPool remembers the
port it served and re-points only its own Serve routes, so another app published through Serve is
left alone: the Serve button reports a route it would take over instead of replacing it. The Settings checklist accepts either mode.

**Serve over HTTP.** Settings has a second Tailscale button that serves plain HTTP on :80 even on a
tailnet with HTTPS certificates; it is allowed, never gated, and Settings shows a warning that
plain HTTP is WireGuard-encrypted but not TLS. The pairing URL a device scans keeps that scheme, and the
checklist's Reachable row probes http:// when only HTTP serves the app.

The Muse companion reaches the device API through Tailscale Serve: HTTPS by
default, plain HTTP where the tailnet has no HTTPS certificates. The
workstation is set with `PENGUPOOL_HOST` (name or IP, required) and
`PENGUPOOL_HOST_HEADER` (the tailnet name, defaulting from the host); the
device token still lives beside the installed code only. `PENGUPOOL_HOST` may
also be the pairing URL the app shows: its scheme picks HTTPS or HTTP and an explicit port in it
is kept. Plain HTTP stays allowed for tailnets without HTTPS certificates and prints one warning
that it is WireGuard-encrypted but not TLS.

Removed: the companion setup's **Local network** path (turn Local network on,
no Serve needed) is gone from SETUP.md and README.md, since the daemon no
longer listens on the network; the plaintext daemon-port transport is no
longer the default and stays only as `PENGUPOOL_TRANSPORT=direct` for older
builds. install.sh no longer asks to place the daemon bootstrap token on the
Muse machine; it holds the paired device token only.

## 0.10.20

An optional Welcome with two ways to start. **Start with a specialist** starts a new team led by
team-lead-agent, with the specialists you tick as members: seven commonly useful ones are suggested
(research, PR review, dev workflow, security, API design, CI and data analysis), and a search reaches
every installed specialist. A folder is asked for whenever a specialist is picked, since they work in a
repository; the lead alone needs none. If the lead does not start, no team is created; a specialist that
does not start is reported, with Retry, and the team keeps its lead. **Start in a folder** opens New
session. A three-row Setup card (agent, decision model, agent permissions) links to each setting.
"Show Welcome when Workspace is empty" turns it off, per window; Settings → General and the empty
map reopen it. The triage banner's Open Settings goes straight to the Decision model.

## 0.10.19

A status report no longer reads as waiting on the owner. The turn-end question "does the
reply end by asking the owner to confirm, approve or choose" now says that a report is not waiting,
even when it says the agent will bring something to the owner later or is waiting on a teammate,
unless it also ends by asking the owner to confirm, approve or choose.

## 0.10.18

Triage uses System One decision models only. The "Ollama chat" format (a general model
over /v1/chat/completions) is removed: it gave no calibrated confidence, yet several checks took its
yes as sure. A saved chat-format model is removed at start, and the app says so: a notice in Settings
until that model is saved again, and the triage banner when it was the active one. The Decision model
panel drops the API format menu, always lists Ollama's decision models, and shows one status line for
where the saved settings stand (not saved, unsaved changes, not checked, checking, failed, passed, in
use); the filled button follows the next step: Save, then Check connection, then Use for triage.
Agent permissions (Settings → Agents): once the owner allows it, PenguPool writes rules into Claude
Code's settings.json and its own file in Codex's rules folder so agents run four PenguPool commands
(send, search, experience search, experience show) without a permission prompt, in every session of
those agents, also outside PenguPool. Turning it off removes exactly those lines; a moved app gets
its lines rewritten at start. Agents are now told to run the CLI with its path shell-quoted, the
spelling the rules match. Theme font sizes no longer drop when a colour class follows them, so
default buttons keep their 13px label size.

## 0.10.17

A model is used for triage only after its connection check passes. Settings keeps Use for
triage off, with a line saying why, until Check connection passes for the provider's saved settings; the
host holds that pass and refuses to make a model active without it ("Run Check connection first"), and
saving the provider again clears it. A model already active stays active: its check at start still
decides whether the app shows it as answering.

## 0.10.16

Agents wait for the triage model instead of cutting it off. A turn's route question and the
go-ahead decided in an agent's Stop now give the model 8 seconds (they gave it 1.5 and 2, while a local
model can take several), and the hooks around them wait long enough (the CLI 11 s for a prompt, 12 s for
a Stop; Claude Code and the pi/OMP extension now give a hook 15 s, as Codex, Gemini and Copilot already
did). A call cut short by its caller's own time limit no longer marks the triage model as not
answering, unless it had already waited as long as the model's own 30-second limit: a model that
fails, or never answers in that time, still does.

## 0.10.15

The Waiting on you panel shows each question whole, wrapped inside the panel. Every item
reads the same way: who asks, their role, their team and how long it has waited; then the question,
cut at four lines with Show all; then the agent's last words for a check-in or the call for a permission
prompt, cut at three lines; then the buttons on their own line. Long choices wrap inside their buttons,
the panel is a little wider and never taller than the window (the list scrolls, the header stays), and
a go-ahead to check has Right and Wrong in words. A check-in now carries the last 1200 characters of the
agent's reply, up from 300.

## 0.10.14

A terminal restored from a snapshot (a reattach, a second viewer) keeps the agent's whole
stack of kitty keyboard modes, not only the top one, so when the agent pops a mode its keys read as
they do on the original terminal; popping past the bottom of the stack clears the modes, as the kitty
spec says. In the web terminal Shift+Enter now types a new line in pi (Ctrl+J; ESC CR submitted
there) and stays ESC CR elsewhere. Checked live: pi; established: Claude Code; the other harnesses
are not checked live. A test lists every harness against what Shift+Enter sends, so a new harness
cannot be added without that being checked.

## 0.10.13

Codex and OMP sessions raise the same needs-input alerts as Claude. A Codex approval prompt
(its PermissionRequest hook) and an OMP approval dialog or ask-tool question put the session in the one
list and notify; the alert goes once it is approved, denied, interrupted (Codex Interrupt) or the turn
ends. pi has no approval dialog or ask tool, so it raises none; its check-ins still come, as for every agent.

## 0.10.12

The helper role is now SPECIALIST, beside LEAD and SME: a skill-backed member that carries its
own journal across teams. The app says Specialists (the page, the hub, the map's pool), a member card
reads SPEC with the tooltip Specialist, and the team member list and guidance agents read name the
role SPECIALIST; so do the CLI help, errors and run log lines. Stored data and the protocol keep the
value "helper".

## 0.10.11

A team whose LEAD is a SPECIALIST keeps one project entry in that specialist's journal. The
lead's context asks it to preserve a self-contained picture of the project when its harness compacts the
session; at the lead's next turn end PenguPool keeps that compaction summary (Claude Code, pi and OMP
sessions; Codex encrypts its compactions, so a Codex LEAD keeps none), under the host's facts about
the team at that moment (team, latest owner instruction, members with their labels and assignments,
repositories, and the compaction it came from), and it replaces the team's previous project entry. Only
compactions that happen anyway count: PenguPool types nothing. A team led by an SME or a session with no
skill gets nothing new, and a team that closes without a compaction keeps no project entry. The entry is
read like any other, through the specialist's own `experience search` and `show`.

## 0.10.10

The map's hover card shows a session's question, last message and folder whole, and a card
taller than the room it has scrolls; the pointer can move into it to scroll, and it keeps clear of the
screen edges. The same holds for every tip in the app.

## 0.10.9

No rules stand in for the triage model. Without one, or when a call to it fails, PenguPool
judges nothing: the owner's words are not read for preferences (no sentence patterns, no session agent
linking lines), a turn is not placed by its tool count or length, a check-in decision makes no line, a team
proposal is the main worker with a note, and a lead's per-Compass decision is refused with "no triage
model is set up". Words left unjudged are not judged later either; the app's prompt keeps that gap in view
until a model works. Check-ins, permissions and asks still go to the owner. `compass word` loses
`--supports` and `--contradicts`: which existing lines the owner's words move is the model's call, and the
session's agent only words new lines.

## 0.10.8

PenguPool needs a triage model, local (Ollama) or remote. Settings can no longer set it to
none, and a model becomes active only once its check passes. The app asks for one under the title bar on
every page while none is set or the set one stops answering, and the prompt goes as soon as one works.
"If the model fails, use simple matching" is removed; a config that still has it is told so once.

## 0.10.7

One place for what waits on the owner. The bell's count, the dock badge and the bell's panel
are one list: team launches and changes, an agent's question, check-ins, permission dialogs, go-aheads to
check and unanswered asks, each with its action. An item leaves it as soon as it is answered anywhere (the
app, the phone, its terminal, through the lead), and its desktop notification is withdrawn. The Inbox,
which kept a copy of agent messages for a day, is gone; the needs-you bar lives in the panel.

## 0.10.6

Needs input stays true to what waits on the owner. A permission prompt keeps its needs-input
status until that same call runs or the turn ends; an unrelated parallel tool no longer clears it. An
AskUserQuestion needs the owner from its first moment. A session stuck on a first-run dialog (trust,
login, setup) shows in Needs input, and an agent's question notifies as "An agent asks you", not
"Team launch ready".

## 0.10.5

Memory reads honestly. Experience search ranks by relevance first; the same team only
breaks ties, then the newest. A helper's past-work list leaves common words out of its query, and with
no match it says "Recent work (no matching entries)" instead of offering the newest as a best match; it
shows whole rows only, within 2 KiB and at most 10. A Compass line is shown whole or not at all (no
clipping that loses its condition), and a condition worded "when …" no longer reads "when when".

## 0.10.4

A helper's journal entry is its Done report itself, kept whole: PenguPool no longer types the
agent's compact command or reads its compaction summary, which mixed every task since the last
compact. Helpers are told how to write that report: a title of the subject and its result, one
task's transferable lesson (not the diff git holds), durable know-how dated, the evidence and its
limit, and never a credential's value. The owner's `pengupool experience export FILE` writes every entry
in full, and `experience import` takes a `drop` list (each with a reason) that it deletes first, so a
rebuild is one export and one import.

## 0.10.3

A typed answer to an ask or check-in (the owner's words PenguPool types into the member, such
as "Answer to your ask ID: …") answers only that: it no longer becomes the team's current task.

## 0.10.2

Three stability fixes in the Compass and journal. A line the owner wrote or confirmed holds
until they replace or delete it: it no longer fades with time, and evidence against it never sweeps it
(inferred lines still do both). A helper's done report waiting for its compaction is kept as its
journal entry when the host shuts down, and an entry with a summary keeps the report under it. A
Compass signal and its effects are written together, so none is left without the effects it had.

## 0.10.1

Two fixes to answers through the lead. The shown check reads the last 8192 characters of the
lead's previous reply, not 1500, so an id near the top of a long reply is no longer missed. A reply that failed no longer closes an ask; any
other reply from the asked member still does, also one still queued for a busy asker.

## 0.10.0

Release of 0.9.39 to 0.9.48 as one build: the Compass curation (0.9.39, 0.9.40), answers
through the lead (0.9.41, 0.9.47, 0.9.48), OpenCode and Local network removed (0.9.44, 0.9.45) and Sync
with GitHub waiting for a picked repository (0.9.46). No changes of its own.

## 0.9.48

The lead may ask PenguPool to decide a waiting item from the owner's Compass:
`pengupool answer ID --per-compass`. PenguPool decides, never the lead: only the owner's confident,
trusted lines (not permission lines), judged by a calibrated decision model at 0.9 for every ask and
check-in, once per item per Compass version, so a refused item is asked again only after the owner's
lines change. Owner-only asks are never decided this way. An ask is answered with the chosen option
("lead per Compass line <uid>"); a check-in gets triage's go-ahead quoting the line. The app's bell
lists what was decided for the owner, with "That was wrong" (a correction against the line, and a
notice to the member), and each team's settings have "Lead decides from your Compass" to turn it off.

## 0.9.47

The owner's words reach the whole subtree. Any active member above another on the team's
tree (a closed member between them doesn't break the chain) sees the questions waiting on the owner
below it, asks and check-ins, at most 20 listed, and carries the owner's answer with `pengupool
answer ITEM_ID` (`ask answer` stays as an alias). The previous reply must show the item's id and
quote its question as it stands: an ask's first 200 characters, a check-in's last 200 (case,
spacing, `>` and backticks aside), so a paraphrase answers nothing. A check-in (`checkin:<id>`) is
answered by typing the owner's whole prompt into the member as the owner's own words, with the app's
check-in guards; the gate records the session it came through, and both run logs carry the full
text. `pengupool ask --owner-only` marks a question only the owner may answer, and helpers are told
to use it when one of their own rules stops them. Every #63 guard stays.

## 0.9.46

Sync with GitHub starts only once the owner picks its repository. Turning it on with none
picked syncs nothing and creates nothing; Settings says to choose a repository, suggesting
pengupool-memory, which is created only if the owner saves that name. A config that already names a
repository syncs as before.

## 0.9.45

Removed: Local network (Settings, `--lan`, and the plain-HTTP pairing URL on the tailnet
name). Phones and other devices reach PenguPool only through Tailscale Serve; the host listens on
loopback alone and refuses any `--http` that is not a loopback address (`:PORT` means
`127.0.0.1:PORT`). A config.json with Local network on is ignored,
with a log line saying so.

## 0.9.44

OpenCode support is removed: it is no longer offered, launched or resumed, and its plugin
is gone. A stored OpenCode session fails to resume with a clear error ("OpenCode is no longer
supported"); start a new session on another agent. The plugin file earlier launches wrote stays on
disk, unread.

## 0.9.41

An ask the owner answers through the lead reaches the member as the owner's own prompt. When
the member is no longer waiting in `pengupool ask`, the owner's whole words are typed in as its next
prompt ("Answer to your ask ID: …"), credited to the owner, so the member's harness sees the owner's
prompt and not a teammate's relay; a waiting member still gets the answer from its wait, and a busy one
gets the words typed at its next stop, once. `pengupool ask "QUESTION" --option A --option B` keeps its options: flags after the question
were read as words of it.

## 0.9.40

A change of mind gets its own Compass line, and the Compass review shows lines that
contradict. When the owner's words only go against existing lines ("red is my favorite" after
"yellow"), the contradiction is recorded at once and the words are worded as a new line, so agents
still see the owner's current preference. With a decision model, the review lists preference lines
that contradict where their conditions overlap: a connected group is one item, each line shown with
its condition. The owner keeps one, and the lines that contradict it directly are deleted with their
evidence; a line that only contradicted another stays for the next review, and the kept line is
unchanged. A line edited since the review is refused ("refresh"). Without a decision model there are
no conflict items, and a review asks the model again only when a shown line changes.

## 0.9.39

The compass agents read holds the owner's prose lines only, guiding lines first, within 8 KiB
for the whole section. Permission answers ("The owner allows Bash for …") stay stored for answering
permission prompts but no longer fill agents' context, and every line is a candidate before any limit,
so a run of newer permission answers can no longer push the owner's preferences out. Each line shows
at most 360 characters, and a line that doesn't fit is skipped rather than ending the list, so one
long line can't hide the rest.

## 0.9.38

The owner can answer a child's waiting ask from the lead's session. The lead's context lists
its direct children's asks, and `pengupool ask answer ASK_ID [--option N]` carries the owner's answer.
The lead chooses no content: the answer is the owner's whole prompt, word for word (with anything
secret-looking left out; a prompt over 2000 characters is refused, never clipped), for the child to read. It is option N only when the whole prompt
is exactly N, "option N" or that option's text. It is accepted only when all of these hold:
- it comes from the lead's own process on this host;
- the lead's turn was opened by a prompt the owner typed;
- no teammate's message could have reached the lead since its previous reply: none pending, none
  delivered since, and no SendMessage to it let through since;
- the ask is a direct child's, made before that prompt, and the lead's previous reply showed it;
- that reply showed exactly one waiting question, or the owner's prompt names this one by its id.

A teammate's message, triage or a notice answers nothing. The answer is recorded as the owner's ("owner
via <session>") with the owner's words; harness permission prompts are not covered.

## 0.9.37

The host keeps each session's terminal history (2000 lines, tmux's default), and snapshots
carry it ahead of the screen (at most 512 KiB, newest lines first). The web app resets before every
snapshot, so it gets the history each time and every viewer shows the same one after a reconnect, a
resize or a session switch. `pengupool attach` writes snapshots into your terminal, so it gets the
history once, on attach. Memory: 112 bytes a cell, so about 27 MB per session at worst with full
120-column lines, 56 MB at 250 columns; typical output far less. Live output is unchanged, and so is
the phone and Muse API. Known limit: Claude Code repaints its live area in place (synchronized
output), and those repaints never become history lines, here or in any terminal
(stablyai/orca#18490).

## 0.9.36

A Claude SendMessage is checked against the session Claude actually delivers it to, the
Claude session of that name. A name shared with another team's session no longer slips past the
team check, and a name two Claude sessions share is refused with a pointer to `pengupool send
--to <session id>`.

## 0.9.35

Team members know each other by session id, not display name, so renaming a session no
longer breaks who reaches whom. The Team members list maps each session id to its current name,
and the send and reply commands PenguPool hands agents name the session id. Agents still call
teammates by display name when they talk to the owner. Sending by name still works.

## 0.9.34

Removed: the team group password backend, unused since v0.9.32 took away the browser
phone view: the team.lock and team.unlock calls, the phone unlock check and the stored password
verifiers. A state.json from an older version still loads; its stored verifiers are ignored, with
no error, and gone from state.json at the next save and from its backup, state.json.bak, at the
save after (backups you made yourself keep them). A team's iOS access still decides whether paired devices see
it.

## 0.9.33

The Muse companion (change detector, status card, answer cards, mini status site) ships
at tools/muse/companion. It uses only the paired-device API with a device token; it never holds the
owner's app token. The mini status site on this machine is a paired device of its own, and its URL
carries a key without which every page is 404. `/sessions` also gives each running agent's status
(working, idle...).

## 0.9.32

The PenguPool app stays on this machine. Over the network (Local network, the tailnet
name, Tailscale Serve) only the paired-device API and the health check answer; the app, its
WebSocket and every other route are 404 there, reachable on this machine or through an SSH
port forward. Removed: the browser phone view (the app opened without the token from a phone or
another machine on the network), and with it the team's group password, which only that view
asked for. Phones use a paired Muse device. A raw TCP forward you set up to the port (SSH,
socat, Serve in TCP mode) counts as this machine, as an SSH forward does; PenguPool's own Serve is
the HTTPS kind.

## 0.9.31

A paired Muse device manages your agent teams: `/sessions` shows each session's team,
role, parent and assignment; a member's ask the team left to you waits on `/next` (go on, reply,
dismiss); `POST /sessions/{id}/action` stops, interrupts or resumes an agent session, with no
error when it already is so; `POST /teams/{id}/members` asks for a team change that waits for
your allow. Teams without iOS access stay hidden, and no agent can use any of it.

## 0.9.30

An agent can put a question only you can settle to you directly (`pengupool ask
[--option A]... "question"`). It waits for your answer, given in the app's Approvals or from a
paired Muse device: one of its options, or a reply in your own words. Other agents cannot answer it. An ask left
unanswered for 15 minutes expires, and the agent goes on with a safe default of its own.

## 0.9.29

A session waiting on you shows what it asks (the permission, the question and its
choices, or the plan to approve) on hover in the app, in `session.list` and in the Muse app's
session list. It is still answered in the session's terminal.

## 0.9.28

The Muse app reaches PenguPool on this machine's own Tailscale name, without Serve
(Local network on); the pairing URL names it. With Local network on, the desktop app no longer
sits at Reconnecting. Pairing a name already paired, or with no name, says so (409, 400) instead
of 502. New guide: tools/muse/muse-app-management.md.

## 0.9.25

New session ▸ New worktree is back: an agent starts in a new worktree of a local
repository, kept in PenguPool's own folder, on a new branch (`pengupool/<name>` by default) or
an existing one.

## 0.9.8

New session and Add member start every agent on its own default model.
Removed: the Model field in New session and in Add member (`pengupool new --model` still
works, and a session resumes on its recorded model).

## 0.9.7

team roles LEAD, SME and HELPER replace the anchor (old anchor data is ignored).
Settings is grouped by topic (General, Agents, Decision model, GitHub, Local network,
Tailscale, Muse app, SSH hosts). New session offers installed agents only, plus Claude Code
(Ollama): Claude through `ollama launch claude` on Ollama's own default model (a missing model
is refused with the `ollama pull` to run). A session waiting on you reads "? Need input" with
a red outline. Muse sessions become ready when their prompt shows, so team messages reach them.
Removed: Set anchor; the Claude Code on Ollama card (address, model, "every session on
Ollama", which moves to New session's default); per-session Ollama models; Ollama for helpers,
team members and the other agents; GitHub Copilot and Gemini CLI from the pickers (their old
sessions still resume); the "(experimental)" labels.

## 0.9.6

the restore question after a reboot also offers Start fresh: the stopped sessions
leave the map and a team with none of its members running is closed (under Closed groups).
Nothing is deleted on disk; scrollback, worktrees, team notes and journals stay.

## 0.9.5

after a reboot or crash ended running sessions, the app asks whether to
restore them (agents resume their conversations). Sessions keep running through an
app quit and an update, and are not asked about.

## 0.9.4

the file preview shows Mermaid diagrams, PDFs and HTML that runs its own
scripts again, and follows a file as an agent writes it. The Tailscale setup is back:
the checklist in Phone connections, the Tailscale app's CLI found when it is not on
PATH, the approval link when Serve needs one, and Serve re-pointed after the host's
port changes.

## 0.9.3

tests hold the host to the app's build (the app restarts an older host;
the daemon writes the version line the app compares). Saving settings no longer
races a Muse device being revoked. `pengupool new` lists muse again.

## 0.9.2

the app again restarts a host that is not its own build (and one that holds
the store but does not answer), so after an update the host runs the new version;
0.9.0 had lost this. `make install` replaces the app in /Applications when it is
there.

## 0.9.1

a session opened by hand in a helper's folder works as that helper again
when the helper lives in a subfolder of its repository, so `experience search`
reaches its journal. Update checks look at this repository's releases (they
answered 404). A remote host whose PenguPool sits in the app, in a folder with a
space, is reached again.

## 0.9.0

updated host and app. Muse Code is an agent (plugin hooks, the hook answer
cap, Muse's read tools, the Muse app API and device pairing). No Teams page: group
settings and close are on the map's group frame, closed groups in the map's
background menu. Copilot and Gemini CLI are experimental agents (OpenCode was, until v0.9.44);
`pengupool doctor` can clean PenguPool v1 out of Claude's settings. A record
written by 0.4–0.8 opens: its search indexes are remade and the experience lessons
column is not kept; update every machine that shares the record together.

## 0.8.5

`pengupool doctor` checks the machine and only reads: PenguPool v1
leftovers, the agent CLIs, a leftover PenguPool MCP server, a Muse plugin that is
off or calls a missing binary or another store, and a host that is doubled, runs
from a disk image, or does not answer.

## 0.8.4

the Helper agent hub: the owner can set the hub's
repository in the Helper agents page; an install still on the old
pengupool-marketplace hub moves to pengupool-agent-hub once (helpers, keys and
journals follow); a bad catalog entry is skipped and named, not fatal.

## 0.8.3

a host that holds the store but no longer answers (run from a disk
image since ejected) is stopped, killed if it must be, and replaced.

## 0.8.2

the app replaces a running host that is not its own build (one from an
older or ejected disk image could not start sessions); a Muse session's hook
answer fits Muse's 16 KiB limit, and Muse's read tools pass a triage hold. Two builds
sharing one store (a dev app and a release app) restart each other's host at
each launch; the app's log says so.

## 0.8.1

the file preview draws Mermaid diagrams in Markdown, runs an HTML
page's scripts with its own assets (isolated), shows PDFs, and follows the
file as it changes; Muse sessions get their hooks again (the app replaces a
PenguPool plugin left from another build instead of failing on it).

## 0.8.0

the Teams page folds into the map (group settings and close on each
group's frame, closed groups from the background menu); a helper's journal
entries carry a one-line lesson from its Done report, and the helper sees the
matching entries with every task, each once per session.

## 0.7.1

a correction is read as a change of preference with PAHF's cue
phrases, and a narrower rule in it is kept as a new line; the drift
benchmark adds PAHF's shopping drift; PAHF credited.

## 0.7.0

Compass lines read as the owner's rule or only likely; search
stems words; hedged words weigh half; lines pulled both ways go to review
(restate or remove); a Gaussian belief runs in shadow beside the Beta one,
reported by `pengupool eval` with a drift benchmark.

## 0.6.2

PenguPool adds no questions beyond the agent's own: a tool call the
host cannot check goes to the agent's permissions, and agents check the
Compass before asking the owner anything else.

## 0.6.1

messages the owner queues while an agent is busy, joined by the
agent into one prompt, still count as the owner's.

## 0.6.0

terminal scrollback kept across session
switches, flexible teams (no size cap, drop on a group, split a subtree) and
backup renamed to sync; agent sessions are resumed automatically after a
power off or crash.

## 0.5.0

the Helper agent hub is `pengupool-agent-hub` (reads its
`.pengupool/catalog.json`); the agent picker lists only installed agents plus
a Terminal option for any other; Settings lists detected agents and drops the
OMP/pi binary-path fields.

## 0.4.0

harness-first. Muse gets hooks (context, Compass, journal), Codex,
OMP and pi promoted (Codex sandbox rule; OMP/pi binary path), Copilot,
Gemini and OpenCode removed; the separate Compass layer (`pengu` CLI, MCP,
owner API) folded into the built-in Compass with a one-time import; a
designated Muse app API with per-device pairing; Tailscale setup checklist.

## 0.3.0

Muse Code harness (interactive sessions in the app, MSP host
client with wire approvals, `muse exec` worker) alongside Claude Code
(account or Ollama-backed).

## 0.2.0

app base (host daemon/gateway/PTY workers, Electron shell,
full web terminal UI) rebranded as PenguPool AI, with the owner-confirmed
Compass integrated into session context and shipped as the `pengu` CLI + MCP
server in the same DMG.
