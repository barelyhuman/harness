// Per-project agent roles (planner / executor / explorer models, specs dir). Go resolves project overrides
// over settings.json defaults; we only cache the result.
import { signal } from "@preact/signals";
import { SessionService } from "../lib/bindings/github.com/sid/harness/internal/app";
import type { AgentRolesInfo } from "../lib/bindings/github.com/sid/harness/internal/app/models";
import { bus } from "../lib/bus";
import { toastError } from "./toast";

export type Role = "planner" | "executor" | "explorer" | "specsDir";

export const rolesByProject = signal<Record<string, AgentRolesInfo>>({});

export async function loadRoles(projectId: string): Promise<AgentRolesInfo | undefined> {
  if (!projectId) return undefined;
  try {
    const r = await SessionService.AgentRoles(projectId);
    rolesByProject.value = { ...rolesByProject.value, [projectId]: r };
    return r;
  } catch (e) { toastError(e); }
  return undefined;
}

export async function setRole(projectId: string, role: Role, value: string): Promise<void> {
  try {
    const r = await SessionService.SetAgentRole(projectId, role, value);
    rolesByProject.value = { ...rolesByProject.value, [projectId]: r };
  } catch (e) { toastError(e); }
}

let started = false;
/** settings.json edits change the defaults: refetch every cached project. */
export function initRoles(): void {
  if (started) return;
  started = true;
  bus.onOpen("settings", (ch) => {
    ch.onJSON(() => { for (const id of Object.keys(rolesByProject.value)) void loadRoles(id); });
  });
}
