import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  post,
  type InboxEntry,
  type Line,
  type Proposal,
  type Question,
  type ResultRow,
  type SessionRow,
} from "./api";

function usePoll<T>(path: string, empty: T, ms = 5000): [T, () => void] {
  const [data, setData] = useState<T>(empty);
  // Callers pass a fresh []/{} literal each render; depending on it would
  // recreate load (and refire the fetch) on every render — a request storm.
  const emptyRef = useRef(empty);
  const load = useCallback(() => {
    api<T>(path)
      .then((d) => setData(d ?? emptyRef.current))
      .catch(() => {});
  }, [path]);
  useEffect(() => {
    load();
    const t = setInterval(load, ms);
    return () => clearInterval(t);
  }, [load, ms]);
  return [data, load];
}

function useAction(): [string, (fn: () => Promise<string>) => void] {
  const [msg, setMsg] = useState("");
  const run = useCallback((fn: () => Promise<string>) => {
    fn()
      .then(setMsg)
      .catch((e: unknown) => setMsg(`failed: ${e instanceof Error ? e.message : String(e)}`));
  }, []);
  return [msg, run];
}

function Lines({ goProposals }: { goProposals: () => void }) {
  const [lines, reload] = usePoll<Line[]>("/lines", []);
  const [words, setWords] = useState("");
  const [exact, setExact] = useState("");
  const [msg, run] = useAction();
  return (
    <div>
      <section className="card">
        <h2>Say a rule once</h2>
        <p className="dim">
          In your own words. It becomes a proposal; nothing is saved until you confirm it.
        </p>
        <div className="row">
          <input
            value={words}
            onChange={(e) => setWords(e.target.value)}
            placeholder="from now on, run the tests without asking"
            size={52}
          />
          <button
            disabled={words.trim() === ""}
            onClick={() =>
              run(async () => {
                const r = await post<{ proposal: string; message: string }>("/ingest", { words });
                setWords("");
                goProposals();
                return r.message;
              })
            }
          >
            Propose
          </button>
        </div>
      </section>
      <section className="card">
        <h2>Confirmed lines ({lines.length})</h2>
        {lines.length === 0 && <p className="dim">None yet. Propose one above, then confirm it.</p>}
        <ul>
          {lines.map((l) => (
            <li key={l.id}>
              {l.rule} <span className="dim">({l.confidence.toFixed(2)}, {l.source})</span>{" "}
              <button
                onClick={() =>
                  run(async () => {
                    await api(`/lines/${l.id}`, { method: "DELETE" });
                    reload();
                    return "forgotten";
                  })
                }
              >
                Forget
              </button>
            </li>
          ))}
        </ul>
        <div className="row">
          <input
            value={exact}
            onChange={(e) => setExact(e.target.value)}
            placeholder="or type the exact line to save"
            size={52}
          />
          <button
            disabled={exact.trim() === ""}
            onClick={() =>
              run(async () => {
                await post("/lines", { rule: exact });
                setExact("");
                reload();
                return "saved";
              })
            }
          >
            Save line
          </button>
        </div>
        {msg !== "" && <p>{msg}</p>}
      </section>
    </div>
  );
}

function Proposals() {
  const [items, reload] = usePoll<Proposal[]>("/proposals", []);
  const [msg, run] = useAction();
  const pending = items.filter((p) => p.status === "proposed");
  const decided = items.filter((p) => p.status !== "proposed");
  return (
    <div>
      <h2>Awaiting your confirmation ({pending.length})</h2>
      {pending.length === 0 && <p className="dim">Nothing awaiting confirmation.</p>}
      <ul>
        {pending.map((p) => (
          <li key={p.id}>
            <span className="kind">{p.kind}</span> {p.words}{" "}
            <button
              onClick={() =>
                run(async () => {
                  const r = await post<{ applied: string; id?: string }>(`/proposals/${p.id}/confirm`, {});
                  reload();
                  return `${r.applied} ${r.id ?? ""}`;
                })
              }
            >
              Confirm
            </button>{" "}
            <button
              onClick={() =>
                run(async () => {
                  await post(`/proposals/${p.id}/dismiss`, {});
                  reload();
                  return "dismissed";
                })
              }
            >
              Dismiss
            </button>
          </li>
        ))}
      </ul>
      {decided.length > 0 && (
        <>
          <h3>Decided</h3>
          <ul>
            {decided.map((p) => (
              <li key={p.id}>
                <span className="kind">{p.kind}</span> {p.words}{" "}
                <span className="dim">({p.status})</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {msg !== "" && <p>{msg}</p>}
    </div>
  );
}

function Questions() {
  const [items, reload] = usePoll<Question[]>("/questions?wait=0", [], 4000);
  const [msg, run] = useAction();
  useEffect(() => {
    window.penguAI?.setBadge(items.length);
  }, [items.length]);
  return (
    <div>
      <h2>Questions from your agents ({items.length})</h2>
      {items.length === 0 && (
        <p className="dim">No session is waiting. When an agent asks, it appears here; no answer means it keeps waiting.</p>
      )}
      <ul>
        {items.map((q) => (
          <li key={q.id}>
            <strong>{q.session}:</strong> {q.question}{" "}
            {q.options.map((o) => (
              <button
                key={o}
                onClick={() =>
                  run(async () => {
                    await post(`/questions/${q.id}/answer`, {
                      option: o,
                      payload_hash: q.payload_hash,
                      idempotency_key: `app-${q.id}-${Date.now()}`,
                    });
                    reload();
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
      {msg !== "" && <p>{msg}</p>}
    </div>
  );
}

function Sessions() {
  const [rows, reload] = usePoll<SessionRow[]>("/sessions", [], 4000);
  const [agent, setAgent] = useState("claude");
  const [task, setTask] = useState("");
  const [trust, setTrust] = useState(false);
  const [msg, run] = useAction();
  return (
    <div>
      <h2>Sessions</h2>
      <div className="row">
        <select value={agent} onChange={(e) => setAgent(e.target.value)}>
          <option value="claude">claude</option>
          <option value="muse">muse</option>
        </select>
        <input
          value={task}
          onChange={(e) => setTask(e.target.value)}
          placeholder="task (Compass lines are prepended)"
          size={44}
        />
        <label>
          <input type="checkbox" checked={trust} onChange={(e) => setTrust(e.target.checked)} /> trust
          workspace (muse)
        </label>
        <button
          onClick={() =>
            run(async () => {
              const r = await post<SessionRow>("/sessions", { agent, task, trustWorkspace: trust });
              setTask("");
              reload();
              return `started ${r.id} (tmux) — attach in a terminal to watch it`;
            })
          }
        >
          Start session
        </button>
      </div>
      {rows.length === 0 && <p className="dim">No sessions yet.</p>}
      <ul>
        {rows.map((r) => (
          <li key={r.id}>
            {r.id} · {r.agent} · {r.alive ? "alive" : "dead"} · {r.task.slice(0, 80)}{" "}
            <button
              onClick={() =>
                run(async () => {
                  await post(`/sessions/${r.id}/stop`, {});
                  reload();
                  return "stopped";
                })
              }
            >
              Stop
            </button>
          </li>
        ))}
      </ul>
      {msg !== "" && <p>{msg}</p>}
    </div>
  );
}

function Inbox() {
  const [sessions] = usePoll<SessionRow[]>("/sessions", [], 8000);
  const [who, setWho] = useState("owner");
  const [to, setTo] = useState("owner");
  const [text, setText] = useState("");
  const [entries, reload] = usePoll<InboxEntry[]>(`/inbox?session=${encodeURIComponent(who)}`, [], 4000);
  const [msg, run] = useAction();
  const ids = ["owner", ...sessions.map((s) => s.id)];
  return (
    <div>
      <h2>Inbox</h2>
      <div className="row">
        <label>
          Viewing{" "}
          <select value={who} onChange={(e) => setWho(e.target.value)}>
            {ids.map((id) => (
              <option key={id} value={id}>
                {id}
              </option>
            ))}
          </select>
        </label>
      </div>
      <ul>
        {entries.map((e) => (
          <li key={e.id}>
            from {e.from}: {e.text}
          </li>
        ))}
      </ul>
      {entries.length === 0 && <p className="dim">No messages for {who}.</p>}
      <div className="row">
        <label>
          To{" "}
          <select value={to} onChange={(e) => setTo(e.target.value)}>
            {ids.map((id) => (
              <option key={id} value={id}>
                {id}
              </option>
            ))}
          </select>
        </label>
        <input value={text} onChange={(e) => setText(e.target.value)} placeholder="message" size={40} />
        <button
          disabled={text.trim() === ""}
          onClick={() =>
            run(async () => {
              await post("/inbox", { from: "owner", to, text });
              setText("");
              reload();
              return "sent";
            })
          }
        >
          Send as owner
        </button>
      </div>
      {msg !== "" && <p>{msg}</p>}
    </div>
  );
}

function Chart() {
  const [rows, reload] = usePoll<ResultRow[]>("/results", [], 8000);
  const [status] = usePoll<{ running: boolean; error: string }>("/experiment/status", { running: false, error: "" }, 4000);
  const [msg, run] = useAction();
  const tasks = [...new Set(rows.map((r) => r.task))];
  const arms = ["without", "with"] as const;
  return (
    <div>
      <h2>Experiment: with vs without the Compass</h2>
      <div className="row">
        <button
          disabled={status.running}
          onClick={() =>
            run(async () => {
              await post("/experiment/run", {});
              return "running — results refresh here when done (needs `muse` on PATH)";
            })
          }
        >
          {status.running ? "Running…" : "Run experiment"}
        </button>
        <button onClick={reload}>Refresh results</button>
      </div>
      {status.error !== "" && <p>last run failed: {status.error}</p>}
      {rows.length === 0 && (
        <p className="dim">No results yet. Run it here, or `pengu experiment` in the repo.</p>
      )}
      {arms.map((a) => {
        const rs = rows.filter((r) => r.arm === a && r.matched !== null);
        const v = rs.length === 0 ? 0 : rs.filter((r) => r.matched).length / rs.length;
        return (
          <p key={a}>
            {a}:{" "}
            <svg width="120" height="12" aria-label={`${Math.round(v * 100)}%`}>
              <rect width="120" height="12" fill="var(--border)" />
              <rect width={Math.round(v * 120)} height="12" fill="var(--accent)" />
            </svg>{" "}
            {Math.round(v * 100)}% decisions matched
          </p>
        );
      })}
      {rows.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>task</th>
              <th>arm</th>
              <th>decision</th>
              <th>interruptions</th>
              <th>pass</th>
            </tr>
          </thead>
          <tbody>
            {tasks.flatMap((t) =>
              rows
                .filter((r) => r.task === t)
                .map((r) => (
                  <tr key={`${r.task}-${r.arm}`}>
                    <td>{r.task}</td>
                    <td>{r.arm}</td>
                    <td>{r.matched === null ? "unclear" : r.matched ? "matched" : "missed"}</td>
                    <td>{r.interruptions}</td>
                    <td>{r.passed === null ? "?" : r.passed ? "pass" : "FAIL"}</td>
                  </tr>
                )),
            )}
          </tbody>
        </table>
      )}
      {msg !== "" && <p>{msg}</p>}
    </div>
  );
}

const tabs = ["lines", "proposals", "questions", "sessions", "inbox", "experiment"] as const;

export default function App() {
  const [tab, setTab] = useState<(typeof tabs)[number]>("lines");
  return (
    <div className="app">
      <header>
        <h1>PenguPool AI</h1>
        <nav>
          {tabs.map((t) => (
            <button key={t} disabled={t === tab} onClick={() => setTab(t)}>
              {t}
            </button>
          ))}
        </nav>
      </header>
      <main>
        {tab === "lines" && <Lines goProposals={() => setTab("proposals")} />}
        {tab === "proposals" && <Proposals />}
        {tab === "questions" && <Questions />}
        {tab === "sessions" && <Sessions />}
        {tab === "inbox" && <Inbox />}
        {tab === "experiment" && <Chart />}
      </main>
    </div>
  );
}
