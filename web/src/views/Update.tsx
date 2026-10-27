import { ArrowUpCircle, Loader2 } from "lucide-react";
import { useState, type ReactElement } from "react";

import { useApp } from "../app/ctx";
import { localDesktop } from "../app/desktop";
import { Button } from "../components/ui/button";
import { Sheet } from "../components/ui/dialog";
import type { Update } from "../protocol/domain";

// In-app update: a pill in the title bar when the host found a newer release. Its sheet has the
// host download the disk image and the desktop app install it and restart; agent sessions keep
// running. In a browser (or on an SSH host) it links to the release page instead.

export function UpdatePill(): ReactElement | null {
  const app = useApp();
  const u = app.domain.update;
  if (u?.newer !== true) return null;
  return (
    <button
      type="button"
      className="no-drag flex items-center gap-1 rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-caption font-medium text-primary hover:bg-primary/20"
      onClick={() => app.showDialog(<UpdateSheet u={u} onClose={() => app.showDialog(null)} />)}
    >
      <ArrowUpCircle className="size-3.5" /> Update {u.latest}
    </button>
  );
}

export function UpdateSheet({ u, onClose }: { u: Update; onClose: () => void }): ReactElement {
  const app = useApp();
  const [step, setStep] = useState<"" | "download" | "restart">("");
  // From 0.8.3 PenguPool is built for Apple Silicon only.
  const intel = localDesktop !== undefined && localDesktop.arch !== "arm64";
  const [error, setError] = useState(intel ? "PenguPool now runs on Apple Silicon Macs only; this Mac can't install the update." : "");
  const install = async (): Promise<void> => {
    if (localDesktop === undefined || u.tag === undefined || intel) return;
    setError("");
    try {
      setStep("download");
      const r = await app.conn.request<{ path: string }>("update.download", { tag: u.tag, asset: u.asset }, 17 * 60_000);
      setStep("restart");
      await localDesktop.installUpdate(r.path);
    } catch (e) {
      setStep("");
      setError((e as Error).message.replace(/^Error invoking remote method '[^']+': (Error: )?/, ""));
    }
  };
  return (
    <Sheet
      title={`PenguPool ${u.latest ?? ""}`}
      description={`You have ${u.current}.`}
      onClose={step === "" ? onClose : () => undefined}
      footer={
        <>
          {error !== "" ? <p className="mr-auto self-center text-label text-danger">{error}</p> : null}
          <Button variant="ghost" disabled={step !== ""} onClick={onClose}>
            Later
          </Button>
          {localDesktop !== undefined ? (
            <Button variant="default" disabled={step !== "" || u.tag === undefined || intel} onClick={() => void install()}>
              {step !== "" ? <Loader2 className="animate-spin" /> : null}
              {step === "download" ? "Downloading…" : step === "restart" ? "Restarting…" : "Update and restart"}
            </Button>
          ) : u.url !== undefined ? (
            <a className="inline-flex h-8 items-center rounded-md bg-primary px-3 text-label font-medium text-primary-foreground hover:bg-primary/90" href={u.url} target="_blank" rel="noreferrer">
              Open the release page
            </a>
          ) : null}
        </>
      }
    >
      {u.notes !== undefined && u.notes !== "" ? <pre className="mb-3 whitespace-pre-wrap font-sans text-label">{u.notes}</pre> : null}
      <p className="text-label text-muted-foreground">
        {localDesktop !== undefined ? "Running agent sessions keep running; PenguPool restarts in a few seconds." : "Download the disk image from the release page, open it and drag PenguPool.app onto Applications."}
      </p>
    </Sheet>
  );
}
