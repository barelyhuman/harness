import { IconDiamond, IconPlus, IconX } from "@tabler/icons-preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ComponentChildren } from "preact";
import { bus } from "../../lib/bus";
import * as T from "../../lib/bindings/github.com/sid/harness/internal/app/termservice";
import { TerminalPane } from "./TerminalPane";
import "./register";
import { reconcile } from "./terms";
import {
  activeTab, activeTerminalScope, agentTabEnabled, agentTabSelected, layouts, leaves, scopeRoots, setScopeFlag, toast,
  type Layout, type Node, type Tab,
} from "./state";

export { mirrorWrite, mirrorCommand, mirrorResult, ensureAgentTerminal } from "./mirror";

const initializing = new Set<string>();

function setLayout(scope: string, l: Layout | null | undefined) {
  if (!l) return;
  layouts.value = { ...layouts.value, [scope]: { ...l, tabs: l.tabs ?? [] } };
  reconcile(scope);
}

async function init(scope: string, root: string) {
  if (initializing.has(scope)) return;
  initializing.add(scope);
  try {
    let l = await T.Layout(scope);
    setLayout(scope, l);
    if (!l?.tabs?.length) {
      l = await T.RestoreLayout(scope, root);
      setLayout(scope, l);
      if (!l?.tabs?.length) {
        await T.NewTab(scope, root);
        setLayout(scope, await T.Layout(scope));
      }
    }
  } catch (err) {
    console.error("terminal init", err);
  } finally {
    initializing.delete(scope);
  }
}

function throttle<A extends unknown[]>(fn: (...a: A) => void, ms: number) {
  let last = 0, t: ReturnType<typeof setTimeout> | undefined, args: A;
  return (...a: A) => {
    args = a;
    const wait = ms - (Date.now() - last);
    if (wait <= 0) { last = Date.now(); fn(...args); }
    else if (!t) t = setTimeout(() => { t = undefined; last = Date.now(); fn(...args); }, wait);
  };
}

type Ctx = { scopeKey: string; tab: Tab; panelRef: { current: HTMLElement | null }; multi: boolean };

function NodeView({ node, path, ctx }: { node: Node; path: number[]; ctx: Ctx }) {
  if (node.type === "leaf") {
    return <TerminalPane scopeKey={ctx.scopeKey} paneId={node.paneId} ptyId={node.ptyId} mirror={node.kind === "agent"} multi={ctx.multi} panelRef={ctx.panelRef} focused={ctx.tab.focused === node.paneId} />;
  }
  return <SplitView node={node} path={path} ctx={ctx} />;
}

function SplitView({ node, path, ctx }: { node: Node; path: number[]; ctx: Ctx }) {
  const row = node.dir === "row";
  const kids = node.children ?? [];
  const base = node.ratios && node.ratios.length === kids.length ? node.ratios : kids.map(() => 1 / kids.length);
  const [override, setOverride] = useState<number[] | null>(null);
  const dragging = useRef(false);
  const box = useRef<HTMLDivElement>(null);
  const ratios = override ?? base;
  const send = useRef(throttle((r: number[]) => void T.SetRatios(ctx.scopeKey, ctx.tab.id, path, r), 50));
  send.current = throttle((r: number[]) => void T.SetRatios(ctx.scopeKey, ctx.tab.id, path, r), 50);
  useEffect(() => { if (!dragging.current) setOverride(null); }, [node.ratios]);

  const startDrag = (i: number) => (ev: PointerEvent) => {
    ev.preventDefault();
    const el = ev.currentTarget as HTMLElement;
    el.setPointerCapture(ev.pointerId);
    dragging.current = true;
    const size = (row ? box.current!.clientWidth : box.current!.clientHeight) || 1;
    const start = row ? ev.clientX : ev.clientY;
    const r0 = [...ratios];
    const total = r0.reduce((a, b) => a + b, 0);
    let cur = r0;
    const move = (m: PointerEvent) => {
      const d = (((row ? m.clientX : m.clientY) - start) / size) * total;
      const min = 0.05 * total;
      const pair = r0[i] + r0[i + 1];
      const a = Math.min(pair - min, Math.max(min, r0[i] + d));
      cur = r0.slice();
      cur[i] = a;
      cur[i + 1] = pair - a;
      setOverride(cur);
      send.current(cur);
    };
    const up = () => {
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
      dragging.current = false;
      void T.SetRatios(ctx.scopeKey, ctx.tab.id, path, cur);
      setTimeout(() => setOverride(null), 250);
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
  };

  const out: ComponentChildren[] = [];
  kids.forEach((c, i) => {
    const key = c.type === "leaf" ? c.paneId : `s${i}`;
    out.push(<div key={key} class="min-h-0 min-w-0 overflow-hidden" style={{ flex: `${ratios[i]} 1 0` }}><NodeView node={c} path={[...path, i]} ctx={ctx} /></div>);
    if (i < kids.length - 1) {
      out.push(
        <div key={`d${i}`} onPointerDown={startDrag(i)} class={`relative shrink-0 z-20 bg-white/10 hover:bg-accent/60 active:bg-accent ${row ? "w-px cursor-col-resize" : "h-px cursor-row-resize"}`}>
          <div class={`absolute ${row ? "-left-1.5 -right-1.5 inset-y-0" : "-top-1.5 -bottom-1.5 inset-x-0"}`} />
        </div>,
      );
    }
  });
  return <div ref={box} class={`flex h-full w-full ${row ? "flex-row" : "flex-col"}`}>{out}</div>;
}

function TabStrip({ scopeKey, root }: { scopeKey: string; root: string }) {
  const l = layouts.value[scopeKey];
  const tabs = l?.tabs ?? [];
  const agentOn = !!agentTabEnabled.value[scopeKey];
  const agentSel = !!agentTabSelected.value[scopeKey];
  const [editing, setEditing] = useState<string | null>(null);
  const [dragFrom, setDragFrom] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);

  const selectGo = (id: string) => { setScopeFlag(agentTabSelected, scopeKey, false); void T.ActivateTab(scopeKey, id); };
  const closeTab = async (t: Tab) => { for (const n of leaves(t.root)) await T.ClosePane(scopeKey, n.paneId!); };
  const cls = (on: boolean) => `group flex items-center gap-2 h-full px-3.5 text-[12px] select-none cursor-default ${on ? "bg-bg text-neutral-100" : "text-neutral-500 hover:text-neutral-300 bg-panel"}`;

  return (
    <div class="flex h-9 shrink-0 items-stretch bg-panel overflow-x-auto">
      {tabs.map((t, i) => {
        const on = !agentSel && (l?.active ?? tabs[0]?.id) === t.id;
        return (
          <div key={t.id} class={cls(on) + (over === i && dragFrom !== null && dragFrom !== i ? " border-l-2 border-l-[#6aa3ff]" : "")} draggable
            onClick={() => selectGo(t.id)}
            onAuxClick={(e) => { if (e.button === 1) void closeTab(t); }}
            onDblClick={() => setEditing(t.id)}
            onDragStart={() => setDragFrom(i)}
            onDragOver={(e) => { e.preventDefault(); setOver(i); }}
            onDragEnd={() => { setDragFrom(null); setOver(null); }}
            onDrop={(e) => { e.preventDefault(); if (dragFrom !== null && dragFrom !== i) void T.MoveTab(scopeKey, dragFrom, i); setDragFrom(null); setOver(null); }}>
            {editing === t.id ? (
              <input autoFocus class="w-24 bg-transparent outline-none border-b border-accent text-neutral-100" defaultValue={t.title}
                onClick={(e) => e.stopPropagation()}
                onBlur={() => setEditing(null)}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  if (e.key === "Enter") { const v = (e.target as HTMLInputElement).value.trim(); if (v) void T.RenameTab(scopeKey, t.id, v); setEditing(null); }
                  else if (e.key === "Escape") setEditing(null);
                }} />
            ) : <span class="truncate max-w-40">{t.title || "shell"}</span>}
            <button class="opacity-0 group-hover:opacity-100 text-neutral-500 hover:text-white leading-none" title="Close tab" onClick={(e) => { e.stopPropagation(); void closeTab(t); }}><IconX size={12} /></button>
          </div>
        );
      })}
      {agentOn && (
        <div class={cls(agentSel)} onClick={() => setScopeFlag(agentTabSelected, scopeKey, true)}>
          <span class="text-[#c792ea]"><IconDiamond size={12} /></span><span>Agent</span>
        </div>
      )}
      <button class="icon-btn-sm self-center mx-1.5 text-neutral-500 hover:bg-hover hover:text-white" title="New terminal" onClick={() => { setScopeFlag(agentTabSelected, scopeKey, false); void T.NewTab(scopeKey, root); }}><IconPlus size={14} /></button>
    </div>
  );
}

export function TerminalPanel({ scopeKey, root }: { scopeKey: string; root: string }) {
  const panelRef = useRef<HTMLDivElement>(null);
  scopeRoots.set(scopeKey, root);

  useEffect(() => {
    activeTerminalScope.value = scopeKey;
    const unsubs: (() => void)[] = [];
    const off = bus.onOpen("termlayout/", (ch) => {
      if (ch.name !== "termlayout/" + scopeKey) return;
      unsubs.push(ch.onJSON<Layout>((l) => setLayout(scopeKey, l)));
    });
    void init(scopeKey, root);
    return () => {
      off();
      unsubs.forEach((u) => u());
      if (activeTerminalScope.value === scopeKey) activeTerminalScope.value = "";
    };
  }, [scopeKey]);

  const l = layouts.value[scopeKey];
  const tab = activeTab(scopeKey);
  const agentSel = !!agentTabSelected.value[scopeKey];

  let body: ComponentChildren;
  if (agentSel) {
    body = <TerminalPane key="agent" scopeKey={scopeKey} mirror focused multi={false} panelRef={panelRef} />;
  } else if (!tab) {
    body = (
      <div class="flex h-full items-center justify-center text-neutral-500 text-[12px]">
        {l ? <button class="btn" onClick={() => void T.NewTab(scopeKey, root)}>New terminal</button> : "Starting terminal…"}
      </div>
    );
  } else {
    const all = leaves(tab.root);
    const multi = all.length > 1;
    const ctx: Ctx = { scopeKey, tab, panelRef, multi };
    if (tab.maximized) {
      const leaf = all.find((n) => n.paneId === tab.focused) ?? all[0];
      body = <TerminalPane key={leaf.paneId} scopeKey={scopeKey} paneId={leaf.paneId} ptyId={leaf.ptyId} mirror={leaf.kind === "agent"} multi={false} maximized focused panelRef={panelRef} />;
    } else {
      body = <NodeView node={tab.root} path={[]} ctx={ctx} />;
    }
  }

  return (
    <div ref={panelRef} data-ctx="terminal" class="relative flex h-full w-full flex-col bg-bg text-neutral-200">
      <TabStrip scopeKey={scopeKey} root={root} />
      <div class="min-h-0 flex-1">{body}</div>
      {toast.value && <div class="absolute bottom-3 left-1/2 -translate-x-1/2 rounded-md bg-raise px-3 py-1.5 text-[12px] shadow-lg">{toast.value}</div>}
    </div>
  );
}
