import { useCallback, useEffect, useRef, useState, type ReactElement } from "react";

import { getJSON, useApp, type AppCtx } from "../app/ctx";

// The PenguPool AI improvement inside the app: the owner's confirmed
// Compass. Say a rule once (it becomes a proposal), confirm it here, and
// it rides along in every session's context. Served by the host daemon
// at /api/ai/* over the pengu store; agents never write there.

interface Line {
  id: string;
  rule: string;
  confidence: number;
  source: string;
}

interface Proposal {
  id: string;
  words: string;
  kind: string;
  status: string;
}

interface Question {
  id: string;
  session: string;
  question: string;
  options: string[];
  payload_hash: string;
}

interface ResultRow {
  task: string;
  arm: string;
  matched: boolean | null;
  interruptions: number;
  passed: boolean | null;
}

async function postJSON<R>(app: AppCtx, path: string, body: unknown): Promise<R> {
  const u = new URL(path, app.conn.baseURL);
  const res = await fetch(u, {
    method: "POST",
    headers: { "X-PenguPool-Token": app.conn.token, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = (await res.json()) as R & { error?: string };
  if (!res.ok) throw new Error(data.error ?? `HTTP ${res.status}`);
  return data;
}

function useAIPoll<T>(app: AppCtx, path: string, empty: T, ms = 5000): [T, () => void] {
  const [data, setData] = useState<T>(empty);
  const emptyRef = useRef(empty);
  const load = useCallback(() => {
    getJSON<T>(app.conn, path, {})
      .then((d) => setData(d ?? emptyRef.current))
      .catch(() => {});
  }, [app.conn, path]);
  useEffect(() => {
    load();
    const t = setInterval(load, ms);
    return () => clearInterval(t);
  }, [load, ms]);
  return [data, load];
}

export function CompassAI(): ReactElement {
  const app = useApp();
  const [lines, reloadLines] = useAIPoll<Line[]>(app, "/api/ai/lines", []);
  const [proposals, reloadProposals] = useAIPoll<Proposal[]>(app, "/api/ai/proposals", []);
  const [questions, reloadQuestions] = useAIPoll<Question[]>(app, "/api/ai/questions", [], 4000);
  const [results] = useAIPoll<ResultRow[]>(app, "/api/ai/results", [], 15000);
  const [words, setWords] = useState("");
  const [exact, setExact] = useState("");
  const [msg, setMsg] = useState("");
  const act = (fn: () => Promise<string>) => {
    fn()
      .then((m) => {
        setMsg(m);
        reloadLines();
        reloadProposals();
        reloadQuestions();
      })
      .catch((e: unknown) => setMsg(`failed: ${e instanceof Error ? e.message : String(e)}`));
  };
  const pending = proposals.filter((p) => p.status === "proposed");
  const arms = ["without", "with"] as const;
  return (
    <div className="mx-auto max-w-3xl space-y-6 overflow-y-auto p-6">
      <div>
        <h2 className="text-title font-semibold">Compass AI</h2>
        <p className="text-label text-muted-foreground">
          Say a rule once; your agents stop asking. Nothing is saved until you confirm it, and
          confirmed lines ride along in every session&rsquo;s context.
        </p>
      </div>

      <section className="space-y-2 rounded-lg border border-border p-4">
        <h3 className="font-medium">Say a rule once</h3>
        <div className="flex gap-2">
          <input
            className="min-w-0 flex-1 rounded-md border border-border bg-transparent px-2 py-1"
            value={words}
            onChange={(e) => setWords(e.target.value)}
            placeholder="from now on, run the tests without asking"
          />
          <button
            type="button"
            className="rounded-md border border-border px-3 py-1 disabled:opacity-50"
            disabled={words.trim() === ""}
            onClick={() =>
              act(async () => {
                const r = await postJSON<{ message: string }>(app, "/api/ai/ingest", { words });
                setWords("");
                return r.message;
              })
            }
          >
            Propose
          </button>
        </div>
      </section>

      <section className="space-y-2 rounded-lg border border-border p-4">
        <h3 className="font-medium">Awaiting your confirmation ({pending.length})</h3>
        {pending.length === 0 && <p className="text-label text-muted-foreground">Nothing awaiting confirmation.</p>}
        <ul className="space-y-2">
          {pending.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center gap-2">
              <span className="text-muted-foreground">{p.kind}</span>
              <span className="min-w-0 flex-1">{p.words}</span>
              <button
                type="button"
                className="rounded-md border border-border px-2 py-0.5"
                onClick={() =>
                  act(async () => {
                    const r = await postJSON<{ applied: string }>(app, `/api/ai/proposals/${p.id}/confirm`, {});
                    return `applied: ${r.applied}`;
                  })
                }
              >
                Confirm
              </button>
              <button
                type="button"
                className="rounded-md border border-border px-2 py-0.5"
                onClick={() =>
                  act(async () => {
                    await postJSON(app, `/api/ai/proposals/${p.id}/dismiss`, {});
                    return "dismissed";
                  })
                }
              >
                Dismiss
              </button>
            </li>
          ))}
        </ul>
      </section>

      <section className="space-y-2 rounded-lg border border-border p-4">
        <h3 className="font-medium">Confirmed lines ({lines.length})</h3>
        {lines.length === 0 && <p className="text-label text-muted-foreground">None yet. Propose one above, then confirm it.</p>}
        <ul className="space-y-2">
          {lines.map((l) => (
            <li key={l.id} className="flex flex-wrap items-center gap-2">
              <span className="min-w-0 flex-1">{l.rule}</span>
              <span className="text-muted-foreground">
                ({l.confidence.toFixed(2)}, {l.source})
              </span>
              <button
                type="button"
                className="rounded-md border border-border px-2 py-0.5"
                onClick={() =>
                  act(async () => {
                    const u = new URL(`/api/ai/lines/${l.id}`, app.conn.baseURL);
                    const res = await fetch(u, { method: "DELETE", headers: { "X-PenguPool-Token": app.conn.token } });
                    if (!res.ok) throw new Error(`HTTP ${res.status}`);
                    return "forgotten";
                  })
                }
              >
                Forget
              </button>
            </li>
          ))}
        </ul>
        <div className="flex gap-2">
          <input
            className="min-w-0 flex-1 rounded-md border border-border bg-transparent px-2 py-1"
            value={exact}
            onChange={(e) => setExact(e.target.value)}
            placeholder="or type the exact line to save"
          />
          <button
            type="button"
            className="rounded-md border border-border px-3 py-1 disabled:opacity-50"
            disabled={exact.trim() === ""}
            onClick={() =>
              act(async () => {
                await postJSON(app, "/api/ai/lines", { rule: exact });
                setExact("");
                return "saved";
              })
            }
          >
            Save line
          </button>
        </div>
      </section>

      <section className="space-y-2 rounded-lg border border-border p-4">
        <h3 className="font-medium">Questions from your agents ({questions.length})</h3>
        {questions.length === 0 && (
          <p className="text-label text-muted-foreground">No session is waiting on you right now.</p>
        )}
        <ul className="space-y-2">
          {questions.map((q) => (
            <li key={q.id} className="flex flex-wrap items-center gap-2">
              <span className="min-w-0 flex-1">
                <strong>{q.session}:</strong> {q.question}
              </span>
              {q.options.map((o) => (
                <button
                  key={o}
                  type="button"
                  className="rounded-md border border-border px-2 py-0.5"
                  onClick={() =>
                    act(async () => {
                      await postJSON(app, `/api/ai/questions/${q.id}/answer`, {
                        option: o,
                        payload_hash: q.payload_hash,
                        idempotency_key: `app-${q.id}-${Date.now()}`,
                      });
                      return `answered: ${o}`;
                    })
                  }
                >
                  {o}
                </button>
              ))}
            </li>
          ))}
        </ul>
      </section>

      <section className="space-y-2 rounded-lg border border-border p-4">
        <h3 className="font-medium">Experiment: with vs without</h3>
        {results.length === 0 && (
          <p className="text-label text-muted-foreground">No results yet. Run `pengu experiment` in the repo.</p>
        )}
        {arms.map((a) => {
          const rs = results.filter((r) => r.arm === a && r.matched !== null);
          const v = rs.length === 0 ? 0 : rs.filter((r) => r.matched).length / rs.length;
          return (
            <p key={a} className="flex items-center gap-2">
              <span className="w-16">{a}:</span>
              <svg width="120" height="12" aria-label={`${Math.round(v * 100)}%`}>
                <rect width="120" height="12" fill="var(--border)" />
                <rect width={Math.round(v * 120)} height="12" fill="var(--accent)" />
              </svg>
              <span>{Math.round(v * 100)}% decisions matched</span>
            </p>
          );
        })}
      </section>

      {msg !== "" && <p className="text-label">{msg}</p>}
    </div>
  );
}
