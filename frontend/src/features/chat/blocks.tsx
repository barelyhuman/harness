import { IconChevronRight, IconPhoto } from "@tabler/icons-preact";
import { memo } from "preact/compat";
import { useContext, useState } from "preact/hooks";
import { SessionService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { Block } from "../../state/transcript";
import { toastError } from "../../state/toast";
import { PermissionCard, PlanCard } from "./cards";
import { ChatCtx } from "./ctx";
import { Markdown } from "./markdown";
import { ToolCard } from "./tools";
import "./styles";

function User(p: { b: Block }) {
  return (
    <div class="flex justify-end">
      <div class="chat-sel max-w-[85%] whitespace-pre-wrap break-words rounded-lg bg-raise px-2.5 py-1.5 text-[13px] leading-[1.5]">
        {p.b.text}
        {!!p.b.images?.length && (
          <div class="mt-1 flex flex-wrap gap-1">
            {p.b.images.map((im, i) => <span key={i} class="rounded px-1.5 text-[10.5px] text-muted"><IconPhoto size={11} class="mr-0.5 inline" /> {im.name || im.mediaType}</span>)}
          </div>
        )}
      </div>
    </div>
  );
}

function Assistant(p: { b: Block }) {
  return <div class={`chat-sel px-0.5 ${p.b.streaming ? "caret-wrap" : ""}`}><Markdown text={p.b.text ?? ""} streaming={p.b.streaming} class={p.b.streaming ? "caret" : ""} /></div>;
}

function Thinking(p: { b: Block }) {
  const [open, setOpen] = useState(false);
  const label = p.b.streaming ? "Thinking…" : p.b.title || "Thought";
  return (
    <div class="px-0.5 text-[12px] text-faint">
      <button class="flex items-center gap-1 hover:text-muted" onClick={() => setOpen(!open)}>
        <IconChevronRight size={11} class={`inline-block transition-transform ${open ? "rotate-90" : ""}`} />
        <span class={p.b.streaming ? "animate-pulse" : ""}>{label}</span>
      </button>
      {open && <div class="chat-sel mt-1 whitespace-pre-wrap pl-2.5 text-[12px] leading-[1.5] text-muted">{p.b.text}</div>}
    </div>
  );
}

const fmtN = (n: number) => (n >= 10000 ? `${(n / 1000).toFixed(n >= 100000 ? 0 : 1)}k` : n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n));
function Footer(p: { b: Block }) {
  const f = p.b.footer;
  if (!f) return null;
  const parts: string[] = [];
  if (f.status && f.status !== "success") parts.push(f.status);
  if (f.inputTokens || f.outputTokens) parts.push(`${fmtN(f.inputTokens)} in · ${fmtN(f.outputTokens)} out`);
  if (f.cacheTokens) parts.push(`${fmtN(f.cacheTokens)} cached`);
  if (f.costUsd) parts.push(`$${f.costUsd.toFixed(f.costUsd < 0.1 ? 4 : 2)}`);
  if (f.durationMs) parts.push(f.durationMs >= 60000 ? `${Math.floor(f.durationMs / 60000)}m ${Math.round((f.durationMs % 60000) / 1000)}s` : `${(f.durationMs / 1000).toFixed(1)}s`);
  return <div class="px-0.5 text-[11px] text-faint">{parts.join(" · ") || f.summary}</div>;
}

function ErrorCard(p: { b: Block }) {
  const { sessionId } = useContext(ChatCtx);
  const e = p.b.error;
  return (
    <div class="flex items-start gap-2 rounded-md border border-err/40 bg-err/10 px-2.5 py-1.5 text-[12px]">
      <div class="min-w-0 flex-1">
        <div class="text-err">{p.b.title || "Error"}{e?.code && <span class="ml-1.5 font-mono text-[10.5px] opacity-70">{e.code}</span>}</div>
        {p.b.text && <div class="chat-sel mt-0.5 whitespace-pre-wrap break-words text-text/80">{p.b.text}</div>}
      </div>
      {e?.fatal && <button class="btn shrink-0" onClick={() => void SessionService.Send(sessionId, { text: "continue", attachments: null, delivery: "send" }).catch(toastError)}>Resume</button>}
    </div>
  );
}

function BlockImpl(p: { b: Block }) {
  const b = p.b;
  switch (b.type) {
    case "user": return <User b={b} />;
    case "assistant_text": return <Assistant b={b} />;
    case "thinking": return <Thinking b={b} />;
    case "tool": return <ToolCard b={b} />;
    case "permission": return <PermissionCard b={b} />;
    case "plan": return <PlanCard b={b} />;
    case "result_footer": return <Footer b={b} />;
    case "error": return <ErrorCard b={b} />;
    default: return null; // todo is pinned above the composer
  }
}
export const BlockView = memo(BlockImpl, (a, b) => a.b === b.b);
