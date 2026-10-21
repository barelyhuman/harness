import { IconArrowDown, IconArrowUp, IconX } from "@tabler/icons-preact";
import { useEffect, useRef, useState } from "preact/hooks";
import * as T from "../../lib/bindings/github.com/sid/harness/internal/app/termservice";
import { setContext } from "../../lib/commands";
import { ensureOpened, fitEntry, getPtyEntry, type Entry } from "./terms";
import { getMirrorEntry } from "./mirror";
import { findFor, showToast } from "./state";

function FindBar({ e }: { e: Entry }) {
  const ref = useRef<HTMLInputElement>(null);
  const [res, setRes] = useState<{ i: number; n: number } | null>(null);
  useEffect(() => {
    ref.current?.focus();
    ref.current?.select();
    const d = e.search.onDidChangeResults((r) => setRes({ i: r.resultIndex, n: r.resultCount }));
    return () => { d.dispose(); e.search.clearDecorations(); };
  }, [e]);
  const opts = { incremental: true, decorations: { matchBackground: "#5a4a1a", activeMatchBackground: "#8a6d1f", matchOverviewRuler: "#8a6d1f", activeMatchColorOverviewRuler: "#e5c07b" } };
  const close = () => { findFor.value = ""; e.term.focus(); };
  return (
    <div class="absolute top-2 right-3 z-20 flex items-center gap-1 rounded-md bg-panel2 px-2.5 py-1.5 shadow-lg text-[12px]">
      <input ref={ref} class="w-44 bg-transparent outline-none text-neutral-200 placeholder:text-neutral-500" placeholder="Find"
        onInput={(ev) => e.search.findNext((ev.target as HTMLInputElement).value, opts)}
        onKeyDown={(ev) => {
          const v = (ev.target as HTMLInputElement).value;
          if (ev.key === "Escape") { ev.preventDefault(); close(); }
          else if (ev.key === "Enter") { ev.preventDefault(); ev.shiftKey ? e.search.findPrevious(v, opts) : e.search.findNext(v, { ...opts, incremental: false }); }
          else if (ev.metaKey && ev.key === "f") { ev.preventDefault(); (ev.target as HTMLInputElement).select(); }
        }} />
      <span class="text-neutral-500 tabular-nums min-w-8 text-right">{res ? (res.n ? `${res.i + 1}/${res.n}` : "0") : ""}</span>
      <button class="icon-btn-sm text-neutral-400 hover:bg-hover hover:text-white" onClick={() => e.search.findPrevious(ref.current?.value ?? "", opts)}><IconArrowUp size={14} /></button>
      <button class="icon-btn-sm text-neutral-400 hover:bg-hover hover:text-white" onClick={() => e.search.findNext(ref.current?.value ?? "", { ...opts, incremental: false })}><IconArrowDown size={14} /></button>
      <button class="icon-btn-sm text-neutral-400 hover:bg-hover hover:text-white" onClick={close}><IconX size={14} /></button>
    </div>
  );
}

function Menu({ x, y, items, onClose }: { x: number; y: number; items: [string, () => void][]; onClose: () => void }) {
  useEffect(() => {
    const h = () => onClose();
    window.addEventListener("mousedown", h);
    return () => window.removeEventListener("mousedown", h);
  }, []);
  return (
    <div class="menu fixed z-50 min-w-44 bg-panel2 shadow-xl text-[12px]" style={{ left: x, top: y }} onMouseDown={(e) => e.stopPropagation()}>
      {items.map(([l, fn]) => l === "-" ? <div class="menu-sep" /> : (
        <div class="menu-item text-neutral-200 hover:bg-white/10 cursor-default" onClick={() => { onClose(); fn(); }}>{l}</div>
      ))}
    </div>
  );
}

export function TerminalPane(p: { scopeKey: string; paneId?: string; ptyId?: string; mirror?: boolean; focused: boolean; multi: boolean; maximized?: boolean; panelRef: { current: HTMLElement | null } }) {
  const host = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const e = p.mirror ? getMirrorEntry(p.scopeKey) : getPtyEntry(p.ptyId!, p.scopeKey);
  e.paneId = p.paneId ?? "";

  useEffect(() => {
    const h = host.current!;
    const fresh = Date.now() - e.createdAt < 2000;
    h.appendChild(e.el);
    ensureOpened(e);
    fitEntry(e);
    e.term.refresh(0, e.term.rows - 1);
    if (!e.readOnly && e.ch) e.ch.sendJSON({ type: "resize", cols: e.term.cols, rows: e.term.rows });
    const ta = e.term.textarea;
    const on = () => setContext("terminalFocus", true);
    const off = () => setContext("terminalFocus", false);
    ta?.addEventListener("focus", on);
    ta?.addEventListener("blur", off);
    const ae = document.activeElement;
    if (p.focused && (fresh || !ae || ae === document.body || p.panelRef.current?.contains(ae))) e.term.focus();
    let t: ReturnType<typeof setTimeout> | undefined;
    const ro = new ResizeObserver(() => { clearTimeout(t); t = setTimeout(() => fitEntry(e), 40); });
    ro.observe(h);
    return () => {
      ro.disconnect();
      clearTimeout(t);
      ta?.removeEventListener("focus", on);
      ta?.removeEventListener("blur", off);
      if (document.activeElement === ta) off();
    };
  }, [e]);

  useEffect(() => { if (p.focused) e.term.focus(); }, [p.focused]);

  const items: [string, () => void][] = [
    ["Copy", () => { const s = e.term.getSelection(); if (s) void navigator.clipboard.writeText(s); }],
    ["Paste", () => { if (!e.readOnly) void navigator.clipboard.readText().then((t) => e.term.paste(t)); }],
    ["Clear", () => e.term.clear()],
    ["Find", () => (findFor.value = e.key)],
    ["-", () => {}],
    ...(!p.mirror && p.paneId ? ([
      ["Split Right", () => void T.Split(p.scopeKey, p.paneId!, "row", "")],
      ["Split Down", () => void T.Split(p.scopeKey, p.paneId!, "col", "")],
      [p.maximized ? "Restore" : "Maximize", () => void T.ToggleMaximize(p.scopeKey)],
      ["Pop out", () => showToast("Pop out: TODO (multi-window comes later)")],
      ["-", () => {}],
      ["Close", () => void T.ClosePane(p.scopeKey, p.paneId!)],
    ] as [string, () => void][]) : []),
  ];

  return (
    <div class="relative h-full w-full min-h-0 min-w-0 transition-opacity duration-150" style={{ opacity: p.multi && !p.focused ? 0.72 : 1, background: "#141414" }}
      onMouseDown={() => { if (!p.focused && p.paneId) void T.FocusPane(p.scopeKey, p.paneId); }}
      onContextMenu={(ev) => { ev.preventDefault(); setMenu({ x: ev.clientX, y: ev.clientY }); }}>
      <div ref={host} class="absolute inset-0 pl-2 pt-1" />
      {p.multi && p.focused && <div class="pointer-events-none absolute inset-0 z-10 border border-accent/50" />}
      {findFor.value === e.key && <FindBar e={e} />}
      {menu && <Menu x={menu.x} y={menu.y} items={items} onClose={() => setMenu(null)} />}
    </div>
  );
}
