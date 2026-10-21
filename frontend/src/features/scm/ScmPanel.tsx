import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { Status, Refresh, StageFiles, UnstageFiles, DiscardFiles, Commit } from "../../lib/bindings/github.com/sid/harness/internal/app/gitservice";
import type { SCMGroup, SCMRow, SCMState } from "../../lib/bindings/github.com/sid/harness/internal/app/models";
import { openTab, diffTabId } from "../../state/tabs";
import { openFile } from "../../state/editor";
import { registerCommand } from "../../lib/commands";
import { CLASS_COLOR, errInfo, fileIcon, joinPath, subscribeJSON, type ErrInfo } from "./util";
import * as I from "./icons";

type Sel = { g: string; path: string };
const key = (g: string, p: string) => g + "\0" + p;
type Dialog = { message: string; detail?: string; ok: string; run: () => void };
type Menu = { x: number; y: number; g: string; row: SCMRow } | { x: number; y: number; g: "more" };

// Handlers of the most recently mounted panel, driven by registered commands.
let active: { commit(): void; stageAll(): void; refresh(): void; focus(): void } | null = null;
let cmdsRegistered = false;
function ensureCommands() {
  if (cmdsRegistered) return;
  cmdsRegistered = true;
  registerCommand({ id: "scm.commit", title: "Git: Commit", run: () => active?.commit() });
  registerCommand({ id: "scm.stageAll", title: "Git: Stage All Changes", run: () => active?.stageAll() });
  registerCommand({ id: "scm.refresh", title: "Git: Refresh", run: () => active?.refresh() });
  registerCommand({ id: "scm.focus", title: "Source Control: Focus", keys: ["ctrl+shift+g"], run: () => active?.focus() });
}

const msgMemory = new Map<string, string>();

function ActionBtn(p: { title: string; onClick: (e: MouseEvent) => void; children: any; class?: string }) {
  return (
    <button
      type="button"
      title={p.title}
      aria-label={p.title}
      class={"flex h-[22px] w-[22px] shrink-0 cursor-pointer items-center justify-center rounded-[5px] border-0 bg-transparent p-0 text-text hover:bg-hover " + (p.class ?? "")}
      onClick={(e) => {
        e.stopPropagation();
        p.onClick(e);
      }}
      onDblClick={(e) => e.stopPropagation()}
    >
      {p.children}
    </button>
  );
}

export function ScmPanel({ scopeKey, root }: { scopeKey: string; root: string }) {
  const [state, setState] = useState<SCMState | null>(null);
  const [err, setErr] = useState<ErrInfo | null>(null);
  const [msg, setMsgRaw] = useState(msgMemory.get(scopeKey) ?? "");
  const [msgErr, setMsgErr] = useState("");
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [tree, setTree] = useState(false);
  const [sel, setSel] = useState<Sel[]>([]);
  const [cursor, setCursor] = useState<Sel | null>(null);
  const [menu, setMenu] = useState<Menu | null>(null);
  const [dropdown, setDropdown] = useState(false);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [busy, setBusy] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const taRef = useRef<HTMLTextAreaElement>(null);
  const stateRef = useRef<SCMState | null>(null);
  stateRef.current = state;

  const setMsg = (v: string) => {
    msgMemory.set(scopeKey, v);
    setMsgRaw(v);
    if (msgErr) setMsgErr("");
  };
  const fail = (e: any) => setErr(errInfo(e));

  useEffect(() => {
    let alive = true;
    const off = subscribeJSON<SCMState>("scm/" + scopeKey, (s) => alive && setState(s));
    Status(root).then((s) => alive && !stateRef.current && setState(s), fail);
    Refresh(scopeKey);
    return () => {
      alive = false;
      off();
    };
  }, [scopeKey, root]);

  const groups: SCMGroup[] = state?.groups ?? [];
  const branch = state?.branch || "HEAD";

  const allRows = useMemo(() => groups.flatMap((g) => (g.rows ?? []).map((r) => ({ g: g.id, r }))), [state]);
  const rowsOf = (id: string) => groups.find((g) => g.id === id)?.rows ?? [];

  // ---- actions ------------------------------------------------------------
  const run = async (fn: () => Promise<unknown>) => {
    setErr(null);
    try {
      await fn();
    } catch (e) {
      fail(e);
    }
  };
  const stage = (paths: string[]) => run(() => StageFiles(root, paths));
  const unstage = (paths: string[]) => run(() => UnstageFiles(root, paths));
  const discard = (items: { g: string; r: SCMRow }[]) => {
    const untracked = items.every((i) => i.r.code === "U" || i.r.badge === "U");
    const one = items.length === 1;
    const name = items[0].r.name;
    setDialog({
      message: untracked
        ? one ? `Are you sure you want to DELETE '${name}'?` : `Are you sure you want to DELETE ${items.length} files?`
        : one ? `Are you sure you want to discard changes in '${name}'?` : `Are you sure you want to discard changes in ${items.length} files?`,
      detail: untracked ? "This action is irreversible!" : "This is IRREVERSIBLE! Your changes will be lost.",
      ok: untracked ? "Delete File" : "Discard Changes",
      run: () => run(() => DiscardFiles(root, items.map((i) => i.r.path))),
    });
  };
  const discardGroup = (g: SCMGroup) => {
    const rows = g.rows ?? [];
    setDialog({
      message: `Are you sure you want to discard ALL changes in ${rows.length} files?`,
      detail: "This is IRREVERSIBLE! Your changes will be lost.",
      ok: "Discard All Changes",
      run: () => run(() => DiscardFiles(root, rows.map((r) => r.path))),
    });
  };
  const stageAll = () => stage([...rowsOf("merge"), ...rowsOf("changes")].map((r) => r.path));
  const unstageAll = () => unstage(rowsOf("staged").map((r) => r.path));
  const discardAll = () => {
    const rows = [...rowsOf("merge"), ...rowsOf("changes")];
    if (rows.length) discardGroup({ id: "changes", label: "", count: rows.length, rows });
  };
  const openDiff = (g: string, r: SCMRow, pin = false) => {
    const staged = g === "staged";
    openTab(scopeKey, { id: diffTabId(r.path, staged), kind: "diff", path: r.path, staged });
    void pin; // tabs.ts has no preview concept for diff tabs: every diff tab is already pinned.
  };
  const openFileRow = (r: SCMRow) => openFile(scopeKey, joinPath(root, r.path));

  const doCommit = async (amend = false, thenPR = false) => {
    setErr(null);
    const s = stateRef.current;
    const text = msg.trim();
    if (!text && !amend) {
      setMsgErr("Please provide a commit message.");
      taRef.current?.focus();
      return;
    }
    const staged = s?.groups?.find((g) => g.id === "staged")?.rows?.length ?? 0;
    const go = async (stageFirst: boolean) => {
      setBusy(true);
      try {
        if (stageFirst) await StageFiles(root, [...rowsOf("merge"), ...rowsOf("changes")].map((r) => r.path));
        await Commit(root, text, amend);
        setMsg("");
        if (thenPR) window.dispatchEvent(new CustomEvent("harness:raise-pr", { detail: { scopeKey } }));
      } catch (e) {
        fail(e);
      } finally {
        setBusy(false);
      }
    };
    if (!staged && !amend) {
      const changes = rowsOf("merge").length + rowsOf("changes").length;
      if (!changes) return setErr({ code: "git", message: "There are no changes to commit." });
      setDialog({
        message: "There are no staged changes to commit.",
        detail: "Would you like to stage all changes and commit them directly?",
        ok: "Yes",
        run: () => void go(true),
      });
      return;
    }
    await go(false);
  };

  // latest-closure handles for commands
  const h = useRef<any>({});
  h.current = { commit: () => doCommit(), stageAll, refresh: () => Refresh(scopeKey), focus: () => taRef.current?.focus() };
  useEffect(() => {
    ensureCommands();
    const me = { commit: () => h.current.commit(), stageAll: () => h.current.stageAll(), refresh: () => h.current.refresh(), focus: () => h.current.focus() };
    active = me;
    return () => {
      if (active === me) active = null;
    };
  }, []);

  // ---- selection ----------------------------------------------------------
  const isSel = (g: string, p: string) => sel.some((s) => s.g === g && s.path === p);
  const clickRow = (e: MouseEvent, g: string, r: SCMRow) => {
    const me = { g, path: r.path };
    setCursor(me);
    if (e.metaKey || e.ctrlKey) {
      setSel(isSel(g, r.path) ? sel.filter((s) => !(s.g === g && s.path === r.path)) : [...sel, me]);
      return;
    }
    if (e.shiftKey && cursor) {
      const flat = visibleFlat();
      const a = flat.findIndex((x) => x.g === cursor.g && x.r.path === cursor.path);
      const b = flat.findIndex((x) => x.g === g && x.r.path === r.path);
      if (a >= 0 && b >= 0) {
        const [lo, hi] = a < b ? [a, b] : [b, a];
        setSel(flat.slice(lo, hi + 1).map((x) => ({ g: x.g, path: x.r.path })));
        return;
      }
    }
    setSel([me]);
    openDiff(g, r);
  };
  const visibleFlat = () => allRows.filter((x) => !collapsed[x.g]);
  const targets = (g: string, r: SCMRow) => {
    if (isSel(g, r.path) && sel.length > 1) return allRows.filter((x) => isSel(x.g, x.r.path));
    return [{ g, r }];
  };

  const onKey = (e: KeyboardEvent) => {
    if ((e.target as HTMLElement).tagName === "TEXTAREA") return;
    const flat = visibleFlat();
    if (!flat.length) return;
    let i = cursor ? flat.findIndex((x) => x.g === cursor.g && x.r.path === cursor.path) : -1;
    const move = (d: number) => {
      e.preventDefault();
      const n = flat[Math.max(0, Math.min(flat.length - 1, i + d))];
      const c = { g: n.g, path: n.r.path };
      setCursor(c);
      if (e.shiftKey && cursor) {
        const a = flat.findIndex((x) => x.g === cursor.g && x.r.path === cursor.path);
        const b = flat.indexOf(n);
        const [lo, hi] = a < b ? [a, b] : [b, a];
        setSel(flat.slice(lo, hi + 1).map((x) => ({ g: x.g, path: x.r.path })));
      } else setSel([c]);
    };
    if (e.key === "ArrowDown") move(1);
    else if (e.key === "ArrowUp") move(-1);
    else if (i >= 0 && e.key === "Enter") {
      e.preventDefault();
      openDiff(flat[i].g, flat[i].r);
    } else if (i >= 0 && e.key === " ") {
      e.preventDefault();
      const items = sel.length > 1 ? allRows.filter((x) => isSel(x.g, x.r.path)) : [flat[i]];
      const st = items.filter((x) => x.g === "staged").map((x) => x.r.path);
      const un = items.filter((x) => x.g !== "staged").map((x) => x.r.path);
      if (un.length) stage(un);
      if (st.length) unstage(st);
    }
  };

  useEffect(() => {
    if (!menu && !dropdown) return;
    const close = () => {
      setMenu(null);
      setDropdown(false);
    };
    window.addEventListener("mousedown", close);
    return () => window.removeEventListener("mousedown", close);
  }, [menu, dropdown]);

  // ---- rendering ----------------------------------------------------------
  const renderRow = (g: string, r: SCMRow, indent: number, showDir: boolean) => {
    const [letter, color] = fileIcon(r.name);
    const selected = isSel(g, r.path);
    const isCursor = cursor?.g === g && cursor.path === r.path;
    const staged = g === "staged";
    return (
      <div
        key={key(g, r.path)}
        role="treeitem"
        aria-selected={selected}
        title={r.path + (r.origPath ? ` (from ${r.origPath})` : "") + " • " + r.title}
        class={"group flex h-[22px] cursor-pointer items-center pr-[8px] text-[13px] leading-[22px] " + (selected ? "bg-sel text-white" : "hover:bg-hover") + (isCursor ? " outline outline-1 -outline-offset-1 outline-[#007fd4]" : "")}
        style={{ paddingLeft: 8 + indent + "px" }}
        onClick={(e) => clickRow(e, g, r)}
        onDblClick={() => openDiff(g, r, true)}
        onContextMenu={(e) => {
          e.preventDefault();
          if (!isSel(g, r.path)) setSel([{ g, path: r.path }]);
          setCursor({ g, path: r.path });
          setMenu({ x: e.clientX, y: e.clientY, g, row: r });
        }}
      >
        <span class="mr-[6px] inline-flex h-4 w-4 shrink-0 items-center justify-center text-[9px] font-bold" style={{ color }}>{letter}</span>
        <span class="shrink-0 truncate" style={{ color: r.class === "scm-deleted" ? CLASS_COLOR[r.class] : undefined, textDecoration: r.class === "scm-deleted" ? "line-through" : undefined }}>{r.name}</span>
        {showDir && r.dir && (
          <span class="ml-[6px] min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap text-[0.9em] opacity-60" style={{ direction: "rtl", textAlign: "left" }}>
            <bdi>{r.dir}</bdi>
          </span>
        )}
        {!(showDir && r.dir) && <span class="flex-1" />}
        <span class="ml-auto hidden shrink-0 items-center group-hover:flex">
          <ActionBtn title="Open File" onClick={() => openFileRow(r)}><I.IconOpenFile /></ActionBtn>
          {!staged && <ActionBtn title={r.code === "U" ? "Delete File" : "Discard Changes"} onClick={() => discard(targets(g, r))}><I.IconDiscard /></ActionBtn>}
          {staged ? (
            <ActionBtn title="Unstage Changes" onClick={() => unstage(targets(g, r).map((x) => x.r.path))}><I.IconRemove /></ActionBtn>
          ) : (
            <ActionBtn title="Stage Changes" onClick={() => stage(targets(g, r).map((x) => x.r.path))}><I.IconAdd /></ActionBtn>
          )}
        </span>
        <span class="ml-[3px] w-[14px] shrink-0 text-center text-[12px] font-medium group-hover:ml-0" style={{ color: CLASS_COLOR[r.class] }} title={r.title}>
          {r.badge}
        </span>
      </div>
    );
  };

  const renderGroup = (g: SCMGroup) => {
    const rows = g.rows ?? [];
    const open = !collapsed[g.id];
    const isStaged = g.id === "staged";
    const body = () => {
      if (!tree) return rows.map((r) => renderRow(g.id, r, 0, true));
      const byDir = new Map<string, SCMRow[]>();
      for (const r of rows) byDir.set(r.dir, [...(byDir.get(r.dir) ?? []), r]);
      return [...byDir.entries()].sort(([a], [b]) => a.localeCompare(b)).flatMap(([dir, rs]) => {
        const depth = dir ? dir.split("/").length : 0;
        const out: any[] = [];
        if (dir) out.push(<div key={g.id + "d" + dir} class="flex h-[22px] items-center text-[13px] opacity-80" style={{ paddingLeft: 8 + 0 + "px" }}><I.IconChevron open /> <span class="ml-1 truncate">{dir}</span></div>);
        void depth;
        return out.concat(rs.map((r) => renderRow(g.id, r, dir ? 16 : 0, false)));
      });
    };
    return (
      <div key={g.id}>
        <div
          class="group sticky top-0 z-[1] flex h-[22px] cursor-pointer items-center bg-panel pr-[8px] text-[11px] font-bold uppercase"
          onClick={() => setCollapsed({ ...collapsed, [g.id]: open })}
        >
          <span class="flex w-4 shrink-0 justify-center"><I.IconChevron open={open} /></span>
          <span class="ml-[2px] min-w-0 flex-1 truncate">{g.label}</span>
          <span class="hidden items-center group-hover:flex">
            {!isStaged && <ActionBtn title="Discard All Changes" onClick={() => discardGroup(g)}><I.IconDiscard /></ActionBtn>}
            {isStaged ? (
              <ActionBtn title="Unstage All Changes" onClick={() => unstage(rows.map((r) => r.path))}><I.IconRemove /></ActionBtn>
            ) : (
              <ActionBtn title="Stage All Changes" onClick={() => stage(rows.map((r) => r.path))}><I.IconAdd /></ActionBtn>
            )}
          </span>
          <span class="ml-[6px] inline-flex h-[18px] min-w-[18px] items-center justify-center rounded-[11px] bg-[#4d4d4d] px-[5px] text-[11px] font-normal text-white">{g.count}</span>
        </div>
        {open && <div role="group">{body()}</div>}
      </div>
    );
  };

  const MenuItem = (p: { label: string; onClick: () => void; disabled?: boolean }) => (
    <div
      class={"mx-[4px] cursor-pointer rounded-[4px] px-[20px] py-[3px] text-[13px] " + (p.disabled ? "opacity-40" : "hover:bg-sel hover:text-white")}
      onMouseDown={(e) => e.stopPropagation()}
      onClick={() => {
        if (p.disabled) return;
        setMenu(null);
        setDropdown(false);
        p.onClick();
      }}
    >
      {p.label}
    </div>
  );
  const Sep = () => <div class="mx-[8px] my-[4px] h-px bg-[#454545]" />;
  const menuBox = "fixed z-50 min-w-[170px] rounded-[5px] border border-border bg-raise py-[4px] shadow-[0_2px_8px_#000a]";

  const hasChanges = allRows.length > 0;
  const isMac = true;
  const hint = `Message (${isMac ? "⌘⏎" : "Ctrl+Enter"} to commit on "${branch}")`;

  return (
    <div
      ref={rootRef}
      data-ctx="scm"
      tabIndex={-1}
      class="relative flex h-full min-h-0 flex-col bg-panel text-[13px] text-text outline-none select-none"
      onKeyDown={onKey}
    >
      {/* header */}
      <div class="flex h-[35px] shrink-0 items-center pl-[20px] pr-[8px]">
        <span class="text-[11px] font-normal uppercase">Source Control</span>
        <span class="flex-1" />
        <ActionBtn title="Commit" onClick={() => doCommit()}><I.IconCheck /></ActionBtn>
        <ActionBtn title="Refresh" onClick={() => Refresh(scopeKey)}><I.IconRefresh /></ActionBtn>
        <ActionBtn title={tree ? "View as List" : "View as Tree"} onClick={() => setTree(!tree)}>{tree ? <I.IconList /> : <I.IconTree />}</ActionBtn>
        <ActionBtn title="More Actions..." onClick={(e) => { const r = (e.currentTarget as HTMLElement).getBoundingClientRect(); setMenu({ x: r.left, y: r.bottom, g: "more" }); }}><I.IconEllipsis /></ActionBtn>
      </div>
      <div class="flex h-[22px] shrink-0 items-center gap-[4px] px-[20px] text-[12px] opacity-80">
        <I.IconBranch />
        <span class="truncate">{branch}</span>
        {!!state && (state.ahead > 0 || state.behind > 0) && (
          <span class="ml-[6px] inline-flex items-center gap-[4px]" title={`${state.behind} behind, ${state.ahead} ahead of ${state.upstream}`}>
            {state.behind > 0 && <span class="inline-flex items-center"><I.IconDown />{state.behind}</span>}
            {state.ahead > 0 && <span class="inline-flex items-center"><I.IconUp />{state.ahead}</span>}
          </span>
        )}
      </div>

      {err && (
        <div class="mx-[12px] mb-[6px] flex shrink-0 items-start gap-[6px] border border-[#be1100] bg-[#5a1d1d] px-[8px] py-[4px] text-[12px] text-white select-text">
          <span class="min-w-0 flex-1 break-words">
            {err.message}
            {err.files?.length ? ` (${err.files.slice(0, 3).join(", ")}${err.files.length > 3 ? ", …" : ""})` : ""}
          </span>
          <button type="button" class="cursor-pointer border-0 bg-transparent p-0 text-white" title="Dismiss" onClick={() => setErr(null)}><I.IconClose /></button>
        </div>
      )}

      {/* commit box */}
      <div class="shrink-0 px-[12px] pb-[8px] pt-[2px]">
        <textarea
          ref={taRef}
          rows={1}
          value={msg}
          placeholder={hint}
          spellcheck={false}
          class={"block max-h-[200px] min-h-[36px] w-full resize-none rounded-[2px] border bg-raise px-[8px] py-[6px] text-[13px] leading-[18px] text-text outline-none placeholder:text-muted focus:border-accent " + (msgErr ? "border-[#be1100]" : "border-transparent")}
          onInput={(e) => {
            const t = e.currentTarget;
            setMsg(t.value);
            t.style.height = "auto";
            t.style.height = Math.min(t.scrollHeight, 200) + "px";
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              doCommit();
            }
          }}
        />
        {msgErr && <div class="mt-[2px] border border-[#be1100] bg-[#5a1d1d] px-[6px] py-[2px] text-[12px] text-white">{msgErr}</div>}
        <div class="relative mt-[8px] flex h-[26px]">
          <button
            type="button"
            disabled={busy}
            class="flex h-full flex-1 cursor-pointer items-center justify-center gap-[4px] rounded-l-[2px] border-0 bg-accent text-[13px] text-white hover:bg-accent disabled:opacity-60"
            onClick={() => doCommit()}
          >
            <I.IconCheck /> Commit
          </button>
          <button
            type="button"
            class="ml-px flex h-full w-[26px] cursor-pointer items-center justify-center rounded-r-[2px] border-0 bg-accent text-white hover:bg-accent"
            title="More Actions..."
            onMouseDown={(e) => e.stopPropagation()}
            onClick={() => setDropdown(!dropdown)}
          >
            <I.IconChevron open />
          </button>
          {dropdown && (
            <div class="absolute right-0 top-[28px] z-50 min-w-[190px] rounded-[5px] border border-border bg-raise py-[4px] shadow-[0_2px_8px_#000a]">
              <MenuItem label="Commit" onClick={() => doCommit()} />
              <MenuItem label="Commit (Amend)" onClick={() => doCommit(true)} />
              <Sep />
              <MenuItem label="Commit & Raise PR" onClick={() => doCommit(false, true)} />
            </div>
          )}
        </div>
      </div>

      {/* groups */}
      <div role="tree" class="min-h-0 flex-1 overflow-y-auto" onMouseDown={() => rootRef.current?.focus({ preventScroll: true } as any)}>
        {groups.map(renderGroup)}
        {state && !hasChanges && <div class="px-[20px] py-[8px] text-[12px] opacity-60">No changes</div>}
      </div>

      {menu && (
        <div class={menuBox} style={{ left: Math.min(menu.x, window.innerWidth - 190), top: Math.min(menu.y, window.innerHeight - 200) }} onMouseDown={(e) => e.stopPropagation()}>
          {menu.g === "more" ? (
            <>
              <MenuItem label="Stage All" onClick={stageAll} disabled={!rowsOf("changes").length && !rowsOf("merge").length} />
              <MenuItem label="Unstage All" onClick={unstageAll} disabled={!rowsOf("staged").length} />
              <Sep />
              <MenuItem label="Discard All" onClick={discardAll} disabled={!rowsOf("changes").length && !rowsOf("merge").length} />
            </>
          ) : (
            (() => {
              const m = menu as Extract<Menu, { row: SCMRow }>;
              const items = targets(m.g, m.row);
              const staged = m.g === "staged";
              return (
                <>
                  <MenuItem label="Open Changes" onClick={() => items.forEach((x) => openDiff(x.g, x.r))} />
                  <MenuItem label="Open File" onClick={() => items.forEach((x) => openFileRow(x.r))} />
                  <Sep />
                  {!staged && <MenuItem label="Stage Changes" onClick={() => stage(items.map((x) => x.r.path))} />}
                  {staged && <MenuItem label="Unstage Changes" onClick={() => unstage(items.map((x) => x.r.path))} />}
                  {!staged && <MenuItem label={m.row.code === "U" ? "Delete File" : "Discard Changes"} onClick={() => discard(items)} />}
                  <Sep />
                  <MenuItem label="Copy Path" onClick={() => navigator.clipboard?.writeText(joinPath(root, m.row.path))} />
                  <MenuItem label="Copy Relative Path" onClick={() => navigator.clipboard?.writeText(m.row.path)} />
                </>
              );
            })()
          )}
        </div>
      )}

      {dialog && (
        <div class="absolute inset-0 z-[60] flex items-start justify-center bg-black/40 pt-[60px]" onMouseDown={(e) => e.stopPropagation()}>
          <div class="mx-[12px] w-full max-w-[320px] rounded-[5px] border border-border bg-raise p-[14px] shadow-[0_4px_16px_#000c]">
            <div class="text-[13px] font-semibold text-white">{dialog.message}</div>
            {dialog.detail && <div class="mt-[6px] text-[12px] opacity-80">{dialog.detail}</div>}
            <div class="mt-[14px] flex justify-end gap-[8px]">
              <button type="button" class="h-[26px] cursor-pointer rounded-[2px] border-0 bg-sel px-[12px] text-[13px] text-white hover:bg-[#45494e]" onClick={() => setDialog(null)}>Cancel</button>
              <button
                type="button"
                class="h-[26px] cursor-pointer rounded-[2px] border-0 bg-accent px-[12px] text-[13px] text-white hover:bg-accent"
                ref={(el) => el?.focus()}
                onClick={() => {
                  const d = dialog;
                  setDialog(null);
                  d.run();
                }}
              >
                {dialog.ok}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
