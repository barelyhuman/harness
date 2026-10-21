import { IconClock, IconX } from "@tabler/icons-preact";
import { signal } from "@preact/signals";
import { useEffect, useRef, useState } from "preact/hooks";
import { SearchService, SessionService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import type { Attachment } from "../../lib/bindings/github.com/sid/harness/internal/app/models";
import { activeScope } from "../../state/scope";
import { toast, toastError } from "../../state/toast";
import { scopeRoot } from "../chat/ctx";
import { cycleEffort, cycleMode, findModel, isBusy, loadModels, models, pickerOpen, registerComposerCommands, sessionRow } from "./actions";
import { ModelPicker } from "./ModelPicker";

type Draft = { text: string; atts: Attachment[] };
const drafts = new Map<string, Draft>();
const queued = signal<Record<string, string[]>>({});

const SLASH = [
  { cmd: "/compact", desc: "Summarise the conversation to free context" },
  { cmd: "/context", desc: "Show context usage" },
  { cmd: "/cost", desc: "Show session cost" },
  { cmd: "/init", desc: "Create a CLAUDE.md for this project" },
  { cmd: "/review", desc: "Review the pending changes" },
  { cmd: "/model", desc: "Switch model (opens the picker)" },
];

type Menu = { kind: "file"; start: number; items: string[]; sel: number } | { kind: "slash"; items: { cmd: string; desc: string }[]; sel: number } | null;

const readB64 = (f: File) =>
  new Promise<string>((res, rej) => {
    const r = new FileReader();
    r.onload = () => res(String(r.result).split(",")[1] ?? "");
    r.onerror = () => rej(r.error);
    r.readAsDataURL(f);
  });

export function Composer(p: { sessionId: string }) {
  const sid = p.sessionId;
  const ta = useRef<HTMLTextAreaElement>(null);
  const [text, setTextState] = useState(drafts.get(sid)?.text ?? "");
  const [atts, setAtts] = useState<Attachment[]>(drafts.get(sid)?.atts ?? []);
  const [menu, setMenu] = useState<Menu>(null);
  const lastEsc = useRef(0);
  const row = sessionRow(sid);
  const busy = isBusy(row);
  const q = queued.value[sid] ?? [];
  const root = scopeRoot(sid);

  useEffect(() => { registerComposerCommands(); void loadModels(); }, []);
  useEffect(() => { drafts.set(sid, { text, atts }); }, [sid, text, atts]);
  // Queued chips are held in Go until the run result; clear them when the run ends.
  useEffect(() => { if (!busy && q.length) queued.value = { ...queued.value, [sid]: [] }; }, [busy]);

  const grow = () => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 220) + "px";
  };
  useEffect(grow, [text]);

  const setText = (t: string, caret?: number) => {
    setTextState(t);
    if (caret != null) requestAnimationFrame(() => { ta.current?.setSelectionRange(caret, caret); ta.current?.focus(); });
  };

  // ⌘L from the terminal: insert the selection as a fenced block.
  useEffect(() => {
    const h = (e: Event) => {
      if (activeScope.value?.sessionId !== sid) return;
      const t = String((e as CustomEvent).detail?.text ?? "");
      if (!t) return;
      const cur = ta.current?.value ?? "";
      const pre = cur && !cur.endsWith("\n") ? "\n" : "";
      setText(`${cur}${pre}\`\`\`\n${t.replace(/\n$/, "")}\n\`\`\`\n`);
      ta.current?.focus();
    };
    window.addEventListener("harness:composer-insert", h);
    return () => window.removeEventListener("harness:composer-insert", h);
  }, [sid]);

  // ---- menus ----
  const fileTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const refreshMenu = (value: string, caret: number) => {
    clearTimeout(fileTimer.current);
    if (/^\/[^\s]*$/.test(value)) {
      const items = SLASH.filter((s) => s.cmd.startsWith(value.toLowerCase()));
      setMenu(items.length ? { kind: "slash", items, sel: 0 } : null);
      return;
    }
    const m = /(^|\s)@([^\s@]*)$/.exec(value.slice(0, caret));
    if (!m || !root) { setMenu(null); return; }
    const start = caret - m[2].length - 1;
    const query = m[2];
    fileTimer.current = setTimeout(() => {
      void SearchService.QuickOpen(root, query, 30).then((r) => {
        const cur = ta.current;
        if (!cur || cur.value.slice(0, cur.selectionStart).slice(start) !== "@" + query) return;
        const items = (r ?? []).map((x) => x.path);
        setMenu(items.length ? { kind: "file", start, items, sel: 0 } : null);
      }).catch(() => setMenu(null));
    }, 60);
  };

  const accept = (idx?: number) => {
    if (!menu) return;
    const si = idx ?? menu.sel;
    const el = ta.current!;
    if (menu.kind === "slash") {
      const c = menu.items[si];
      setMenu(null);
      if (c.cmd === "/model") { setText(""); void loadModels(); pickerOpen.value = true; return; }
      setText(c.cmd + " ", c.cmd.length + 1);
    } else {
      const path = menu.items[si];
      const caret = el.selectionStart;
      const ins = "@" + path + " ";
      const next = text.slice(0, menu.start) + ins + text.slice(caret);
      setMenu(null);
      setText(next, menu.start + ins.length);
    }
  };

  // ---- attachments ----
  const addFiles = async (files: File[]) => {
    const imgs = files.filter((f) => f.type.startsWith("image/"));
    if (!imgs.length) return false;
    try {
      const added = await Promise.all(imgs.map(async (f, i) => ({ name: f.name || `image-${Date.now()}-${i}.${f.type.split("/")[1] || "png"}`, mediaType: f.type, data: await readB64(f) })));
      setAtts((a) => [...a, ...added]);
    } catch (e) { toastError(e); }
    return true;
  };

  // ---- sending ----
  const send = async (mode: "auto" | "steer") => {
    const t = text.trim();
    if (!t && !atts.length) return;
    const delivery = !busy ? "send" : mode === "steer" ? "steer" : "queue";
    const snapshot = { text, atts };
    setTextState("");
    setAtts([]);
    setMenu(null);
    if (delivery === "queue") queued.value = { ...queued.value, [sid]: [...(queued.value[sid] ?? []), t || `${atts.length} image(s)`] };
    try {
      await SessionService.Send(sid, { text: t, attachments: atts.length ? atts : null, delivery });
    } catch (e) {
      setTextState(snapshot.text);
      setAtts(snapshot.atts);
      if (delivery === "queue") queued.value = { ...queued.value, [sid]: (queued.value[sid] ?? []).slice(0, -1) };
      toastError(e);
    }
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.isComposing || e.keyCode === 229) return;
    if (menu) {
      const n = menu.items.length;
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const sel = (menu.sel + (e.key === "ArrowDown" ? 1 : n - 1)) % n;
        setMenu({ ...menu, sel } as Menu);
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") { e.preventDefault(); accept(); return; }
      if (e.key === "Escape") { e.preventDefault(); setMenu(null); return; }
    }
    if (e.key === "Enter" && !e.shiftKey && !e.altKey) {
      e.preventDefault();
      void send(e.metaKey || e.ctrlKey ? "steer" : "auto");
      return;
    }
    if (e.key === "Escape" && busy) {
      e.preventDefault();
      const now = Date.now();
      if (now - lastEsc.current < 700) {
        lastEsc.current = 0;
        void SessionService.Interrupt(sid).then(() => toast("Interrupted", "info", 1500)).catch(toastError);
      } else {
        lastEsc.current = now;
        toast("Press Esc again to stop", "info", 900);
      }
    }
  };

  const m = findModel(models.value, row);
  const modelLabel = m?.label ?? (row?.model || "model");
  const chip = "flex h-[22px] items-center gap-1 rounded-md px-1.5 text-[11.5px] text-muted hover:bg-hover hover:text-text";

  return (
    <div data-ctx="composer" class="relative px-3 pb-3 pt-1">
      {q.length > 0 && (
        <div class="mb-1 flex flex-wrap gap-1">
          {q.map((t, i) => <span key={i} class="max-w-[260px] truncate rounded-full px-2 py-0.5 text-[11px] text-faint" title="Queued: sent after the current run"><IconClock size={11} class="mr-1 inline" />{t}</span>)}
        </div>
      )}
      <div
        class="rounded-lg bg-panel2 focus-within:border-accent/60"
        onDragOver={(e) => { if (e.dataTransfer?.types.includes("Files")) e.preventDefault(); }}
        onDrop={(e) => { const fs = [...(e.dataTransfer?.files ?? [])]; if (fs.some((f) => f.type.startsWith("image/"))) { e.preventDefault(); void addFiles(fs); } }}
      >
        {atts.length > 0 && (
          <div class="flex flex-wrap gap-1.5 px-2 pt-2">
            {atts.map((a, i) => (
              <div key={i} class="group relative h-12 w-12 overflow-hidden rounded">
                <img src={`data:${a.mediaType};base64,${a.data}`} class="h-full w-full object-cover" alt={a.name} />
                <button class="absolute right-0 top-0 hidden h-4 w-4 items-center justify-center bg-black/70 text-[10px] group-hover:flex" onClick={() => setAtts(atts.filter((_, j) => j !== i))}><IconX size={10} /></button>
              </div>
            ))}
          </div>
        )}
        <textarea
          ref={ta}
          rows={1}
          value={text}
          class="block max-h-[220px] min-h-[34px] w-full resize-none bg-transparent px-3 py-2 text-[13px] leading-[1.5] outline-none placeholder:text-faint"
          placeholder={busy ? "Queue a message (⏎) or steer (⌘⏎)…" : "Message the agent…  @ to attach files, / for commands"}
          onInput={(e) => { const v = e.currentTarget.value; setTextState(v); refreshMenu(v, e.currentTarget.selectionStart); }}
          onKeyDown={onKeyDown}
          onPaste={(e) => {
            const fs = [...(e.clipboardData?.files ?? [])];
            if (fs.some((f) => f.type.startsWith("image/"))) { e.preventDefault(); void addFiles(fs); }
          }}
        />
        <div class="relative flex items-center gap-1 px-1.5 pb-1.5">
          <button class={chip} title="Model (⌘/)" onClick={() => { void loadModels(); pickerOpen.value = !pickerOpen.value; }}>{modelLabel}</button>
          <button class={chip} title="Mode (⇧Tab)" onClick={() => void cycleMode(sid)}>{row?.mode || "mode"}</button>
          <button class={chip} title="Effort (⌘E)" onClick={() => void cycleEffort(sid)}>{row?.effort ? `effort: ${row.effort}` : "effort"}</button>
          <div class="flex-1" />
          {busy && <button class="btn !py-0 text-[11px]" onClick={() => void SessionService.Interrupt(sid).catch(toastError)}>Stop</button>}
          <button class="btn btn-primary !py-0 text-[11px]" disabled={!text.trim() && !atts.length} onClick={() => void send("auto")}>{busy ? "Queue" : "Send"}</button>
          {pickerOpen.value && (
            <>
              <div class="fixed inset-0 z-20" onClick={() => (pickerOpen.value = false)} />
              <ModelPicker sessionId={sid} row={row} onClose={() => { pickerOpen.value = false; ta.current?.focus(); }} />
            </>
          )}
        </div>
      </div>
      {menu && (
        <div class="pop absolute bottom-full left-3 z-30 mb-1 max-h-[240px] w-[min(520px,calc(100%-24px))] overflow-y-auto rounded-lg bg-panel2 py-1 shadow-2xl">
          {menu.kind === "file"
            ? menu.items.map((it, i) => (
                <div key={it} class={`flex cursor-pointer gap-2 px-3 py-0.5 font-mono text-[12px] ${i === menu.sel ? "bg-sel" : "hover:bg-hover"}`} onMouseDown={(e) => { e.preventDefault(); accept(i); }}>
                  <span class="truncate">{it}</span>
                </div>
              ))
            : menu.items.map((it, i) => (
                <div key={it.cmd} class={`flex cursor-pointer gap-3 px-3 py-0.5 text-[12px] ${i === menu.sel ? "bg-sel" : "hover:bg-hover"}`} onMouseDown={(e) => { e.preventDefault(); accept(i); }}>
                  <span class="font-mono">{it.cmd}</span><span class="truncate text-faint">{it.desc}</span>
                </div>
              ))}
        </div>
      )}
    </div>
  );
}
