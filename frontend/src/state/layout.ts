// Region sizes / visibility (spec 03). Persisted to UIStateService key "layout", debounced.
import { effect, signal } from "@preact/signals";
import { loadUI, saveUI } from "./uistate";

export type RightMode = "scm" | "explorer" | "search";
export const sidebarW = signal(268);
export const rightW = signal(340);
export const termH = signal(260);
export const sidebarOpen = signal(true);
export const rightOpen = signal(true);
export const termOpen = signal(true);
export const rightMode = signal<RightMode>("scm");

export const LIMITS = { sidebar: [200, 520], right: [260, 760], term: [120, 700] } as const;
export const clamp = (v: number, [lo, hi]: readonly [number, number] | readonly number[]) => Math.max(lo, Math.min(hi, v));

let loaded = false;
export async function loadLayout(): Promise<void> {
  const l = await loadUI<Record<string, any>>("layout", {});
  if (typeof l.sidebarW === "number") sidebarW.value = clamp(l.sidebarW, LIMITS.sidebar);
  if (typeof l.rightW === "number") rightW.value = clamp(l.rightW, LIMITS.right);
  if (typeof l.termH === "number") termH.value = clamp(l.termH, LIMITS.term);
  if (typeof l.sidebarOpen === "boolean") sidebarOpen.value = l.sidebarOpen;
  if (typeof l.rightOpen === "boolean") rightOpen.value = l.rightOpen;
  if (typeof l.termOpen === "boolean") termOpen.value = l.termOpen;
  if (l.rightMode === "scm" || l.rightMode === "explorer" || l.rightMode === "search") rightMode.value = l.rightMode;
  loaded = true;
  effect(() => {
    const v = {
      sidebarW: sidebarW.value, rightW: rightW.value, termH: termH.value, sidebarOpen: sidebarOpen.value,
      rightOpen: rightOpen.value, termOpen: termOpen.value, rightMode: rightMode.value,
    };
    if (loaded) saveUI("layout", v);
  });
}
