// Model layer: one ITextModel per absolute path (uri file://<path>), reference counted, created
// from FSService.Read. Dirty/etag state lives in signals so tab strips and banners can render it.
import { signal, type Signal } from "@preact/signals";
import type * as Monaco from "monaco-editor";
import { bus } from "../../lib/bus";
import { ensureMonaco, THEME } from "../../lib/monaco-setup";
import { FSService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import { errInfo, toast } from "../../state/toast";
import { openTab } from "../../state/tabs";
import { lspDidSave } from "../../lib/lsp-bridge";

export type ModelInfo = {
  etag: string;
  binary: boolean;
  loaded: boolean;
  dirty: boolean;
  /** file changed on disk while the model has unsaved edits (banner) */
  diskChanged: boolean;
  /** Read failed (e.g. deleted) */
  error: string;
};

const EMPTY: ModelInfo = { etag: "", binary: false, loaded: false, dirty: false, diskChanged: false, error: "" };

type Rec = {
  model: Monaco.editor.ITextModel | null;
  refs: number;
  savedAlt: number;
  timer?: ReturnType<typeof setTimeout>;
  loading?: Promise<Rec>;
  saving?: boolean;
};

const recs = new Map<string, Rec>();
const sigs = new Map<string, Signal<ModelInfo>>();
const GRACE_MS = 30_000;

/** Reactive info for a path (created on demand; safe to read before the model exists). */
export function modelInfo(path: string): Signal<ModelInfo> {
  let s = sigs.get(path);
  if (!s) sigs.set(path, (s = signal({ ...EMPTY })));
  return s;
}
const patch = (path: string, p: Partial<ModelInfo>) => {
  const s = modelInfo(path);
  s.value = { ...s.value, ...p };
};

/** Signal accessor: reading it inside a component/computed subscribes to the dirty state. */
export function isDirty(path: string): boolean {
  return modelInfo(path).value.dirty;
}
export function tabTitle(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? path : path.slice(i + 1);
}

export function applyTheme(): typeof Monaco {
  const m = ensureMonaco();
  m.editor.setTheme(THEME);
  return m;
}

export const uriFor = (path: string) => applyTheme().Uri.file(path);
/** Reverse of uriFor for model uris. */
export const pathOf = (uri: Monaco.Uri) => uri.fsPath;

/** Existing model without taking a reference. */
export function peekModel(path: string): Monaco.editor.ITextModel | null {
  return recs.get(path)?.model ?? null;
}

/**
 * Acquire (+1 ref) the model for path, loading it from disk on first use. Resolves null for binary
 * files (no model). Always pair with releaseModel(path), also when the result is null.
 */
export async function getModel(path: string): Promise<Monaco.editor.ITextModel | null> {
  let r = recs.get(path);
  if (!r) recs.set(path, (r = { model: null, refs: 0, savedAlt: 0 }));
  r.refs++;
  if (r.timer) clearTimeout(r.timer);
  r.timer = undefined;
  if (r.model) return r.model;
  if (!r.loading) r.loading = load(path, r);
  try {
    await r.loading;
  } catch (e) {
    patch(path, { loaded: true, error: errInfo(e).message });
  }
  return r.model;
}

async function load(path: string, r: Rec): Promise<Rec> {
  const m = applyTheme();
  try {
    const fc = await FSService.Read(path);
    patch(path, { etag: fc.etag, binary: fc.binary, loaded: true, error: "", dirty: false, diskChanged: false });
    if (!fc.binary) {
      const model = m.editor.createModel(fc.content, fc.language || undefined, uriFor(path));
      r.model = model;
      r.savedAlt = model.getAlternativeVersionId();
      model.onDidChangeContent(() => {
        const d = model.getAlternativeVersionId() !== r!.savedAlt;
        if (d !== modelInfo(path).value.dirty) patch(path, { dirty: d });
      });
    }
  } finally {
    r.loading = undefined;
  }
  return r;
}

/** Release a reference; unreferenced clean models are disposed after a grace period. */
export function releaseModel(path: string): void {
  const r = recs.get(path);
  if (!r) return;
  r.refs = Math.max(0, r.refs - 1);
  if (r.refs > 0) return;
  scheduleDispose(path, r);
}
function scheduleDispose(path: string, r: Rec) {
  if (r.timer) clearTimeout(r.timer);
  r.timer = setTimeout(() => {
    if (r.refs > 0) return;
    if (modelInfo(path).value.dirty) return; // never drop unsaved edits; discardModel() does
    disposeRec(path);
  }, GRACE_MS);
}
function disposeRec(path: string) {
  const r = recs.get(path);
  if (!r) return;
  r.model?.dispose();
  recs.delete(path);
  sigs.delete(path);
}
/** Drop a model and its unsaved edits (used when a file is deleted/renamed). */
export function discardModel(path: string): void {
  const r = recs.get(path);
  if (r && r.refs === 0) disposeRec(path);
}

/** Make sure a model exists for a uri (used by LSP before returning locations in other files). */
export async function ensureModelForUri(uri: Monaco.Uri): Promise<void> {
  if (uri.scheme !== "file") return;
  const path = uri.fsPath;
  await getModel(path);
  releaseModel(path); // lingers for GRACE_MS so peek/references widgets can resolve it
}

// ---- saving -------------------------------------------------------------------------------

export const conflict = signal<{ scopeKey: string; path: string } | null>(null);

export async function saveModel(scopeKey: string, path: string, force = false): Promise<"ok" | "conflict" | "error" | "noop"> {
  const r = recs.get(path);
  const model = r?.model;
  if (!r || !model || r.saving) return "noop";
  r.saving = true;
  const alt = model.getAlternativeVersionId();
  try {
    const etag = await FSService.Write(path, model.getValue(), force ? "" : modelInfo(path).value.etag);
    r.savedAlt = alt;
    patch(path, { etag, dirty: model.getAlternativeVersionId() !== alt, diskChanged: false });
    if (conflict.value?.path === path) conflict.value = null;
    lspDidSave(model);
    return "ok";
  } catch (e) {
    const info = errInfo(e);
    if (info.code === "conflict") {
      conflict.value = { scopeKey, path };
      return "conflict";
    }
    toast(`Save failed: ${info.message}`, "error");
    return "error";
  } finally {
    r.saving = false;
  }
}

export const overwriteConflict = () => {
  const c = conflict.value;
  if (c) void saveModel(c.scopeKey, c.path, true);
};
export const compareConflict = () => {
  const c = conflict.value;
  if (!c) return;
  openTab(c.scopeKey, { id: "diff:conflict:" + c.path, kind: "diff", path: c.path, staged: false });
  conflict.value = null;
};
export const cancelConflict = () => {
  conflict.value = null;
};

// ---- external changes ---------------------------------------------------------------------

function lineEdit(model: Monaco.editor.ITextModel, text: string): { edit: Monaco.editor.IIdentifiedSingleEditOperation; first: number; last: number } | null {
  const m = ensureMonaco();
  const oldL = model.getLinesContent();
  const newL = text.split(/\r\n|\r|\n/);
  let p = 0;
  while (p < oldL.length && p < newL.length && oldL[p] === newL[p]) p++;
  let s = 0;
  while (s < oldL.length - p && s < newL.length - p && oldL[oldL.length - 1 - s] === newL[newL.length - 1 - s]) s++;
  const oldSeg = oldL.length - s - p;
  const newSeg = newL.slice(p, newL.length - s);
  if (oldSeg === 0 && newSeg.length === 0) return null;
  const eol = model.getEOL();
  const maxCol = (ln: number) => model.getLineMaxColumn(ln);
  let range: Monaco.Range;
  let out: string;
  if (oldSeg === 0) {
    if (p < oldL.length) {
      range = new m.Range(p + 1, 1, p + 1, 1);
      out = newSeg.join(eol) + eol;
    } else {
      range = new m.Range(oldL.length, maxCol(oldL.length), oldL.length, maxCol(oldL.length));
      out = eol + newSeg.join(eol);
    }
  } else if (newSeg.length === 0) {
    const endLn = p + oldSeg;
    if (endLn < oldL.length) range = new m.Range(p + 1, 1, endLn + 1, 1);
    else if (p > 0) range = new m.Range(p, maxCol(p), endLn, maxCol(endLn));
    else range = new m.Range(1, 1, endLn, maxCol(endLn));
    out = "";
  } else {
    const endLn = p + oldSeg;
    range = new m.Range(p + 1, 1, endLn, maxCol(endLn));
    out = newSeg.join(eol);
  }
  return { edit: { range, text: out }, first: p + 1, last: p + Math.max(1, newSeg.length) };
}

function flash(model: Monaco.editor.ITextModel, first: number, last: number) {
  const m = ensureMonaco();
  const n = model.getLineCount();
  const range = new m.Range(Math.min(first, n), 1, Math.min(last, n), 1);
  const ids = model.deltaDecorations([], [{ range, options: { isWholeLine: true, className: "harness-flash-line" } }]);
  setTimeout(() => {
    if (!model.isDisposed()) model.deltaDecorations(ids, []);
  }, 1500);
}

/** Replace the model's content with what's on disk (keeps cursor/undo, flashes changed lines). */
export async function reloadFromDisk(path: string, opts: { flash?: boolean } = {}): Promise<void> {
  const r = recs.get(path);
  if (!r?.model) return;
  const model = r.model;
  const fc = await FSService.Read(path);
  if (fc.binary) return;
  if (fc.content !== model.getValue()) {
    const le = lineEdit(model, fc.content);
    if (le) {
      model.pushStackElement();
      model.pushEditOperations([], [le.edit], () => null);
      model.pushStackElement();
      if (opts.flash !== false) flash(model, le.first, le.last);
    }
  }
  r.savedAlt = model.getAlternativeVersionId();
  patch(path, { etag: fc.etag, dirty: false, diskChanged: false, error: "" });
}

/** "Keep mine": adopt the disk etag but keep the buffer, so the next save overwrites. */
export async function keepMine(path: string): Promise<void> {
  try {
    const fc = await FSService.Read(path);
    patch(path, { etag: fc.etag, diskChanged: false });
  } catch {
    patch(path, { diskChanged: false });
  }
}

async function onDiskChanged(paths: string[]) {
  for (const path of paths) {
    const r = recs.get(path);
    if (!r?.model) continue;
    const info = modelInfo(path).value;
    try {
      const fc = await FSService.Read(path);
      if (fc.etag === info.etag) continue; // our own write or no real change
      if (info.dirty) patch(path, { diskChanged: true });
      else await reloadFromDisk(path);
    } catch {
      /* deleted/unreadable: keep buffer */
    }
  }
}

const watchers = new Map<string, { n: number; off: () => void }>();
/** Refcounted subscription to `fs/<scopeKey>`; returns an unsubscribe. */
export function watchFs(scopeKey: string): () => void {
  let w = watchers.get(scopeKey);
  if (!w) {
    const chans = new Set<() => void>();
    const off = bus.onOpen("fs/" + scopeKey, (ch) => {
      if (ch.name !== "fs/" + scopeKey) return;
      chans.add(ch.onJSON<{ paths?: string[] }>((v) => v?.paths && void onDiskChanged(v.paths)));
    });
    w = { n: 0, off: () => { off(); chans.forEach((f) => f()); } };
    watchers.set(scopeKey, w);
  }
  w.n++;
  return () => {
    const x = watchers.get(scopeKey);
    if (!x || --x.n > 0) return;
    x.off();
    watchers.delete(scopeKey);
  };
}

// ---- editor/scope bookkeeping -------------------------------------------------------------

/** Which scope each live editor belongs to (for cross-file navigation from LSP results). */
export const editorScopes = new WeakMap<object, string>();
export let lastScope = "";
export const setLastScope = (s: string) => { lastScope = s; };
