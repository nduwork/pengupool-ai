#!/usr/bin/env node
// muse-companion mini site — a live PenguPool status page for a phone.
//
// Runs ON the PenguPool machine (see install-host.sh). Serves one phone-
// first page that polls /status.json every 10 s; the server answers each
// poll by reading the owner WebSocket API with the CURRENT bootstrap
// token (read fresh from hub.token on every fetch — the token rotates at
// every daemon start, so nothing is cached or configured by hand).
//
// No dependencies: Node 21+ (global WebSocket) and the standard library.
//
//   node serve.mjs [--port 7790] [--bind tailnet|all|<ip>]
//                  [--ws ws://[::1]:7787/ws]
//                  [--token-file ~/.pengupool-ai/v2/run/hub.token]
//
// Env equivalents: MC_PORT, MC_BIND, MC_WS, MC_TOKEN_FILE.
//
// Exposure: the default bind is this machine's Tailscale address only, so
// the page is reachable from your own devices on your tailnet and from
// nowhere else. Session names and statuses are all it shows; it cannot
// send input or change anything. Binding "all" also exposes it on the
// LAN — the startup log warns when that happens.
import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import { execFileSync } from "node:child_process";

// ---------- config ----------
function arg(name, env, dflt) {
  const i = process.argv.indexOf("--" + name);
  if (i >= 0 && process.argv[i + 1]) return process.argv[i + 1];
  return process.env[env] || dflt;
}
const PORT = parseInt(arg("port", "MC_PORT", "7790"), 10);
const BIND = arg("bind", "MC_BIND", "tailnet");
const WS_URL = arg("ws", "MC_WS", "ws://[::1]:7787/ws");
const TOKEN_FILE = arg("token-file", "MC_TOKEN_FILE",
  os.homedir() + "/.pengupool-ai/v2/run/hub.token");
const POLL_MS = 10000;

// ---------- status rules (PenguPool's own: web/src/app/ctx.tsx) ----------
const STALE_MS = 10 * 60 * 1000;
const STATUS = {
  working:     ["Active", "active"],
  idle:        ["Waiting", "waiting"],
  approval:    ["? Need input", "need"],
  starting:    ["Starting", "start"],
  stale:       ["Stale", "stale"],
  terminal:    ["Not an agent", "waiting"],
  ended:       ["Ended", "stale"],
  exited:      ["Exited", "stale"],
  lost:        ["Lost", "need"],
  unreachable: ["Unreachable", "need"],
};
const parseMs = (ts) => { const t = Date.parse(ts || ""); return Number.isNaN(t) ? null : t; };
function ago(ts) {
  const ms = parseMs(ts); if (ms === null) return "";
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
function shownKey(s, member, checkins) {
  if (s.state !== "running") return STATUS[s.state] ? s.state : "ended";
  let st = s.status || "idle";
  if (st === "idle" && (!member || member.role !== "helper")) {
    const ms = parseMs(s.active_at);
    if (ms !== null && Date.now() - ms > STALE_MS) st = "stale";
  }
  if (!["working", "lost", "unreachable"].includes(st) && checkins.has(s.id)) return "approval";
  return STATUS[st] ? st : "idle";
}

// ---------- owner /ws client (17-byte frame protocol, v1) ----------
function fetchDaemon() {
  return new Promise((resolve, reject) => {
    let token;
    try { token = fs.readFileSync(TOKEN_FILE, "utf8").trim(); }
    catch (e) { reject(new Error("cannot read token file " + TOKEN_FILE)); return; }
    const ws = new WebSocket(WS_URL + "?token=" + encodeURIComponent(token));
    ws.binaryType = "arraybuffer";
    let seq = 0n;
    const send = (type, payload, channel = 0) => {
      const f = new Uint8Array(17 + payload.length), v = new DataView(f.buffer);
      v.setUint8(0, type); v.setUint32(1, channel); v.setBigUint64(5, seq++);
      v.setUint32(13, payload.length); f.set(payload, 17); ws.send(f);
    };
    const control = (msg) => send(1, new TextEncoder().encode(JSON.stringify(msg)));
    const got = {};
    const timer = setTimeout(() => { try { ws.close(); } catch {} reject(new Error("daemon read timed out")); }, 8000);
    const done = (fn, val) => { clearTimeout(timer); clearTimeout(grace); try { ws.close(); } catch {} fn(val); };
    const finish = () => {
      const checkins = new Set((got.gates || []).filter(g => g.kind === "checkin").map(g => g.session_id));
      done(resolve, buildPayload(got.sessions, got.domain, got.version, checkins));
    };
    // If the gates answer lags the rest, settle for what we have after a grace period.
    const grace = setTimeout(() => { if (got.sessions && got.domain) finish(); }, 2500);
    grace.unref?.();
    ws.onopen = () => control({ id: "mc1", type: "hello", v: 1, client: { kind: "cli", name: "muse-companion-site" } });
    ws.onerror = () => done(reject, new Error("cannot reach the PenguPool daemon at " + WS_URL));
    ws.onmessage = (e) => {
      const b = new Uint8Array(e.data);
      if (b[0] !== 1) return;
      let m; try { m = JSON.parse(new TextDecoder().decode(b.slice(17))); } catch { return; }
      if (m.type === "error") return done(reject, new Error("daemon error: " + (m.message || m.code)));
      if (m.type === "hello.ok") {
        control({ id: "mc2", type: "session.list" });
        control({ id: "mc3", type: "settings.get" });
        control({ id: "mc4", type: "gates.open" });
        control({ id: "mc5", type: "state.subscribe" });
        return;
      }
      if (m.type === "session.list") got.sessions = m.sessions || [];
      else if (m.type === "settings.get") got.version = (m.result || {}).version;
      else if (m.type === "gates.open") {
        const r = m.result || [];
        got.gates = Array.isArray(r) ? r : (r.gates || []);
      } else if (m.type === "state.snapshot") got.domain = m.domain || {};
      if (got.sessions && got.domain && got.gates) finish();
    };
  });
}

function buildPayload(sessions, domain, version, checkins) {
  const byId = new Map(sessions.map(s => [s.id, s]));
  const placed = new Set();
  const teams = [];
  let needsYou = 0;
  const row = (s, member, depth) => {
    const key = shownKey(s, member, checkins);
    const [label, tone] = STATUS[key];
    if (key === "approval") needsYou++;
    return {
      name: s.name || "?", lead: !!(member && member.role === "main"), depth,
      label, tone, ctx: (s.context_pct ?? null),
      ctxTone: s.context_pct == null ? "" : s.context_pct < 30 ? "ok" : s.context_pct < 60 ? "warn" : "bad",
      ago: ago(s.active_at),
    };
  };
  for (const t of domain.teams || []) {
    if (t.state !== "active") continue;
    const members = (t.members || []).filter(m => m.state === "active" && byId.has(m.session_id));
    if (!members.length) continue;
    const ids = new Set(members.map(m => m.session_id));
    const main = members.find(m => m.role === "main") || members[0];
    const kids = new Map(), roots = [];
    for (const m of members) {
      let p = m.parent;
      if (!ids.has(p)) p = (m === main) ? "" : main.session_id;
      if (p === "") roots.push(m.session_id);
      else { if (!kids.has(p)) kids.set(p, []); kids.get(p).push(m.session_id); }
    }
    const rows = [];
    const walk = (sid, depth) => {
      if (placed.has(sid)) return; placed.add(sid);
      rows.push(row(byId.get(sid), members.find(m => m.session_id === sid), depth));
      for (const ch of kids.get(sid) || []) walk(ch, depth + 1);
    };
    roots.forEach(r => walk(r, 0));
    teams.push({ name: t.name || "team", rows });
  }
  const ungrouped = sessions.filter(s => s.harness !== "shell" && !placed.has(s.id)).map(s => row(s, null, 0));
  const terminals = sessions.filter(s => s.harness === "shell").map(s => ({
    name: s.name || "terminal",
    cwd: ((s.cwd || "").replace(/\/$/, "").split("/").pop() || "/"),
  }));
  return { version: version || null, fetchedAt: Date.now(), needsYou, teams, ungrouped, terminals };
}

// ---------- cache ----------
let cache = null, cacheAt = 0, inflight = null;
async function status() {
  if (cache && Date.now() - cacheAt < 2000) return cache;
  if (!inflight) {
    inflight = fetchDaemon()
      .then(p => { cache = p; cacheAt = Date.now(); inflight = null; return p; })
      .catch(e => { inflight = null; throw e; });
  }
  return inflight;
}

// ---------- the page ----------
const PAGE = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="apple-mobile-web-app-capable" content="yes">
<title>PenguPool — live status</title>
<style>
:root{--bg:#ffffff;--surface:#f6f7f9;--line:#e3e6eb;--text:#111418;--muted:#5b6472;
--success:#15803d;--warning:#b45309;--danger:#c0262d;--faint:#8a91a0}
@media (prefers-color-scheme: dark){:root{--bg:#0c0e12;--surface:#15181e;--line:#262b33;
--text:#e8ebef;--muted:#9aa3b2;--success:#4ade80;--warning:#fbbf24;--danger:#f87171;--faint:#6b7280}}
*{box-sizing:border-box}html,body{margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;
background:var(--bg);color:var(--text);-webkit-text-size-adjust:100%}
.wrap{max-width:600px;margin:0 auto;padding:0 0 env(safe-area-inset-bottom)}
header{position:sticky;top:0;background:var(--bg);padding:14px 16px 10px;border-bottom:1px solid var(--line);
display:flex;align-items:center;gap:8px;z-index:2}
h1{font-size:17px;margin:0;font-weight:700}
.badge{font-size:11px;padding:1px 7px;border:1px solid var(--line);border-radius:999px;color:var(--muted)}
.live{margin-left:auto;font-size:12px;color:var(--muted);display:flex;align-items:center;gap:6px}
button.refresh{font:inherit;font-size:12px;color:var(--muted);background:var(--surface);
border:1px solid var(--line);border-radius:999px;padding:4px 10px;cursor:pointer}
.banner{margin:12px 16px 0;padding:10px 12px;border-radius:10px;background:var(--danger);color:#fff;
font-weight:600;font-size:14px;display:none}
.ghd{padding:14px 16px 4px;font-size:12px;font-weight:700;letter-spacing:.04em;
text-transform:uppercase;color:var(--muted)}
.row{display:flex;align-items:center;gap:9px;min-height:48px;padding:6px 16px;border-bottom:1px solid var(--line);font-size:15px}
.dot{width:9px;height:9px;border-radius:50%;flex:none}
.d-active{background:var(--success);animation:pl 1.6s ease-in-out infinite}
.d-waiting{background:var(--warning)}.d-need{background:var(--danger)}
.d-stale{background:var(--faint)}.d-start{background:var(--faint);animation:pl 1.6s ease-in-out infinite}
@keyframes pl{50%{opacity:.35}}
.nm{font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.tag{font-size:10px;padding:0 5px;border:1px solid var(--line);border-radius:4px;color:var(--muted)}
.st{font-size:13px;white-space:nowrap}
.s-active{color:var(--success)}.s-waiting{color:var(--warning)}.s-need{color:var(--danger);font-weight:700}.s-stale{color:var(--faint)}.s-start{color:var(--faint)}
.right{margin-left:auto;display:flex;gap:10px;align-items:center;flex:none;font-size:12px;color:var(--muted)}
.ctx-ok{color:var(--success);font-weight:600}.ctx-warn{color:var(--warning);font-weight:600}.ctx-bad{color:var(--danger);font-weight:600}
footer{padding:12px 16px;font-size:12px;color:var(--muted);line-height:1.55}
.err{color:var(--danger)}
</style></head><body><div class="wrap">
<header><h1>PenguPool</h1><span class="badge" id="ver"></span>
<div class="live"><span class="dot d-active" id="livedot"></span><span id="upd">loading…</span></div>
<button class="refresh" id="refresh" type="button">Refresh</button></header>
<div class="banner" id="banner"></div>
<main id="main"></main>
<footer>Live from the PenguPool API on this machine, refreshed every 10 s.
Green pulsing = Active, amber = Waiting, red = ? Need input, grey = Stale or ended.
Read-only — served by muse-companion on your tailnet.</footer>
</div>
<script id="boot" type="application/json">__PAYLOAD__</script>
<script>
var POLL=${POLL_MS};
function esc(s){return String(s).replace(/[&<>"]/g,function(c){return{"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c];});}
function rowHtml(r){
  var right="";
  if(r.ctx!=null) right+='<span class="ctx-'+r.ctxTone+'">'+r.ctx+'%</span>';
  if(r.ago) right+="<span>"+esc(r.ago)+"</span>";
  return '<div class="row" style="padding-left:'+(16+r.depth*20)+'px">'
    +'<span class="dot d-'+r.tone+'"></span><span class="nm">'+esc(r.name)+"</span>"
    +(r.lead?' <span class="tag">Lead</span>':"")
    +'<span class="st s-'+r.tone+'">'+esc(r.label)+'</span>'
    +'<span class="right">'+right+"</span></div>";
}
function render(p){
  document.getElementById("ver").textContent = p.version ? "v"+p.version : "";
  var b=document.getElementById("banner");
  if(p.needsYou>0){ b.style.display="block"; b.textContent = p.needsYou===1 ? "1 agent needs your input" : p.needsYou+" agents need your input"; }
  else b.style.display="none";
  var h="";
  p.teams.forEach(function(t){ h+='<div class="ghd">'+esc(t.name)+" · "+t.rows.length+" agents</div>"; t.rows.forEach(function(r){h+=rowHtml(r);}); });
  if(p.ungrouped.length){ h+='<div class="ghd">Ungrouped</div>'; p.ungrouped.forEach(function(r){h+=rowHtml(r);}); }
  if(p.terminals.length){ h+='<div class="ghd">Terminals</div>'; p.terminals.forEach(function(t){
    h+='<div class="row"><span class="nm" style="font-weight:500">'+esc(t.name)+'</span><span class="right"><span>'+esc(t.cwd)+"</span></span></div>"; }); }
  document.getElementById("main").innerHTML=h;
  window.__fetchedAt=p.fetchedAt; window.__stale=!!p.stale;
}
function tick(){
  var el=document.getElementById("upd");
  if(!window.__fetchedAt){ el.textContent="loading…"; return; }
  var s=Math.max(0,Math.round((Date.now()-window.__fetchedAt)/1000));
  el.innerHTML=(window.__stale?'<span class="err">daemon unreachable — showing last known · </span>':"")
    +"updated "+(s<2?"just now":s+"s ago");
  document.getElementById("livedot").className="dot "+(window.__stale?"d-need":"d-active");
}
function poll(){
  fetch("status.json",{cache:"no-store"}).then(function(r){return r.json();})
    .then(function(p){ render(p); tick(); })
    .catch(function(){ window.__stale=true; tick(); });
}
try{ render(JSON.parse(document.getElementById("boot").textContent)); }catch(e){}
tick(); setInterval(tick,1000); setInterval(poll,POLL);
document.addEventListener("visibilitychange",function(){ if(!document.hidden) poll(); });
document.getElementById("refresh").addEventListener("click",poll);
</script></body></html>`;

// ---------- http ----------
function resolveBind() {
  if (BIND !== "tailnet") return BIND === "all" ? "0.0.0.0" : BIND;
  try {
    const ip = execFileSync("tailscale", ["ip", "-4"], { timeout: 5000 }).toString().trim().split("\n")[0];
    if (ip) return ip;
  } catch {}
  console.error("warning: could not read this machine's Tailscale address; binding all interfaces");
  return "0.0.0.0";
}
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  try {
    if (url.pathname === "/health") {
      res.writeHead(200, { "Content-Type": "application/json" });
      return res.end(JSON.stringify({ ok: true }));
    }
    if (url.pathname === "/status.json") {
      let payload;
      try { payload = await status(); }
      catch (e) {
        if (cache) payload = { ...cache, stale: true };
        else { res.writeHead(502, { "Content-Type": "application/json" }); return res.end(JSON.stringify({ error: String(e.message || e) })); }
      }
      res.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" });
      return res.end(JSON.stringify(payload));
    }
    if (url.pathname === "/") {
      let payload = cache;
      try { payload = await status(); }
      catch { if (payload) payload = { ...payload, stale: true }; }
      const boot = payload ? JSON.stringify(payload).replace(/</g, "\\u003c") : "null";
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" });
      return res.end(PAGE.replace("__PAYLOAD__", boot || "null").replace("${POLL_MS}", String(POLL_MS)));
    }
    res.writeHead(404, { "Content-Type": "text/plain" });
    res.end("not found");
  } catch (e) {
    res.writeHead(500, { "Content-Type": "text/plain" });
    res.end("error: " + (e.message || e));
  }
});
const bindAddr = resolveBind();
server.listen(PORT, bindAddr, () => {
  console.log(`muse-companion mini site on http://${bindAddr}:${PORT} (ws ${WS_URL})`);
  if (bindAddr === "0.0.0.0") console.log("warning: bound to all interfaces — reachable from the LAN as well as the tailnet");
});
