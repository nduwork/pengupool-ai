// PenguPool 2.0 plugin for OpenCode. PenguPool loads it per launch through OPENCODE_CONFIG_CONTENT
// ("plugin": ["file://…"]); it is never installed into ~/.config/opencode.
//
//  - status: loaded → SessionStart (idle), session.status busy → working, session.idle → Stop
//  - context: experimental.chat.system.transform appends the team context PenguPool returns
//  - messages: team messages arrive in $PENGUPOOL_STORE/run/inbox/<session>.jsonl; each is typed into
//    the TUI prompt and submitted while the session is idle, then confirmed with Delivered
//  - the floor: tool.execute.before asks PenguPool (PreToolUse) and throws, which stops the call, on deny or ask
//  - PenguPool failing never breaks OpenCode: every call is bounded and errors are swallowed
import { execFile } from "node:child_process"
import * as fs from "node:fs"
import * as path from "node:path"

const SID = process.env.PENGUPOOL_SESSION_ID || ""
const STORE = process.env.PENGUPOOL_STORE || ""
const CLI = process.env.PENGUPOOL_CLI || "pengupool"
const INBOX = SID && STORE ? path.join(STORE, "run", "inbox", `${SID}.jsonl`) : ""

// run is the hook's output, or null when it could not run (no CLI, a timeout, a failure).
const run = (event, payload) =>
  new Promise((resolve) => {
    if (!SID) return resolve("")
    const child = execFile(CLI, ["hook", "opencode", event], { timeout: 5000 }, (e, out) => resolve(e ? null : String(out).trim()))
    child.stdin?.on("error", () => {})
    child.stdin?.end(JSON.stringify(payload))
  })
const hook = async (event, payload) => (await run(event, payload)) ?? ""

const contextOf = (out) => {
  try {
    return JSON.parse(out)?.hookSpecificOutput?.additionalContext || ""
  } catch {
    return ""
  }
}

export const PenguPool = async ({ client, directory }) => {
  if (!SID) return {}
  let idle = true
  let context = contextOf(await hook("SessionStart", { cwd: directory }))

  if (INBOX) {
    const poll = async () => {
      if (!idle) return
      let raw = ""
      const taking = `${INBOX}.${process.pid}.taking`
      try {
        fs.renameSync(INBOX, taking) // atomic hand-off: the daemon appends to a fresh file after this
        raw = fs.readFileSync(taking, "utf8")
        fs.unlinkSync(taking)
      } catch {
        return
      }
      const msgs = raw.split("\n").filter((l) => l.trim()).map((l) => { try { return JSON.parse(l) } catch { return null } }).filter(Boolean)
      if (!msgs.length) return
      try {
        await client.tui.appendPrompt({ body: { text: msgs.map((m) => String(m.text)).join("\n\n") } })
        await client.tui.submitPrompt()
        idle = false
        void hook("Delivered", { ids: msgs.map((m) => String(m.id)) })
      } catch {
        fs.appendFileSync(INBOX, raw) // the TUI was not ready: put them back for the next poll
      }
    }
    setInterval(() => void poll(), 1000).unref?.()
  }

  return {
    event: async ({ event }) => {
      const p = event.properties || {}
      if (event.type === "session.created" && !p.info?.parentID) {
        context = contextOf(await hook("SessionStart", { session_id: p.info?.id, cwd: directory })) || context
      } else if (event.type === "session.status" && p.status?.type === "busy" && idle) {
        idle = false
        await hook("UserPromptSubmit", { status_only: true })
      } else if (event.type === "session.idle") {
        idle = true
        await hook("Stop", {})
      }
    },
    "tool.execute.before": async (input, output) => {
      const out = await run("PreToolUse", { tool_name: input.tool, tool_input: output.args, tool_use_id: input.callID })
      if (out === null) throw new Error("PenguPool could not check this tool call; ask the owner")
      let h
      try {
        h = JSON.parse(out)?.hookSpecificOutput
      } catch {}
      if (h?.permissionDecision === "deny" || h?.permissionDecision === "ask") throw new Error(h.permissionDecisionReason || "PenguPool: the owner decides this.")
    },
    "chat.message": async () => {
      context = contextOf(await hook("UserPromptSubmit", {})) || context
    },
    "experimental.chat.system.transform": async (_input, output) => {
      if (context && Array.isArray(output?.system)) output.system.push(context)
    },
  }
}
