// Command + keybinding registry and key dispatch (spec 04).
//
// HOW FEATURES PLUG IN
//  - Register commands with `registerCommand({id, title, keys?, when?, run})` (returns an unregister fn; call it
//    on unmount if the command is component-scoped). Default `keys` are VS Code format, lowercase:
//    "cmd+d", "alt+cmd+left", "cmd+k cmd+s" (chord). A command with no `when` is global: it fires even while
//    focus is inside Monaco/xterm/inputs. Add a `when` to scope it ("terminalFocus && !composerFocus").
//  - User overrides (keybindings.json, parsed by Go) are layered over defaults; `remove:true` deletes a default.
//  - CONTEXT KEYS: put a data attribute on your feature's root container and the shell sets the context from
//    focusin/focusout (the nearest ancestor of the focused element wins):
//        data-ctx="terminal"  -> terminalFocus
//        data-ctx="editor"    -> editorFocus
//        data-ctx="composer"  -> composerFocus
//    Anything else may call `setContext(key, value)` directly. `pickerOpen` is set by the shell whenever a
//    palette/picker/sheet is open.
//  - Inside editable targets (inputs, Monaco, xterm) plain typing (no ctrl/cmd/alt) always passes through;
//    only chords matching a binding whose `when` holds are taken (capture phase, preventDefault).
//  - `keyLabel(cmdId)` returns the current shortcut as ⌃⌥⇧⌘ symbols for tooltips/palette.
import { signal } from "@preact/signals";
import { chordLabel, evalWhen, eventStroke, parseChord } from "./keys";
import { SettingsService } from "./bindings/github.com/sid/harness/internal/app";
import { bus } from "./bus";

export type Command = {
  id: string; // e.g. "terminal.split.right"
  title: string; // shown in the palette
  keys?: string[]; // default bindings, VS Code format, lowercase: "cmd+d", "cmd+k cmd+s"
  when?: string; // e.g. "terminalFocus && !composerFocus"
  run: (args?: unknown) => void | Promise<void>;
};

const registry = new Map<string, Command>();
export const commandsVersion = signal(0);
/** Most recently run command ids, newest first (in memory; the palette persists it). */
export const recentCommands = signal<string[]>([]);

export function registerCommand(cmd: Command): () => void {
  registry.set(cmd.id, cmd);
  commandsVersion.value++;
  return () => {
    if (registry.get(cmd.id) === cmd) registry.delete(cmd.id);
    commandsVersion.value++;
  };
}

export function allCommands(): Command[] {
  return [...registry.values()];
}

export function runCommand(id: string, args?: unknown): void {
  const c = registry.get(id);
  if (!c) return;
  recentCommands.value = [id, ...recentCommands.value.filter((x) => x !== id)].slice(0, 30);
  try {
    const r = c.run(args);
    if (r && typeof (r as Promise<void>).catch === "function") (r as Promise<void>).catch((e) => console.error("command", id, e));
  } catch (e) {
    console.error("command", id, e);
  }
}

// ---- context keys ----
const context = new Map<string, unknown>();
export const contextVersion = signal(0);
export function setContext(key: string, value: unknown): void {
  if (context.get(key) === value) return;
  context.set(key, value);
  contextVersion.value++;
}
export function getContext(key: string): unknown {
  return context.get(key);
}

// ---- bindings ----
type Binding = { chord: string[]; command: string; when?: string; args?: unknown; user: boolean };
type UserKb = { key: string; command: string; when?: string; args?: unknown; remove?: boolean };

let userBindings: UserKb[] = [];
const userVersion = signal(0);
let table: Binding[] = [];
let tableVer = "";

function bindings(): Binding[] {
  const ver = `${commandsVersion.peek()}:${userVersion.peek()}`;
  if (ver === tableVer) return table;
  const out: Binding[] = [];
  for (const c of registry.values())
    for (const k of c.keys ?? []) out.push({ chord: parseChord(k), command: c.id, when: c.when, user: false });
  for (const u of userBindings) {
    const chord = parseChord(u.key);
    if (!chord.length) continue;
    if (u.remove) {
      for (let i = out.length - 1; i >= 0; i--)
        if (out[i].command === u.command && !out[i].user && out[i].chord.join(" ") === chord.join(" ")) out.splice(i, 1);
    } else out.push({ chord, command: u.command, when: u.when || undefined, args: u.args, user: true });
  }
  table = out;
  tableVer = ver;
  return out;
}

/** Current shortcut for a command, e.g. "⇧⌘P" (chords: "⌘K ⌘S"); "" if unbound. */
export function keyLabel(cmdId: string): string {
  void commandsVersion.value;
  void userVersion.value;
  const bs = bindings().filter((b) => b.command === cmdId);
  const b = bs[bs.length - 1];
  return b ? chordLabel(b.chord) : "";
}

let pending: string[] = [];
let pendingTimer: ReturnType<typeof setTimeout> | undefined;
const clearPending = () => { pending = []; clearTimeout(pendingTimer); };

const getCtx = (k: string) => context.get(k);

function isEditable(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el || !el.tagName) return false;
  return el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable;
}

function onKeyDown(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return;
  const stroke = eventStroke(e);
  if (!stroke) return;
  // Typing passes straight through editable targets.
  if (!pending.length && isEditable(e.target) && !e.ctrlKey && !e.metaKey && !e.altKey && e.key.length === 1) return;
  const seq = [...pending, stroke];
  clearPending();
  const bs = bindings();
  let exact: Binding | undefined;
  let prefix = false;
  for (let i = bs.length - 1; i >= 0; i--) {
    const b = bs[i];
    if (b.chord.length < seq.length) continue;
    let ok = true;
    for (let j = 0; j < seq.length; j++) if (b.chord[j] !== seq[j]) { ok = false; break; }
    if (!ok || !evalWhen(b.when, getCtx)) continue;
    if (b.chord.length === seq.length) { exact ??= b; } else prefix = true;
  }
  if (exact) {
    e.preventDefault();
    e.stopPropagation();
    runCommand(exact.command, exact.args);
  } else if (prefix) {
    e.preventDefault();
    e.stopPropagation();
    pending = seq;
    pendingTimer = setTimeout(clearPending, 1800);
  }
}

function focusCtx() {
  const el = document.activeElement as HTMLElement | null;
  const c = el?.closest?.("[data-ctx]")?.getAttribute("data-ctx");
  setContext("terminalFocus", c === "terminal");
  setContext("editorFocus", c === "editor");
  setContext("composerFocus", c === "composer");
  setContext("inputFocus", isEditable(el) && c !== "terminal" && c !== "editor");
}

let started = false;
/** Install the global dispatcher; call once from main.tsx. Loads and live-tracks user keybindings from Go. */
export function initKeys(): void {
  if (started) return;
  started = true;
  window.addEventListener("keydown", onKeyDown, true);
  document.addEventListener("focusin", focusCtx, true);
  document.addEventListener("focusout", () => setTimeout(focusCtx, 0), true);
  focusCtx();
  const apply = (r: { bindings?: UserKb[] | null } | null | undefined) => {
    userBindings = (r?.bindings ?? []) as UserKb[];
    userVersion.value++;
  };
  void SettingsService.GetKeybindings().then(apply).catch(() => {});
  bus.onOpen("keybindings", (ch) => {
    ch.onJSON(apply as (v: unknown) => void);
    void SettingsService.GetKeybindings().then(apply).catch(() => {});
  });
}
