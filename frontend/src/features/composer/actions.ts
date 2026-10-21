// Composer actions shared by chips, menus and global commands. All logic is Go-side (bindings); this only
// sequences calls, toasts and activates forked sessions.
import { signal } from "@preact/signals";
import { SessionService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { ModelInfo } from "../../lib/bindings/github.com/sid/harness/internal/app/models";
import { registerCommand } from "../../lib/commands";
import { sessionsByProject, type SessionRow } from "../../state/agents";
import { activateSession, activeScope } from "../../state/scope";
import { toast, toastError } from "../../state/toast";

export const EFFORTS = ["low", "medium", "high", "xhigh", "max"];

export const models = signal<ModelInfo[]>([]);
export const pickerOpen = signal(false);

export async function loadModels(): Promise<ModelInfo[]> {
  try {
    models.value = (await SessionService.ListModels()) ?? [];
  } catch (e) { toastError(e); }
  return models.value;
}

export function sessionRow(id: string | null | undefined): SessionRow | undefined {
  if (!id) return undefined;
  for (const list of Object.values(sessionsByProject.value)) {
    const r = list.find((s) => s.id === id);
    if (r) return r;
  }
  return undefined;
}

export const isBusy = (r?: SessionRow) => r?.status === "running" || r?.status === "needs_approval";

export function findModel(list: ModelInfo[], r?: SessionRow): ModelInfo | undefined {
  if (!r) return undefined;
  return list.find((m) => m.key === r.model) ?? list.find((m) => m.id === r.model && m.provider === r.provider) ?? list.find((m) => m.id === r.model);
}

export const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s);

async function afterSwitch(sourceId: string, forked?: string) {
  if (forked) {
    toast("Forked a new session with a handoff", "success");
    await activateSession(forked);
    return;
  }
  try {
    const s = await SessionService.GetSession(sourceId);
    const m = findModel(models.value, s as unknown as SessionRow);
    toast(`Model: ${m?.label ?? s.model}`, "success");
  } catch { toast("Model changed", "success"); }
}

export async function setModel(sessionId: string, key: string): Promise<void> {
  try {
    const r = await SessionService.SetModel(sessionId, key);
    await afterSwitch(sessionId, r?.forkedSessionId);
  } catch (e) { toastError(e); }
}

export async function cycleModel(sessionId: string, dir: 1 | -1): Promise<void> {
  try {
    if (!models.value.length) await loadModels();
    const r = await SessionService.CycleModel(sessionId, dir);
    await afterSwitch(sessionId, r?.forkedSessionId);
  } catch (e) { toastError(e); }
}

export async function cycleMode(sessionId: string): Promise<void> {
  try {
    await SessionService.CycleMode(sessionId);
    const s = await SessionService.GetSession(sessionId);
    toast(`Mode: ${s.mode}`, "success");
  } catch (e) { toastError(e); }
}

export async function setMode(sessionId: string, mode: string): Promise<void> {
  try { await SessionService.SetMode(sessionId, mode); toast(`Mode: ${mode}`, "success"); } catch (e) { toastError(e); }
}

export async function cycleEffort(sessionId: string): Promise<void> {
  const cur = sessionRow(sessionId)?.effort ?? "";
  const next = EFFORTS[(EFFORTS.indexOf(cur) + 1) % EFFORTS.length];
  try { await SessionService.SetEffort(sessionId, next); toast(`Effort: ${next} (applies at next launch)`, "success"); } catch (e) { toastError(e); }
}

const activeSessionId = () => activeScope.value?.sessionId || null;

let registered = false;
/** Idempotent; commands act on the active scope's session. */
export function registerComposerCommands(): void {
  if (registered) return;
  registered = true;
  const withSession = (fn: (id: string) => void) => () => { const id = activeSessionId(); if (id) fn(id); };
  registerCommand({ id: "composer.model.pick", title: "Chat: Switch Model", keys: ["cmd+/"], when: "!editorFocus", run: withSession(() => { void loadModels(); pickerOpen.value = true; }) });
  registerCommand({ id: "composer.model.next", title: "Chat: Next Favourite Model", keys: ["alt+cmd+/"], run: withSession((id) => void cycleModel(id, 1)) });
  registerCommand({ id: "composer.model.prev", title: "Chat: Previous Favourite Model", keys: ["shift+alt+cmd+/"], run: withSession((id) => void cycleModel(id, -1)) });
  registerCommand({ id: "composer.mode.cycle", title: "Chat: Cycle Mode", keys: ["shift+tab"], when: "composerFocus", run: withSession((id) => void cycleMode(id)) });
  registerCommand({ id: "composer.effort.cycle", title: "Chat: Cycle Effort", keys: ["cmd+e"], when: "!editorFocus && !terminalFocus", run: withSession((id) => void cycleEffort(id)) });
}
