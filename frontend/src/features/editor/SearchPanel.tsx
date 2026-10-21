// Find in files: SearchService.Grep -> `search/<streamID>` frames, grouped by file.
import { useEffect, useRef, useState } from "preact/hooks";
import { bus } from "../../lib/bus";
import { SearchService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import { openFile, searchRequest, searchSeed } from "../../state/editor";
import { errInfo } from "../../state/toast";

type Match = { line: number; column: number; text: string; submatches: { start: number; end: number }[] };
type FileResult = { path: string; matches: Match[] };
type Frame = { path?: string; matches?: Match[]; done?: boolean; error?: string };

const MAX_MATCHES = 3000;
const enc = new TextEncoder();
const dec = new TextDecoder();
/** Go reports submatch offsets as UTF-8 byte offsets; convert to UTF-16 indices. */
function byteToIdx(text: string, b: number): number {
  const bytes = enc.encode(text);
  return dec.decode(bytes.subarray(0, Math.min(b, bytes.length))).length;
}

export function SearchPanel({ scopeKey, root }: { scopeKey: string; root: string }) {
  const [query, setQuery] = useState(searchSeed.value);
  const [regex, setRegex] = useState(false);
  const [cs, setCs] = useState(false);
  const [glob, setGlob] = useState("");
  const [results, setResults] = useState<FileResult[]>([]);
  const [status, setStatus] = useState<"idle" | "running" | "done" | "error">("idle");
  const [err, setErr] = useState("");
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const input = useRef<HTMLInputElement>(null);
  const stop = useRef<() => void>(() => {});

  // ⌘⇧F: focus + select (and adopt seed text).
  useEffect(() => {
    const unsub = searchRequest.subscribe(() => {
      if (searchSeed.value) { setQuery(searchSeed.value); searchSeed.value = ""; }
      input.current?.focus();
      input.current?.select();
    });
    return unsub;
  }, []);

  useEffect(() => {
    stop.current();
    if (!query.trim()) { setResults([]); setStatus("idle"); return; }
    let live = true;
    const t = setTimeout(async () => {
      setResults([]); setStatus("running"); setErr("");
      let id = "";
      let off: () => void = () => {};
      let total = 0;
      const finish = (s: "done" | "error") => { off(); if (live) setStatus(s); };
      stop.current = () => { live = false; off(); if (id) void SearchService.CancelGrep(id).catch(() => {}); };
      try {
        id = await SearchService.Grep(root, query, { regex, caseSensitive: cs, glob });
        if (!live) { void SearchService.CancelGrep(id).catch(() => {}); return; }
        const name = "search/" + id;
        const chOffs: (() => void)[] = [];
        const unOpen = bus.onOpen(name, (ch) => {
          if (ch.name !== name) return;
          chOffs.push(ch.onJSON<Frame>((f) => {
            if (!live || !f) return;
            if (f.error) { setErr(f.error); finish("error"); return; }
            if (f.done) { finish("done"); return; }
            if (f.path && f.matches && total < MAX_MATCHES) {
              total += f.matches.length;
              const fr: FileResult = { path: f.path, matches: f.matches };
              setResults((r) => [...r, fr]);
            }
          }));
          ch.sendJSON({});
        });
        off = () => { unOpen(); chOffs.forEach((f) => f()); };
      } catch (e) {
        setErr(errInfo(e).message);
        finish("error");
      }
    }, 250);
    return () => { clearTimeout(t); live = false; stop.current(); };
  }, [query, regex, cs, glob, root]);

  const abs = (p: string) => (p.startsWith("/") ? p : root.replace(/\/+$/, "") + "/" + p);
  const total = results.reduce((n, r) => n + r.matches.length, 0);
  const tog = (on: boolean) => `rounded px-1.5 py-0.5 text-[11px] ${on ? "bg-blue-500/30 text-blue-200" : "text-neutral-400 hover:bg-white/10"}`;

  return (
    <div data-ctx="search" class="flex h-full min-h-0 flex-col text-[12px]">
      <div class="space-y-1.5 border-b border-neutral-800 p-2">
        <div class="flex items-center gap-1">
          <input
            ref={input}
            value={query}
            placeholder="Search"
            class="min-w-0 flex-1 rounded border border-neutral-700 bg-bg px-2 py-1 outline-none focus:border-blue-500/60"
            onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
          />
          <button class={tog(cs)} title="Match case" onClick={() => setCs(!cs)}>Aa</button>
          <button class={tog(regex)} title="Regex" onClick={() => setRegex(!regex)}>.*</button>
        </div>
        <input
          value={glob}
          placeholder="files to include (e.g. *.ts, !dist)"
          class="w-full rounded border border-neutral-700 bg-bg px-2 py-1 outline-none focus:border-blue-500/60"
          onInput={(e) => setGlob((e.target as HTMLInputElement).value)}
        />
        <div class="text-neutral-500">
          {status === "running" && "Searching... "}
          {status !== "idle" && `${total}${total >= MAX_MATCHES ? "+" : ""} results in ${results.length} files`}
          {err && <span class="ml-2 text-red-400">{err}</span>}
        </div>
      </div>
      <div class="min-h-0 flex-1 overflow-auto py-1">
        {results.map((f) => {
          const open = !collapsed[f.path];
          return (
            <div key={f.path}>
              <div
                class="flex cursor-pointer items-center gap-1 px-2 py-0.5 hover:bg-white/5"
                onClick={() => setCollapsed({ ...collapsed, [f.path]: open })}
              >
                <span class="w-3 text-neutral-500">{open ? "▾" : "▸"}</span>
                <span class="truncate text-neutral-200">{f.path.split("/").pop()}</span>
                <span class="truncate text-neutral-500">{f.path.includes("/") ? f.path.slice(0, f.path.lastIndexOf("/")) : ""}</span>
                <span class="ml-auto rounded bg-white/10 px-1 text-[10px]">{f.matches.length}</span>
              </div>
              {open && f.matches.map((m, i) => <MatchRow key={i} m={m} onOpen={() => {
                const sm = m.submatches[0];
                openFile(scopeKey, abs(f.path), true, m.line, sm ? byteToIdx(m.text, sm.start) + 1 : 1);
              }} />)}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function MatchRow({ m, onOpen }: { m: Match; onOpen: () => void }) {
  // Trim leading whitespace, then render with highlighted submatches.
  const lead = m.text.length - m.text.trimStart().length;
  const text = m.text.replace(/[\r\n]+$/, "");
  const parts: any[] = [];
  let pos = lead;
  for (const s of m.submatches) {
    const a = byteToIdx(text, s.start), b = byteToIdx(text, s.end);
    if (a < pos) continue;
    parts.push(text.slice(pos, a));
    parts.push(<mark class="rounded-sm bg-amber-500/30 text-amber-100">{text.slice(a, b)}</mark>);
    pos = b;
  }
  parts.push(text.slice(pos, pos + 300));
  return (
    <div class="flex cursor-pointer gap-2 whitespace-pre py-px pl-7 pr-2 hover:bg-white/5" onClick={onOpen}>
      <span class="shrink-0 text-neutral-600">{m.line}</span>
      <span class="truncate text-neutral-400">{parts}</span>
    </div>
  );
}
