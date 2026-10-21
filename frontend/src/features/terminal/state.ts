// Shared module-level terminal state (signals + per-scope bookkeeping).
import { signal } from "@preact/signals";
import type { Layout, Node, Tab } from "../../lib/bindings/github.com/sid/harness/internal/termlayout/models";

export type { Layout, Node, Tab };

/** Scope whose TerminalPanel is currently mounted; terminal commands act on it. */
export const activeTerminalScope = signal<string>("");

/** Latest Go-pushed layout per scope. */
export const layouts = signal<Record<string, Layout>>({});

/** Scope root (cwd for new panes), recorded when a panel mounts. */
export const scopeRoots = new Map<string, string>();

/**
 * Frontend-only "Agent" tab (display-only exception to "Go owns logic": the Go
 * NewTab API has no `kind` parameter, so the agent mirror lives beside the Go tabs).
 */
export const agentTabEnabled = signal<Record<string, boolean>>({});
export const agentTabSelected = signal<Record<string, boolean>>({});

/** ptyId (or "agent") of the pane whose find bar is open. */
export const findFor = signal<string>("");

export const toast = signal<string>("");
let toastTimer: ReturnType<typeof setTimeout> | undefined;
export function showToast(msg: string) {
  toast.value = msg;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toast.value = ""), 2500);
}

export function setScopeFlag(sig: typeof agentTabEnabled, scope: string, v: boolean) {
  if (!!sig.value[scope] === v) return;
  sig.value = { ...sig.value, [scope]: v };
}

export function leaves(n: Node | undefined, out: Node[] = []): Node[] {
  if (!n) return out;
  if (n.type === "leaf") out.push(n);
  else for (const c of n.children ?? []) leaves(c, out);
  return out;
}

export function activeTab(scope: string): Tab | undefined {
  const l = layouts.value[scope];
  return l?.tabs?.find((t) => t.id === l.active) ?? l?.tabs?.[0];
}
