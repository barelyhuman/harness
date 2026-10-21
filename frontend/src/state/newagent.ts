// "New agent" entry point: opens the inline start screen on a project's main checkout.
import { signal } from "@preact/signals";
import { currentProjectId, projects } from "./agents";
import { activateMain, activeScope } from "./scope";
import { activateTab } from "./tabs";

/** Bumped on every request so the start screen re-focuses its input even when already visible. */
export const focusStart = signal(0);

export async function newAgent(projectId?: string): Promise<void> {
  const pid = projectId ?? activeScope.value?.projectId ?? currentProjectId.value ?? projects.value[0]?.id;
  if (!pid) return;
  await activateMain(pid);
  const s = activeScope.value;
  if (s) activateTab(s.key, "chat");
  focusStart.value++;
}
