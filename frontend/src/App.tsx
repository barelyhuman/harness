import { useEffect } from "preact/hooks";
import { bus } from "./lib/bus";
import { activeScope } from "./state/scope";
import { termOpen, rightOpen, sidebarOpen, rightMode, sidebarW, rightW, termH, LIMITS, clamp } from "./state/layout";
import { scopeTabs, tabsByScope } from "./state/tabs";
import { openOverlay } from "./state/ui";
import { keyLabel } from "./lib/commands";
import { Sidebar } from "./features/agents/Sidebar";
import { TabStrip } from "./features/shell/TabStrip";
import { Overlays, Toasts } from "./features/shell/Overlays";
import { Icon, Resizer } from "./features/shell/ui";
import { TerminalPanel } from "./features/terminal/TerminalPanel";
import { EditorTab } from "./features/editor/EditorTab";
import { DiffTab } from "./features/diff/DiffTab";
import { ChatTab } from "./features/chat/ChatTab";
import { ScmPanel } from "./features/scm/ScmPanel";
import { SearchPanel } from "./features/editor/SearchPanel";
import { AgentDialogs } from "./features/agents/dialogs";
import { Explorer } from "./features/explorer/Explorer";

function ToggleBtn(p: { icon: string; on: boolean; cmd: string; title: string; onClick: () => void }) {
  return (
    <button class={`no-drag rounded p-1 hover:bg-hover ${p.on ? "text-text" : "text-faint"}`} title={`${p.title} ${keyLabel(p.cmd)}`} onClick={p.onClick}>
      <Icon name={p.icon} size={15} />
    </button>
  );
}

function Center() {
  const s = activeScope.value;
  void tabsByScope.value;
  if (!s) {
    return (
      <div class="flex flex-1 flex-col items-center justify-center gap-3 text-muted">
        <div>No project open</div>
        <button class="btn" onClick={() => openOverlay({ kind: "picker" })}>Open a project (⌘O)</button>
      </div>
    );
  }
  const st = scopeTabs(s.key);
  return (
    <div class="relative min-h-0 flex-1 bg-bg" key={s.key}>
      {st.tabs.map((t) => (
        <div key={t.id} class="absolute inset-0 min-h-0" style={{ display: t.id === st.active ? "block" : "none" }}>
          {t.kind === "chat" ? <ChatTab sessionId={s.sessionId || null} scopeKey={s.key} />
            : t.kind === "file" ? <EditorTab scopeKey={s.key} root={s.root} path={t.path} />
            : <DiffTab scopeKey={s.key} root={s.root} path={t.path} staged={t.staged} />}
        </div>
      ))}
    </div>
  );
}

export function App() {
  const s = activeScope.value;
  useEffect(() => {
    const off = bus.onOpen("app", (ch) =>
      ch.onJSON<{ type: string; shells?: number }>((m) => {
        if (m.type === "quit-request") openOverlay({ kind: "quit", shells: m.shells ?? 0 });
      }),
    );
    return off;
  }, []);

  return (
    <div class="flex h-full w-full overflow-hidden bg-bg text-text">
      {sidebarOpen.value && (
        <>
          <div class="flex shrink-0 flex-col bg-panel" style={{ width: sidebarW.value }}>
            <Sidebar />
          </div>
          <Resizer dir="x" get={() => sidebarW.value} set={(v) => (sidebarW.value = clamp(v, LIMITS.sidebar))} />
        </>
      )}
      <div class="flex min-w-0 flex-1 flex-col">
        <div class="drag flex h-9 shrink-0 items-stretch bg-panel" style={{ paddingLeft: sidebarOpen.value ? 0 : 78 }}>
          {!sidebarOpen.value && (
            <div class="flex items-center pr-1"><ToggleBtn icon="sidebar" on={false} cmd="view.toggleSidebar" title="Show Sidebar" onClick={() => (sidebarOpen.value = true)} /></div>
          )}
          {s ? <TabStrip scopeKey={s.key} /> : null}
          <div class="drag flex-1" />
          <div class="flex items-center gap-0.5 pr-2">
            <ToggleBtn icon="panel" on={termOpen.value} cmd="view.toggleTerminal" title="Terminal" onClick={() => (termOpen.value = !termOpen.value)} />
            <ToggleBtn icon="right" on={rightOpen.value} cmd="view.toggleRightPane" title="Right Pane" onClick={() => (rightOpen.value = !rightOpen.value)} />
          </div>
        </div>
        <div class="flex min-h-0 flex-1">
          <div class="flex min-h-0 min-w-0 flex-1 flex-col"><Center /></div>
          {rightOpen.value && (
            <>
              <Resizer dir="x" sign={-1} get={() => rightW.value} set={(v) => (rightW.value = clamp(v, LIMITS.right))} />
              <div class="flex min-h-0 shrink-0 flex-col bg-panel" style={{ width: rightW.value }}>
                <div class="flex h-8 shrink-0 items-center gap-1 px-2">
                  {(["scm", "explorer", "search"] as const).map((m) => (
                    <button key={m} onClick={() => (rightMode.value = m)} class={`rounded px-2 py-0.5 text-[11.5px] ${rightMode.value === m ? "bg-sel text-text" : "text-muted hover:bg-hover"}`}>
                      {m === "scm" ? "Changes" : m === "explorer" ? "Explorer" : "Search"}
                    </button>
                  ))}
                </div>
                <div class="min-h-0 flex-1 overflow-auto" key={s?.key}>
                  {s ? (rightMode.value === "scm" ? <ScmPanel scopeKey={s.key} root={s.root} /> : rightMode.value === "search" ? <SearchPanel scopeKey={s.key} root={s.root} /> : <Explorer scopeKey={s.key} root={s.root} />) : null}
                </div>
              </div>
            </>
          )}
        </div>
        {s && (
          <>
            {termOpen.value && <Resizer dir="y" sign={-1} get={() => termH.value} set={(v) => (termH.value = clamp(v, LIMITS.term))} />}
            <div class="shrink-0 bg-bg" style={{ height: termOpen.value ? termH.value : 0, display: termOpen.value ? "block" : "none" }} key={s.key}>
              <TerminalPanel scopeKey={s.key} root={s.root} />
            </div>
          </>
        )}
      </div>
      <AgentDialogs />
      <Overlays />
      <Toasts />
    </div>
  );
}
