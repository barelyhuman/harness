import { IconCheck, IconStar, IconStarFilled } from "@tabler/icons-preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { ProjectService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { ModelInfo } from "../../lib/bindings/github.com/sid/harness/internal/app/models";
import type { SessionRow } from "../../state/agents";
import { cap, findModel, loadModels, models, pickerOpen, setModel } from "./actions";

type Item = { m: ModelInfo; pos: number[] };

function Label(p: { text: string; pos: number[] }) {
  if (!p.pos.length) return <>{p.text}</>;
  const set = new Set(p.pos);
  return <>{[...p.text].map((c, i) => (set.has(i) ? <mark key={i} class="hl">{c}</mark> : c))}</>;
}

// With onPick (no session yet) the picker only reports the choice; curKey marks the selected model.
export function ModelPicker(p: { sessionId: string; row?: SessionRow; onClose: () => void; onPick?: (m: ModelInfo) => void; curKey?: string }) {
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const [ranked, setRanked] = useState<Item[] | null>(null);
  const [confirm, setConfirm] = useState<ModelInfo | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const all = models.value;
  const cur = p.curKey ? all.find((m) => m.key === p.curKey) : findModel(all, p.row);

  useEffect(() => { if (!all.length) void loadModels(); input.current?.focus(); }, []);

  useEffect(() => {
    if (!q.trim()) { setRanked(null); return; }
    let dead = false;
    const cands = all.map((m) => m.label);
    void ProjectService.Fuzzy(q, cands, 40).then((r) => {
      if (dead) return;
      setRanked((r ?? []).map((x) => ({ m: all[x.index], pos: x.positions ?? [] })));
    }).catch(() => {});
    return () => { dead = true; };
  }, [q, all]);

  const flat: Item[] = useMemo(() => {
    if (ranked) return ranked;
    const fav = all.filter((m) => m.favourite);
    const rest = all.filter((m) => !m.favourite);
    return [...fav, ...rest].map((m) => ({ m, pos: [] }));
  }, [ranked, all]);

  // Group by provider when not searching (favourites first inside each group).
  const groups = useMemo(() => {
    if (ranked) return [{ provider: "", items: ranked }];
    const by = new Map<string, Item[]>();
    for (const it of flat) { const l = by.get(it.m.provider) ?? []; l.push(it); by.set(it.m.provider, l); }
    return [...by.entries()].map(([provider, items]) => ({ provider, items }));
  }, [flat, ranked]);
  const order = groups.flatMap((g) => g.items);

  const choose = (m: ModelInfo) => {
    if (m.key === cur?.key) return p.onClose();
    if (p.onPick) { p.onClose(); return p.onPick(m); }
    if (p.row && m.provider !== p.row.provider) return setConfirm(m);
    p.onClose();
    void setModel(p.sessionId, m.key);
  };

  const onKey = (e: KeyboardEvent) => {
    e.stopPropagation();
    if (confirm) {
      if (e.key === "Enter") { e.preventDefault(); p.onClose(); void setModel(p.sessionId, confirm.key); }
      else if (e.key === "Escape") { e.preventDefault(); setConfirm(null); }
      return;
    }
    if (e.key === "ArrowDown") { e.preventDefault(); setSel((s) => Math.min(order.length - 1, s + 1)); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setSel((s) => Math.max(0, s - 1)); }
    else if (e.key === "Enter") { e.preventDefault(); const it = order[sel]; if (it) choose(it.m); }
    else if (e.key === "Escape") { e.preventDefault(); p.onClose(); }
  };

  useEffect(() => { setSel(0); }, [q]);

  return (
    <div class="pop absolute bottom-full left-0 z-30 mb-1 w-[320px] overflow-hidden rounded-lg bg-panel2 shadow-2xl" onKeyDown={onKey}>
      {confirm ? (
        <div class="p-3 text-[12px]">
          <div class="font-medium">Switch to {cap(confirm.provider)}?</div>
          <div class="mt-1 text-muted">This forks a new session with a handoff in the same worktree.</div>
          <div class="mt-1 font-mono text-[11px] text-faint">{confirm.label}</div>
          <div class="mt-2 flex gap-1.5">
            <button class="btn btn-primary" autoFocus onClick={() => { p.onClose(); void setModel(p.sessionId, confirm.key); }}>Switch ⏎</button>
            <button class="btn" onClick={() => setConfirm(null)}>Cancel</button>
          </div>
        </div>
      ) : (
        <>
          <input ref={input} class="h-10 w-full bg-transparent px-4 text-[12.5px] outline-none placeholder:text-faint" placeholder="Search models…" value={q} onInput={(e) => setQ(e.currentTarget.value)} />
          <div class="max-h-[300px] menu overflow-y-auto">
            {order.length === 0 && <div class="px-3 py-2 text-[12px] text-faint">{all.length ? "No match" : "Loading…"}</div>}
            {groups.map((g) => (
              <div key={g.provider}>
                {g.provider && <div class="section-h text-faint">{cap(g.provider)}</div>}
                {g.items.map((it) => {
                  const idx = order.indexOf(it);
                  return (
                    <div key={it.m.key} class={`menu-item cursor-pointer text-[12.5px] ${idx === sel ? "bg-sel" : "hover:bg-hover"}`} onMouseEnter={() => setSel(idx)} onClick={() => choose(it.m)}>
                      <span class={it.m.favourite ? "text-warn" : "text-transparent"}>{it.m.favourite ? <IconStarFilled size={12} /> : <IconStar size={12} />}</span>
                      <span class="min-w-0 flex-1 truncate"><Label text={it.m.label} pos={it.pos} /></span>
                      {ranked && <span class="text-[10.5px] text-faint">{it.m.provider}</span>}
                      {it.m.key === cur?.key && <span class="text-accent"><IconCheck size={12} /></span>}
                    </div>
                  );
                })}
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

export { pickerOpen };
