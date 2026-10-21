import { useEffect, useRef, useState } from "preact/hooks";
import { ProjectService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { Entry } from "../../lib/bindings/github.com/sid/harness/internal/fs/models";
import { currentProjectId, projects, refreshProjects, sessionsByProject } from "../../state/agents";
import { activateMain } from "../../state/scope";
import { toast, toastError } from "../../state/toast";
import { closeOverlay } from "../../state/ui";
import { Highlight, Icon } from "../shell/ui";

const ROW = 34;
const VIEW = 320;
type Row = { key: string; label: string; positions?: number[] | null; entry?: Entry; projectId?: string; sub?: string };

const parentOf = (p: string) => p.replace(/\/[^/]+\/?$/, "") || "/";
const join = (d: string, n: string) => (d.endsWith("/") ? d + n : d + "/" + n);

export function ProjectPicker() {
  const [mode, setMode] = useState<"recent" | "path">("recent");
  const [text, setText] = useState("");
  const [dir, setDir] = useState("");
  const [home, setHome] = useState("");
  const [hidden, setHidden] = useState(false);
  const [rows, setRows] = useState<Row[]>([]);
  const [sel, setSel] = useState(0);
  const [top, setTop] = useState(0);
  const [badges, setBadges] = useState<Record<string, { branch: string; worktrees: number } | undefined>>({});
  const scroller = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => { ProjectService.Home().then(setHome).catch(() => {}); input.current?.focus(); }, []);

  // Rebuild rows when mode / dir / filter / hidden changes.
  useEffect(() => {
    let dead = false;
    const apply = (r: Row[]) => { if (!dead) { setRows(r); setSel(0); setTop(0); if (scroller.current) scroller.current.scrollTop = 0; } };
    (async () => {
      try {
        if (mode === "recent") {
          const names = projects.value.map((p) => p.name);
          let order = projects.value.map((_, i) => ({ index: i, positions: null as number[] | null }));
          if (text.trim()) order = ((await ProjectService.Fuzzy(text, names, 50)) ?? []).map((m) => ({ index: m.index, positions: m.positions }));
          apply(order.map(({ index, positions }) => {
            const p = projects.value[index];
            const n = (sessionsByProject.value[p.id] ?? []).filter((s) => s.status !== "archived").length;
            return { key: p.id, label: p.name, positions, projectId: p.id, sub: `${p.path.replace(home, "~")}${n ? ` · ${n} agent${n > 1 ? "s" : ""}` : ""}` };
          }));
        } else {
          const ents = (await ProjectService.ListDir(dir, true, hidden)) ?? [];
          const names = ents.map((e) => e.name);
          let order = ents.map((_, i) => ({ index: i, positions: null as number[] | null }));
          if (text) order = ((await ProjectService.FilterEntries(text, names)) ?? []).map((m) => ({ index: m.index, positions: m.positions }));
          if (!dead) apply(order.map(({ index, positions }) => ({ key: ents[index].path, label: ents[index].name, positions, entry: ents[index] })));
        }
      } catch (e) { if (!dead) { setRows([]); toastError(e); } }
    })();
    return () => { dead = true; };
  }, [mode, dir, hidden, mode === "recent" ? text : text, home]);

  // Repo badges only for rows in the viewport.
  const start = Math.max(0, Math.floor(top / ROW) - 2);
  const end = Math.min(rows.length, Math.ceil((top + VIEW) / ROW) + 2);
  useEffect(() => {
    if (mode !== "path") return;
    const need = rows.slice(start, end).filter((r) => r.entry?.isRepo && !(r.key in badges)).map((r) => r.key);
    if (!need.length) return;
    let dead = false;
    ProjectService.RepoBadges(need).then((b) => { if (!dead) setBadges((old) => ({ ...old, ...Object.fromEntries(need.map((k) => [k, b?.[k] ?? undefined])) })); }).catch(() => {});
    return () => { dead = true; };
  }, [rows, start, end, mode]);

  const enterPath = (d: string, filter = "") => { setMode("path"); setDir(d); setText(filter); };
  const resolveDir = (part: string): string => {
    if (part.startsWith("~")) return home + part.slice(1);
    if (part.startsWith("/")) return part;
    return join(dir || home, part);
  };
  const onInput = (v: string) => {
    if (mode === "recent") {
      if (/^[/~.]/.test(v)) { return jumpFromText(v); }
      return setText(v);
    }
    if (v.includes("/") || v === "~" ) return jumpFromText(v);
    setText(v);
  };
  const jumpFromText = (v: string) => {
    if (v === "~") return enterPath(home);
    if (v === ".") return enterPath(dir || projects.value.find((p) => p.id === currentProjectId.value)?.path || home);
    const i = v.lastIndexOf("/");
    if (i < 0) return setText(v);
    let d = resolveDir(v.slice(0, i + 1) || "/");
    if (d.length > 1) d = d.replace(/\/+$/, "") || "/";
    enterPath(d, v.slice(i + 1));
  };

  const openRow = async (r?: Row) => {
    try {
      if (mode === "recent") {
        if (!r?.projectId) return;
        await ProjectService.TouchProject(r.projectId).catch(() => {});
        currentProjectId.value = r.projectId;
        closeOverlay();
        await activateMain(r.projectId);
        return;
      }
      const path = r?.entry?.path ?? dir;
      const p = await ProjectService.AddProject(path);
      await refreshProjects();
      currentProjectId.value = p.id;
      closeOverlay();
      await activateMain(p.id);
    } catch (e) { toastError(e); }
  };

  const move = (n: number) => {
    const s = Math.max(0, Math.min(rows.length - 1, sel + n));
    setSel(s);
    const el = scroller.current;
    if (el) {
      if (s * ROW < el.scrollTop) el.scrollTop = s * ROW;
      else if ((s + 1) * ROW > el.scrollTop + VIEW) el.scrollTop = (s + 1) * ROW - VIEW;
    }
  };

  const key = (e: KeyboardEvent) => {
    const r = rows[sel];
    const k = e.key;
    if (k === "Escape") { e.preventDefault(); e.stopPropagation(); if (mode === "path" && !text) { setMode("recent"); } else closeOverlay(); return; }
    if (k === "ArrowDown" || (e.ctrlKey && k === "n")) { e.preventDefault(); move(1); }
    else if (k === "ArrowUp" || (e.ctrlKey && k === "p")) { e.preventDefault(); move(-1); }
    else if (k === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); toast("TODO: open project in a new window (Wails multi-window not wired yet)", "info"); }
    else if (k === "Enter") { e.preventDefault(); void openRow(r); }
    else if (k === "." && e.metaKey) { e.preventDefault(); setHidden((h) => !h); }
    else if (k === "ArrowRight" || k === "Tab") {
      if (k === "ArrowRight" && mode === "path" && text && input.current && input.current.selectionStart !== text.length) return;
      if (k === "Tab") e.preventDefault();
      if (mode === "recent" && r?.projectId && (k === "Tab" || !text || input.current?.selectionStart === text.length)) {
        e.preventDefault();
        const pr = projects.value.find((p) => p.id === r.projectId);
        if (pr) enterPath(pr.path);
      } else if (mode === "path" && r?.entry) { e.preventDefault(); enterPath(r.entry.path); }
    } else if (k === "ArrowLeft" && mode === "path" && (text === "" || input.current?.selectionStart === 0)) {
      e.preventDefault();
      if (dir === "/" || dir === "") { setMode("recent"); setText(""); } else enterPath(parentOf(dir));
    }
  };

  const shown = rows.slice(start, end);
  return (
    <>
      <div class="fixed inset-0 z-40" onMouseDown={closeOverlay} />
      <div class="pop absolute left-2 right-2 top-[44px] z-50 flex flex-col overflow-hidden rounded-xl bg-panel2 shadow-2xl shadow-black/60" style={{ minWidth: 360 }}>
        {mode === "path" && (
          <div class="flex items-center gap-1 px-4 py-2 text-[11px] text-muted">
            <Icon name="folder" size={11} /> <span class="min-w-0 flex-1 truncate" title={dir}>{dir.replace(home, "~")}</span>
            {hidden && <span class="rounded bg-sel px-1">hidden</span>}
          </div>
        )}
        <input ref={input} class="h-10 bg-transparent px-4 outline-none" placeholder={mode === "recent" ? "Search projects, or type / ~ . to browse" : "Filter, or type a path like ~/code/"} value={text} onInput={(e) => onInput(e.currentTarget.value)} onKeyDown={key} />
        <div ref={scroller} class="overflow-y-auto" style={{ maxHeight: VIEW }} onScroll={(e) => setTop(e.currentTarget.scrollTop)}>
          <div style={{ height: rows.length * ROW, position: "relative" }}>
            {shown.map((r, j) => {
              const i = start + j;
              const b = badges[r.key];
              return (
                <div key={r.key} onMouseMove={() => setSel(i)} onClick={() => (mode === "path" && r.entry ? enterPath(r.entry.path) : openRow(r))} onDblClick={() => openRow(r)}
                  class={`absolute inset-x-1.5 flex cursor-default items-center gap-2.5 rounded-lg px-3 ${i === sel ? "bg-accent/20" : ""}`} style={{ top: i * ROW, height: ROW }}>
                  <Icon name={r.entry?.isRepo ? "branch" : "folder"} size={13} class={r.entry?.isRepo ? "text-accent" : "text-muted"} />
                  <span class="min-w-0 truncate"><Highlight text={r.label} positions={r.positions} /></span>
                  {r.sub && <span class="min-w-0 flex-1 truncate text-[11px] text-faint">{r.sub}</span>}
                  <span class="flex-1" />
                  {b && <span class="shrink-0 rounded px-1.5 text-[10px] text-muted">{b.branch}{b.worktrees > 1 ? ` · ${b.worktrees} wt` : ""}</span>}
                </div>
              );
            })}
          </div>
          {rows.length === 0 && <div class="px-3 py-3 text-muted">{mode === "recent" ? "No projects. Type / or ~ to browse." : "Empty"}</div>}
        </div>
        <div class="flex gap-3 px-4 py-2 text-[10.5px] text-faint">
          <span>⏎ open</span><span>→ enter</span><span>← up</span><span>⌘. hidden</span><span>⌘⏎ new window</span>
        </div>
      </div>
    </>
  );
}
