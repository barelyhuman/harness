// Feature-scoped CSS injected once (style.css belongs to the shell owner).
const css = `
.chat-sel, .chat-sel * { user-select: text; -webkit-user-select: text; }
.md { font-size: 13px; line-height: 1.55; overflow-wrap: anywhere; }
.md > *:first-child { margin-top: 0 } .md > *:last-child { margin-bottom: 0 }
.md p { margin: .45em 0 } .md ul, .md ol { margin: .4em 0; padding-left: 1.4em } .md li { margin: .15em 0 }
.md ul { list-style: disc } .md ol { list-style: decimal }
.md h1,.md h2,.md h3,.md h4 { font-weight: 600; margin: .9em 0 .35em; line-height: 1.3 }
.md h1 { font-size: 1.25em } .md h2 { font-size: 1.12em } .md h3,.md h4 { font-size: 1em }
.md a { color: var(--color-accent); text-decoration: none } .md a:hover { text-decoration: underline }
.md code { font-family: var(--font-mono); font-size: .92em; background: #ffffff10; padding: .08em .35em; border-radius: 4px }
.md blockquote { border-left: 2px solid var(--color-border); margin: .5em 0; padding: 0 .8em; color: var(--color-muted) }
.md hr { border: 0; border-top: 1px solid var(--color-border); margin: .8em 0 }
.md table { border-collapse: collapse; margin: .5em 0; font-size: .95em } .md th,.md td { background: #ffffff08; padding: 2px 8px }
.md .cb { margin: .5em 0;  border-radius: 7px; background: #101012; overflow: hidden }
.md .cb-bar { display: flex; justify-content: space-between; align-items: center; padding: 2px 8px; font-size: 10.5px; color: var(--color-faint); }
.md .cb-copy { background: none; border: 0; color: var(--color-faint); cursor: pointer; padding: 0 2px; opacity: 0 }
.md .cb:hover .cb-copy { opacity: 1 } .md .cb-copy:hover { color: var(--color-text) }
.md .cb pre { margin: 0; padding: 7px 10px; overflow-x: auto }
.md .cb pre code { background: none; padding: 0; font-size: 12px; line-height: 1.5; white-space: pre; font-family: var(--font-mono) }
.caret::after { content: ""; display: inline-block; width: 6px; height: 1em; margin-left: 2px; vertical-align: text-bottom; background: var(--color-muted); animation: pulse-dot 1s ease-in-out infinite }
.diff-l { display: block; padding: 0 8px; white-space: pre; min-width: max-content }
.diff-a { background: #4ade8018; color: #9be9b4 } .diff-r { background: #f8717118; color: #f7a3a3 } .diff-h { color: var(--color-accent); opacity: .7 } .diff-m { color: var(--color-faint) }
@keyframes chat-spin { to { transform: rotate(360deg) } } .spin { animation: chat-spin .8s linear infinite }
`;
let done = false;
export function injectChatStyles() {
  if (done) return;
  done = true;
  const s = document.createElement("style");
  s.textContent = css;
  document.head.appendChild(s);
}
injectChatStyles();
