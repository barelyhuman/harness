// Small shared presentational bits for the shell: icons, highlighted text, resizer, modal frame.
import type { ComponentChildren } from "preact";
import { useEffect, useRef } from "preact/hooks";
import { closeOverlay } from "../../state/ui";

const P: Record<string, string> = {
  chevron: "M6 4l4 4-4 4",
  folder: "M2 4.5A1.5 1.5 0 013.5 3h2.6l1.4 1.5h5A1.5 1.5 0 0114 6v5.5a1.5 1.5 0 01-1.5 1.5h-9A1.5 1.5 0 012 11.5z",
  branch: "M5 3v7m0 0a2 2 0 100 4 2 2 0 000-4zm6-7a2 2 0 100 4 2 2 0 000-4zm0 4c0 3-6 1-6 6",
  terminal: "M2.5 4l4 4-4 4M8 12h5.5",
  plus: "M8 3v10M3 8h10",
  x: "M4 4l8 8M12 4l-8 8",
  file: "M4 2h5l3 3v9H4zM9 2v3h3",
  diff: "M8 3v6M5 6h6M5 13h6",
  chat: "M3 3h10v7H7l-3 3v-3H3z",
  sidebar: "M2.5 3h11v10h-11zM6 3v10",
  right: "M2.5 3h11v10h-11zM10 3v10",
  panel: "M2.5 3h11v10h-11zM2.5 9.5h11",
  search: "M7 3a4 4 0 100 8 4 4 0 000-8zm3 7l3.5 3.5",
  check: "M3.5 8.5l3 3 6-7",
  pr: "M5 3v10M5 3a1.5 1.5 0 100 .01M5 13a1.5 1.5 0 100 .01M11 13V7a2 2 0 00-2-2H8",
  more: "M3.5 8h.01M8 8h.01M12.5 8h.01",
  up: "M8 13V3M4 7l4-4 4 4",
  sparkle: "M8 2l1.3 3.7L13 7l-3.7 1.3L8 12l-1.3-3.7L3 7l3.7-1.3z",
  cursor: "M3 2l10 5-4.2 1.3L7 13z",
};
export function Icon(p: { name: keyof typeof P | string; size?: number; class?: string }) {
  const s = p.size ?? 14;
  return (
    <svg width={s} height={s} viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" class={`shrink-0 ${p.class ?? ""}`}>
      <path d={P[p.name] ?? P.file} />
    </svg>
  );
}

/** Render `text` with matched rune positions (from Go fuzzy) highlighted. */
export function Highlight(p: { text: string; positions?: number[] | null }) {
  if (!p.positions?.length) return <>{p.text}</>;
  const set = new Set(p.positions);
  const runes = Array.from(p.text);
  const out: any[] = [];
  let buf = "", on = false;
  const flush = () => { if (buf) out.push(on ? <mark class="hl">{buf}</mark> : buf); buf = ""; };
  runes.forEach((ch, i) => { const m = set.has(i); if (m !== on) { flush(); on = m; } buf += ch; });
  flush();
  return <>{out}</>;
}

export function Resizer(p: { dir: "x" | "y"; get: () => number; set: (v: number) => void; sign?: 1 | -1; onDoubleClick?: () => void }) {
  const down = (e: PointerEvent) => {
    e.preventDefault();
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture(e.pointerId);
    const start = p.dir === "x" ? e.clientX : e.clientY;
    const base = p.get();
    const sign = p.sign ?? 1;
    const move = (ev: PointerEvent) => p.set(base + sign * ((p.dir === "x" ? ev.clientX : ev.clientY) - start));
    const up = () => { el.removeEventListener("pointermove", move); el.removeEventListener("pointerup", up); document.body.style.cursor = ""; };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    document.body.style.cursor = p.dir === "x" ? "col-resize" : "row-resize";
  };
  return (
    <div
      onPointerDown={down}
      onDblClick={p.onDoubleClick}
      class={`no-drag shrink-0 z-20 relative ${p.dir === "x" ? "w-[1px] cursor-col-resize bg-transparent" : "h-[1px] cursor-row-resize bg-transparent"} hover:bg-accent/70 transition-colors`}
    >
      <div class={`absolute ${p.dir === "x" ? "-left-[3px] w-[7px] inset-y-0" : "-top-[3px] h-[7px] inset-x-0"}`} />
    </div>
  );
}

/** Centered/top-anchored modal frame; Escape and backdrop click close it. */
export function Modal(p: { children: ComponentChildren; width?: number; top?: boolean; onClose?: () => void; label?: string }) {
  const close = p.onClose ?? closeOverlay;
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    const k = (e: KeyboardEvent) => { if (e.key === "Escape") { e.stopPropagation(); close(); } };
    window.addEventListener("keydown", k, true);
    return () => { window.removeEventListener("keydown", k, true); prev?.focus?.(); };
  }, []);
  return (
    <div class="fixed inset-0 z-50 flex justify-center bg-black/40" style={{ alignItems: p.top ? "flex-start" : "center", paddingTop: p.top ? 80 : 0 }} onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div ref={ref} role="dialog" aria-label={p.label} class="pop rounded-xl bg-panel2 shadow-2xl shadow-black/60 max-h-[75vh] flex flex-col overflow-hidden" style={{ width: p.width ?? 520, maxWidth: "92vw" }}>
        {p.children}
      </div>
    </div>
  );
}
