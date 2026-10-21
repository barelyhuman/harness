// Terminal commands, registered once at module load; they act on activeTerminalScope.
import { registerCommand } from "../../lib/commands";
import * as T from "../../lib/bindings/github.com/sid/harness/internal/app/termservice";
import { CHORDS } from "./keys";
import { activeTab, activeTerminalScope, agentTabSelected, findFor, leaves } from "./state";
import { entries, type Entry } from "./terms";
import { getMirrorEntry } from "./mirror";

function scope() { return activeTerminalScope.value; }

/** Focused pane of the active tab (Go terminal), or the Agent mirror when its tab is selected. */
function target(): { scope: string; paneId: string; entry?: Entry; mirror: boolean } | undefined {
  const s = scope();
  if (!s) return;
  if (agentTabSelected.value[s]) return { scope: s, paneId: "", entry: getMirrorEntry(s), mirror: true };
  const tab = activeTab(s);
  if (!tab) return { scope: s, paneId: "", mirror: false };
  const leaf = leaves(tab.root).find((n) => n.paneId === tab.focused);
  return { scope: s, paneId: tab.focused, entry: leaf?.ptyId ? entries.get(leaf.ptyId) : undefined, mirror: false };
}

const when = "terminalFocus";
function def(id: string, title: string, run: () => void | Promise<void>, w: string | undefined = when) {
  registerCommand({ id, title, keys: CHORDS[id], when: w, run });
}
const pane = (fn: (s: string, p: string) => unknown) => () => {
  const t = target();
  if (t && !t.mirror && t.paneId) void fn(t.scope, t.paneId);
};
const sc = (fn: (s: string) => unknown) => () => { const s = scope(); if (s) void fn(s); };

def("terminal.split.right", "Terminal: Split Right", pane((s, p) => T.Split(s, p, "row", "")));
def("terminal.split.down", "Terminal: Split Down", pane((s, p) => T.Split(s, p, "col", "")));
for (const d of ["left", "right", "up", "down"]) {
  const D = d[0].toUpperCase() + d.slice(1);
  def(`terminal.focus.${d}`, `Terminal: Focus Pane ${D}`, sc((s) => T.FocusDir(s, d)));
  def(`terminal.resize.${d}`, `Terminal: Resize Pane ${D}`, sc((s) => T.ResizeDir(s, d)));
}
def("terminal.maximize", "Terminal: Toggle Maximize Pane", sc((s) => T.ToggleMaximize(s)));
def("terminal.close", "Terminal: Close Pane", pane((s, p) => T.ClosePane(s, p)));
def("terminal.newTab", "Terminal: New Tab", sc((s) => { agentTabSelected.value = { ...agentTabSelected.value, [s]: false }; return T.NewTab(s, scopeRootOf(s)); }));
def("terminal.nextTab", "Terminal: Next Tab", sc((s) => T.NextTab(s)));
def("terminal.prevTab", "Terminal: Previous Tab", sc((s) => T.PrevTab(s)));
def("terminal.clear", "Terminal: Clear", () => target()?.entry?.term.clear());
def("terminal.find", "Terminal: Find", () => {
  const t = target();
  if (t?.entry) findFor.value = t.entry.key;
});
def("terminal.sendSelectionToAgent", "Terminal: Send Selection to Agent", () => {
  const t = target();
  const text = t?.entry?.term.getSelection();
  if (text) window.dispatchEvent(new CustomEvent("harness:composer-insert", { detail: { text, source: "terminal", scopeKey: t!.scope } }));
});

import { scopeRoots } from "./state";
function scopeRootOf(s: string) { return scopeRoots.get(s) ?? ""; }
