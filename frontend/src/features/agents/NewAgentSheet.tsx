import { useEffect, useState } from "preact/hooks";
import { ProjectService, SessionService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import * as Sess from "../../lib/bindings/github.com/sid/harness/internal/app/sessionservice";
import { currentProjectId, projects, refreshSessions } from "../../state/agents";
import { activateSession } from "../../state/scope";
import { toast, toastError } from "../../state/toast";
import { closeOverlay } from "../../state/ui";
import { Modal } from "../shell/ui";

type ModelInfo = { key: string; provider: string; id: string; label: string; favourite?: boolean };
const FALLBACK: ModelInfo[] = [
  { key: "claude/default", provider: "claude", id: "default", label: "Claude (default)" },
  { key: "cursor/auto", provider: "cursor", id: "auto", label: "Cursor (auto)" },
];

export function NewAgentSheet(p: { projectId?: string }) {
  const [pid, setPid] = useState(p.projectId ?? currentProjectId.value ?? projects.value[0]?.id ?? "");
  const [models, setModels] = useState<ModelInfo[]>(FALLBACK);
  const [model, setModel] = useState("");
  const [base, setBase] = useState("");
  const [title, setTitle] = useState("");
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const lm = (Sess as any).ListModels as undefined | (() => Promise<ModelInfo[] | null>);
    lm?.().then((m) => m?.length && setModels(m)).catch(() => {});
  }, []);
  useEffect(() => { if (!model && models.length) setModel((models.find((m) => m.favourite) ?? models[0]).key); }, [models]);
  useEffect(() => { // default base ref is the repo's branch, resolved by Go; hint from RepoBadges
    const pr = projects.value.find((x) => x.id === pid);
    if (!pr) return;
    ProjectService.RepoBadges([pr.path]).then((b) => setBase(b?.[pr.path]?.branch ?? "")).catch(() => {});
  }, [pid]);

  const m = models.find((x) => x.key === model);
  const go = async () => {
    if (!pid || !m) return;
    setBusy(true);
    try {
      const s = await SessionService.CreateSession(pid, {
        title: title.trim(), provider: m.provider, model: m.key, mode: "", effort: "", scopeKind: "worktree", baseRef: base, forkedFrom: "",
      });
      await refreshSessions(pid);
      closeOverlay();
      await activateSession(s.id);
      const send = (Sess as any).Send as undefined | ((id: string, m: unknown) => Promise<void>);
      if (prompt.trim()) {
        if (send) await send(s.id, { text: prompt, attachments: [], delivery: "send" });
        else toast("Agent created; SessionService.Send is not available yet, so the prompt was not sent", "info");
      }
    } catch (e) { toastError(e); setBusy(false); }
  };
  const key = (e: KeyboardEvent) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) go(); };

  return (
    <Modal width={560} top label="New agent">
      <div class="flex flex-col gap-3 p-5" onKeyDown={key}>
        <div class="text-[14px] font-semibold">New agent</div>
        <div class="grid grid-cols-2 gap-3">
          <label class="flex flex-col gap-1 text-[11px] text-muted">Project
            <select class="field" value={pid} onChange={(e) => setPid(e.currentTarget.value)}>
              {projects.value.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
            </select>
          </label>
          <label class="flex flex-col gap-1 text-[11px] text-muted">Base branch
            <input class="field" value={base} placeholder="current branch" onInput={(e) => setBase(e.currentTarget.value)} />
          </label>
          <label class="col-span-2 flex flex-col gap-1 text-[11px] text-muted">Provider / model
            <select class="field" value={model} onChange={(e) => setModel(e.currentTarget.value)}>
              {models.map((x) => <option key={x.key} value={x.key}>{x.provider} · {x.label}</option>)}
            </select>
          </label>
        </div>
        <input class="field" placeholder="Title (optional)" value={title} onInput={(e) => setTitle(e.currentTarget.value)} />
        <textarea class="field h-32 resize-none" autofocus placeholder="What should the agent do?" value={prompt} onInput={(e) => setPrompt(e.currentTarget.value)} />
        <div class="flex items-center justify-end gap-2">
          <span class="kbd mr-auto">⌘⏎ to create</span>
          <button class="btn" onClick={closeOverlay}>Cancel</button>
          <button class="btn btn-primary" disabled={busy || !pid} onClick={go}>{busy ? "Creating..." : "Create"}</button>
        </div>
      </div>
    </Modal>
  );
}
