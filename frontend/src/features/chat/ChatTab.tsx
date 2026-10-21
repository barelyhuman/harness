import { IconArrowDown, IconChevronRight, IconCircle, IconCircleCheck, IconCircleHalf2 } from "@tabler/icons-preact";
// Chat tab: transcript (Go-built blocks), pinned todos, composer. Spec 11.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import { activeScope } from "../../state/scope";
import { transcriptFor, type Block } from "../../state/transcript";
import { Composer } from "../composer/Composer";
import { isBusy, sessionRow } from "../composer/actions";
import { ReviewBar } from "../review/ReviewBar";
import { BlockView } from "./blocks";
import { isBashPerm, respond } from "./cards";
import { ChatCtx } from "./ctx";
import "./styles";

const VIRTUAL_AT = 200;
const EST = 84;
const OVERSCAN = 600;

function flat(list: Block[], out: Block[] = []): Block[] {
  for (const b of list) { out.push(b); if (b.children?.length) flat(b.children, out); }
  return out;
}

function Item(p: { b: Block; ro: ResizeObserver | null }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el || !p.ro) return;
    p.ro.observe(el);
    return () => p.ro!.unobserve(el);
  }, [p.ro]);
  return <div ref={ref} data-id={p.b.id} class="pb-2"><BlockView b={p.b} /></div>;
}

function List(p: { items: Block[]; loaded: boolean }) {
  const { items } = p;
  const virtual = items.length > VIRTUAL_AT;
  const sc = useRef<HTMLDivElement>(null);
  const heights = useRef(new Map<string, number>());
  const stuck = useRef(true);
  const [jump, setJump] = useState(false);
  const [, bump] = useState(0);
  const [view, setView] = useState({ top: 0, h: 600 });
  const raf = useRef(0);

  const toBottom = () => {
    const el = sc.current;
    if (el) el.scrollTop = el.scrollHeight;
  };

  const ro = useMemo(() => {
    if (typeof ResizeObserver === "undefined") return null;
    return new ResizeObserver((entries) => {
      let changed = false;
      for (const en of entries) {
        const id = (en.target as HTMLElement).dataset.id!;
        const h = Math.round((en.target as HTMLElement).offsetHeight);
        if (h && heights.current.get(id) !== h) { heights.current.set(id, h); changed = true; }
      }
      if (changed && virtual) bump((n) => n + 1);
      if (stuck.current) toBottom();
    });
  }, [virtual]);
  useEffect(() => () => ro?.disconnect(), [ro]);

  useEffect(() => {
    const el = sc.current;
    if (!el) return;
    const r = new ResizeObserver(() => setView((v) => ({ ...v, h: el.clientHeight })));
    r.observe(el);
    return () => r.disconnect();
  }, []);

  useLayoutEffect(() => { if (stuck.current) toBottom(); }, [items, p.loaded]);

  const onScroll = () => {
    const el = sc.current!;
    const near = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    stuck.current = near;
    setJump(!near);
    if (virtual && !raf.current) raf.current = requestAnimationFrame(() => { raf.current = 0; setView({ top: el.scrollTop, h: el.clientHeight }); });
  };

  let start = 0, end = items.length, padTop = 0, padBottom = 0;
  if (virtual) {
    const offs: number[] = new Array(items.length + 1);
    let acc = 0;
    for (let i = 0; i < items.length; i++) { offs[i] = acc; acc += heights.current.get(items[i].id) ?? EST; }
    offs[items.length] = acc;
    const lo = view.top - OVERSCAN, hi = view.top + view.h + OVERSCAN;
    start = 0;
    while (start < items.length - 1 && offs[start + 1] < lo) start++;
    end = start;
    while (end < items.length && offs[end] < hi) end++;
    padTop = offs[start];
    padBottom = acc - offs[end];
  }

  return (
    <div class="relative min-h-0 flex-1">
      <div ref={sc} class="chat-sel h-full overflow-y-auto overflow-x-hidden px-4 pt-3" onScroll={onScroll}>
        <div class="mx-auto max-w-[820px]" style={virtual ? { paddingTop: padTop, paddingBottom: padBottom } : undefined}>
          {items.slice(start, end).map((b) => <Item key={b.id} b={b} ro={ro} />)}
        </div>
        <div class="h-2" />
      </div>
      {jump && (
        <button class="pop absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full bg-raise px-3 py-1 text-[11.5px] shadow-lg hover:bg-raise" onClick={() => { stuck.current = true; setJump(false); toBottom(); }}>
          <IconArrowDown size={12} class="mr-1 inline" />Jump to latest
        </button>
      )}
    </div>
  );
}

function Todos(p: { todos: NonNullable<Block["todos"]> }) {
  const [open, setOpen] = useState(true);
  const done = p.todos.filter((t) => t.status === "completed").length;
  if (!p.todos.length) return null;
  return (
    <div class="mx-3 mb-1 rounded-md bg-panel2/70 text-[12px]">
      <button class="flex w-full items-center gap-2 px-2 py-1 text-left text-muted hover:text-text" onClick={() => setOpen(!open)}>
        <IconChevronRight size={11} class={`inline-block transition-transform ${open ? "rotate-90" : ""}`} />
        <span>Todos</span><span class="text-faint">{done}/{p.todos.length}</span>
        <span class="h-[3px] max-w-[120px] flex-1 overflow-hidden rounded bg-border"><span class="block h-full bg-run" style={{ width: `${(done / p.todos.length) * 100}%` }} /></span>
      </button>
      {open && (
        <ul class="max-h-[140px] overflow-y-auto px-2 pb-1.5">
          {p.todos.map((t, i) => (
            <li key={i} class={`flex gap-1.5 py-px ${t.status === "completed" ? "text-faint line-through" : t.status === "in_progress" ? "text-text" : "text-muted"}`}>
              <span class="w-3 shrink-0 text-center">{t.status === "completed" ? <IconCircleCheck size={12} /> : t.status === "in_progress" ? <IconCircleHalf2 size={12} /> : <IconCircle size={12} />}</span>
              <span>{t.content}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Empty(p: { scopeKey: string }) {
  return (
    <div class="flex h-full flex-col items-center justify-center gap-3 text-center">
      <div class="text-[13px] text-muted">Start an agent in this checkout</div>
      <button class="btn btn-primary" onClick={() => window.dispatchEvent(new CustomEvent("harness:new-agent", { detail: { scopeKey: p.scopeKey } }))}>New agent</button>
    </div>
  );
}

function Chat(p: { sessionId: string; scopeKey: string }) {
  const store = transcriptFor(p.sessionId);
  const blocks = store.blocks.value;
  const loaded = store.loaded.value;
  const all = useMemo(() => flat(blocks), [blocks]);
  const items = useMemo(() => blocks.filter((b) => b.type !== "todo"), [blocks]);
  const todos = useMemo(() => {
    for (let i = all.length - 1; i >= 0; i--) if (all[i].type === "todo") return all[i].todos ?? [];
    return null;
  }, [all]);
  const pend = useMemo(() => {
    for (let i = all.length - 1; i >= 0; i--) {
      const b = all[i];
      if (b.type === "permission" && b.permission?.state === "pending") return b;
    }
    return null;
  }, [all]);
  const root = useRef<HTMLDivElement>(null);
  const pendRef = useRef(pend);
  pendRef.current = pend;

  // Permission keys (⏎ allow/run, ⌘⏎ always, Esc deny/cancel) for the latest pending card.
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const b = pendRef.current;
      const el = root.current;
      if (!b || !el || el.offsetParent === null || e.isComposing) return;
      const t = e.target as HTMLElement;
      const inComposer = !!t.closest?.("[data-ctx=composer]") && t.tagName === "TEXTAREA";
      const editable = t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable;
      if (editable && !(inComposer && !(t as HTMLTextAreaElement).value.trim())) return;
      if (!editable && t !== document.body && !el.contains(t)) return;
      if (sessionRow(p.sessionId) && activeScope.value?.sessionId && activeScope.value.sessionId !== p.sessionId) return;
      const bash = isBashPerm(b);
      let behavior: "allow" | "allow_always" | "deny" | null = null;
      if (e.key === "Enter" && !e.shiftKey && !e.altKey && !e.ctrlKey) behavior = e.metaKey ? (bash ? null : "allow_always") : "allow";
      else if (e.key === "Escape" && !e.metaKey && !e.altKey && !e.ctrlKey) behavior = "deny";
      if (!behavior) return;
      e.preventDefault();
      e.stopPropagation();
      void respond(p.sessionId, b.permission!.reqId, behavior);
    };
    window.addEventListener("keydown", h, true);
    return () => window.removeEventListener("keydown", h, true);
  }, [p.sessionId]);

  const row = sessionRow(p.sessionId);
  const ctx = useMemo(() => ({ sessionId: p.sessionId, scopeKey: p.scopeKey, pendingReq: pend?.permission?.reqId ?? null }), [p.sessionId, p.scopeKey, pend]);

  return (
    <ChatCtx.Provider value={ctx}>
      <div ref={root} class="flex h-full min-h-0 flex-col bg-bg">
        <ReviewBar sessionId={p.sessionId} scopeKey={p.scopeKey} />
        {loaded && items.length === 0 ? (
          <div class="flex flex-1 items-center justify-center text-[12.5px] text-faint">{isBusy(row) ? "Working…" : "Send a message to start."}</div>
        ) : (
          <List items={items} loaded={loaded} />
        )}
        {todos && <Todos todos={todos} />}
        <Composer sessionId={p.sessionId} />
      </div>
    </ChatCtx.Provider>
  );
}

export function ChatTab(p: { sessionId: string | null; scopeKey: string }) {
  if (!p.sessionId) return <Empty scopeKey={p.scopeKey} />;
  return <Chat key={p.sessionId} sessionId={p.sessionId} scopeKey={p.scopeKey} />;
}
