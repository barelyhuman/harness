// Active scope (spec 03): drives explorer, SCM, terminals, editor tabs. Go resolves scopes; we only hold the choice.
import { signal } from "@preact/signals";
import { FSService, SessionService } from "../lib/bindings/github.com/sid/harness/internal/app";
import { loadUI, saveUI } from "./uistate";
import { toastError } from "./toast";

export type Scope = { key: string; projectId: string; kind: string; sessionId: string; root: string };

export const activeScope = signal<Scope | null>(null);
/** Scopes whose watchers are live, most recent last (LRU, max 8). */
const watched: string[] = [];
const LRU = 8;

function touchWatch(s: Scope) {
  const i = watched.indexOf(s.key);
  if (i >= 0) watched.splice(i, 1);
  else void FSService.WatchScope(s.key, s.root).catch(toastError);
  watched.push(s.key);
  while (watched.length > LRU) {
    const old = watched.shift()!;
    void FSService.UnwatchScope(old).catch(() => {});
  }
}

export function setScope(s: Scope): void {
  if (activeScope.value?.key === s.key) return;
  activeScope.value = s;
  touchWatch(s);
  saveUI("scope", { kind: s.kind, projectId: s.projectId, sessionId: s.sessionId }, 200);
}

export async function activateSession(sessionId: string): Promise<void> {
  try { setScope(await SessionService.Scope(sessionId)); } catch (e) { toastError(e); }
}
export async function activateMain(projectId: string): Promise<void> {
  try { setScope(await SessionService.MainScope(projectId)); } catch (e) { toastError(e); }
}

/** Restore the last scope at launch; falls back to the first project's main checkout. */
export async function restoreScope(firstProjectId?: string): Promise<void> {
  const last = await loadUI<{ kind?: string; projectId?: string; sessionId?: string }>("scope", {});
  try {
    if (last.kind === "worktree" && last.sessionId) {
      try { return setScope(await SessionService.Scope(last.sessionId)); } catch { /* archived/deleted */ }
    }
    const pid = last.projectId || firstProjectId;
    if (pid) setScope(await SessionService.MainScope(pid));
    else if (firstProjectId) setScope(await SessionService.MainScope(firstProjectId));
  } catch {
    if (firstProjectId) await activateMain(firstProjectId);
  }
}
