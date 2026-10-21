import { AppService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import { dismissToast, toastError, toasts } from "../../state/toast";
import { closeOverlay, overlay } from "../../state/ui";
import { Modal } from "./ui";
import { Palette } from "../palette/Palette";
import { QuickOpen } from "../palette/QuickOpen";
import { NewAgentSheet } from "../agents/NewAgentSheet";

export function Toasts() {
  return (
    <div class="pointer-events-none fixed bottom-4 right-4 z-[70] flex flex-col items-end gap-2">
      {toasts.value.map((t) => (
        <div key={t.id} onClick={() => dismissToast(t.id)} class={`pop pointer-events-auto max-w-[420px] cursor-default rounded-lg border px-3 py-2 text-[12px] shadow-xl shadow-black/50 ${t.kind === "error" ? "border-err/40 bg-[#2a1717] text-red-200" : t.kind === "success" ? "border-run/40 bg-[#14231a] text-green-200" : "border-border bg-raise text-text"}`}>
          {t.msg}
        </div>
      ))}
    </div>
  );
}

function QuitSheet(p: { shells: number }) {
  const quit = () => AppService.ConfirmQuit().catch(toastError);
  return (
    <Modal width={400} label="Quit">
      <div class="p-5">
        <div class="mb-1 text-[14px] font-semibold">Quit Harness?</div>
        <div class="mb-4 text-muted">
          {p.shells} shell{p.shells === 1 ? "" : "s"} will be closed (agents keep running in the background).
        </div>
        <div class="flex justify-end gap-2">
          <button class="btn" onClick={closeOverlay}>Cancel</button>
          <button class="btn btn-primary" autofocus ref={(el) => el?.focus()} onClick={quit}>Quit</button>
        </div>
      </div>
    </Modal>
  );
}

export function Overlays() {
  const o = overlay.value;
  if (!o) return null;
  switch (o.kind) {
    case "palette": return <Palette />;
    case "quickopen": return <QuickOpen />;
    case "newagent": return <NewAgentSheet projectId={o.projectId} />;
    case "quit": return <QuitSheet shells={o.shells} />;
    default: return null; // picker is a popover rendered by the sidebar header
  }
}
