# Command line

The app includes the `pengupool` command-line tool, at
`PenguPool AI.app/Contents/Resources/bin/pengupool`; the installer links it into `~/.local/bin` as
`pengupool-ai`. `pengupool help` lists every command. The store is `~/.pengupool-ai/v2` (override with
`--store` or `PENGUPOOL_HOME`).

## Your own terminal

| Command | Does |
| --- | --- |
| `new [--name N] [--cwd DIR] [--harness claude\|muse\|codex\|pi\|omp] [--model M]` | Start an agent session (Claude Code by default) |
| `ls [--json]` | List sessions |
| `attach <session>` | Attach this terminal to a session |
| `kill <session>` | Stop a session |
| `web` | Print the browser app's URL for the running host |
| `doctor [--fix] [--restore]` | Check this machine: agent CLIs, keychain, what reaches the Compass |
| `experience export FILE` / `experience import FILE` | Back up or restore specialists' journals |
| `daemon [--store DIR] [--http ADDR]` | Run the host yourself (the app normally does this) |
| `version` | Print the version |

## Inside an agent session

Agents run these themselves; PenguPool tells each agent the exact command, with the tool's full path
quoted.

| Command | Does |
| --- | --- |
| `send --to MEMBER [--type tell\|ask\|done\|blocked] TEXT` | Message a teammate (parent or child on the team's tree) |
| `search WORDS` | Search your Compass and the specialist's own journal |
| `experience search WORDS` / `experience show ID` | A specialist's own journal entries |
| `ask [--owner-only] [--option O]... QUESTION` | Ask you a question; waits for the answer |
| `team request add --skill S \| remove MEMBER` | Ask you to change who is on the team |
| `decide` | Ask your decision model typed questions (JSON in, JSON out) |

Only the first four can be allowed without a prompt (see
[Agent permissions](harnesses.md#agent-permissions)). Requests that change teams, approvals or the
Compass come from agents only as requests you answer.

For the phone and agent-client APIs, see [Phone access](phone.md).
