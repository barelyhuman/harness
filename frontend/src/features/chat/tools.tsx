import { memo } from "preact/compat";
import { useContext, useState } from "preact/hooks";
import type { Block } from "../../state/transcript";
import { openFile } from "../../state/editor";
import { diffTabId, openTab } from "../../state/tabs";
import { toastError } from "../../state/toast";
import { ensureAgentTerminal } from "../terminal/mirror";
import { Icon } from "../shell/ui";
import { absPath, relPath, ChatCtx, scopeRoot } from "./ctx";
import { BlockView } from "./blocks";

export function StatusIcon(p: { status: string; denied?: boolean }) {
  if (p.status === "pending" || p.status === "running")
    return <svg class="spin shrink-0 text-muted" width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2"><path d="M8 2a6 6 0 106 6" stroke-linecap="round" /></svg>;
  if (p.status === "error" || p.denied) return <span class="shrink-0 text-err"><Icon name="x" size={12} /></span>;
  return <span class="shrink-0 text-run"><Icon name="check" size={12} /></span>;
}

function firstLine(s: string): string {
  const i = s.indexOf("\n");
  return i < 0 ? s : s.slice(0, i) + " …";
}

export function DiffView(p: { diff: string; max?: number }) {
  const [all, setAll] = useState(false);
  const lines = p.diff.replace(/\n$/, "").split("\n");
  const max = p.max ?? 60;
  const shown = all ? lines : lines.slice(0, max);
  return (
    <div class="overflow-x-auto bg-bg py-1 font-mono text-[11.5px] leading-[1.5]">
      {shown.map((l, i) => {
        const c = l.startsWith("@@") ? "diff-h" : l.startsWith("+++") || l.startsWith("---") || l.startsWith("diff ") || l.startsWith("index ") ? "diff-m" : l[0] === "+" ? "diff-a" : l[0] === "-" ? "diff-r" : "";
        return <span key={i} class={`diff-l ${c}`}>{l || " "}</span>;
      })}
      {!all && lines.length > max && (
        <button class="px-2 pt-1 text-[11px] text-muted hover:text-text" onClick={() => setAll(true)}>Show {lines.length - max} more lines</button>
      )}
    </div>
  );
}

function Shell(p: { status: string; denied?: boolean; head: any; actions?: any; body?: any; open?: boolean; onToggle?: () => void }) {
  return (
    <div class="overflow-hidden rounded-md bg-panel2/70">
      <div class="flex min-h-[26px] items-center gap-1.5 px-2 text-[12px]">
        <StatusIcon status={p.status} denied={p.denied} />
        <div class="flex min-w-0 flex-1 items-center gap-1.5" onClick={p.onToggle} style={p.onToggle ? "cursor:pointer" : ""}>{p.head}</div>
        {p.actions}
        {p.onToggle && (
          <button class="shrink-0 text-faint hover:text-text" onClick={p.onToggle} title={p.open ? "Collapse" : "Expand"}>
            <span class={`inline-block transition-transform ${p.open ? "rotate-90" : ""}`}><Icon name="chevron" size={12} /></span>
          </button>
        )}
      </div>
      {p.body}
    </div>
  );
}

function BashCard(p: { b: Block }) {
  const { scopeKey } = useContext(ChatCtx);
  const t = p.b.tool!;
  const out = t.output ?? "";
  const n = t.outputLines ?? (out ? out.split("\n").length : 0);
  const [expanded, setExpanded] = useState(false);
  const lines = out ? out.replace(/\n$/, "").split("\n") : [];
  const long = lines.length > 20;
  const shown = long && !expanded ? lines.slice(0, 20) : lines;
  const failed = t.exitCode != null && t.exitCode !== 0;
  return (
    <Shell
      status={t.status}
      denied={t.denied}
      head={<><span class="text-faint">$</span><span class="truncate font-mono text-[12px]" title={t.command}>{firstLine(t.command ?? "")}</span>{failed && <span class="shrink-0 rounded bg-err/15 px-1 text-[10.5px] text-err">exit {t.exitCode}</span>}{t.interrupted && <span class="shrink-0 text-[10.5px] text-warn">interrupted</span>}</>}
      actions={<button class="shrink-0 text-[11px] text-muted hover:text-text" onClick={() => void ensureAgentTerminal(scopeKey).catch(toastError)}>Open in terminal</button>}
      body={lines.length > 0 && (
        <div class="bg-bg">
          <pre class="chat-sel m-0 max-h-[360px] overflow-auto px-2 py-1 font-mono text-[11.5px] leading-[1.5] text-muted">{shown.join("\n")}</pre>
          {long && (
            <button class="w-full py-0.5 text-[11px] text-muted hover:text-text" onClick={() => setExpanded(!expanded)}>
              {expanded ? "Show less" : `Show ${Math.max(n, lines.length) - 20} more lines`}
            </button>
          )}
          {t.outputTruncated && <div class="px-2 pb-1 text-[10.5px] text-faint">output truncated</div>}
        </div>
      )}
    />
  );
}

function EditCard(p: { b: Block }) {
  const { sessionId, scopeKey } = useContext(ChatCtx);
  const root = scopeRoot(sessionId);
  const t = p.b.tool!;
  const [open, setOpen] = useState(!p.b.collapsed);
  const abs = absPath(root, t.path ?? "");
  const hasDiff = !!t.diff;
  return (
    <Shell
      status={t.status}
      denied={t.denied}
      open={open}
      onToggle={hasDiff ? () => setOpen(!open) : undefined}
      head={<>
        <span class="shrink-0 text-muted">{t.kind === "write" ? "Write" : "Edit"}</span>
        <button class="truncate font-mono text-[12px] hover:text-accent hover:underline" title={abs} onClick={(e) => { e.stopPropagation(); if (abs) openFile(scopeKey, abs); }}>{t.path}</button>
        {(t.added ?? 0) > 0 && <span class="shrink-0 font-mono text-[11px] text-run">+{t.added}</span>}
        {(t.removed ?? 0) > 0 && <span class="shrink-0 font-mono text-[11px] text-err">−{t.removed}</span>}
        {t.denied && <span class="shrink-0 text-[10.5px] text-faint">denied</span>}
      </>}
      actions={abs && t.status === "ok" ? <button class="shrink-0 text-[11px] text-muted hover:text-text" onClick={() => openTab(scopeKey, { id: diffTabId(relPath(root, abs), false), kind: "diff", path: relPath(root, abs), staged: false })}>Open diff</button> : null}
      body={open && hasDiff && <DiffView diff={t.diff!} />}
    />
  );
}

function OneLiner(p: { b: Block }) {
  const { sessionId, scopeKey } = useContext(ChatCtx);
  const root = scopeRoot(sessionId);
  const t = p.b.tool!;
  const label = p.b.title || `${t.name} ${t.path ?? ""}`;
  const canOpen = t.kind === "read" && t.path;
  return (
    <div class="flex min-h-[22px] items-center gap-1.5 px-2 text-[12px] text-muted">
      <StatusIcon status={t.status} denied={t.denied} />
      {canOpen ? (
        <button class="truncate text-left hover:text-text hover:underline" onClick={() => openFile(scopeKey, absPath(root, t.path!))}>{label}</button>
      ) : <span class="truncate">{label}</span>}
    </div>
  );
}

function TaskCard(p: { b: Block }) {
  const t = p.b.tool!;
  const kids = p.b.children ?? [];
  const [open, setOpen] = useState(!p.b.collapsed);
  return (
    <Shell
      status={t.status}
      open={open}
      onToggle={() => setOpen(!open)}
      head={<><span class="shrink-0 text-muted">Task</span><span class="truncate">{p.b.title || t.name}</span>{kids.length > 0 && <span class="shrink-0 text-[11px] text-faint">{kids.length} step{kids.length > 1 ? "s" : ""}</span>}</>}
      body={open && kids.length > 0 && (
        <div class="space-y-1 py-1.5 pl-3 pr-1.5">
          {kids.map((c) => <BlockView key={c.id} b={c} />)}
        </div>
      )}
    />
  );
}

function GenericCard(p: { b: Block }) {
  const t = p.b.tool!;
  const [open, setOpen] = useState(false);
  let json = "";
  if (open) { try { json = JSON.stringify(t.input ?? {}, null, 2); } catch { json = String(t.input); } }
  return (
    <Shell
      status={t.status}
      denied={t.denied}
      open={open}
      onToggle={() => setOpen(!open)}
      head={<><span class="shrink-0 text-muted">{t.name}</span><span class="truncate text-faint">{p.b.title}</span></>}
      body={open && (
        <div class="bg-bg">
          <pre class="chat-sel m-0 max-h-[300px] overflow-auto px-2 py-1 font-mono text-[11.5px] text-muted">{json}</pre>
          {t.output && <pre class="chat-sel m-0 max-h-[300px] overflow-auto px-2 py-1 font-mono text-[11.5px] text-muted">{t.output}</pre>}
        </div>
      )}
    />
  );
}

function ToolImpl(p: { b: Block }) {
  const t = p.b.tool;
  if (!t) return null;
  switch (t.kind) {
    case "bash": return <BashCard b={p.b} />;
    case "edit":
    case "write": return <EditCard b={p.b} />;
    case "read":
    case "search": return <OneLiner b={p.b} />;
    case "task": return <TaskCard b={p.b} />;
    default: return <GenericCard b={p.b} />;
  }
}
export const ToolCard = memo(ToolImpl, (a, b) => a.b === b.b);
