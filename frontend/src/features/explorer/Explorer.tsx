// Lazy, virtualised file tree. Directories load on expand via ProjectService.ListDir and refresh on
// `fs/<scopeKey>` events; git colours come from `scm/<scopeKey>` row classes.
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { FSService, ProjectService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { Entry } from "../../lib/bindings/github.com/sid/harness/internal/fs/models";
import { openFile } from "../../state/editor";
import { toast, toastError } from "../../state/toast";
import { CLASS_COLOR, fileIcon, joinPath, subscribeJSON } from "../scm/util";

const ROW_H = 22;
type Row = { kind: "entry"; e: Entry; depth: number } | { kind: "new"; parent: string; isDir: boolean; depth: number };
type ScmRow = { path: string; class: string };
type ScmState = { groups?: { rows?: ScmRow[] }[] };

const parentOf = (p: string) => p.slice(0, p.lastIndexOf("/")) || "/";
const baseOf = (p: string) => p.slice(p.lastIndexOf("/") + 1);

export function Explorer({ scopeKey, root }: { scopeKey: string; root: string }) {
  const r = root.replace(/\/+$/, "");
  const [tree, setTree] = useState<Record<string, Entry[]>>({});
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set([r]));
  const [selected, setSelected] = useState<string>("");
  const [renaming, setRenaming] = useState<string>("");
  const [creating, setCreating] = useState<{ parent: string; isDir: boolean } | null>(null);
  const [menu, setMenu] = useState<{ x: number; y: number; target: Entry | null } | null>(null);
  const [confirmDel, setConfirmDel] = useState<Entry | null>(null);
  const [colors, setColors] = useState<Map<string, string>>(new Map());
  const [view, setView] = useState({ top: 0, h: 600 });
  const box = useRef<HTMLDivElement>(null);
  const expandedRef = useRef(expanded);
  expandedRef.current = expanded;

  const load = useCallback(async (dir: string) => {
    try {
      const es = (await ProjectService.ListDir(dir, false, true)) ?? [];
      setTree((t) => ({ ...t, [dir]: es.filter((e) => e.name !== ".git") }));
    } catch (e) {
      if (dir === r) toastError(e);
    }
  }, [r]);

  // initial + root change
  useEffect(() => {
    setTree({});
    setExpanded(new Set([r]));
    void load(r);
  }, [r, load]);

  // fs events: refresh affected expanded folders (debounced)
  useEffect(() => {
    const dirty = new Set<string>();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const off = subscribeJSON<{ paths?: string[] }>("fs/" + scopeKey, (v) => {
      for (const p of v.paths ?? []) {
        const d = parentOf(p);
        if (expandedRef.current.has(d)) dirty.add(d);
        if (expandedRef.current.has(p)) dirty.add(p);
      }
      if (dirty.size && !timer) {
        timer = setTimeout(() => {
          timer = undefined;
          const ds = [...dirty];
          dirty.clear();
          ds.forEach((d) => void load(d));
        }, 120);
      }
    });
    return () => { off(); if (timer) clearTimeout(timer); };
  }, [scopeKey, load]);

  // scm decorations
  useEffect(() => {
    return subscribeJSON<ScmState>("scm/" + scopeKey, (s) => {
      const m = new Map<string, string>();
      for (const g of s.groups ?? []) {
        for (const row of g.rows ?? []) {
          const abs = joinPath(r, row.path);
          if (!m.has(abs)) m.set(abs, row.class);
          // colour ancestors (up to root) so collapsed folders hint at changes
          let d = parentOf(abs);
          while (d.length > r.length && !m.has(d)) { m.set(d, row.class); d = parentOf(d); }
        }
      }
      setColors(m);
    });
  }, [scopeKey, r]);

  const toggle = (e: Entry) => {
    const next = new Set(expanded);
    if (next.has(e.path)) next.delete(e.path);
    else { next.add(e.path); if (!tree[e.path]) void load(e.path); }
    setExpanded(next);
  };

  const rows = useMemo(() => {
    const out: Row[] = [];
    const walk = (dir: string, depth: number) => {
      if (creating?.parent === dir) out.push({ kind: "new", parent: dir, isDir: creating.isDir, depth });
      for (const e of tree[dir] ?? []) {
        out.push({ kind: "entry", e, depth });
        if (e.isDir && expanded.has(e.path)) walk(e.path, depth + 1);
      }
    };
    walk(r, 0);
    return out;
  }, [tree, expanded, creating, r]);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setView((v) => ({ ...v, h: el.clientHeight })));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const start = Math.max(0, Math.floor(view.top / ROW_H) - 8);
  const end = Math.min(rows.length, Math.ceil((view.top + view.h) / ROW_H) + 8);

  // ---- actions ----
  const beginCreate = (parent: string, isDir: boolean) => {
    const next = new Set(expanded);
    next.add(parent);
    setExpanded(next);
    if (!tree[parent]) void load(parent);
    setCreating({ parent, isDir });
  };
  const commitCreate = async (name: string) => {
    const c = creating;
    setCreating(null);
    if (!c || !name.trim()) return;
    const path = c.parent + "/" + name.trim();
    try {
      await FSService.Create(path, c.isDir);
      await load(c.parent);
      if (!c.isDir) openFile(scopeKey, path, false);
      setSelected(path);
    } catch (e) { toastError(e); }
  };
  const commitRename = async (e: Entry, name: string) => {
    setRenaming("");
    const n = name.trim();
    if (!n || n === e.name) return;
    const to = parentOf(e.path) + "/" + n;
    try {
      await FSService.Rename(e.path, to);
      await load(parentOf(e.path));
      setSelected(to);
    } catch (err) { toastError(err); }
  };
  const doDelete = async (e: Entry) => {
    setConfirmDel(null);
    try {
      await FSService.Delete(e.path);
      await load(parentOf(e.path));
    } catch (err) { toastError(err); }
  };
  const entryOf = (p: string) => rows.find((x): x is Extract<Row, { kind: "entry" }> => x.kind === "entry" && x.e.path === p)?.e;
  const targetDir = (e: Entry | null) => (!e ? r : e.isDir ? e.path : parentOf(e.path));

  const onKey = (ev: KeyboardEvent) => {
    if ((ev.target as HTMLElement).tagName === "INPUT") return;
    const list = rows.filter((x): x is Extract<Row, { kind: "entry" }> => x.kind === "entry");
    const i = list.findIndex((x) => x.e.path === selected);
    const cur = i >= 0 ? list[i].e : undefined;
    const select = (j: number) => {
      const x = list[Math.max(0, Math.min(list.length - 1, j))];
      if (!x) return;
      setSelected(x.e.path);
      const idx = rows.indexOf(x);
      const el = box.current!;
      if (idx * ROW_H < el.scrollTop) el.scrollTop = idx * ROW_H;
      else if ((idx + 1) * ROW_H > el.scrollTop + el.clientHeight) el.scrollTop = (idx + 1) * ROW_H - el.clientHeight;
    };
    if (ev.key === "ArrowDown") { ev.preventDefault(); select(i + 1); }
    else if (ev.key === "ArrowUp") { ev.preventDefault(); select(i < 0 ? 0 : i - 1); }
    else if (ev.key === "ArrowRight" && cur?.isDir) { ev.preventDefault(); if (!expanded.has(cur.path)) toggle(cur); else select(i + 1); }
    else if (ev.key === "ArrowLeft" && cur) {
      ev.preventDefault();
      if (cur.isDir && expanded.has(cur.path)) toggle(cur);
      else { const p = parentOf(cur.path); const j = list.findIndex((x) => x.e.path === p); if (j >= 0) select(j); }
    } else if (ev.key === "Enter" && cur) { ev.preventDefault(); cur.isDir ? toggle(cur) : openFile(scopeKey, cur.path, false); }
    else if (ev.key === "F2" && cur) { ev.preventDefault(); setRenaming(cur.path); }
    else if (ev.key === "Backspace" && ev.metaKey && cur) { ev.preventDefault(); setConfirmDel(cur); }
  };

  const menuItems = (t: Entry | null): [string, () => void][] => {
    const items: [string, () => void][] = [
      ["New File", () => beginCreate(targetDir(t), false)],
      ["New Folder", () => beginCreate(targetDir(t), true)],
    ];
    if (t) {
      items.push(
        ["Rename", () => setRenaming(t.path)],
        ["Delete", () => setConfirmDel(t)],
        ["Copy Path", () => void navigator.clipboard?.writeText(t.path)],
        ["Copy Relative Path", () => void navigator.clipboard?.writeText(t.path.slice(r.length + 1))],
        ["Reveal in Finder", () => toast("Reveal in Finder is not available yet", "info")],
      );
    }
    return items;
  };

  return (
    <div data-ctx="explorer" class="relative flex h-full min-h-0 flex-col text-[12.5px] text-neutral-300">
      <div class="flex items-center gap-1 px-2 py-1 text-[11px] text-neutral-500">
        <span class="flex-1 truncate uppercase tracking-wide">{baseOf(r)}</span>
        <button class="rounded px-1 hover:bg-white/10" title="New File" onClick={() => beginCreate(targetDir(entryOf(selected) ?? null), false)}>+ File</button>
        <button class="rounded px-1 hover:bg-white/10" title="New Folder" onClick={() => beginCreate(targetDir(entryOf(selected) ?? null), true)}>+ Dir</button>
        <button class="rounded px-1 hover:bg-white/10" title="Refresh" onClick={() => [...expanded].forEach((d) => void load(d))}>{"↻"}</button>
      </div>
      <div
        ref={box}
        tabIndex={0}
        class="min-h-0 flex-1 overflow-auto outline-none"
        onScroll={(e) => setView((v) => ({ ...v, top: (e.target as HTMLElement).scrollTop }))}
        onKeyDown={onKey}
        onContextMenu={(e) => { if (e.target === e.currentTarget) { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY, target: null }); } }}
      >
        <div style={{ height: rows.length * ROW_H, position: "relative" }}>
          {rows.slice(start, end).map((row, k) => {
            const top = (start + k) * ROW_H;
            if (row.kind === "new") {
              return (
                <div key={"new"} class="absolute left-0 right-0 flex items-center px-2" style={{ top, height: ROW_H, paddingLeft: 8 + row.depth * 12 + 16 }}>
                  <NameInput initial="" onDone={(n) => void commitCreate(n)} onCancel={() => setCreating(null)} />
                </div>
              );
            }
            const { e, depth } = row;
            const open = e.isDir && expanded.has(e.path);
            const cls = colors.get(e.path);
            const color = cls ? CLASS_COLOR[cls] : undefined;
            const [glyph, gcolor] = fileIcon(e.name);
            return (
              <div
                key={e.path}
                class={`absolute left-0 right-0 flex cursor-default items-center gap-1 pr-2 ${selected === e.path ? "bg-white/10" : "hover:bg-white/5"}`}
                style={{ top, height: ROW_H, paddingLeft: 8 + depth * 12 }}
                onClick={() => {
                  setSelected(e.path);
                  box.current?.focus();
                  if (e.isDir) toggle(e);
                  else openFile(scopeKey, e.path, true);
                }}
                onDblClick={() => { if (!e.isDir) openFile(scopeKey, e.path, false); }}
                onContextMenu={(ev) => { ev.preventDefault(); setSelected(e.path); setMenu({ x: ev.clientX, y: ev.clientY, target: e }); }}
              >
                <span class="w-3 shrink-0 text-center text-[9px] text-neutral-500">{e.isDir ? (open ? "▼" : "▶") : ""}</span>
                {!e.isDir && <span class="w-4 shrink-0 text-center text-[8px] font-bold" style={{ color: gcolor }}>{glyph}</span>}
                {renaming === e.path ? (
                  <NameInput initial={e.name} onDone={(n) => void commitRename(e, n)} onCancel={() => setRenaming("")} />
                ) : (
                  <span class={`truncate ${e.symlink ? "italic" : ""}`} style={color ? { color } : undefined}>{e.name}</span>
                )}
                {color && !e.isDir && <span class="ml-auto h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: color }} />}
              </div>
            );
          })}
        </div>
      </div>
      {menu && (
        <div class="fixed inset-0 z-50" onClick={() => setMenu(null)} onContextMenu={(e) => { e.preventDefault(); setMenu(null); }}>
          <div class="absolute min-w-40 rounded-md border border-neutral-700 bg-panel2 py-1 text-[12px] shadow-xl" style={{ left: menu.x, top: menu.y }}>
            {menuItems(menu.target).map(([label, fn]) => (
              <div key={label} class="cursor-default px-3 py-1 hover:bg-blue-600/60" onClick={() => { setMenu(null); fn(); }}>{label}</div>
            ))}
          </div>
        </div>
      )}
      {confirmDel && (
        <div class="absolute inset-0 z-50 flex items-center justify-center bg-black/50">
          <div class="w-72 rounded-lg border border-neutral-700 bg-panel2 p-4 shadow-xl">
            <div class="mb-1 text-[13px] text-neutral-100">Move to Trash?</div>
            <div class="mb-4 truncate text-[12px] text-neutral-400">{confirmDel.name}</div>
            <div class="flex justify-end gap-2 text-[12px]">
              <button class="rounded px-3 py-1 hover:bg-white/10" onClick={() => setConfirmDel(null)}>Cancel</button>
              <button class="rounded bg-red-900/60 px-3 py-1 text-red-100 hover:bg-red-900" onClick={() => void doDelete(confirmDel)}>Delete</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function NameInput(p: { initial: string; onDone: (v: string) => void; onCancel: () => void }) {
  const ref = useRef<HTMLInputElement>(null);
  const done = useRef(false);
  useEffect(() => {
    const el = ref.current!;
    el.focus();
    const dot = p.initial.lastIndexOf(".");
    el.setSelectionRange(0, dot > 0 ? dot : p.initial.length);
  }, []);
  const finish = (commit: boolean) => {
    if (done.current) return;
    done.current = true;
    commit ? p.onDone(ref.current!.value) : p.onCancel();
  };
  return (
    <input
      ref={ref}
      defaultValue={p.initial}
      class="min-w-0 flex-1 rounded border border-blue-500/60 bg-bg px-1 outline-none"
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === "Enter") finish(true);
        else if (e.key === "Escape") finish(false);
      }}
      onBlur={() => finish(true)}
    />
  );
}
