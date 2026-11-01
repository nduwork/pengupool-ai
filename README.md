<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/screenshots/banner-dark.png">
  <img alt="PenguPool AI: your coding-agent team, guided by your Compass, within reach from Muse" src="assets/screenshots/banner-light.png">
</picture>

<p align="center">Run Muse Code and other supported agents side by side, carry your preferences into their sessions, and use the Muse app to follow work and answer supported team requests from your phone.</p>

<p align="center">
  <a href="https://github.com/nduwork/pengupool-ai/releases/latest">Download</a> ·
  <a href="#demo">Watch the demo</a> ·
  <a href="docs/getting-started.md">Get started</a> ·
  <a href="docs/phone.md">Muse setup</a>
</p>

<p align="center"><sub>Built for the Meta Global AI Developer Hackathon · Build for Your World</sub></p>


## What you can do

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/screenshots/screen-map-dark.webp">
  <img alt="The team map: team-lead-agent with three specialists (pr-review-agent on Muse Code) and an SME, a lit route, and an open ask" src="assets/screenshots/screen-map-light.webp">
</picture>

- **Run agents side by side.** Claude Code, Codex, Muse Code, pi and OMP, plus Claude Code through
  Ollama, each in its own terminal session. Sessions keep running when the app or its host restarts.
- **Start a team.** Welcome's *Start with a specialist* starts a team led by team-lead-agent with the
  specialists you pick (research, PR review, security, …), or start one agent in a folder and grow a
  team around it on the map.
- **Say it once.** Tell any session how you want things ("small commits", "never touch the migrations
  folder"). It becomes a line in your **Compass**, and every agent gets your Compass in its context.
  Each specialist also keeps a private **journal** of the jobs it finished and starts its next job with them.
- **Answer what waits on you.** Questions, check-ins and permission prompts collect in *Waiting on
  you*, with enough context to decide. With the Muse app paired, you answer team questions and
  check-ins from your phone; permission prompts are answered in the agent's terminal.

## Built with Meta

**Muse Code does the coding.** PenguPool launches Meta's coding agent and connects its lifecycle hooks
to team context, your Compass and specialists' journals. Muse Code controls its model selection;
PenguPool does not pin a Muse model version.

**Muse keeps you connected.** Pair the Muse app with PenguPool over Tailscale Serve to follow sessions,
send instructions and answer supported team requests. Agents' tool-permission dialogs stay in the
terminal.

**Triage is separate.** PenguPool requires a configured System One decision model for its memory and
check-in judgments. Its local setup default is Together AI's Tev1 through Ollama, which is not a Meta
model. Who does what: [model responsibilities](docs/harnesses.md#who-does-what).

## Install

1. Download `PenguPool-AI-<version>-mac-arm64.dmg` from the
   [latest release](https://github.com/nduwork/pengupool-ai/releases/latest). It runs on macOS on
   Apple Silicon.
2. Open the disk image, drag **PenguPool AI.app** onto Applications, and open it.
3. Install and sign in to at least one coding agent yourself (for example Claude Code). PenguPool runs
   the agents it finds installed; it never installs them.

The app starts its host in the background and keeps its data in `~/.pengupool-ai/v2`. To build from
source instead, see [CONTRIBUTING.md](CONTRIBUTING.md).

## Your first session

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/screenshots/screen-welcome-dark.webp">
  <img alt="The Welcome screen: Start with a specialist, Start in a folder, and a three-step setup card" src="assets/screenshots/screen-welcome-light.webp">
</picture>

1. **Open the app.** The empty Workspace shows Welcome with a short Setup card.
2. **Set a decision model.** PenguPool needs a System One triage model, local (Ollama) or online. In
   Settings → Decision model: pick it, **Save**, **Check connection**, then **Use for triage**.
3. **Decide agent permissions.** Settings → Agents → Agent permissions lets agents run four PenguPool
   team commands without a prompt, or keep asking. Either choice is fine.
4. **Start.** *Start with a specialist* (a team, with a work folder for the specialists) or *Start in a
   folder* (one agent, ⌘N).
5. **Work.** Talk to the agent in its terminal. What needs you appears in *Waiting on you* (the bell)
   and as a notification.

Step by step: [docs/getting-started.md](docs/getting-started.md).


## Demo

<!-- DEMO VIDEO: link once the video exists. -->
<!-- DEMO GIFS: two short loops from motion-video-agent, under assets/media/. -->
The demo video is coming. It walks through a Muse Code session at work, answering its question from
the Muse app, and a specialist reusing its journal on the next job.

## Help

| I want to… | Read |
| --- | --- |
| Install, set up and start my first session | [Getting started](docs/getting-started.md) |
| Work with sessions, teams, the Compass and journals | [Guide](docs/guide.md) |
| Know which agents work and what PenguPool sets up for them | [Agents (harnesses)](docs/harnesses.md) |
| Answer from my phone with the Muse app | [Phone access](docs/phone.md) |
| Fix something that isn't working | [Troubleshooting](docs/troubleshooting.md) |
| Use the command line | [CLI](docs/cli.md) |

## Contribute or report a problem

Report a bug or ask for a feature in [Issues](https://github.com/nduwork/pengupool-ai/issues). To build,
test or change PenguPool, read [CONTRIBUTING.md](CONTRIBUTING.md). Each release's changes are in
[CHANGELOG.md](CHANGELOG.md).

## License

[MIT](LICENSE).

## Credits

- **PAHF** — *Learning Personalized Agents from Human Feedback*, Kaiqu Liang, Julia Kruk,
  Shengyi Qian, et al. (Meta Superintelligence Labs, Princeton University, Duke University),
  [github.com/facebookresearch/PAHF](https://github.com/facebookresearch/PAHF), MIT License.
  The Compass's memory pipeline was compared against PAHF's: its feedback loop (keep only
  personal information, summarise it in one sentence, update after a correction) and its
  finding that stale preferences used confidently hurt most after a change shaped how the
  Compass weighs corrections and fades silent lines. Borrowed: the change-of-preference cue
  phrases and the carve-out for narrower rules in the question that decides whether a
  correction changes a preference (from PAHF's `check_update_prompt`), and the drift rate of its
  shopping personas (682 of 1200 preference lines change) as a scenario in the Compass drift
  benchmark. No PAHF code or data files are included. PAHF is Copyright (c) Meta Platforms,
  Inc. and affiliates, under the MIT License.
