import { activateTab, closeTab, scopeTabs, tabsByScope, type Tab } from "../../state/tabs";
import { Icon } from "./ui";

const base = (p: string) => p.split("/").pop() ?? p;
const label = (t: Tab) => (t.kind === "chat" ? "Chat" : t.kind === "file" ? base(t.path) : `${base(t.path)} (${t.staged ? "staged" : "changes"})`);

export function TabStrip(p: { scopeKey: string }) {
  void tabsByScope.value;
  const st = scopeTabs(p.scopeKey);
  return (
    <div class="flex min-w-0 items-stretch overflow-x-auto">
      {st.tabs.map((t) => {
        const on = t.id === st.active;
        return (
          <div
            key={t.id}
            title={t.kind === "chat" ? "Chat (⌘0)" : t.path}
            onMouseDown={(e) => { if (e.button === 1) { e.preventDefault(); closeTab(p.scopeKey, t.id); } else activateTab(p.scopeKey, t.id); }}
            class={`no-drag group flex shrink-0 cursor-default items-center gap-1.5 border-r border-border px-3 text-[12px] ${on ? "bg-bg text-text" : "text-muted hover:bg-hover"} ${t.kind === "file" && t.preview ? "italic" : ""}`}
          >
            <Icon name={t.kind === "chat" ? "chat" : t.kind === "diff" ? "diff" : "file"} size={12} class={on ? "text-accent" : ""} />
            <span class="max-w-[160px] truncate">{label(t)}</span>
            {t.kind !== "chat" && (
              <button class="rounded p-0.5 text-faint opacity-0 hover:bg-sel hover:text-text group-hover:opacity-100" style={on ? { opacity: 1 } : {}} onMouseDown={(e) => e.stopPropagation()} onClick={() => closeTab(p.scopeKey, t.id)} aria-label="Close tab">
                <Icon name="x" size={10} />
              </button>
            )}
          </div>
        );
      })}
    </div>
  );
}
