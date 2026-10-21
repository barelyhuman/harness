import { IconCheck, IconX } from "@tabler/icons-preact";
import { memo } from "preact/compat";
import { useContext, useState } from "preact/hooks";
import { SessionService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { Block } from "../../state/transcript";
import { toastError } from "../../state/toast";
import { ChatCtx } from "./ctx";
import { Markdown } from "./markdown";

export function respond(sessionId: string, reqId: string, behavior: "allow" | "allow_always" | "deny", message = "") {
  return SessionService.Respond(sessionId, reqId, { behavior, message }).catch(toastError);
}

export function permCommand(b: Block): string {
  const inp = b.permission?.input;
  if (inp && typeof inp === "object" && typeof (inp as any).command === "string") return (inp as any).command;
  return "";
}
export const isBashPerm = (b: Block) => /^bash$/i.test(b.permission?.tool ?? "") || !!permCommand(b);

const kbd = (s: string) => <span class="ml-1 opacity-60">{s}</span>;

function PermissionImpl(p: { b: Block }) {
  const { sessionId, pendingReq } = useContext(ChatCtx);
  const perm = p.b.permission!;
  const [busy, setBusy] = useState(false);
  const go = (bh: "allow" | "allow_always" | "deny") => { setBusy(true); void respond(sessionId, perm.reqId, bh).finally(() => setBusy(false)); };
  const bash = isBashPerm(p.b);
  const cmd = permCommand(p.b);

  if (perm.state !== "pending") {
    const label = perm.state === "allowed" ? (perm.always ? "Always allowed" : bash ? "Ran" : "Allowed") : perm.state === "denied" ? (bash ? "Cancelled" : "Denied") : "Cancelled";
    const ok = perm.state === "allowed";
    return (
      <div class="flex items-center gap-1.5 px-2 text-[11.5px] text-faint">
        <span class={`inline-flex `}>{ok ? <IconCheck size={13} /> : <IconX size={13} />}</span>
        <span>{label}</span><span class="truncate font-mono">{cmd || perm.tool}</span>
        {perm.message && <span class="truncate">· {perm.message}</span>}
      </div>
    );
  }
  const active = pendingReq === perm.reqId;
  let inputJson = "";
  if (!bash && perm.input != null) { try { inputJson = JSON.stringify(perm.input, null, 1); } catch { /* ignore */ } }
  return (
    <div class={`rounded-md bg-panel2 ${active ? "ring-1 ring-warn/60" : ""}`}>
      <div class="flex items-center gap-1.5 px-2 pt-1.5 text-[12px]">
        <span class="inline-block h-[7px] w-[7px] rounded-full bg-warn" />
        <span class="text-muted">{bash ? "Run command?" : `Allow ${perm.tool}?`}</span>
        {perm.description && !bash && <span class="truncate text-faint">{perm.description}</span>}
      </div>
      {(bash ? cmd : inputJson) && (
        <pre class="chat-sel m-0 mx-2 mt-1 max-h-[160px] overflow-auto rounded bg-bg px-2 py-1 font-mono text-[11.5px] text-text">{bash ? `$ ${cmd}` : inputJson}</pre>
      )}
      {perm.reason && <div class="px-2 pt-1 text-[11px] text-faint">{perm.reason}</div>}
      <div class="flex gap-1.5 p-2">
        {bash ? (
          <>
            <button class="btn btn-primary" disabled={busy} onClick={() => go("allow")}>Run{active && kbd("⏎")}</button>
            <button class="btn" disabled={busy} onClick={() => go("deny")}>Cancel{active && kbd("Esc")}</button>
          </>
        ) : (
          <>
            <button class="btn btn-primary" disabled={busy} onClick={() => go("allow")}>Allow{active && kbd("⏎")}</button>
            <button class="btn" disabled={busy} onClick={() => go("allow_always")}>Always allow{active && kbd("⌘⏎")}</button>
            <button class="btn" disabled={busy} onClick={() => go("deny")}>Deny{active && kbd("Esc")}</button>
          </>
        )}
      </div>
    </div>
  );
}
export const PermissionCard = memo(PermissionImpl, (a, b) => a.b === b.b);

function PlanImpl(p: { b: Block }) {
  const { sessionId } = useContext(ChatCtx);
  const plan = p.b.plan!;
  const [fb, setFb] = useState("");
  const [asking, setAsking] = useState(false);
  const pending = plan.state === "pending";
  return (
    <div class="rounded-md bg-panel2">
      <div class="flex items-center gap-2 px-2 py-1 text-[12px]">
        <span class="font-medium">Plan</span>
        {!pending && <span class={`text-[11px] ${plan.state === "allowed" ? "text-run" : "text-faint"}`}>{plan.state === "allowed" ? "approved" : plan.state}</span>}
      </div>
      <div class="chat-sel max-h-[420px] overflow-auto px-3 py-2"><Markdown text={p.b.text ?? ""} /></div>
      {pending && (
        <div class="p-2">
          {asking && (
            <textarea class="field mb-2 h-16 resize-none" placeholder="What should change? (optional)" value={fb} autoFocus onInput={(e) => setFb(e.currentTarget.value)} />
          )}
          <div class="flex gap-1.5">
            <button class="btn btn-primary" onClick={() => void respond(sessionId, plan.reqId, "allow")}>Approve</button>
            {asking
              ? <button class="btn" onClick={() => void respond(sessionId, plan.reqId, "deny", fb.trim())}>Send feedback</button>
              : <button class="btn" onClick={() => setAsking(true)}>Keep planning</button>}
          </div>
        </div>
      )}
      {!pending && plan.message && <div class="px-2 pb-1.5 text-[11px] text-faint">{plan.message}</div>}
    </div>
  );
}
export const PlanCard = memo(PlanImpl, (a, b) => a.b === b.b);
