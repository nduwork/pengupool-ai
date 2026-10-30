#!/usr/bin/env node
// muse-companion mini site — a live PenguPool status page for a phone.
//
// Runs ON the PenguPool machine (see install-host.sh). Serves one phone-
// first page that polls status.json every 10 s; the server answers each
// poll from the PenguPool paired-device API (/api/muse/v1/sessions) with
// the site's own device token. It never holds the owner's app token.
//
// No dependencies: Node 22+ (global fetch) and the standard library.
//
//   node serve.mjs [--port 7790] [--bind tailnet|all|<ip>]
//                  [--api http://<this machine's Tailscale name>:7787]
//
// Env equivalents: MC_PORT, MC_BIND, MC_API.
// Files (made by install-host.sh, mode 0600): ~/.muse-companion/device-token
// (pair the site as a device under Settings › Muse app) and
// ~/.muse-companion/site-key (the key the page URL must carry: ?key=...).
//
// Exposure: the default bind is this machine's Tailscale address only, and
// every request but /health needs the site key, so the page answers only
// your own devices that hold the URL. It shows session names and statuses;
// it cannot send input or change anything.
import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import crypto from "node:crypto";
import { execFileSync } from "node:child_process";

// ---------- config ----------
function arg(name, env, dflt) {
  const i = process.argv.indexOf("--" + name);
  if (i >= 0 && process.argv[i + 1]) return process.argv[i + 1];
  return process.env[env] || dflt;
}
const PORT = parseInt(arg("port", "MC_PORT", "7790"), 10);
const BIND = arg("bind", "MC_BIND", "tailnet");
const DIR = os.homedir() + "/.muse-companion";
const read = (f) => fs.readFileSync(DIR + "/" + f, "utf8").trim();
const KEY = read("site-key"); // read once: changing it means restarting the site
if (KEY.length < 16) { console.error("site-key is too short; re-run install-host.sh"); process.exit(1); }
const POLL_MS = 10000;

// launchd's PATH lacks Homebrew and the app bundle, so try where Tailscale's CLI lives.
function tailscale(args) {
  for (const cli of ["tailscale", "/opt/homebrew/bin/tailscale", "/usr/local/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"]) {
    try { return execFileSync(cli, args, { timeout: 5000 }).toString(); } catch {}
  }
  return "";
}
// The device API answers only on this machine's Tailscale name, so that is the address the site calls.
let apiBase = arg("api", "MC_API", "");
function api() {
  if (apiBase) return apiBase;
  try {
    const name = (JSON.parse(tailscale(["status", "--json"])).Self || {}).DNSName || "";
    if (name) apiBase = "http://" + name.replace(/\.$/, "") + ":7787";
  } catch {}
  return apiBase;
}

// ---------- status rules (PenguPool's own: web/src/app/ctx.tsx) ----------
const STATUS = {
  working:     ["Active", "active"],
  idle:        ["Waiting", "waiting"],
  approval:    ["? Need input", "need"],
  starting:    ["Starting", "start"],
  ended:       ["Ended", "stale"],
  lost:        ["Lost", "need"],
  unreachable: ["Unreachable", "need"],
};
function shownKey(s) {
  if (s.state !== "running") return "ended";
  if (s.waiting) return "approval";
  return STATUS[s.status] ? s.status : "idle";
}

// ---------- the device API ----------
async function fetchDaemon() {
  const base = api();
  if (!base) throw new Error("cannot read this machine's Tailscale name (set MC_API)");
  let token;
  try { token = read("device-token"); } catch { throw new Error("no device token in " + DIR + "/device-token"); }
  const res = await fetch(base + "/api/muse/v1/sessions", {
    headers: { Authorization: "Bearer " + token }, signal: AbortSignal.timeout(8000),
  });
  if (!res.ok) throw new Error("the device API answered " + res.status + (res.status === 401 ? " (device token revoked?)" : ""));
  return buildPayload(await res.json() || []);
}

function buildPayload(sessions) {
  const byId = new Map(sessions.map(s => [s.session, s]));
  const placed = new Set();
  const teams = [];
  let needsYou = 0;
  const row = (s, depth) => {
    const key = shownKey(s);
    const [label, tone] = STATUS[key];
    if (key === "approval") needsYou++;
    return { name: s.name || s.title || "?", role: s.role || "", depth, label, tone };
  };
  const byTeam = new Map();
  for (const s of sessions) if (s.team_id) { if (!byTeam.has(s.team_id)) byTeam.set(s.team_id, []); byTeam.get(s.team_id).push(s); }
  for (const members of byTeam.values()) {
    const ids = new Set(members.map(m => m.session));
    const main = members.find(m => m.role === "LEAD") || members[0];
    const kids = new Map(), roots = [];
    for (const m of members) {
      let p = m.parent;
      if (!ids.has(p)) p = (m === main) ? "" : main.session;
      if (p === "") roots.push(m.session);
      else { if (!kids.has(p)) kids.set(p, []); kids.get(p).push(m.session); }
    }
    const rows = [];
    const walk = (sid, depth) => {
      if (placed.has(sid)) return; placed.add(sid);
      rows.push(row(byId.get(sid), depth));
      for (const ch of kids.get(sid) || []) walk(ch, depth + 1);
    };
    roots.forEach(r => walk(r, 0));
    teams.push({ name: members[0].team || "team", rows });
  }
  const ungrouped = sessions.filter(s => !placed.has(s.session)).map(s => row(s, 0));
  return { fetchedAt: Date.now(), needsYou, teams, ungrouped };
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
footer{padding:12px 16px;font-size:12px;color:var(--muted);line-height:1.55}
.err{color:var(--danger)}
</style></head><body><div class="wrap">
<header><h1>PenguPool</h1>
<div class="live"><span class="dot d-active" id="livedot"></span><span id="upd">loading…</span></div>
<button class="refresh" id="refresh" type="button">Refresh</button></header>
<div class="banner" id="banner"></div>
<main id="main"></main>
<footer>Live from PenguPool on this machine, refreshed every 10 s.
Green pulsing = Active, amber = Waiting, red = ? Need input, grey = Ended.
Read-only, from the paired-device API — served by muse-companion on your tailnet.</footer>
</div>
<script id="boot" type="application/json">__PAYLOAD__</script>
<script>
var POLL=${POLL_MS};
function esc(s){return String(s).replace(/[&<>"]/g,function(c){return{"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c];});}
function rowHtml(r){
  return '<div class="row" style="padding-left:'+(16+r.depth*20)+'px">'
    +'<span class="dot d-'+r.tone+'"></span><span class="nm">'+esc(r.name)+"</span>"
    +(r.role?' <span class="tag">'+esc(r.role)+'</span>':"")
    +'<span class="st s-'+r.tone+'">'+esc(r.label)+'</span></div>';
}
function render(p){
  var b=document.getElementById("banner");
  if(p.needsYou>0){ b.style.display="block"; b.textContent = p.needsYou===1 ? "1 agent needs your input" : p.needsYou+" agents need your input"; }
  else b.style.display="none";
  var h="";
  p.teams.forEach(function(t){ h+='<div class="ghd">'+esc(t.name)+" · "+t.rows.length+" agents</div>"; t.rows.forEach(function(r){h+=rowHtml(r);}); });
  if(p.ungrouped.length){ h+='<div class="ghd">Ungrouped</div>'; p.ungrouped.forEach(function(r){h+=rowHtml(r);}); }
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
  fetch("status.json"+location.search,{cache:"no-store"}).then(function(r){return r.json();})
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
  const ip = tailscale(["ip", "-4"]).trim().split("\n")[0];
  if (ip) return ip;
  // No address: refuse to start rather than open the page to the LAN (MC_BIND=all does that on purpose).
  console.error("cannot read this machine's Tailscale address; not starting (set MC_BIND to an address, or all)");
  process.exit(1);
}
function keyOK(url) {
  const a = Buffer.from(url.searchParams.get("key") || ""), b = Buffer.from(KEY);
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  try {
    if (url.pathname === "/health") {
      res.writeHead(200, { "Content-Type": "application/json" });
      return res.end(JSON.stringify({ ok: true }));
    }
    if (!keyOK(url)) {
      res.writeHead(404, { "Content-Type": "text/plain" });
      return res.end("not found");
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
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" });
      return res.end(PAGE.replace("__PAYLOAD__", () => boot));
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
  console.log(`muse-companion mini site on http://${bindAddr}:${PORT}/?key=… (device API ${api() || "not found yet"})`);
  if (bindAddr === "0.0.0.0") console.log("warning: bound to all interfaces — reachable from the LAN as well as the tailnet");
});
