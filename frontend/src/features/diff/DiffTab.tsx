import { useEffect, useRef, useState } from "preact/hooks";
import type * as Monaco from "monaco-editor";
import { ensureMonaco } from "../../lib/monaco-setup";
import { ShowFile, Hunks, StageHunk, UnstageHunk, RevertHunk, StageFiles, UnstageFiles } from "../../lib/bindings/github.com/sid/harness/internal/app/gitservice";
import { Read, Write } from "../../lib/bindings/github.com/sid/harness/internal/app/fsservice";
import { Get, Set as SetUI } from "../../lib/bindings/github.com/sid/harness/internal/app/uistateservice";
import type { Hunk } from "../../lib/bindings/github.com/sid/harness/internal/git/models";
import { openFile } from "../../state/editor";
import { errInfo, joinPath, subscribeJSON, type ErrInfo } from "../scm/util";
import * as I from "../scm/icons";

const THEME = "harness-dark"; // TODO: swap for applyTheme() from features/editor/monaco.ts once it exists.

function Btn(p: { title: string; onClick: () => void; children: any; label?: string }) {
  return (
    <button type="button" title={p.title} onClick={p.onClick} class="flex h-[24px] cursor-pointer items-center gap-1 rounded-[5px] border-0 bg-transparent px-[6px] text-[12px] text-text hover:bg-hover">
      {p.children}
      {p.label}
    </button>
  );
}

function langFor(monaco: typeof Monaco, path: string, hint?: string): string {
  if (hint) return hint;
  const ext = "." + (path.split(".").pop() ?? "").toLowerCase();
  return monaco.languages.getLanguages().find((l) => l.extensions?.includes(ext))?.id ?? "plaintext";
}

export function DiffTab({ scopeKey, root, path: rawPath, staged }: { scopeKey: string; root: string; path: string; staged: boolean }) {
  // Tabs may carry an absolute path (chat tool cards, conflict compare); Go wants repo-relative.
  const prefix = root.replace(/\/+$/, "") + "/";
  const path = rawPath.startsWith(prefix) ? rawPath.slice(prefix.length) : rawPath;
  const host = useRef<HTMLDivElement>(null);
  const api = useRef<{ toggle(): void; next(dir: "next" | "previous"): void; reload(): void; save(): void } | null>(null);
  const [inline, setInline] = useState(false);
  const [err, setErr] = useState<ErrInfo | null>(null);
  const [dirty, setDirty] = useState(false);
  const inlineRef = useRef(false);

  useEffect(() => {
    const monaco = ensureMonaco();
    const abs = joinPath(root, path);
    let disposed = false;
    let etag = "";
    let savedText = "";
    let hunks: Hunk[] = [];
    let widgets: Monaco.editor.IContentWidget[] = [];
    let busy = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let lastSig = "";

    const uriBase = `harness-diff://${encodeURIComponent(scopeKey)}/${staged ? "staged" : "work"}/${path}`;
    const orig = monaco.editor.createModel("", "plaintext", monaco.Uri.parse(uriBase + "?orig"));
    const mod = monaco.editor.createModel("", "plaintext", monaco.Uri.parse(uriBase + "?mod"));
    const ed = monaco.editor.createDiffEditor(host.current!, {
      theme: THEME,
      automaticLayout: true,
      renderSideBySide: true,
      originalEditable: false,
      readOnly: staged,
      glyphMargin: false,
      renderOverviewRuler: true,
      scrollBeyondLastLine: false,
      minimap: { enabled: false },
      fontSize: 13,
      ignoreTrimWhitespace: false,
      useInlineViewWhenSpaceIsLimited: false,
    });
    ed.setModel({ original: orig, modified: mod });
    const me = ed.getModifiedEditor();

    const applyInline = (v: boolean) => {
      inlineRef.current = v;
      setInline(v);
      ed.updateOptions({ renderSideBySide: !v });
    };
    Get("diff.inline").then((s) => !disposed && s && applyInline(JSON.parse(s) === true), () => {});

    const setText = (m: Monaco.editor.ITextModel, text: string) => {
      if (m.getValue() !== text) m.setValue(text);
    };

    const clearWidgets = () => {
      widgets.forEach((w) => me.removeContentWidget(w));
      widgets = [];
    };

    const hunkOp = async (h: Hunk, op: "stage" | "unstage" | "revert") => {
      if (busy) return;
      busy = true;
      try {
        if (op === "stage") await StageHunk(root, path, h.id);
        else if (op === "unstage") await UnstageHunk(root, path, h.id);
        else await RevertHunk(root, path, h.id);
        setErr(null);
      } catch (e) {
        const i = errInfo(e);
        if (i.code !== "stale_hunk") setErr(i); // stale ids are refetched silently below
      } finally {
        busy = false;
        await load(true);
      }
    };

    const renderHunks = () => {
      clearWidgets();
      const lines = mod.getLineCount();
      hunks.forEach((h, idx) => {
        // New side of the hunk (the index for staged diffs, the working file otherwise).
        const line = Math.min(Math.max(h.newLines > 0 ? h.newStart : h.newStart + 1, 1), lines);
        const dom = document.createElement("div");
        dom.style.cssText = "display:flex;gap:2px;margin-left:24px;padding:1px;background:#252526;border:1px solid #454545;border-radius:4px;opacity:.85;font:11px -apple-system,system-ui,sans-serif;z-index:5";
        const mk = (label: string, title: string, op: "stage" | "unstage" | "revert") => {
          const b = document.createElement("button");
          b.textContent = label;
          b.title = title;
          b.style.cssText = "background:transparent;border:0;color:#cccccc;cursor:pointer;padding:0 6px;height:16px;border-radius:3px;font:inherit";
          b.onmouseenter = () => (b.style.background = "#5a5d5e80");
          b.onmouseleave = () => (b.style.background = "transparent");
          b.onmousedown = (e) => e.preventDefault();
          b.onclick = () => hunkOp(h, op);
          dom.appendChild(b);
        };
        if (staged) mk("Unstage", "Unstage Block", "unstage");
        else {
          mk("Stage", "Stage Block", "stage");
          mk("Revert", "Revert Block", "revert");
        }
        const w: Monaco.editor.IContentWidget = {
          getId: () => `hunk-${idx}-${h.id}`,
          getDomNode: () => dom,
          getPosition: () => ({ position: { lineNumber: line, column: mod.getLineMaxColumn(line) }, preference: [monaco.editor.ContentWidgetPositionPreference.EXACT] }),
        };
        me.addContentWidget(w);
        widgets.push(w);
      });
    };

    const load = async (force = false) => {
      try {
        let left: string, right: string;
        let lang: string | undefined;
        if (staged) {
          [left, right] = await Promise.all([ShowFile(root, "HEAD", path), ShowFile(root, ":0", path)]);
        } else {
          left = await ShowFile(root, ":0", path);
          if (!left) left = await ShowFile(root, "HEAD", path);
          try {
            const f = await Read(abs);
            right = f.content;
            etag = f.etag;
            lang = f.language;
          } catch {
            right = ""; // deleted in the working tree
            etag = "";
          }
        }
        const hs = (await Hunks(root, path, staged)) ?? [];
        if (disposed) return;
        const top = me.getScrollTop();
        const left_ = ed.getOriginalEditor().getScrollTop();
        const l = langFor(monaco, path, lang);
        if (orig.getLanguageId() !== l) {
          monaco.editor.setModelLanguage(orig, l);
          monaco.editor.setModelLanguage(mod, l);
        }
        setText(orig, left);
        const userEdited = !staged && mod.getValue() !== savedText;
        if (!userEdited || force) {
          setText(mod, right);
          savedText = right;
          setDirty(false);
        }
        hunks = hs;
        renderHunks();
        me.setScrollTop(top);
        ed.getOriginalEditor().setScrollTop(left_);
      } catch (e) {
        if (!disposed) setErr(errInfo(e));
      }
    };

    const schedule = () => {
      clearTimeout(timer);
      timer = setTimeout(() => load(), 120);
    };

    const save = async () => {
      if (staged) return;
      try {
        const text = mod.getValue();
        etag = await Write(abs, text, etag);
        savedText = text;
        setDirty(false);
        setErr(null);
        schedule();
      } catch (e) {
        setErr(errInfo(e));
      }
    };

    me.onDidChangeModelContent(() => !staged && setDirty(mod.getValue() !== savedText));
    if (!staged) me.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => void save());
    const nav = (dir: "next" | "previous") => ed.goToDiff(dir);
    for (const e of [ed.getModifiedEditor(), ed.getOriginalEditor()]) {
      e.addCommand(monaco.KeyMod.Alt | monaco.KeyCode.F5, () => nav("next"));
      e.addCommand(monaco.KeyMod.Shift | monaco.KeyMod.Alt | monaco.KeyCode.F5, () => nav("previous"));
    }

    api.current = {
      toggle: () => {
        const v = !inlineRef.current;
        applyInline(v);
        SetUI("diff.inline", JSON.stringify(v)).catch(() => {});
      },
      next: nav,
      reload: () => void load(true),
      save: () => void save(),
    };

    // Reload when this path's SCM rows change or the working file changes on disk.
    const sigOf = (s: any) =>
      JSON.stringify((s?.groups ?? []).flatMap((g: any) => (g.rows ?? []).filter((r: any) => r.path === path).map((r: any) => [g.id, r.code])));
    const offScm = subscribeJSON<any>("scm/" + scopeKey, (s) => {
      const sig = sigOf(s);
      if (sig !== lastSig) {
        lastSig = sig;
        schedule();
      }
    });
    const offFs = subscribeJSON<{ paths?: string[] }>("fs/" + scopeKey, (v) => {
      if (!v?.paths || v.paths.includes(abs)) schedule();
    });

    void load(true);
    return () => {
      disposed = true;
      api.current = null;
      clearTimeout(timer);
      offScm();
      offFs();
      clearWidgets();
      ed.dispose();
      orig.dispose();
      mod.dispose();
    };
  }, [scopeKey, root, path, staged]);

  const wholeFile = async () => {
    try {
      if (staged) await UnstageFiles(root, [path]);
      else await StageFiles(root, [path]);
    } catch (e) {
      setErr(errInfo(e));
    }
  };

  const name = path.split("/").pop();
  return (
    <div class="flex h-full min-h-0 flex-col bg-bg" data-ctx="diff">
      <div class="flex h-[30px] shrink-0 items-center gap-[2px] border-b border-border px-[8px] text-[12px] text-text">
        <span class="mr-2 truncate">
          {name}
          <span class="opacity-60"> ({staged ? "Index" : "Working Tree"})</span>
          {dirty && <span class="ml-1">●</span>}
        </span>
        <span class="flex-1" />
        <Btn title="Previous Change (⇧⌥F5)" onClick={() => api.current?.next("previous")}><I.IconPrev /></Btn>
        <Btn title="Next Change (⌥F5)" onClick={() => api.current?.next("next")}><I.IconNext /></Btn>
        <Btn title={inline ? "Show Side-by-Side" : "Show Inline"} onClick={() => api.current?.toggle()}>{inline ? <I.IconSplit /> : <I.IconInline />}</Btn>
        <Btn title="Open File" onClick={() => openFile(scopeKey, joinPath(root, path))}><I.IconOpenFile /></Btn>
        <Btn title={staged ? "Unstage File" : "Stage File"} onClick={wholeFile}>{staged ? <I.IconRemove /> : <I.IconAdd />}</Btn>
      </div>
      {err && (
        <div class="flex shrink-0 items-start gap-2 border-b border-[#be1100] bg-[#5a1d1d] px-3 py-1 text-[12px] text-white">
          <span class="min-w-0 flex-1 break-words">{err.message}</span>
          {err.code === "conflict" && <button type="button" class="cursor-pointer border-0 bg-transparent text-white underline" onClick={() => { setErr(null); api.current?.reload(); }}>Reload</button>}
          <button type="button" class="cursor-pointer border-0 bg-transparent p-0 text-white" onClick={() => setErr(null)}><I.IconClose /></button>
        </div>
      )}
      <div ref={host} class="min-h-0 flex-1" />
    </div>
  );
}
