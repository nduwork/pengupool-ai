import { Check, Copy, ExternalLink, GitBranch, GitPullRequest, Lock, RefreshCw, Search } from "lucide-react";
import { useEffect, useMemo, useState, type ReactElement } from "react";
import { toast } from "sonner";

import { useApp } from "../app/ctx";
import { desktop } from "../app/desktop";
import { Button } from "../components/ui/button";
import { Badge, Card, Input, Label, Textarea } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import { cn } from "../lib/utils";

// The GitHub account, through the GitHub CLI's own sign-in: the Settings card, the repository
// picker in New session → Clone, and Propose changes (a pull request, through a fork when the
// account cannot push).

interface Account {
  login: string;
  active: boolean;
  state: string;
}

export interface GitHubStatus {
  state: "no_cli" | "signed_out" | "authorizing" | "signed_in";
  accounts: Account[];
  user?: { login: string; avatar_url: string };
  user_code?: string;
  verification_uri?: string;
  message?: string;
}

export interface GitHubRepo {
  full_name: string;
  description: string;
  private: boolean;
  fork: boolean;
  push: boolean;
  pushed_at: string;
  clone_url: string;
}

const openURL = (u: string): void => (desktop !== undefined ? desktop.openExternal(u) : void window.open(u, "_blank"));

/** github.status, polled while a sign-in is waiting for the browser. */
export function useGitHub(): [GitHubStatus | null, (s: GitHubStatus | undefined) => void, () => void] {
  const app = useApp();
  const [s, setS] = useState<GitHubStatus | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    void app.call<GitHubStatus>("github.status").then((r) => live && r !== undefined && setS(r));
    return () => {
      live = false;
    };
  }, [n]);
  useEffect(() => {
    if (s?.state !== "authorizing") return;
    const t = window.setTimeout(() => setN((x) => x + 1), 2000);
    return () => window.clearTimeout(t);
  }, [s, n]);
  return [s, (r) => r !== undefined && setS(r), () => setN((x) => x + 1)];
}

function Avatar({ login }: { login: string }): ReactElement {
  return <img src={`https://github.com/${encodeURIComponent(login)}.png?size=64`} alt="" className="size-8 shrink-0 rounded-full bg-muted" />;
}

/** Settings → General → GitHub account. */
export function GitHubAccount(): ReactElement {
  const app = useApp();
  const [s, set, reload] = useGitHub();
  const [copied, setCopied] = useState(false);
  const [confirm, setConfirm] = useState("");
  if (s === null) return <p className="text-label text-faint">Loading…</p>;
  const signIn = async (): Promise<void> => set(await app.call<GitHubStatus>("github.login"));

  return (
    <div className="grid grid-cols-1 gap-4">
      <div>
        <h2 className="text-title font-semibold">GitHub account</h2>
        <p className="text-label text-muted-foreground">
          Clone any repository your account can reach, private ones included, and propose changes as pull requests. When you can't push to a repository, PenguPool proposes through your fork. Sign-in
          is the GitHub CLI's, so the token stays in the system keyring and PenguPool never stores it. Files, Git status and diffs work without it.
        </p>
      </div>

      {s.state === "no_cli" ? (
        <Card className="grid grid-cols-1 gap-2 p-4 text-label">
          <p className="font-medium">The GitHub CLI isn't installed</p>
          <p className="text-muted-foreground">
            Install it, then check again: <code className="rounded bg-muted px-1.5 py-0.5 font-mono">brew install gh</code>
          </p>
          <div>
            <Button onClick={reload}>
              <RefreshCw /> Check again
            </Button>
          </div>
        </Card>
      ) : null}

      {s.state === "authorizing" ? (
        <Card className="grid grid-cols-1 justify-items-start gap-3 p-4 text-label">
          <p className="font-medium">Approve PenguPool in your browser</p>
          {s.user_code !== undefined && s.user_code !== "" ? (
            <>
              <p className="text-muted-foreground">GitHub asks for this code:</p>
              <div className="flex items-center gap-2">
                <span className="rounded-md border border-border bg-background px-3 py-1.5 font-mono text-title font-semibold tracking-widest">{s.user_code}</span>
                <Button
                  variant="ghost"
                  onClick={() => {
                    void navigator.clipboard.writeText(s.user_code ?? "").then(() => setCopied(true));
                  }}
                >
                  {copied ? <Check /> : <Copy />} {copied ? "Copied" : "Copy"}
                </Button>
              </div>
              <div className="flex gap-2">
                <Button variant="default" onClick={() => openURL(s.verification_uri ?? "https://github.com/login/device")}>
                  <ExternalLink /> Open GitHub
                </Button>
                <Button variant="ghost" onClick={async () => set(await app.call<GitHubStatus>("github.cancel"))}>
                  Cancel
                </Button>
              </div>
            </>
          ) : (
            <p className="text-muted-foreground">Starting sign-in…</p>
          )}
          <p className="text-caption text-faint">This page updates by itself once you approve.</p>
        </Card>
      ) : null}

      {s.message !== undefined && s.message !== "" ? <p className="rounded-md bg-warning/10 p-2.5 text-caption text-warning">{s.message}</p> : null}

      {s.accounts.length > 0 ? (
        <Card className="divide-y divide-border">
          {s.accounts.map((a) => (
            <div key={a.login} className="flex items-center gap-3 px-4 py-3 text-label">
              <Avatar login={a.login} />
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium">{a.login}</p>
                {a.state !== "success" ? <p className="text-caption text-warning">Needs signing in again</p> : null}
              </div>
              {a.active ? <Badge tone="success">In use</Badge> : null}
              {a.state !== "success" ? (
                <Button size="sm" onClick={() => void signIn()}>
                  Sign in again
                </Button>
              ) : !a.active ? (
                <Button size="sm" variant="ghost" onClick={async () => set(await app.call<GitHubStatus>("github.switch", { login: a.login }))}>
                  Use this account
                </Button>
              ) : null}
              {confirm === a.login ? (
                <>
                  <Button
                    size="sm"
                    variant="danger"
                    onClick={async () => {
                      set(await app.call<GitHubStatus>("github.logout", { login: a.login }));
                      setConfirm("");
                    }}
                  >
                    Sign out of gh
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setConfirm("")}>
                    Keep
                  </Button>
                </>
              ) : (
                <Button size="sm" variant="ghost" onClick={() => setConfirm(a.login)} title="Also signs the GitHub CLI out of this account on this machine">
                  Sign out
                </Button>
              )}
            </div>
          ))}
        </Card>
      ) : null}

      {s.state === "signed_out" || s.state === "signed_in" ? (
        <div>
          <Button variant={s.state === "signed_out" ? "default" : "secondary"} onClick={() => void signIn()}>
            <GitBranch /> {s.state === "signed_out" ? "Sign in with GitHub" : "Add another account"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** New session → Clone: the account's repositories, searchable; picking one fills the URL and destination. */
export function GitHubRepoPicker({ onPick, selected }: { onPick: (r: GitHubRepo) => void; selected: string }): ReactElement {
  const app = useApp();
  const [s] = useGitHub();
  const [repos, setRepos] = useState<GitHubRepo[] | null>(null);
  const [q, setQ] = useState("");
  const [error, setError] = useState("");
  const load = async (refresh: boolean): Promise<void> => {
    setError("");
    try {
      setRepos(await app.conn.request<GitHubRepo[]>("github.repos", { refresh }));
    } catch (e) {
      setError((e as Error).message);
    }
  };
  useEffect(() => {
    if (s?.state === "signed_in") void load(false);
  }, [s?.state, s?.user?.login]);
  const shown = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    return (repos ?? []).filter((r) => words.every((w) => `${r.full_name} ${r.description}`.toLowerCase().includes(w))).slice(0, 200);
  }, [repos, q]);

  if (s === null) return <></>;
  if (s.state !== "signed_in")
    return (
      <p className="rounded-md bg-muted p-2.5 text-caption text-muted-foreground">
        Sign in to GitHub in{" "}
        <button type="button" className="font-medium text-foreground underline underline-offset-2" onClick={() => app.setNav("settings")}>
          Settings
        </button>{" "}
        to pick from your repositories, private ones included.
      </p>
    );
  return (
    <Label hint={`Repositories ${s.user?.login ?? "your account"} can reach, most recently pushed first.`}>
      <span className="flex items-center">
        From GitHub
        <button type="button" onClick={() => void load(true)} className="ml-auto rounded p-0.5 text-faint hover:bg-hover hover:text-foreground" aria-label="Reload repositories">
          <RefreshCw className="size-3.5" />
        </button>
      </span>
      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-faint" />
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search repositories" className="pl-8" aria-label="Search repositories" />
      </div>
      <div className="max-h-52 overflow-auto rounded-md border border-border" role="listbox" aria-label="GitHub repositories">
        {error !== "" ? <p className="p-2.5 text-caption text-danger">{error}</p> : null}
        {repos === null && error === "" ? <p className="p-2.5 text-caption text-faint">Loading repositories…</p> : null}
        {repos !== null && shown.length === 0 ? <p className="p-2.5 text-caption text-faint">No repository matches.</p> : null}
        {shown.map((r) => (
          <button
            key={r.full_name}
            type="button"
            role="option"
            aria-selected={selected === r.clone_url}
            onClick={() => onPick(r)}
            className={cn("grid w-full grid-cols-1 gap-0.5 border-b border-border px-2.5 py-1.5 text-left last:border-b-0 hover:bg-hover", selected === r.clone_url && "bg-selected")}
          >
            <span className="flex items-center gap-1.5 text-label">
              {r.private ? <Lock className="size-3 shrink-0 text-faint" /> : null}
              <span className="truncate font-medium">{r.full_name}</span>
              {!r.push ? (
                <span className="ml-auto shrink-0 text-micro text-faint" title="You can't push here; proposals go through your fork">
                  via fork
                </span>
              ) : null}
            </span>
            {r.description !== "" ? <span className="truncate text-caption text-muted-foreground">{r.description}</span> : null}
          </button>
        ))}
      </div>
    </Label>
  );
}

/** Propose changes: the repository's committed work as a pull request. */
export function ProposeSheet({ path, branch }: { path: string; branch?: string }): ReactElement {
  const app = useApp();
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [pr, setPr] = useState("");
  const close = (): void => app.showDialog(null);
  return (
    <Sheet
      title="Propose changes"
      description={path}
      onClose={close}
      footer={
        pr !== "" ? (
          <>
            <Button variant="ghost" onClick={close}>
              Close
            </Button>
            <Button variant="default" onClick={() => openURL(pr)}>
              <ExternalLink /> View pull request
            </Button>
          </>
        ) : (
          <>
            <Button variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button
              variant="default"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                const r = await app.call<{ pr: string }>("github.propose", { path, title, body });
                setBusy(false);
                if (r !== undefined) {
                  setPr(r.pr);
                  toast.success("Pull request opened");
                }
              }}
            >
              <GitPullRequest /> {busy ? "Proposing…" : "Open pull request"}
            </Button>
          </>
        )
      }
    >
      {pr !== "" ? (
        <p className="text-label">
          Opened <span className="font-mono">{pr}</span>.
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-4">
          <p className="text-caption text-muted-foreground">
            Pushes the current commit{branch !== undefined ? ` of ${branch}` : ""} and opens a pull request. If you can't push to the repository, it goes through your fork, which is created if needed.
            Uncommitted changes are not included; commit them first. Your working copy is left as it is.
          </p>
          <Label hint="Defaults to the last commit's subject.">
            Title
            <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Last commit's subject" autoFocus />
          </Label>
          <Label>
            Description <span className="font-normal text-faint">optional</span>
            <Textarea value={body} onChange={(e) => setBody(e.target.value)} rows={4} />
          </Label>
        </div>
      )}
    </Sheet>
  );
}
