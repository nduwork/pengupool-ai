import { marked } from "marked";
import { useEffect, useMemo, useState, type ReactElement } from "react";

import { getJSON, useApp, usePersisted, type EditorTab } from "../app/ctx";
import { Button } from "../components/ui/button";
import { cn, fullTime } from "../lib/utils";

// Read-only file previews and diffs in the upper-centre pane. Opening one never touches the
// terminal or the PTY size. Markdown (with Mermaid diagrams), HTML, PDF and images open rendered, as in
// VS Code's preview, with a switch to the source, and follow the file as it changes. Rendered documents
// live in a sandboxed frame: no scripts (HTML can opt in: it then runs from its own URL in an opaque
// origin, so its relative assets load but it cannot read the host's answers), no access to the app.

interface FileResp {
  content?: string;
  binary?: boolean;
  truncated?: boolean;
  size?: number;
  mtime?: string;
  /** Read-only URL of the file's bytes; its folder resolves relative assets. */
  raw?: string;
}

type PreviewKind = "markdown" | "html" | "image" | "pdf";

export function previewKind(path: string): PreviewKind | null {
  const ext = path.toLowerCase().split(".").pop() ?? "";
  if (["md", "markdown", "mdown", "mkd"].includes(ext)) return "markdown";
  if (["html", "htm", "xhtml"].includes(ext)) return "html";
  if (ext === "pdf") return "pdf";
  if (["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "ico", "avif"].includes(ext)) return "image";
  return null;
}

/** The app's theme for a rendered Markdown document, read from the live tokens. */
function themeCSS(): string {
  const css = typeof document === "undefined" ? undefined : getComputedStyle(document.documentElement);
  const v = (n: string): string => css?.getPropertyValue(n).trim() ?? "";
  return `:root{color-scheme:light dark}
body{margin:0;padding:24px 32px 48px;max-width:860px;font:14px/1.6 ${v("--font-sans") || "system-ui"};color:${v("--foreground")};background:${v("--panel")}}
a{color:${v("--primary")}}h1,h2{border-bottom:1px solid ${v("--border")};padding-bottom:.3em}
h1,h2,h3,h4{line-height:1.25;margin:1.4em 0 .6em}code,pre{font-family:${v("--font-mono") || "ui-monospace,monospace"};font-size:12.5px}
:not(pre)>code{background:${v("--muted")};padding:.15em .35em;border-radius:4px}
pre{background:${v("--muted")};padding:12px 14px;border-radius:6px;overflow:auto}
blockquote{margin:0;padding:0 1em;color:${v("--muted-foreground")};border-left:3px solid ${v("--border")}}
table{border-collapse:collapse}th,td{border:1px solid ${v("--border")};padding:6px 12px}th{background:${v("--muted")}}
img{max-width:100%}.mermaid{margin:1em 0;text-align:center}.mermaid-error{color:${v("--danger")}}hr{border:0;border-top:1px solid ${v("--border")}}li+li{margin-top:.2em}`;
}

const escapeAttr = (s: string): string => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");

/** A document for the sandboxed frame. base resolves relative links and images in the file's folder. */
export function frameDoc(kind: "markdown" | "html", content: string, base: string | undefined): string {
  const baseTag = base !== undefined ? `<base href="${escapeAttr(base)}">` : "";
  if (kind === "markdown") {
    const body = marked.parse(content, { async: false, gfm: true });
    return `<!doctype html><html><head><meta charset="utf-8">${baseTag}<style>${themeCSS()}</style></head><body>${body}</body></html>`;
  }
  if (baseTag === "") return content;
  return /<head[^>]*>/i.test(content) ? content.replace(/<head[^>]*>/i, (h) => h + baseTag) : baseTag + content;
}

const MERMAID = /<pre><code class="language-mermaid">([\s\S]*?)<\/code><\/pre>/g;
const unescapeHTML = (s: string): string => s.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, "&");
let diagrams = 0;

/** Draws a rendered Markdown document's Mermaid blocks as SVG here, with the bundled Mermaid (no
 *  network), so the frame still runs no scripts. A block that does not parse stays code, under its error. */
export async function withDiagrams(doc: string, dark: boolean): Promise<string> {
  const blocks = [...doc.matchAll(MERMAID)];
  if (blocks.length === 0) return doc;
  const { default: mermaid } = await import("mermaid");
  mermaid.initialize({ startOnLoad: false, securityLevel: "strict", theme: dark ? "dark" : "default" });
  for (const [whole, src] of blocks) {
    let html: string;
    try {
      html = `<div class="mermaid">${(await mermaid.render(`pp-mermaid-${++diagrams}`, unescapeHTML(src))).svg}</div>`;
    } catch (e) {
      html = `<p class="mermaid-error">Mermaid: ${escapeAttr(e instanceof Error ? e.message : String(e))}</p>${whole}`;
    }
    doc = doc.replace(whole, () => html);
  }
  return doc;
}

function Preview({ kind, f, path, scripts }: { kind: PreviewKind; f: FileResp; path: string; scripts: boolean }): ReactElement {
  const app = useApp();
  const raw = f.raw !== undefined ? new URL(f.raw, app.conn.baseURL).toString() : undefined;
  const folder = raw?.slice(0, raw.lastIndexOf("/") + 1);
  const plain = useMemo(() => (kind === "markdown" || kind === "html" ? frameDoc(kind, f.content ?? "", folder) : ""), [kind, f.content, folder]);
  const [doc, setDoc] = useState(plain);
  useEffect(() => {
    let live = true;
    setDoc(plain);
    if (kind === "markdown") void withDiagrams(plain, document.documentElement.classList.contains("dark")).then((d) => live && setDoc(d));
    return () => {
      live = false;
    };
  }, [kind, plain]);
  if (kind === "image")
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto bg-[repeating-conic-gradient(var(--muted)_0_25%,transparent_0_50%)] bg-[length:16px_16px] p-6">
        {raw !== undefined ? <img src={raw} alt={path} className="max-h-full max-w-full object-contain shadow-sm" /> : null}
      </div>
    );
  const frame = "min-h-0 w-full flex-1 border-0 bg-panel";
  // The viewer of a PDF cannot run sandboxed; the file is served from the host's origin, not the app's.
  if (kind === "pdf") return raw !== undefined ? <iframe key={f.mtime} title={`Preview of ${path}`} className={frame} src={raw} /> : <p className="p-4 text-label text-faint">No preview.</p>;
  if (kind === "html" && scripts && raw !== undefined) return <iframe key={f.mtime} title={`Preview of ${path}`} className={frame} sandbox="allow-scripts" src={`${raw}?run=1`} />;
  return <iframe title={`Preview of ${path}`} className={frame} sandbox="" srcDoc={doc} />;
}

export function FileView({ tab }: { tab: EditorTab }): ReactElement {
  const app = useApp();
  const [f, setF] = useState<FileResp | string | null>(null);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    // Follow the file: an agent may be writing it. A read with nothing new changes nothing.
    const t = setInterval(() => document.hidden || setTick((n) => n + 1), 2000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => {
    let live = true;
    getJSON<FileResp>(app.conn, "/api/files", { session: tab.sessionId, path: tab.path })
      .then((r) => live && setF((old) => (typeof old === "object" && old !== null && old.mtime === r.mtime && old.content === r.content ? old : r)))
      .catch((e: Error) => live && setF((old) => (typeof old === "object" && old !== null ? old : e.message)));
    return () => {
      live = false;
    };
  }, [app.conn, tab.sessionId, tab.path, tick]);
  const kind = previewKind(tab.path);
  const [mode, setMode] = usePersisted<"preview" | "source">(`file.mode.${kind ?? "text"}`, "preview");
  const [scripts, setScripts] = useState(false);
  if (f === null) return <p className="p-4 text-label text-faint">Loading…</p>;
  if (typeof f === "string") return <p className="p-4 text-label text-danger">{f}</p>;
  const note = [f.truncated === true ? "showing the first 256 KiB" : "read-only", f.mtime !== undefined ? `modified ${fullTime(f.mtime)}` : ""].filter((x) => x !== "").join(" · ");
  const previewing = kind !== null && (mode === "preview" || f.binary === true);
  const bar = (
    <PathBar path={tab.path} note={note}>
      {kind === "html" && previewing ? (
        <Button size="sm" variant={scripts ? "default" : "ghost"} onClick={() => setScripts(!scripts)} title="Run the page's scripts in an isolated frame that cannot read other workspace files">
          {scripts ? "Scripts on" : "Scripts off"}
        </Button>
      ) : null}
      {kind !== null && f.binary !== true ? (
        <span className="flex overflow-hidden rounded-md border border-border">
          {(["preview", "source"] as const).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setMode(m)}
              aria-pressed={mode === m}
              className={cn("px-2 py-0.5 text-caption text-muted-foreground hover:bg-hover", mode === m && "bg-selected font-medium text-foreground")}
            >
              {m === "preview" ? "Preview" : "Source"}
            </button>
          ))}
        </span>
      ) : null}
    </PathBar>
  );
  if (previewing)
    return (
      <div className="flex h-full min-h-0 flex-col">
        {bar}
        <Preview kind={kind} f={f} path={tab.path} scripts={scripts} />
      </div>
    );
  if (f.binary === true) return <p className="p-4 text-label text-muted-foreground">Binary file ({f.size ?? 0} bytes): no preview.</p>;
  const lines = (f.content ?? "").split("\n");
  return (
    <div className="min-h-full">
      {bar}
      <table className={codeTable}>
        <tbody>
          {lines.map((l, i) => (
            <tr key={i} className="hover:bg-hover/50">
              <td className={lnCell}>{i + 1}</td>
              <td className={srcCell}>{l}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const codeTable = "w-full border-collapse font-mono text-caption leading-5";
const lnCell = "w-px select-none whitespace-nowrap border-r border-border px-3 text-right align-top text-faint";
const srcCell = "whitespace-pre-wrap break-all px-3 align-top";

function PathBar({ path, note, children }: { path: string; note?: string; children?: React.ReactNode }): ReactElement {
  return (
    <div className="sticky top-0 z-10 flex h-8 items-center gap-2 border-b border-border bg-panel/95 px-3 backdrop-blur">
      <span className="truncate font-mono text-caption">{path}</span>
      {note !== undefined ? <span className="text-micro text-faint">{note}</span> : null}
      <span className="ml-auto flex items-center gap-1">{children}</span>
    </div>
  );
}

interface Row {
  kind: " " | "+" | "-" | "@";
  text: string;
  oldNo?: number;
  newNo?: number;
}

/** Parses a unified diff into rows that carry the base and current line numbers. */
export function parseDiff(diff: string): Row[] {
  const rows: Row[] = [];
  let o = 0;
  let n = 0;
  for (const line of diff.split("\n")) {
    const h = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(line);
    if (h !== null) {
      o = Number(h[1]);
      n = Number(h[2]);
      rows.push({ kind: "@", text: line });
      continue;
    }
    if (line.startsWith("diff ") || line.startsWith("index ") || line.startsWith("--- ") || line.startsWith("+++ ") || line.startsWith("new file") || line.startsWith("deleted file") || line.startsWith("\\")) continue;
    if (line.startsWith("+")) rows.push({ kind: "+", text: line.slice(1), newNo: n++ });
    else if (line.startsWith("-")) rows.push({ kind: "-", text: line.slice(1), oldNo: o++ });
    else if (line.startsWith(" ")) rows.push({ kind: " ", text: line.slice(1), oldNo: o++, newNo: n++ });
  }
  return rows;
}

export function DiffText({ diff, mode }: { diff: string; mode: "unified" | "split" }): ReactElement {
  const rows = parseDiff(diff);
  if (rows.length === 0) return <p className="p-4 text-label text-muted-foreground">No differences.</p>;
  const add = "bg-success/12";
  const del = "bg-danger/12";
  const hunk = "bg-info/8 text-info";
  if (mode === "unified") {
    return (
      <table className={codeTable}>
        <tbody>
          {rows.map((r, i) =>
            r.kind === "@" ? (
              <tr key={i} className={hunk}>
                <td colSpan={3} className="px-3 py-0.5">
                  {r.text}
                </td>
              </tr>
            ) : (
              <tr key={i} className={r.kind === "+" ? add : r.kind === "-" ? del : ""}>
                <td className={lnCell}>{r.oldNo ?? ""}</td>
                <td className={lnCell}>{r.newNo ?? ""}</td>
                <td className={srcCell}>
                  <span className={cn("mr-1 select-none", r.kind === "+" ? "text-success" : r.kind === "-" ? "text-danger" : "text-faint")}>
                    {r.kind === " " ? " " : r.kind}
                  </span>
                  {r.text}
                </td>
              </tr>
            ),
          )}
        </tbody>
      </table>
    );
  }
  // Side by side: pair each run of deletions with the following run of additions.
  const pairs: Array<[Row | null, Row | null] | Row> = [];
  for (let i = 0; i < rows.length; ) {
    const r = rows[i];
    if (r.kind === "@") {
      pairs.push(r);
      i++;
    } else if (r.kind === " ") {
      pairs.push([r, r]);
      i++;
    } else {
      const dels: Row[] = [];
      const adds: Row[] = [];
      while (i < rows.length && rows[i].kind === "-") dels.push(rows[i++]);
      while (i < rows.length && rows[i].kind === "+") adds.push(rows[i++]);
      for (let k = 0; k < Math.max(dels.length, adds.length); k++) pairs.push([dels[k] ?? null, adds[k] ?? null]);
    }
  }
  return (
    <table className={cn(codeTable, "table-fixed")}>
      <tbody>
        {pairs.map((p, i) =>
          !Array.isArray(p) ? (
            <tr key={i} className={hunk}>
              <td colSpan={4} className="px-3 py-0.5">
                {p.text}
              </td>
            </tr>
          ) : (
            <tr key={i}>
              <td className={cn(lnCell, "w-12")}>{p[0]?.oldNo ?? ""}</td>
              <td className={cn(srcCell, p[0]?.kind === "-" && del)}>{p[0]?.text ?? ""}</td>
              <td className={cn(lnCell, "w-12 border-l")}>{p[1]?.newNo ?? ""}</td>
              <td className={cn(srcCell, p[1]?.kind === "+" && add)}>{p[1]?.text ?? ""}</td>
            </tr>
          ),
        )}
      </tbody>
    </table>
  );
}

export function DiffView({ tab }: { tab: EditorTab }): ReactElement {
  const app = useApp();
  const [mode, setMode] = usePersisted<"unified" | "split">("diff.mode", "unified");
  const [d, setD] = useState<{ diff: string; stale?: boolean } | string | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    getJSON<{ diff: string; stale?: boolean }>(app.conn, "/api/diff", {
      session: tab.sessionId,
      path: tab.path,
      staged: tab.staged === true ? "1" : "",
    })
      .then((r) => live && setD(r))
      .catch((e: Error) => live && setD(e.message));
    return () => {
      live = false;
    };
  }, [app.conn, tab.sessionId, tab.path, tab.staged, n]);
  return (
    <div className="min-h-full">
      <PathBar path={tab.path} note={tab.staged === true ? "staged · HEAD → index" : "working · index → files"}>
        <Button size="sm" variant={mode === "unified" ? "secondary" : "ghost"} onClick={() => setMode("unified")}>
          Unified
        </Button>
        <Button size="sm" variant={mode === "split" ? "secondary" : "ghost"} onClick={() => setMode("split")}>
          Side by side
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setN(n + 1)}>
          Refresh
        </Button>
      </PathBar>
      {d === null ? (
        <p className="p-4 text-label text-faint">Loading…</p>
      ) : typeof d === "string" ? (
        <p className="p-4 text-label text-danger">{d}</p>
      ) : (
        <>
          {d.stale === true ? <p className="m-3 rounded-md bg-warning/10 p-2 text-caption text-warning">The repository changed while this diff was read; refresh it.</p> : null}
          <DiffText diff={d.diff} mode={mode} />
        </>
      )}
    </div>
  );
}
