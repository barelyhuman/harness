// Centre tab strip per scope (spec 03): pinned Chat tab first, then file and diff tabs.
// Fixed contract shared by feature modules; the shell owner may extend internals.
import { signal } from "@preact/signals";

export type Tab =
  | { id: string; kind: "chat"; pinned: true }
  | { id: string; kind: "file"; path: string; preview?: boolean }
  | { id: string; kind: "diff"; path: string; staged: boolean };

export type ScopeTabs = { tabs: Tab[]; active: string };

export const tabsByScope = signal<Record<string, ScopeTabs>>({});

function get(scopeKey: string): ScopeTabs {
  return tabsByScope.value[scopeKey] ?? { tabs: [{ id: "chat", kind: "chat", pinned: true }], active: "chat" };
}

function put(scopeKey: string, st: ScopeTabs) {
  tabsByScope.value = { ...tabsByScope.value, [scopeKey]: st };
}

export function scopeTabs(scopeKey: string): ScopeTabs {
  return get(scopeKey);
}

export function openTab(scopeKey: string, tab: Exclude<Tab, { kind: "chat" }>): void {
  const st = get(scopeKey);
  const existing = st.tabs.find((t) => t.id === tab.id);
  let tabs = st.tabs;
  if (!existing) {
    // A preview tab is replaced by the next preview tab.
    tabs = tab.kind === "file" && tab.preview ? tabs.filter((t) => !(t.kind === "file" && t.preview)) : tabs;
    tabs = [...tabs, tab];
  }
  put(scopeKey, { tabs, active: tab.id });
}

export function activateTab(scopeKey: string, id: string): void {
  put(scopeKey, { ...get(scopeKey), active: id });
}

export function closeTab(scopeKey: string, id: string): void {
  const st = get(scopeKey);
  if (id === "chat") return;
  const idx = st.tabs.findIndex((t) => t.id === id);
  const tabs = st.tabs.filter((t) => t.id !== id);
  const active = st.active === id ? (tabs[Math.max(0, idx - 1)]?.id ?? "chat") : st.active;
  put(scopeKey, { tabs, active });
}

export const fileTabId = (path: string) => `file:${path}`;
export const diffTabId = (path: string, staged: boolean) => `diff:${staged ? "staged" : "work"}:${path}`;
