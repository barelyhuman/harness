import type * as Monaco from "monaco-editor";
import { Hunks } from "../../lib/bindings/github.com/sid/harness/internal/app/gitservice";
import { joinPath, subscribeJSON } from "./util";

let styled = false;
function ensureStyle() {
  if (styled) return;
  styled = true;
  const s = document.createElement("style");
  s.textContent = `
.qd-added,.qd-modified{width:3px !important;margin-left:3px}
.qd-added{background:#2ea04370;border-left:0;background:#587c0c}
.qd-modified{background:#0c7d9d}
.qd-deleted{width:0 !important;height:0 !important;margin-left:3px;border-style:solid;border-width:4px 0 4px 5px;border-color:transparent transparent transparent #f14c4c;margin-top:-4px}
`;
  document.head.appendChild(s);
}

/** VS Code style quick-diff gutter (unstaged hunks vs index). Returns a dispose function. */
export function attachQuickDiff(editor: Monaco.editor.IStandaloneCodeEditor, scopeKey: string, root: string, path: string): () => void {
  ensureStyle();
  // path may be absolute (editor tabs) or repo relative
  const prefix = root.replace(/\/+$/, "") + "/";
  const rel = path.startsWith(prefix) ? path.slice(prefix.length) : path;
  const abs = path.startsWith("/") ? path : joinPath(root, path);
  let coll: Monaco.editor.IEditorDecorationsCollection | undefined;
  let disposed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let lastSig = "";

  const refresh = async () => {
    try {
      const hunks = (await Hunks(root, rel, false)) ?? [];
      if (disposed) return;
      const sig = JSON.stringify(hunks.map((h) => [h.id, h.newStart, h.newLines, h.oldLines]));
      if (sig === lastSig) return;
      lastSig = sig;
      const max = editor.getModel()?.getLineCount() ?? 1;
      const decos: Monaco.editor.IModelDeltaDecoration[] = hunks.map((h) => {
        let cls = "qd-modified";
        let start = h.newStart;
        let end = h.newStart + Math.max(h.newLines, 1) - 1;
        if (h.oldLines === 0) cls = "qd-added";
        else if (h.newLines === 0) {
          cls = "qd-deleted";
          start = end = h.newStart + 1;
        }
        start = Math.min(Math.max(start, 1), max);
        end = Math.min(Math.max(end, start), max);
        return { range: { startLineNumber: start, startColumn: 1, endLineNumber: end, endColumn: 1 }, options: { isWholeLine: true, linesDecorationsClassName: cls } };
      });
      if (!coll) coll = editor.createDecorationsCollection(decos);
      else coll.set(decos);
    } catch {
      /* not a repo file, or transient git error: leave decorations as they are */
    }
  };
  const schedule = () => {
    clearTimeout(timer);
    timer = setTimeout(refresh, 200);
  };

  const offScm = subscribeJSON("scm/" + scopeKey, schedule);
  const offFs = subscribeJSON<{ paths?: string[] }>("fs/" + scopeKey, (v) => {
    if (!v?.paths || v.paths.includes(abs)) schedule();
  });
  void refresh();
  return () => {
    disposed = true;
    clearTimeout(timer);
    offScm();
    offFs();
    coll?.clear();
  };
}
