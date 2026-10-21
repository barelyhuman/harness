// Projects + sessions + terminal layouts as presented in the agents sidebar. Go owns the data; we cache it.
import { signal } from "@preact/signals";
import { ProjectService, SessionService, TermService } from "../lib/bindings/github.com/sid/harness/internal/app";
import type { Project, Session } from "../lib/bindings/github.com/sid/harness/internal/workspace/models";
import type { Layout } from "../lib/bindings/github.com/sid/harness/internal/termlayout/models";
import { bus } from "../lib/bus";
import { toastError } from "./toast";

export type SessionRow = Session & { dirty?: number | boolean };

export const projects = signal<Project[]>([]);
export const sessionsByProject = signal<Record<string, SessionRow[]>>({});
export const termLayouts = signal<Record<string, Layout>>({});
/** Row keys ("main:<pid>" / "session:<id>") expanded to show terminals. */
export const expanded = signal<Set<string>>(new Set());
export const currentProjectId = signal<string | null>(null);

export async function refreshProjects(): Promise<Project[]> {
  try {
    const ps = (await ProjectService.ListProjects()) ?? [];
    projects.value = ps;
    await Promise.all(ps.map((p) => refreshSessions(p.id)));
    if (!currentProjectId.value || !ps.some((p) => p.id === currentProjectId.value)) currentProjectId.value = ps[0]?.id ?? null;
    return ps;
  } catch (e) {
    toastError(e);
    return [];
  }
}

export async function refreshSessions(projectId: string): Promise<void> {
  try {
    const ss = (await SessionService.ListSessions(projectId, true)) ?? [];
    const old = sessionsByProject.value[projectId] ?? [];
    const merged = ss.map((s) => ({ ...old.find((o) => o.id === s.id), ...s }) as SessionRow);
    sessionsByProject.value = { ...sessionsByProject.value, [projectId]: merged };
  } catch (e) {
    toastError(e);
  }
}

/** `sessions` channel: Go sends the full row array for all projects (archived included). Replace, keeping extra fields from ListSessions. */
function applyRows(rows: SessionRow[]) {
  const by: Record<string, SessionRow[]> = {};
  for (const p of projects.value) by[p.id] = [];
  for (const r of rows) {
    if (!r?.id || !r.projectId) continue;
    const old = sessionsByProject.value[r.projectId]?.find((o) => o.id === r.id);
    (by[r.projectId] ??= []).push({ ...old, ...r } as SessionRow);
  }
  sessionsByProject.value = by;
}

export function setLayout(scopeKey: string, l: Layout) {
  termLayouts.value = { ...termLayouts.value, [scopeKey]: l };
}
export async function loadLayoutFor(scopeKey: string): Promise<void> {
  try { setLayout(scopeKey, await TermService.Layout(scopeKey)); } catch { /* none yet */ }
}

export function toggleExpanded(key: string) {
  const s = new Set(expanded.value);
  if (s.has(key)) s.delete(key);
  else { s.add(key); void loadLayoutFor(key); }
  expanded.value = s;
}

let started = false;
export function initAgents(): void {
  if (started) return;
  started = true;
  bus.onOpen("sessions", (ch) => {
    ch.onJSON<SessionRow[]>((v) => { if (Array.isArray(v)) applyRows(v); });
    void refreshProjects();
  });
  bus.onOpen("termlayout/", (ch) => {
    const key = ch.name.slice("termlayout/".length);
    ch.onJSON<Layout>((l) => setLayout(key, l));
  });
}
