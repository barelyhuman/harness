// Markdown rendering: marked -> DOMPurify. Code fences are colourised lazily with monaco.editor.colorize
// (only once the block stops streaming) and get a copy button.
import DOMPurify from "dompurify";
import { Marked } from "marked";
import { memo } from "preact/compat";
import { useEffect, useMemo, useRef } from "preact/hooks";
import { toast } from "../../state/toast";

const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

const md = new Marked({
  gfm: true,
  breaks: false,
  renderer: {
    code({ text, lang }) {
      const l = (lang ?? "").split(/\s+/)[0];
      return `<div class="cb" data-lang="${esc(l)}"><div class="cb-bar"><span>${esc(l)}</span><button type="button" class="cb-copy">Copy</button></div><pre><code>${esc(text)}</code></pre></div>`;
    },
  },
});

const purify = DOMPurify;
export function renderMarkdown(src: string): string {
  const html = md.parse(src, { async: false }) as string;
  return purify.sanitize(html, { ADD_ATTR: ["data-lang"], FORBID_TAGS: ["style", "form", "input"] });
}

let monacoP: Promise<typeof import("monaco-editor")> | undefined;
function monaco() {
  monacoP ??= import("../editor/monaco").then((m) => m.applyTheme());
  return monacoP;
}

/** Colourise one plain text snippet to HTML (Monaco token spans). */
export async function colorizeHtml(text: string, lang: string): Promise<string> {
  const m = await monaco();
  return m.editor.colorize(text, lang || "plaintext", { tabSize: 2 });
}

function openLink(href: string) {
  if (!/^https?:/i.test(href)) return;
  void import("@wailsio/runtime").then((r) => r.Browser.OpenURL(href)).catch(() => window.open(href, "_blank"));
}

function MarkdownImpl(p: { text: string; streaming?: boolean; class?: string }) {
  const html = useMemo(() => renderMarkdown(p.text), [p.text]);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (p.streaming || !ref.current) return;
    const root = ref.current;
    let dead = false;
    root.querySelectorAll<HTMLElement>(".cb").forEach((cb) => {
      const code = cb.querySelector("code");
      const lang = cb.dataset.lang ?? "";
      if (!code || !lang || cb.dataset.done) return;
      const text = code.textContent ?? "";
      if (text.length > 20000) return;
      cb.dataset.done = "1";
      colorizeHtml(text, lang)
        .then((h) => { if (!dead && code.isConnected) code.innerHTML = h; })
        .catch(() => {});
    });
    return () => { dead = true; };
  }, [html, p.streaming]);

  const onClick = (e: MouseEvent) => {
    const t = e.target as HTMLElement;
    const btn = t.closest(".cb-copy");
    if (btn) {
      const code = btn.closest(".cb")?.querySelector("code")?.textContent ?? "";
      void navigator.clipboard.writeText(code).then(() => toast("Copied", "success", 1200), () => {});
      return;
    }
    const a = t.closest("a");
    if (a) {
      e.preventDefault();
      openLink(a.getAttribute("href") ?? "");
    }
  };

  return <div ref={ref} class={`md ${p.class ?? ""}`} onClick={onClick} dangerouslySetInnerHTML={{ __html: html }} />;
}
export const Markdown = memo(MarkdownImpl);
