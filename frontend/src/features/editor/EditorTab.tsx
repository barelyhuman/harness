// One Monaco editor instance per mounted tab; models are shared per path (see monaco.ts).
// Strategy: editor per tab (simple lifecycle, native undo/selection per model) with view state
// (scroll/cursor/folds) saved on unmount and restored on mount, keyed by scope+path.
import { useEffect, useRef } from "preact/hooks";
import { effect } from "@preact/signals";
import type * as Monaco from "monaco-editor";
import { freeReservedKeys } from "../../lib/monaco-setup";
import { attachModel, configureLsp } from "../../lib/lsp-bridge";
import { activeEditor, consumeReveal, openFile, pinFile, revealTick } from "../../state/editor";
import { toast } from "../../state/toast";
import { attachQuickDiff } from "../scm/quickdiff";
import {
  applyTheme, cancelConflict, compareConflict, conflict, editorScopes, ensureModelForUri, getModel, keepMine,
  lastScope, modelInfo, overwriteConflict, reloadFromDisk, releaseModel, saveModel, setLastScope, tabTitle, watchFs,
} from "./monaco";

const viewStates = new Map<string, Monaco.editor.ICodeEditorViewState | null>();
let globalReady = false;

function setupGlobal(m: typeof Monaco) {
  if (globalReady) return;
  globalReady = true;
  configureLsp({ notify: (msg) => toast(msg, "info"), ensureModel: ensureModelForUri });
  // Go-to-definition / references into other files open a real tab and reveal the position.
  m.editor.registerEditorOpener({
    openCodeEditor(source, resource, sel) {
      if (resource.scheme !== "file") return false;
      const scope = (source && editorScopes.get(source)) || undefined;
      const s = scope ?? lastScope;
      if (!s) return false;
      const line = sel ? ("startLineNumber" in sel ? sel.startLineNumber : sel.lineNumber) : undefined;
      const col = sel ? ("startLineNumber" in sel ? sel.startColumn : sel.column) : undefined;
      openFile(s, resource.fsPath, false, line, col);
      return true;
    },
  });
}

export function EditorTab(props: { scopeKey: string; root: string; path: string }) {
  const { scopeKey, root, path } = props;
  const host = useRef<HTMLDivElement>(null);
  const info = modelInfo(path).value;
  const c = conflict.value;

  useEffect(() => {
    const m = applyTheme();
    setupGlobal(m);
    const vsKey = scopeKey + "\n" + path;
    let disposed = false;
    const cleanups: (() => void)[] = [];
    const editor = m.editor.create(host.current!, {
      model: null,
      theme: "harness-dark",
      automaticLayout: true,
      minimap: { enabled: false },
      fontSize: 13,
      fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
      scrollBeyondLastLine: false,
      renderWhitespace: "selection",
      smoothScrolling: true,
      padding: { top: 8 },
      fixedOverflowWidgets: true,
    });
    editorScopes.set(editor, scopeKey);
    freeReservedKeys(editor);
    editor.addCommand(m.KeyMod.CtrlCmd | m.KeyCode.KeyS, () => void saveModel(scopeKey, path));

    const focusDisp = editor.onDidFocusEditorText(() => {
      activeEditor.value = { scopeKey, path };
      setLastScope(scopeKey);
    });
    cleanups.push(() => focusDisp.dispose());
    cleanups.push(watchFs(scopeKey));

    const reveal = () => {
      const r = consumeReveal(scopeKey, path);
      if (!r || !editor.getModel()) return;
      editor.setPosition({ lineNumber: r.line, column: r.col });
      editor.revealLineInCenter(r.line);
      editor.focus();
    };
    cleanups.push(effect(() => { void revealTick.value; reveal(); }));

    void (async () => {
      const model = await getModel(path);
      if (disposed) { releaseModel(path); return; }
      cleanups.push(() => releaseModel(path));
      if (!model) return; // binary / error: placeholder rendered by JSX
      editor.setModel(model);
      const vs = viewStates.get(vsKey);
      if (vs) editor.restoreViewState(vs);
      reveal();
      const ch = model.onDidChangeContent(() => pinFile(scopeKey, path));
      cleanups.push(() => ch.dispose());
      cleanups.push(attachModel(model, root, path));
      try { cleanups.push(attachQuickDiff(editor, scopeKey, root, path)); } catch { /* optional */ }
    })();

    return () => {
      disposed = true;
      viewStates.set(vsKey, editor.saveViewState());
      if (activeEditor.value?.path === path && activeEditor.value.scopeKey === scopeKey) activeEditor.value = null;
      for (const f of cleanups.reverse()) { try { f(); } catch { /* ignore */ } }
      editor.dispose();
    };
  }, [scopeKey, root, path]);

  return (
    <div data-ctx="editor" class="relative flex h-full w-full flex-col bg-bg">
      {info.diskChanged && (
        <div class="flex items-center gap-3 bg-amber-950/40 px-3 py-1.5 text-[12px] text-amber-200">
          <span class="flex-1">File changed on disk: {tabTitle(path)}</span>
          <button class="rounded px-2 py-0.5 hover:bg-white/10" onClick={() => void reloadFromDisk(path)}>Reload</button>
          <button class="rounded px-2 py-0.5 hover:bg-white/10" onClick={() => void keepMine(path)}>Keep mine</button>
        </div>
      )}
      <div class="relative min-h-0 flex-1">
        <div ref={host} class="absolute inset-0" style={{ display: info.binary || (info.error && !info.loaded) ? "none" : "block" }} />
        {info.binary && <div class="absolute inset-0 flex items-center justify-center text-neutral-500">binary file</div>}
        {!info.binary && info.error && (
          <div class="absolute inset-0 flex items-center justify-center text-neutral-500">{info.error}</div>
        )}
      </div>
      {c && c.path === path && (
        <div class="absolute inset-0 z-50 flex items-center justify-center bg-black/50">
          <div class="w-[380px] rounded-lg bg-panel2 p-4 shadow-xl">
            <div class="mb-1 text-[13px] font-medium text-neutral-100">Save conflict</div>
            <div class="mb-4 text-[12px] text-neutral-400">{tabTitle(path)} changed on disk since you opened it.</div>
            <div class="flex justify-end gap-2 text-[12px]">
              <button class="rounded px-3 py-1 hover:bg-white/10" onClick={cancelConflict}>Cancel</button>
              <button class="rounded px-3 py-1 hover:bg-white/10" onClick={compareConflict}>Compare</button>
              <button class="rounded bg-red-900/60 px-3 py-1 text-red-100 hover:bg-red-900" onClick={overwriteConflict}>Overwrite</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
