# Command line

The app includes the `pengupool` command-line tool, at
`/Applications/PenguPool AI.app/Contents/Resources/bin/pengupool` when you installed from the disk
image. Building from source with `make install` also links it into `~/.local/bin` as `pengupool-ai`. `pengupool help` lists every command. The store is `~/.pengupool-ai/v2` (override with
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
| `experience export FILE` / `experience import FILE` | Back up or restore helpers' journals |
| `daemon [--store DIR] [--http ADDR]` | Run the host yourself (the app normally does this) |
| `version` | Print the version |

## Inside an agent session

Agents run these themselves; PenguPool tells each agent the exact command, with the tool's full path
quoted.

| Command | Does |
| --- | --- |
| `send --to MEMBER [--type tell\|task\|ask\|done\|blocked] TEXT` | Message a teammate (parent or child on the team's tree) |
| `search WORDS` | Search your Compass and the helper's own journal |
| `experience search WORDS` / `experience show ID` | A helper's own journal entries |
| `ask [--owner-only] [--option O]... QUESTION` | Ask you a question; waits for the answer |
| `answer ITEM_ID` | Carry your answer, typed in this session, to a question waiting below it on the team's tree |
| `team request add --skill S \| remove MEMBER` | Ask you to change who is on the team |
| `compass word` | Word your last words as Compass lines, when PenguPool asks this session to |
| `decide` | Ask your decision model typed questions (JSON in, JSON out) |

The commands in this table can be allowed without a prompt (see
[Agent permissions](harnesses.md#agent-permissions)); every other command still asks. The host checks
each call either way: requests that change teams, approvals or the Compass come from agents only as
requests you answer.

For the phone and agent-client APIs, see [Phone access](phone.md).
