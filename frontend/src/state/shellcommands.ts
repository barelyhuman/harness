// Default global commands owned by the shell (spec 04 default map).
import { SettingsService } from "../lib/bindings/github.com/sid/harness/internal/app";
import { registerCommand } from "../lib/commands";
import { activateSession, activeScope } from "./scope";
import { currentProjectId, projects, sessionsByProject } from "./agents";
import { rightMode, rightOpen, sidebarOpen, termOpen } from "./layout";
import { openOverlay, toggleOverlay } from "./ui";
import { activateTab, closeTab, scopeTabs } from "./tabs";
import { toast, toastError } from "./toast";
import { openFile, searchRequest } from "./editor";
import { openAgentDialog } from "../features/agents/dialogs";

export function agentList() {
  return projects.value.flatMap((p) => (sessionsByProject.value[p.id] ?? []).filter((s) => s.status !== "archived"));
}

export function registerShellCommands(): void {
  const reg = registerCommand;
  reg({ id: "workbench.quickOpen", title: "Go to File...", keys: ["cmd+p"], run: () => openOverlay({ kind: "quickopen" }) });
  reg({ id: "workbench.commandPalette", title: "Show All Commands", keys: ["cmd+shift+p"], run: () => toggleOverlay({ kind: "palette" }) });
  reg({ id: "agents.new", title: "New Agent", keys: ["cmd+n"], run: () => openOverlay({ kind: "newagent" }) });
  reg({ id: "projects.openPicker", title: "Open Project...", keys: ["cmd+o"], run: () => toggleOverlay({ kind: "picker" }) });
  reg({
    id: "projects.settings", title: "Project Settings...",
    run: () => {
      const id = currentProjectId.value;
      if (!id) { toast("Open a project first.", "info"); return; }
      openOverlay({ kind: "projectsettings", projectId: id });
    },
  });
  for (let i = 1; i <= 9; i++)
    reg({
      id: `agents.jump.${i}`, title: `Jump to Agent ${i}`, keys: [`cmd+${i}`],
      run: () => { const s = agentList()[i - 1]; if (s) void activateSession(s.id); },
    });
  reg({ id: "view.toggleSidebar", title: "Toggle Agents Sidebar", keys: ["cmd+b"], run: () => { sidebarOpen.value = !sidebarOpen.value; } });
  reg({ id: "view.toggleRightPane", title: "Toggle Right Pane", keys: ["alt+cmd+b"], run: () => { rightOpen.value = !rightOpen.value; } });
  reg({ id: "view.toggleTerminal", title: "Toggle Terminal Panel", keys: ["ctrl+`"], run: () => { termOpen.value = !termOpen.value; } });
  reg({ id: "view.showChanges", title: "Show Changes (Source Control)", run: () => { rightMode.value = "scm"; rightOpen.value = true; } });
  reg({ id: "view.showExplorer", title: "Show Explorer", keys: ["cmd+shift+e"], run: () => { rightMode.value = "explorer"; rightOpen.value = true; } });
  reg({ id: "workbench.chat", title: "Activate Chat Tab", keys: ["cmd+0"], run: () => { const s = activeScope.value; if (s) activateTab(s.key, "chat"); } });
  reg({ id: "workbench.closeTab", title: "Close Tab", keys: ["cmd+w"], when: "!terminalFocus", run: () => {
    const s = activeScope.value; if (s) closeTab(s.key, scopeTabs(s.key).active);
  } });
  const openSettings = (which: "settings" | "keybindings") => async () => {
    const s = activeScope.value;
    try {
      const path = await SettingsService.OpenSettingsFile(which);
      if (!s) return toast(`Settings file: ${path}`);
      openFile(s.key, path, false);
    } catch (e) { toastError(e); }
  };
  reg({ id: "workbench.openSettings", title: "Open Settings (JSON)", keys: ["cmd+,"], run: openSettings("settings") });
  reg({ id: "workbench.openKeybindings", title: "Open Keyboard Shortcuts (JSON)", keys: ["cmd+k cmd+s"], run: openSettings("keybindings") });
}

/** Cross-module window events and signals that drive shell UI. Call once from main.tsx. */
export function initShellEvents(): void {
  const rowFor = (scopeKey: string) => {
    if (!scopeKey.startsWith("session:")) return undefined;
    const id = scopeKey.slice("session:".length);
    for (const list of Object.values(sessionsByProject.value)) {
      const r = list.find((x) => x.id === id);
      if (r) return r;
    }
    return undefined;
  };
  window.addEventListener("harness:new-agent", (e) => {
    const k = (e as CustomEvent<{ scopeKey?: string }>).detail?.scopeKey ?? activeScope.value?.key ?? "";
    const projectId = k.startsWith("main:") ? k.slice("main:".length) : rowFor(k)?.projectId ?? activeScope.value?.projectId;
    openOverlay({ kind: "newagent", projectId });
  });
  window.addEventListener("harness:raise-pr", (e) => {
    const k = (e as CustomEvent<{ scopeKey?: string }>).detail?.scopeKey ?? activeScope.value?.key ?? "";
    const row = rowFor(k);
    if (!row) {
      toast("Pull requests are raised per agent worktree. Start an agent, or switch to one, to raise a PR.", "info");
      return;
    }
    openAgentDialog({ kind: "pr", s: row });
  });
  // Find in files (⌘⇧F): show the search mode of the right pane.
  let first = true;
  searchRequest.subscribe(() => {
    if (first) { first = false; return; }
    rightMode.value = "search";
    rightOpen.value = true;
  });
}
