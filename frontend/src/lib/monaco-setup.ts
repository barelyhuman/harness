import * as monaco from "monaco-editor";
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker";
import JsonWorker from "monaco-editor/language/json/json.worker.js?worker";
import CssWorker from "monaco-editor/language/css/css.worker.js?worker";
import HtmlWorker from "monaco-editor/language/html/html.worker.js?worker";
import TsWorker from "monaco-editor/language/typescript/ts.worker.js?worker";

let ready = false;

// Monaco's `editor.main` registers every basic-language through lazy loaders (the grammar chunk is
// only fetched when a model of that language is created), so no grammar is loaded up front.
// Workers: editor + json/css/html/ts only.

export const THEME = "harness-dark";

/** Chords the app owns; Monaco's default bindings for these are removed (see `freeReservedKeys`). */
const RESERVED = new Set([
  "cmd+p", "cmd+shift+p", "cmd+n", "cmd+b", "cmd+w", "cmd+shift+f", "cmd+shift+e", "cmd+\\",
  "cmd+0", "cmd+1", "cmd+2", "cmd+3", "cmd+4", "cmd+5", "cmd+6", "cmd+7", "cmd+8", "cmd+9",
  "ctrl+tab", "ctrl+shift+tab", "cmd+j", "cmd+shift+n", "cmd+shift+w", "cmd+t", "cmd+,", "cmd+q",
]);

function defineTheme(m: typeof monaco) {
  m.editor.defineTheme(THEME, {
    base: "vs-dark",
    inherit: true,
    rules: [
      { token: "comment", foreground: "6a737d", fontStyle: "italic" },
      { token: "keyword", foreground: "c586c0" },
      { token: "string", foreground: "ce9178" },
      { token: "number", foreground: "b5cea8" },
      { token: "type", foreground: "4ec9b0" },
    ],
    colors: {
      "editor.background": "#141414",
      "editor.foreground": "#d4d4d4",
      "editorGutter.background": "#141414",
      "editor.lineHighlightBackground": "#1c1c1c",
      "editor.lineHighlightBorder": "#1c1c1c",
      "editor.selectionBackground": "#264f7880",
      "editorLineNumber.foreground": "#4b4b4b",
      "editorLineNumber.activeForeground": "#a0a0a0",
      "editorCursor.foreground": "#e5e5e5",
      "editorIndentGuide.background1": "#262626",
      "editorWidget.background": "#1c1c1c",
      "editorWidget.border": "#2e2e2e",
      "editorSuggestWidget.background": "#1c1c1c",
      "editorSuggestWidget.border": "#2e2e2e",
      "editorHoverWidget.background": "#1c1c1c",
      "editorHoverWidget.border": "#2e2e2e",
      "input.background": "#181818",
      "scrollbarSlider.background": "#ffffff14",
      "scrollbarSlider.hoverBackground": "#ffffff24",
      "diffEditor.insertedTextBackground": "#2ea04326",
      "diffEditor.removedTextBackground": "#f8514926",
    },
  });
}

function injectCss() {
  const s = document.createElement("style");
  s.textContent = `
.harness-flash-line { background: rgba(234, 179, 8, 0.28); animation: harness-flash 1.4s ease-out forwards; }
@keyframes harness-flash { 0% { background: rgba(234,179,8,.38) } 100% { background: rgba(234,179,8,0) } }
.harness-reveal-line { background: rgba(96,165,250,.2); }
`;
  document.head.appendChild(s);
}

/** Configure Monaco's ESM workers + theme once and return the monaco namespace. */
export function ensureMonaco(): typeof monaco {
  if (!ready) {
    (self as any).MonacoEnvironment = {
      getWorker(_id: string, label: string): Worker {
        switch (label) {
          case "json":
            return new JsonWorker();
          case "css":
          case "scss":
          case "less":
            return new CssWorker();
          case "html":
          case "handlebars":
          case "razor":
            return new HtmlWorker();
          case "typescript":
          case "javascript":
            return new TsWorker();
          default:
            return new EditorWorker();
        }
      },
    };
    defineTheme(monaco);
    injectCss();
    // The in-browser TS worker has no project context (imports, node_modules), so semantic errors
    // would be bogus. Semantics come from the LSP bridge; keep only syntax checks.
    try {
      const ts = (monaco.languages as any).typescript;
      for (const d of [ts?.typescriptDefaults, ts?.javascriptDefaults]) {
        d?.setDiagnosticsOptions({ noSemanticValidation: true, noSyntaxValidation: false, noSuggestionDiagnostics: true });
      }
    } catch { /* ignore */ }
    ready = true;
  }
  return monaco;
}

let keysFreed = false;
/**
 * Remove Monaco's built-in bindings for chords the app owns, so those keydowns are not consumed by
 * Monaco and bubble to the app's dispatcher. Uses the standalone keybinding service's dynamic
 * keybinding rules (`-commandId`), which is how VS Code unbinds defaults. Needs an editor instance.
 */
export function freeReservedKeys(ed: monaco.editor.IStandaloneCodeEditor): string[] {
  if (keysFreed) return [];
  const removed: string[] = [];
  let items: any[];
  let svc: any;
  try {
    svc = (ed as any)._standaloneKeybindingService;
    if (!svc?.getKeybindings || !svc?.addDynamicKeybinding) return removed;
    items = svc.getKeybindings() as any[];
  } catch { return removed; } // private API moved: the app's capture listener still wins
  keysFreed = true;
  for (const item of items) {
    try {
    const rk = item?.resolvedKeybinding;
    const cmd = item.command as string | null;
    if (!rk || !cmd || cmd.startsWith("-")) continue;
    const label: string | null = rk.getUserSettingsLabel?.() ?? null;
    if (!label) continue;
    // chords: "cmd+k cmd+p" -> only block when the first chord is reserved.
    const first = label.split(" ")[0].toLowerCase();
    if (!RESERVED.has(first)) continue;
    const num = toNumericKeybinding(monaco, first);
    if (num === 0) continue;
    svc.addDynamicKeybinding(`-${cmd}`, num, () => {}, item.when ?? undefined);
    removed.push(`${first} -> ${cmd}`);
    } catch { /* ignore this binding */ }
  }
  return removed;
}

function toNumericKeybinding(m: typeof monaco, chord: string): number {
  const parts = chord.split("+");
  const key = parts.pop()!;
  let n = 0;
  for (const p of parts) {
    if (p === "cmd") n |= m.KeyMod.CtrlCmd;
    else if (p === "shift") n |= m.KeyMod.Shift;
    else if (p === "alt") n |= m.KeyMod.Alt;
    else if (p === "ctrl") n |= m.KeyMod.WinCtrl;
  }
  let code: number | undefined;
  if (/^[a-z]$/.test(key)) code = (m.KeyCode as any)["Key" + key.toUpperCase()];
  else if (/^[0-9]$/.test(key)) code = (m.KeyCode as any)["Digit" + key];
  else if (key === "tab") code = m.KeyCode.Tab;
  else if (key === ",") code = m.KeyCode.Comma;
  else if (key === "\\") code = m.KeyCode.Backslash;
  return code === undefined ? 0 : n | code;
}
