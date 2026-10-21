import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { ProjectService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import { allCommands, commandsVersion, keyLabel, recentCommands, runCommand } from "../../lib/commands";
import { loadUI, saveUI } from "../../state/uistate";
import { closeOverlay } from "../../state/ui";
import { Highlight, Modal } from "../shell/ui";

let recentsLoaded = false;
async function loadRecents() {
  if (recentsLoaded) return;
  recentsLoaded = true;
  const r = await loadUI<{ ids: string[] }>("recentCommands", { ids: [] });
  const cur = recentCommands.value;
  recentCommands.value = [...cur, ...r.ids.filter((x) => !cur.includes(x))].slice(0, 30);
}

type Item = { id: string; title: string; positions?: number[] | null };

export function Palette() {
  void commandsVersion.value;
  const cmds = useMemo(() => allCommands(), []);
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const [items, setItems] = useState<Item[]>([]);
  const list = useRef<HTMLDivElement>(null);

  useEffect(() => { void loadRecents(); }, []);
  useEffect(() => {
    let dead = false;
    if (!q.trim()) {
      const rec = recentCommands.value;
      const byId = new Map(cmds.map((c) => [c.id, c]));
      const first = rec.map((id) => byId.get(id)).filter(Boolean) as typeof cmds;
      const rest = cmds.filter((c) => !rec.includes(c.id)).sort((a, b) => a.title.localeCompare(b.title));
      setItems([...first, ...rest].map((c) => ({ id: c.id, title: c.title })));
      setSel(0);
      return;
    }
    ProjectService.Fuzzy(q, cmds.map((c) => c.title), 60).then((ms) => {
      if (dead) return;
      setItems((ms ?? []).map((m) => ({ id: cmds[m.index].id, title: cmds[m.index].title, positions: m.positions })));
      setSel(0);
    }).catch(() => {});
    return () => { dead = true; };
  }, [q]);

  useEffect(() => { (list.current?.children[sel] as HTMLElement | undefined)?.scrollIntoView({ block: "nearest" }); }, [sel]);

  const run = (it?: Item) => {
    if (!it) return;
    closeOverlay();
    saveUI("recentCommands", { ids: [it.id, ...recentCommands.value.filter((x) => x !== it.id)].slice(0, 30) }, 100);
    setTimeout(() => runCommand(it.id), 0);
  };
  const key = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) { e.preventDefault(); setSel((s) => Math.min(items.length - 1, s + 1)); }
    else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) { e.preventDefault(); setSel((s) => Math.max(0, s - 1)); }
    else if (e.key === "Enter") { e.preventDefault(); run(items[sel]); }
  };
  return (
    <Modal width={560} top label="Command palette">
      <input class="bg-transparent px-4 py-3 text-[14px] outline-none" autofocus ref={(el) => el?.focus()} placeholder="Type a command..." value={q} onInput={(e) => setQ(e.currentTarget.value)} onKeyDown={key} />
      <div ref={list} class="max-h-[50vh] overflow-y-auto py-1">
        {items.length === 0 && <div class="px-4 py-3 text-muted">No matching commands</div>}
        {items.map((it, i) => (
          <div key={it.id} onMouseMove={() => setSel(i)} onClick={() => run(it)} class={`flex cursor-default items-center gap-3 px-4 py-1.5 ${i === sel ? "bg-accent/20" : ""}`}>
            <span class="min-w-0 flex-1 truncate"><Highlight text={it.title} positions={it.positions} /></span>
            <span class="kbd">{keyLabel(it.id)}</span>
          </div>
        ))}
      </div>
    </Modal>
  );
}
