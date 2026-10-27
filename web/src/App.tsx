import { useCallback, useEffect, useState } from "react";
import {
  api,
  type InboxEntry,
  type Line,
  type Proposal,
  type ResultRow,
  type SessionRow,
} from "./api";

function usePoll<T>(path: string, empty: T, ms = 5000): [T, () => void] {
  const [data, setData] = useState<T>(empty);
  const load = useCallback(() => {
    api<T>(path).then(setData).catch(() => {});
  }, [path]);
  useEffect(() => {
    load();
    const t = setInterval(load, ms);
    return () => clearInterval(t);
  }, [load, ms]);
  return [data, load];
}

function Lines() {
  const [lines] = usePoll<Line[]>("/lines", []);
  if (lines.length === 0) return <p>No confirmed lines yet. Say a rule once.</p>;
  return (
    <ul>
      {lines.map((l) => (
        <li key={l.id}>
          {l.rule} <span className="dim">({l.confidence.toFixed(2)})</span>
        </li>
      ))}
    </ul>
  );
}

function Proposals() {
  const [items, reload] = usePoll<Proposal[]>("/proposals", []);
  const [done, setDone] = useState("");
  async function confirm(id: string) {
    try {
      const r = await api<{ applied: string; id?: string }>(`/proposals/${id}/confirm`, { method: "POST" });
      setDone(`${r.applied} ${r.id ?? ""}`);
      reload();
    } catch (e) {
      setDone(`failed: ${e}`);
    }
  }
  const pending = items.filter((p) => p.status === "proposed");
  return (
    <div>
      {done !== "" && <p>{done}</p>}
      {pending.length === 0 && <p>Nothing awaiting confirmation.</p>}
      <ul>
        {pending.map((p) => (
          <li key={p.id}>
            <span className="kind">{p.kind}</span> {p.words}{" "}
            <button onClick={() => void confirm(p.id)}>Confirm</button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Sessions() {
  const [rows] = usePoll<SessionRow[]>("/sessions", []);
  if (rows.length === 0) return <p>No sessions.</p>;
  return (
    <ul>
      {rows.map((r) => (
        <li key={r.id}>
          {r.id} · {r.agent} · {r.alive ? "alive" : "dead"} · {r.task.slice(0, 80)}
        </li>
      ))}
    </ul>
  );
}

function Inbox() {
  const [who, setWho] = useState("owner");
  const [entries] = usePoll<InboxEntry[]>(`/inbox?session=${encodeURIComponent(who)}`, [], 5000);
  return (
    <div>
      <label>
        Session{" "}
        <input value={who} onChange={(e) => setWho(e.target.value)} placeholder="owner or session id" />
      </label>
      <ul>
        {entries.map((e) => (
          <li key={e.id}>
            from {e.from}: {e.text}
          </li>
        ))}
      </ul>
    </div>
  );
}

function Chart() {
  const [rows] = usePoll<ResultRow[]>("/results", []);
  if (rows.length === 0) return <p>No experiment results yet. Run: pengu experiment</p>;
  const tasks = [...new Set(rows.map((r) => r.task))];
  const arms = ["without", "with"] as const;
  return (
    <div>
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
    </div>
  );
}

const tabs = ["lines", "proposals", "sessions", "inbox", "experiment"] as const;

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
        {tab === "lines" && <Lines />}
        {tab === "proposals" && <Proposals />}
        {tab === "sessions" && <Sessions />}
        {tab === "inbox" && <Inbox />}
        {tab === "experiment" && <Chart />}
      </main>
    </div>
  );
}
