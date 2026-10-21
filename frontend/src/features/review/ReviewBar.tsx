import { sessionRow } from "../composer/actions";
import { rightMode, rightOpen } from "../../state/layout";
import { Icon } from "../shell/ui";

const STATUS: Record<string, { c: string; l: string; anim?: boolean }> = {
  running: { c: "bg-run", l: "Running", anim: true },
  needs_approval: { c: "bg-warn", l: "Needs approval" },
  idle: { c: "bg-faint", l: "Idle" },
  error: { c: "bg-err", l: "Error" },
  archived: { c: "bg-faint/40", l: "Archived" },
};
const prColor = (s: string) => (s === "merged" ? "text-[#b392f0] border-[#b392f0]/40" : s === "closed" ? "text-err border-err/40" : "text-run border-run/40");

export function ReviewBar(p: { sessionId: string; scopeKey: string }) {
  const r = sessionRow(p.sessionId);
  if (!r) return <div class="h-8 shrink-0" />;
  const st = STATUS[r.status] ?? STATUS.idle;
  const dirty = typeof r.dirty === "number" ? r.dirty : r.dirty ? 1 : 0;
  return (
    <div class="flex h-8 shrink-0 items-center gap-2 px-3 text-[12px]">
      <span title={st.l} class={`h-[7px] w-[7px] shrink-0 rounded-full ${st.c} ${st.anim ? "dot-run" : ""}`} />
      <span class="min-w-0 truncate font-medium" title={r.title}>{r.title || r.branch || "New agent"}</span>
      {r.branch && <span class="flex shrink-0 items-center gap-1 text-muted"><Icon name="branch" size={11} /><span class="max-w-[160px] truncate font-mono text-[11px]">{r.branch}</span></span>}
      <span class="shrink-0 text-[11px] text-faint">{st.l}</span>
      {dirty > 0 && <span class="shrink-0 text-[11px] text-warn">{dirty} changed file{dirty > 1 ? "s" : ""}</span>}
      {r.prUrl && <span class={`shrink-0 rounded border px-1 text-[10.5px] ${prColor(r.prState)}`}>PR {r.prState || "open"}</span>}
      <div class="flex-1" />
      <button class="btn btn-sm" onClick={() => { rightMode.value = "scm"; rightOpen.value = true; }}>Review changes</button>
      {!r.prUrl && <button class="btn btn-sm" onClick={() => window.dispatchEvent(new CustomEvent("harness:raise-pr", { detail: { scopeKey: p.scopeKey } }))}>Raise PR</button>}
    </div>
  );
}
