import { useEffect, useRef, useState } from "preact/hooks";
import { SearchService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { QuickOpenResult } from "../../lib/bindings/github.com/sid/harness/internal/app/models";
import { openFile } from "../../state/editor";
import { activeScope } from "../../state/scope";
import { toastError } from "../../state/toast";
import { closeOverlay } from "../../state/ui";
import { Highlight, Modal } from "../shell/ui";

export function QuickOpen() {
  const scope = activeScope.value;
  const [q, setQ] = useState("");
  const [res, setRes] = useState<QuickOpenResult[]>([]);
  const [sel, setSel] = useState(0);
  const list = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!scope) return;
    let dead = false;
    SearchService.QuickOpen(scope.root, q, 50).then((r) => { if (!dead) { setRes(r ?? []); setSel(0); } }).catch(toastError);
    return () => { dead = true; };
  }, [q, scope?.root]);
  useEffect(() => { (list.current?.children[sel] as HTMLElement | undefined)?.scrollIntoView({ block: "nearest" }); }, [sel]);

  const open = (r?: QuickOpenResult) => {
    if (!r || !scope) return;
    closeOverlay();
    openFile(scope.key, `${scope.root.replace(/\/$/, "")}/${r.path}`);
  };
  const key = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) { e.preventDefault(); setSel((s) => Math.min(res.length - 1, s + 1)); }
    else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) { e.preventDefault(); setSel((s) => Math.max(0, s - 1)); }
    else if (e.key === "Enter") { e.preventDefault(); open(res[sel]); }
  };
  return (
    <Modal width={620} top label="Quick open">
      <input class="border-b border-border bg-transparent px-4 py-3 text-[14px] outline-none" autofocus ref={(el) => el?.focus()} placeholder="Go to file..." value={q} onInput={(e) => setQ(e.currentTarget.value)} onKeyDown={key} />
      <div ref={list} class="max-h-[50vh] overflow-y-auto py-1">
        {!scope && <div class="px-4 py-3 text-muted">Open a project first</div>}
        {res.map((r, i) => {
          const slash = r.path.lastIndexOf("/");
          const name = r.path.slice(slash + 1);
          const off = slash + 1;
          return (
            <div key={r.path} onMouseMove={() => setSel(i)} onClick={() => open(r)} class={`flex cursor-default items-baseline gap-2 px-4 py-1 ${i === sel ? "bg-accent/20" : ""}`}>
              <span class="shrink-0"><Highlight text={name} positions={r.positions?.filter((p) => p >= off).map((p) => p - off)} /></span>
              <span class="min-w-0 truncate text-[11px] text-faint"><Highlight text={r.path.slice(0, Math.max(0, slash))} positions={r.positions?.filter((p) => p < slash)} /></span>
            </div>
          );
        })}
      </div>
    </Modal>
  );
}
