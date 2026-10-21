import { useEffect, useState } from "preact/hooks";
import { ProjectService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import { projects, refreshProjects } from "../../state/agents";
import { toastError } from "../../state/toast";
import { closeOverlay } from "../../state/ui";
import { Modal } from "../shell/ui";

// Setup commands run once, in order, in a "setup" terminal tab whenever a new
// agent worktree is created for this project (spec 12). They never block the
// session: a failure just stays visible in that tab.
export function ProjectSettingsSheet(p: { projectId: string }) {
  const pr = projects.value.find((x) => x.id === p.projectId);
  const [text, setText] = useState((pr?.setupCmds ?? []).join("\n"));
  const [busy, setBusy] = useState(false);

  useEffect(() => { setText((pr?.setupCmds ?? []).join("\n")); }, [p.projectId]);

  const save = async () => {
    setBusy(true);
    try {
      const cmds = text.split("\n").map((l) => l.trim()).filter(Boolean);
      await ProjectService.SetSetupCmds(p.projectId, cmds);
      await refreshProjects();
      closeOverlay();
    } catch (e) { toastError(e); } finally { setBusy(false); }
  };
  const key = (e: KeyboardEvent) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) save(); };

  if (!pr) return null;
  return (
    <Modal width={560} top label="Project settings">
      <div class="flex flex-col gap-3 p-5" onKeyDown={key}>
        <div class="text-[14px] font-semibold">{pr.name}</div>
        <label class="flex flex-col gap-1 text-[11px] text-muted">
          Setup commands
          <textarea
            class="field h-32 resize-none font-mono text-[12px]"
            autofocus
            placeholder={"one command per line, e.g.\nnpm install"}
            value={text}
            onInput={(e) => setText(e.currentTarget.value)}
          />
          <span class="text-faint">Run in order in a new "setup" terminal tab each time an agent starts here. Failures don't block the agent.</span>
        </label>
        <div class="flex items-center justify-end gap-2">
          <span class="kbd mr-auto">⌘⏎ to save</span>
          <button class="btn" onClick={closeOverlay}>Cancel</button>
          <button class="btn btn-primary" disabled={busy} onClick={save}>{busy ? "Saving..." : "Save"}</button>
        </div>
      </div>
    </Modal>
  );
}
