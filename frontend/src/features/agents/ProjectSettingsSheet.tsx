import { useEffect, useState } from "preact/hooks";
import { ProjectService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import { projects, refreshProjects } from "../../state/agents";
import { loadRoles, rolesByProject, setRole, type Role } from "../../state/roles";
import { toastError } from "../../state/toast";
import { closeOverlay } from "../../state/ui";
import { RoleChip } from "../composer/RoleChip";
import { Modal } from "../shell/ui";

const ROLES: { role: Exclude<Role, "specsDir">; label: string; hint: string }[] = [
  { role: "planner", label: "Planner", hint: "Runs sessions started in Plan mode." },
  { role: "executor", label: "Executor", hint: "Takes over when you approve a plan (another provider forks a new session)." },
  { role: "explorer", label: "Explorer", hint: "Read-only search subagent for Claude sessions (applies at the next launch)." },
];

// Setup commands run once, in order, in a "setup" terminal tab whenever a new
// agent worktree is created for this project (spec 12). They never block the
// session: a failure just stays visible in that tab.
export function ProjectSettingsSheet(p: { projectId: string; role?: Role }) {
  const pr = projects.value.find((x) => x.id === p.projectId);
  const [text, setText] = useState((pr?.setupCmds ?? []).join("\n"));
  const [busy, setBusy] = useState(false);
  const info = rolesByProject.value[p.projectId];
  const [specsDir, setSpecsDir] = useState(info?.project.specsDir ?? "");

  useEffect(() => { setText((pr?.setupCmds ?? []).join("\n")); }, [p.projectId]);
  useEffect(() => { void loadRoles(p.projectId).then((r) => setSpecsDir(r?.project.specsDir ?? "")); }, [p.projectId]);

  const save = async () => {
    setBusy(true);
    try {
      const cmds = text.split("\n").map((l) => l.trim()).filter(Boolean);
      await ProjectService.SetSetupCmds(p.projectId, cmds);
      if (specsDir.trim() !== (info?.project.specsDir ?? "")) await setRole(p.projectId, "specsDir", specsDir.trim());
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
        <div class="flex flex-col gap-1 text-[11px] text-muted">
          Agents
          {ROLES.map(({ role, label, hint }) => {
            const own = info?.project[role] ?? "";
            const def = info?.defaults[role] ?? "";
            return (
              <div key={role} class="flex items-center gap-2">
                <RoleChip label={label} value={info?.roles[role] ?? ""} placeholder={role === "explorer" ? "built-in" : role === "executor" ? "same model" : "session model"} below autoOpen={p.role === role} title={hint} onPick={(k) => void setRole(p.projectId, role, k)} />
                <span class="truncate text-faint">{hint}</span>
                {own && <button class="ml-auto shrink-0 text-faint hover:text-text hover:underline" title={def ? `settings.json: ${def}` : "no settings.json default"} onClick={() => void setRole(p.projectId, role, "")}>reset</button>}
              </div>
            );
          })}
          <span class="text-faint">Defaults come from <span class="font-mono">agents.*</span> in settings.json; choices here apply to this project only.</span>
        </div>
        <label class="flex flex-col gap-1 text-[11px] text-muted">
          Plan specs folder
          <input class="field font-mono text-[12px]" placeholder={info?.defaults.specsDir ?? "docs-spec/plans"} value={specsDir} onInput={(e) => setSpecsDir(e.currentTarget.value)} />
          <span class="text-faint">Every plan an agent shows is saved here (relative to the project root) and pointed to in new sessions.</span>
        </label>
        <label class="flex flex-col gap-1 text-[11px] text-muted">
          Setup commands
          <textarea
            class="field h-32 resize-none font-mono text-[12px]"
            autofocus={!p.role}
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
