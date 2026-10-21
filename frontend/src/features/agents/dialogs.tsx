import { signal } from "@preact/signals";
import { useState } from "preact/hooks";
import { Browser } from "@wailsio/runtime";
import { SessionService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import { refreshSessions, type SessionRow } from "../../state/agents";
import { rightMode, rightOpen } from "../../state/layout";
import { activateSession } from "../../state/scope";
import { errInfo, toast, toastError } from "../../state/toast";
import { Modal } from "../shell/ui";

type D =
  | { kind: "rename" | "restore" | "delete" | "pr"; s: SessionRow }
  | { kind: "archive"; s: SessionRow; files?: string[] };
const dialog = signal<D | null>(null);
const close = () => (dialog.value = null);

export function openAgentDialog(d: D) {
  dialog.value = d;
  // Archive tries without force first; only a dirty error brings up the confirm.
  if (d.kind === "archive") void archive(d.s, false);
}

async function archive(s: SessionRow, force: boolean) {
  try {
    await SessionService.ArchiveSession(s.id, force);
    close();
    toast("Archived " + (s.title || "agent"), "success");
    await refreshSessions(s.projectId);
  } catch (e) {
    const i = errInfo(e);
    if (i.code === "dirty") dialog.value = { kind: "archive", s, files: i.files ?? [] };
    else { close(); toastError(e); }
  }
}

function FileList(p: { files?: string[] }) {
  return (
    <div class="my-2 max-h-40 overflow-auto rounded bg-bg p-2 font-mono text-[11px] text-muted">
      {(p.files ?? []).map((f) => <div key={f}>{f}</div>)}
    </div>
  );
}

function Frame(p: { title: string; children: any; actions: any }) {
  return (
    <Modal width={440} onClose={close}>
      <div class="p-5">
        <div class="mb-2 text-[14px] font-semibold">{p.title}</div>
        {p.children}
        <div class="mt-4 flex justify-end gap-2">{p.actions}</div>
      </div>
    </Modal>
  );
}

function Rename(p: { s: SessionRow }) {
  const [v, setV] = useState(p.s.title);
  const go = async () => {
    try { await SessionService.RenameSession(p.s.id, v.trim()); close(); await refreshSessions(p.s.projectId); } catch (e) { toastError(e); }
  };
  return (
    <Frame title="Rename agent" actions={<><button class="btn" onClick={close}>Cancel</button><button class="btn btn-primary" onClick={go}>Rename</button></>}>
      <input class="field" autofocus ref={(el) => el?.select()} value={v} onInput={(e) => setV(e.currentTarget.value)} onKeyDown={(e) => e.key === "Enter" && go()} />
    </Frame>
  );
}

function Pr(p: { s: SessionRow }) {
  const [title, setTitle] = useState(p.s.title);
  const [body, setBody] = useState("");
  const [files, setFiles] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const go = async () => {
    setBusy(true);
    try {
      const url = await SessionService.RaisePR(p.s.id, title, body);
      close();
      toast("PR raised: " + url, "success");
      void Browser.OpenURL(url);
      await refreshSessions(p.s.projectId);
    } catch (e) {
      const i = errInfo(e);
      if (i.code === "dirty") setFiles(i.files ?? []);
      else toastError(e);
    }
    setBusy(false);
  };
  const openChanges = async () => {
    close();
    await activateSession(p.s.id);
    rightMode.value = "scm";
    rightOpen.value = true;
  };
  return (
    <Frame
      title="Raise pull request"
      actions={files ? <><button class="btn" onClick={close}>Close</button><button class="btn btn-primary" onClick={openChanges}>Open Changes</button></> : <><button class="btn" onClick={close}>Cancel</button><button class="btn btn-primary" disabled={busy || !title.trim()} onClick={go}>{busy ? "Raising..." : "Raise PR"}</button></>}
    >
      {files ? (
        <div class="text-muted">Commit or discard these changes first:<FileList files={files} /></div>
      ) : (
        <div class="flex flex-col gap-2">
          <input class="field" autofocus placeholder="Title" value={title} onInput={(e) => setTitle(e.currentTarget.value)} />
          <textarea class="field h-28 resize-none" placeholder="Description" value={body} onInput={(e) => setBody(e.currentTarget.value)} />
        </div>
      )}
    </Frame>
  );
}

export function AgentDialogs() {
  const d = dialog.value;
  if (!d) return null;
  const name = d.s.title || "this agent";
  switch (d.kind) {
    case "rename": return <Rename s={d.s} />;
    case "pr": return <Pr s={d.s} />;
    case "archive":
      if (!d.files) return null; // first attempt in flight
      return (
        <Frame title="Archive with uncommitted changes?" actions={<><button class="btn" onClick={close}>Cancel</button><button class="btn btn-danger" onClick={() => archive(d.s, true)}>Archive anyway</button></>}>
          <div class="text-muted">The worktree for {name} has changes:<FileList files={d.files} /></div>
        </Frame>
      );
    case "restore":
      return (
        <Frame title="Restore agent" actions={<><button class="btn" onClick={close}>Cancel</button><button class="btn btn-primary" onClick={async () => { try { await SessionService.RestoreSession(d.s.id); close(); await refreshSessions(d.s.projectId); } catch (e) { toastError(e); } }}>Restore</button></>}>
          <div class="text-muted">Restore {name} and its worktree?</div>
        </Frame>
      );
    case "delete":
      return (
        <Frame title="Delete agent" actions={<><button class="btn" onClick={close}>Cancel</button><button class="btn btn-danger" onClick={async () => { try { await SessionService.DeleteSession(d.s.id, true); close(); await refreshSessions(d.s.projectId); } catch (e) { toastError(e); } }}>Delete</button></>}>
          <div class="text-muted">Permanently delete {name}? This cannot be undone.</div>
        </Frame>
      );
  }
}
