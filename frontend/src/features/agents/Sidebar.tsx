import { signal } from "@preact/signals";
import { Browser } from "@wailsio/runtime";
import { TermService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { Node } from "../../lib/bindings/github.com/sid/harness/internal/termlayout/models";
import { keyLabel } from "../../lib/commands";
import { currentProjectId, expanded, projects, sessionsByProject, termLayouts, toggleExpanded, type SessionRow } from "../../state/agents";
import { sidebarOpen, termOpen } from "../../state/layout";
import { newAgent } from "../../state/newagent";
import { activateMain, activateSession, activeScope } from "../../state/scope";
import { toastError } from "../../state/toast";
import { openOverlay, overlay } from "../../state/ui";
import { ProjectPicker } from "../projectpicker/ProjectPicker";
import { Icon } from "../shell/ui";
import { openAgentDialog } from "./dialogs";

const menu = signal<{ x: number; y: number; s: SessionRow } | null>(null);
const showArchived = signal<Record<string, boolean>>({});

const STATUS: Record<string, { color: string; label: string; anim?: boolean }> = {
  running: { color: "bg-run", label: "Running", anim: true },
  needs_approval: { color: "bg-warn", label: "Needs approval" },
  idle: { color: "bg-faint", label: "Idle" },
  error: { color: "bg-err", label: "Error" },
  archived: { color: "bg-faint/40", label: "Archived" },
};

function StatusDot(p: { status: string }) {
  const s = STATUS[p.status] ?? STATUS.idle;
  return <span title={s.label} class={`inline-block h-[7px] w-[7px] shrink-0 rounded-full ${s.color} ${s.anim ? "dot-run" : ""}`} />;
}

function ProviderIcon(p: { provider: string }) {
  return p.provider === "cursor" ? <Icon name="cursor" size={12} class="text-muted" /> : <Icon name="sparkle" size={12} class="text-[#d9885c]" />;
}

function prColor(state: string) {
  return state === "merged" ? "text-[#b392f0] border-[#b392f0]/40" : state === "closed" ? "text-err border-err/40" : "text-run border-run/40";
}

async function focusPane(scopeKey: string, paneId: string, ensure: () => Promise<void>) {
  try {
    if (activeScope.value?.key !== scopeKey) await ensure();
    termOpen.value = true;
    await TermService.FocusPane(scopeKey, paneId);
  } catch (e) { toastError(e); }
}

function leaves(n: Node | undefined, out: Node[] = []): Node[] {
  if (!n) return out;
  if (n.type === "leaf") out.push(n);
  else n.children?.forEach((c) => leaves(c, out));
  return out;
}

function Terminals(p: { scopeKey: string; ensure: () => Promise<void> }) {
  const l = termLayouts.value[p.scopeKey];
  if (!l?.tabs?.length) return <div class="py-1 pl-9 text-[11px] text-faint">No terminals</div>;
  return (
    <div class="pb-1">
      {l.tabs.map((t) => {
        const ls = leaves(t.root);
        return (
          <div key={t.id}>
            <div class="flex h-6 items-center gap-1.5 pl-8 pr-2 text-[11.5px] text-muted">
              <Icon name="terminal" size={11} />
              <span class="truncate">{t.title || "Terminal"}</span>
            </div>
            {ls.map((n) => {
              const focused = t.focused === n.paneId && l.active === t.id;
              return (
                <div
                  key={n.paneId}
                  onClick={() => n.paneId && focusPane(p.scopeKey, n.paneId, p.ensure)}
                  class={`mx-1.5 flex h-6 cursor-default items-center gap-1.5 rounded-md pr-2 text-[11.5px] hover:bg-hover ${focused ? "text-text" : "text-muted"}`}
                  style={{ paddingLeft: ls.length > 1 ? 44 : 32 }}
                >
                  <span class={`h-[5px] w-[5px] shrink-0 rounded-full ${focused ? "bg-accent" : "bg-faint"}`} />
                  <Icon name={n.kind === "agent" ? "sparkle" : n.kind === "setup" ? "check" : "terminal"} size={10} />
                  <span class="truncate">{n.kind === "agent" ? "agent run" : n.kind === "setup" ? "setup" : "shell"}{ls.length > 1 ? ` ${ls.indexOf(n) + 1}` : ""}</span>
                </div>
              );
            })}
          </div>
        );
      })}
    </div>
  );
}

function Row(p: { rowKey: string; active: boolean; onClick: () => void; ensure: () => Promise<void>; session?: SessionRow; title: string; branch?: string; dirty?: number | boolean; onMenu?: (e: MouseEvent) => void }) {
  const open = expanded.value.has(p.rowKey);
  const s = p.session;
  const dirtyN = typeof p.dirty === "number" ? p.dirty : p.dirty ? 1 : 0;
  return (
    <div>
      <div
        onClick={p.onClick}
        onContextMenu={p.onMenu}
        class={`row group cursor-default ${p.active ? "bg-sel text-text" : "text-text/90 hover:bg-hover"} ${s?.status === "archived" ? "opacity-60" : ""}`}
      >
        <button class="icon-btn-sm !h-[18px] !w-[18px] text-faint hover:text-text" onClick={(e) => { e.stopPropagation(); toggleExpanded(p.rowKey); }} aria-label="Toggle terminals">
          <Icon name="chevron" size={11} class={`transition-transform ${open ? "rotate-90" : ""}`} />
        </button>
        {s ? <StatusDot status={s.status} /> : <Icon name="folder" size={12} class="text-muted" />}
        {s && <ProviderIcon provider={s.provider} />}
        <span class="min-w-0 flex-1 truncate">{p.title}</span>
        {p.branch && <span class="flex max-w-[84px] shrink-0 items-center gap-0.5 truncate text-[10.5px] text-faint"><Icon name="branch" size={10} /><span class="truncate">{p.branch}</span></span>}
        {dirtyN > 0 && <span title={`${dirtyN} changed file(s)`} class="shrink-0 rounded bg-warn/15 px-1 text-[10px] text-warn">{dirtyN}</span>}
        {s?.prUrl && (
          <button title={`PR ${s.prState || "open"}`} class={`shrink-0 rounded border px-1 text-[9.5px] ${prColor(s.prState)}`} onClick={(e) => { e.stopPropagation(); void Browser.OpenURL(s.prUrl); }}>PR</button>
        )}
        {s && p.onMenu && (
          <button class="icon-btn-sm shrink-0 text-faint opacity-0 hover:text-text group-hover:opacity-100" onClick={(e) => { e.stopPropagation(); p.onMenu!(e as unknown as MouseEvent); }} aria-label="Menu"><Icon name="more" size={12} /></button>
        )}
      </div>
      {open && <Terminals scopeKey={p.rowKey} ensure={p.ensure} />}
    </div>
  );
}

function ContextMenu() {
  const m = menu.value;
  if (!m) return null;
  const close = () => (menu.value = null);
  const archived = m.s.status === "archived";
  const items: [string, () => void, boolean?][] = [
    ["Rename...", () => openAgentDialog({ kind: "rename", s: m.s })],
    ...(archived
      ? ([["Restore", () => openAgentDialog({ kind: "restore", s: m.s })]] as [string, () => void][])
      : ([["Archive", () => openAgentDialog({ kind: "archive", s: m.s })]] as [string, () => void][])),
    ["Raise PR...", () => openAgentDialog({ kind: "pr", s: m.s })],
    ["Delete...", () => openAgentDialog({ kind: "delete", s: m.s }), true],
  ];
  return (
    <>
      <div class="fixed inset-0 z-40" onMouseDown={close} onContextMenu={(e) => { e.preventDefault(); close(); }} />
      <div class="pop menu fixed z-50 min-w-[160px] bg-raise shadow-xl shadow-black/50" style={{ left: Math.min(m.x, window.innerWidth - 180), top: Math.min(m.y, window.innerHeight - 160) }}>
        {items.map(([label, fn, danger]) => (
          <div key={label} onClick={() => { close(); fn(); }} class={`menu-item cursor-default hover:bg-sel ${danger ? "text-err" : ""}`}>{label}</div>
        ))}
      </div>
    </>
  );
}

export function Sidebar() {
  const cur = projects.value.find((p) => p.id === currentProjectId.value);
  const scope = activeScope.value;
  const openMenu = (s: SessionRow) => (e: MouseEvent) => { e.preventDefault(); e.stopPropagation(); menu.value = { x: e.clientX, y: e.clientY, s }; };
  return (
    <>
      <div class="drag flex h-9 shrink-0 items-center justify-end pr-2" style={{ paddingLeft: 78 }}>
        <button class="no-drag icon-btn text-muted hover:bg-hover hover:text-text" title={`Hide sidebar ${keyLabel("view.toggleSidebar")}`} onClick={() => (sidebarOpen.value = false)}>
          <Icon name="sidebar" size={15} />
        </button>
      </div>
      <div class="relative px-2 pb-1 pt-2">
        <button class="flex h-9 w-full items-center gap-2 rounded-md px-2.5 text-left hover:bg-hover" title={`Switch project ${keyLabel("projects.openPicker")}`} onClick={() => openOverlay({ kind: "picker" })}>
          <Icon name="folder" size={13} class="text-accent" />
          <span class="min-w-0 flex-1 truncate font-medium">{cur?.name ?? "Open project"}</span>
          <Icon name="chevron" size={11} class="rotate-90 text-faint" />
        </button>
        {overlay.value?.kind === "picker" && <ProjectPicker />}
      </div>
      <div class="px-2 pb-2">
        <button class="btn flex w-full items-center justify-center gap-1.5" onClick={() => void newAgent(currentProjectId.value ?? undefined)}>
          <Icon name="plus" size={12} /> New Agent <span class="kbd ml-1">{keyLabel("agents.new")}</span>
        </button>
      </div>
      <div class="min-h-0 flex-1 overflow-y-auto pb-3">
        {projects.value.length === 0 && <div class="px-4 py-6 text-center text-muted">No projects yet. Press ⌘O to add one.</div>}
        {projects.value.map((pr) => {
          const all = sessionsByProject.value[pr.id] ?? [];
          const live = all.filter((s) => s.status !== "archived");
          const arch = all.filter((s) => s.status === "archived");
          const mainKey = `main:${pr.id}`;
          return (
            <div key={pr.id} class="mt-3 first:mt-0">
              <div class="group flex h-7 items-center gap-1 px-4 text-[10.5px] font-semibold uppercase tracking-wide text-faint">
                <span class="truncate">{pr.name}</span>
                <button class="icon-btn-sm ml-auto opacity-0 hover:text-text group-hover:opacity-100" title="Project settings" onClick={() => openOverlay({ kind: "projectsettings", projectId: pr.id })}><Icon name="gear" size={11} /></button>
                <button class="icon-btn-sm opacity-0 hover:text-text group-hover:opacity-100" title="New agent in project" onClick={() => void newAgent(pr.id)}><Icon name="plus" size={11} /></button>
              </div>
              <Row rowKey={mainKey} title="Main checkout" active={scope?.key === mainKey} onClick={() => activateMain(pr.id)} ensure={() => activateMain(pr.id)} />
              {live.map((s) => {
                const key = `session:${s.id}`;
                return <Row key={s.id} rowKey={key} session={s} title={s.title || s.branch || "New agent"} branch={s.branch} dirty={s.dirty} active={scope?.key === key} onClick={() => activateSession(s.id)} ensure={() => activateSession(s.id)} onMenu={openMenu(s)} />;
              })}
              {arch.length > 0 && (
                <>
                  <div class="flex h-7 cursor-default items-center gap-1 pl-4 text-[11px] text-faint hover:text-muted" onClick={() => (showArchived.value = { ...showArchived.value, [pr.id]: !showArchived.value[pr.id] })}>
                    <Icon name="chevron" size={10} class={showArchived.value[pr.id] ? "rotate-90" : ""} /> Archived ({arch.length})
                  </div>
                  {showArchived.value[pr.id] && arch.map((s) => (
                    <Row key={s.id} rowKey={`session:${s.id}`} session={s} title={s.title || s.branch || "New agent"} branch={s.branch} active={false} onClick={() => openAgentDialog({ kind: "restore", s })} ensure={() => activateSession(s.id)} onMenu={openMenu(s)} />
                  ))}
                </>
              )}
            </div>
          );
        })}
      </div>
      <ContextMenu />
    </>
  );
}
