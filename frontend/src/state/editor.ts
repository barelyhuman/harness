// Editor state: openFile (with optional reveal position), pending reveals, active editor path,
// find-in-files panel request. Kept free of Monaco imports so the shell can import it eagerly.
import { signal } from "@preact/signals";
import { registerCommand } from "../lib/commands";
import { openTab, fileTabId, tabsByScope } from "./tabs";

export type Reveal = { line: number; col: number };
const pending = new Map<string, Reveal>();
const key = (scopeKey: string, path: string) => scopeKey + "\n" + path;
/** Bumped whenever a reveal is queued so an already-mounted EditorTab can react. */
export const revealTick = signal(0);

export function consumeReveal(scopeKey: string, path: string): Reveal | undefined {
  const k = key(scopeKey, path);
  const r = pending.get(k);
  pending.delete(k);
  return r;
}

/** Pin a preview tab (double click, first edit). */
export function pinFile(scopeKey: string, path: string): void {
  const st = tabsByScope.value[scopeKey];
  const id = fileTabId(path);
  if (!st?.tabs.some((t) => t.id === id && t.kind === "file" && t.preview)) return;
  tabsByScope.value = {
    ...tabsByScope.value,
    [scopeKey]: { ...st, tabs: st.tabs.map((t) => (t.id === id && t.kind === "file" ? { ...t, preview: false } : t)) },
  };
}

export function openFile(scopeKey: string, path: string, preview = true, line?: number, col?: number): void {
  if (line !== undefined) {
    pending.set(key(scopeKey, path), { line, col: col ?? 1 });
    revealTick.value++;
  }
  openTab(scopeKey, { id: fileTabId(path), kind: "file", path, preview });
  if (!preview) pinFile(scopeKey, path);
}

/** Path + scope of the editor that last had focus (target for ⌘S etc). */
export const activeEditor = signal<{ scopeKey: string; path: string } | null>(null);

/** Find-in-files: the shell shows SearchPanel when `searchRequest` changes; focus tick re-focuses its input. */
export const searchRequest = signal(0);
export const searchSeed = signal("");
export function showSearch(seed?: string): void {
  if (seed !== undefined) searchSeed.value = seed;
  searchRequest.value++;
}

registerCommand({
  id: "search.findInFiles",
  title: "Find in Files",
  keys: ["cmd+shift+f"],
  run: () => showSearch(),
});
registerCommand({
  id: "editor.save",
  title: "File: Save",
  keys: ["cmd+s"],
  when: "editorFocus",
  run: async () => {
    const a = activeEditor.value;
    if (!a) return;
    const { saveModel } = await import("../features/editor/monaco");
    await saveModel(a.scopeKey, a.path);
  },
});
