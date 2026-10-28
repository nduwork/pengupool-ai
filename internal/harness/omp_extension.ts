// PenguPool AI extension for OMP and pi (OMP is a pi fork with the same extension API). PenguPool
// passes it with --hook (OMP) or -e (pi) at launch; it is never installed into ~/.omp or ~/.pi, so
// it cannot collide with another copy.
//
//  - status: agent_start → working, agent_end (unless willContinue) → idle, plus the context %
//  - context: before_agent_start appends the team context PenguPool returns to the system prompt
//  - messages: team messages arrive in $PENGUPOOL_STORE/run/inbox/<session>.jsonl; each one is handed
//    to pi.sendUserMessage with agent attribution, which starts a turn when the session is idle
//  - the floor: tool_call asks PenguPool (PreToolUse) and blocks the call when it answers deny or ask
//  - a subagent or a one-shot run is not the pool session and reports nothing
//  - PenguPool failing never breaks the OMP session: every call is bounded and errors are swallowed
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent"
import { execFile } from "node:child_process"
import * as fs from "node:fs"
import * as path from "node:path"

const SID = process.env.PENGUPOOL_SESSION_ID || ""
const STORE = process.env.PENGUPOOL_STORE || ""
const CLI = process.env.PENGUPOOL_CLI || "pengupool"
const HARNESS = process.env.PENGUPOOL_HARNESS || "omp"
const INBOX = SID && STORE ? path.join(STORE, "run", "inbox", `${SID}.jsonl`) : ""

// run is the hook's output, or null when it could not run (no CLI, a timeout, a failure).
const run = (event: string, payload: Record<string, unknown>) =>
  new Promise<string | null>((resolve) => {
    if (!SID) return resolve("")
    const child = execFile(CLI, ["hook", HARNESS, event], { timeout: 5000 }, (e, out) => resolve(e ? null : String(out).trim()))
    child.stdin?.on("error", () => {}) // EPIPE from a CLI that exited early must not end omp
    child.stdin?.end(JSON.stringify(payload))
  })
const hook = async (event: string, payload: Record<string, unknown>) => (await run(event, payload)) ?? ""

interface Ctx {
  agent?: { kind: "main" | "sub" }
  mode?: string
  cwd?: string
  isIdle?(): boolean
  getContextUsage?(): { percent: number } | undefined
  sessionManager?: { getSessionId(): string; getSessionFile(): string | undefined }
}

export default function (pi: ExtensionAPI) {
  const pooled = (ctx: Ctx) => !!SID && ctx.agent?.kind !== "sub" && ctx.mode !== "print" && ctx.mode !== "json"
  let last: Ctx | undefined
  const pct = (ctx: Ctx) => {
    const p = ctx.getContextUsage?.()?.percent
    return typeof p === "number" ? p : undefined
  }
  const bind = async (ctx: Ctx) => {
    if (!pooled(ctx)) return
    last = ctx
    const wanted = process.env.PENGUPOOL_SESSION_NAME
    delete process.env.PENGUPOOL_SESSION_NAME // only the first start names the session
    const api = pi as any // pi and OMP differ here; neither may lack it, but never fail on it
    if (wanted && api.getSessionName && !api.getSessionName()) api.setSessionName?.(wanted)
    await hook("SessionStart", {
      session_id: ctx.sessionManager?.getSessionId(),
      transcript_path: ctx.sessionManager?.getSessionFile(),
      cwd: ctx.cwd,
      context_pct: pct(ctx),
    })
  }
  pi.on("session_start", async (_e, ctx) => bind(ctx as Ctx))
  pi.on("session_switch", async (_e, ctx) => bind(ctx as Ctx))
  pi.on("session_branch", async (_e, ctx) => bind(ctx as Ctx))
  pi.on("agent_start", async (_e, ctx) => {
    if (pooled(ctx as Ctx)) await hook("UserPromptSubmit", { status_only: true })
  })
  pi.on("agent_end", async (e: any, ctx) => {
    if (e?.willContinue || !pooled(ctx as Ctx)) return
    last = ctx as Ctx
    await hook("Stop", { context_pct: pct(ctx as Ctx) })
  })
  pi.on("turn_end", async (_e, ctx) => {
    if (pooled(ctx as Ctx)) await hook("Status", { context_pct: pct(ctx as Ctx) })
  })
  pi.on("before_agent_start", async (event: any, ctx) => {
    if (!pooled(ctx as Ctx)) return
    const out = await hook("UserPromptSubmit", { prompt: String(event.prompt || "") })
    let text = ""
    try { text = JSON.parse(out)?.hookSpecificOutput?.additionalContext || "" } catch {}
    if (!text) return
    const sp: unknown = event.systemPrompt
    return { systemPrompt: Array.isArray(sp) ? [...sp, text] : `${sp}\n\n${text}` } as any
  })
  pi.on("tool_call", async (e: any, ctx) => {
    if (!pooled(ctx as Ctx)) return
    const out = await run("PreToolUse", { tool_name: String(e.toolName), tool_input: e.input, tool_use_id: e.toolCallId })
    if (out === null) return { block: true, reason: "PenguPool could not check this tool call; ask the owner" }
    try {
      const h = JSON.parse(out)?.hookSpecificOutput
      if (h?.permissionDecision === "deny" || h?.permissionDecision === "ask") return { block: true, reason: String(h.permissionDecisionReason || "PenguPool: the owner decides this.") }
    } catch {}
  })
  pi.on("session_shutdown", async (_e, ctx) => {
    if (pooled(ctx as Ctx)) await hook("SessionEnd", {})
  })

  if (INBOX) {
    const poll = () => {
      let raw = ""
      const taking = `${INBOX}.${process.pid}.taking`
      try {
        fs.renameSync(INBOX, taking) // atomic hand-off: the daemon appends to a fresh file after this
        raw = fs.readFileSync(taking, "utf8")
        fs.unlinkSync(taking)
      } catch { return }
      const ids: string[] = []
      for (const line of raw.split("\n")) {
        if (!line.trim()) continue
        try {
          const m = JSON.parse(line)
          const idle = last?.isIdle?.() ?? true
          pi.sendUserMessage(String(m.text), idle ? { attribution: "agent" } as any : { deliverAs: "followUp", attribution: "agent" } as any)
          ids.push(String(m.id))
        } catch {}
      }
      if (ids.length) void hook("Delivered", { ids })
    }
    setInterval(poll, 1000).unref?.()
  }
}
