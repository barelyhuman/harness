// Key parsing, labels and `when` expression evaluation (spec 04). Pure helpers, no DOM state.

const MOD_ORDER = ["ctrl", "shift", "alt", "cmd"] as const;
const ALIASES: Record<string, string> = {
  meta: "cmd", command: "cmd", super: "cmd", win: "cmd", control: "ctrl", option: "alt", opt: "alt",
  esc: "escape", return: "enter", del: "delete", arrowleft: "left", arrowright: "right", arrowup: "up", arrowdown: "down",
  backtick: "`", space: "space", spacebar: "space",
};
const CODE_NAMES: Record<string, string> = {
  Backquote: "`", Minus: "-", Equal: "=", BracketLeft: "[", BracketRight: "]", Backslash: "\\", Semicolon: ";",
  Quote: "'", Comma: ",", Period: ".", Slash: "/", Space: "space", Enter: "enter", Tab: "tab", Escape: "escape",
  Backspace: "backspace", Delete: "delete", ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down",
  Home: "home", End: "end", PageUp: "pageup", PageDown: "pagedown",
};

function build(mods: Set<string>, key: string): string {
  return [...MOD_ORDER.filter((m) => mods.has(m)), key].join("+");
}

/** Normalise one stroke ("Cmd+Shift+P") to canonical lowercase "shift+cmd+p". */
export function normStroke(s: string): string {
  const parts = s.trim().toLowerCase().split("+");
  let key = parts.pop() ?? "";
  if (key === "" && parts.length && s.trim().endsWith("++")) key = "+";
  const mods = new Set<string>();
  for (const p of parts) if (p) mods.add(ALIASES[p] ?? p);
  return build(mods, ALIASES[key] ?? key);
}

/** Normalise a chord ("cmd+k cmd+s") to a list of canonical strokes. */
export function parseChord(s: string): string[] {
  return s.trim().split(/\s+/).filter(Boolean).map(normStroke);
}

export function eventStroke(e: KeyboardEvent): string | null {
  if (["Shift", "Control", "Alt", "Meta", "CapsLock"].includes(e.key)) return null;
  let key: string;
  if (CODE_NAMES[e.code]) key = CODE_NAMES[e.code];
  else if (/^Key[A-Z]$/.test(e.code)) key = e.code.slice(3).toLowerCase();
  else if (/^Digit\d$/.test(e.code)) key = e.code.slice(5);
  else if (/^F\d+$/.test(e.code)) key = e.code.toLowerCase();
  else key = e.key.toLowerCase();
  const mods = new Set<string>();
  if (e.ctrlKey) mods.add("ctrl");
  if (e.shiftKey) mods.add("shift");
  if (e.altKey) mods.add("alt");
  if (e.metaKey) mods.add("cmd");
  return build(mods, key);
}

const SYMBOLS: Record<string, string> = {
  ctrl: "⌃", alt: "⌥", shift: "⇧", cmd: "⌘", enter: "⏎", escape: "⎋", left: "←", right: "→", up: "↑", down: "↓",
  backspace: "⌫", delete: "⌦", tab: "⇥", space: "Space",
};
export function strokeLabel(stroke: string): string {
  const parts = stroke.split("+");
  const key = parts.pop() ?? "";
  const mods = parts.map((m) => SYMBOLS[m] ?? m).join("");
  const k = SYMBOLS[key] ?? (key.length === 1 ? key.toUpperCase() : key.toUpperCase());
  return mods + k;
}
export function chordLabel(chord: string[]): string {
  return chord.map(strokeLabel).join(" ");
}

// ---- when expressions: && || ! ( ) == != over context keys ----
type Node =
  | { t: "key"; k: string }
  | { t: "lit"; v: string }
  | { t: "not"; a: Node }
  | { t: "and" | "or"; a: Node; b: Node }
  | { t: "eq" | "ne"; a: Node; b: Node };

function tokenize(src: string): string[] {
  const out: string[] = [];
  const re = /\s*(&&|\|\|\||\|\||==|!=|!|\(|\)|'[^']*'|"[^"]*"|[^\s&|!()=]+)/gy;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src))) out.push(m[1]);
  return out;
}

function parse(src: string): Node | null {
  const toks = tokenize(src);
  let i = 0;
  const peek = () => toks[i];
  const orE = (): Node => {
    let a = andE();
    while (peek() === "||") { i++; a = { t: "or", a, b: andE() }; }
    return a;
  };
  const andE = (): Node => {
    let a = eqE();
    while (peek() === "&&") { i++; a = { t: "and", a, b: eqE() }; }
    return a;
  };
  const eqE = (): Node => {
    const a = unary();
    if (peek() === "==" || peek() === "!=") {
      const t = toks[i++] === "==" ? "eq" : "ne";
      return { t, a, b: unary() };
    }
    return a;
  };
  const unary = (): Node => {
    const t = toks[i++];
    if (t === "!") return { t: "not", a: unary() };
    if (t === "(") { const n = orE(); if (peek() === ")") i++; return n; }
    if (t === undefined) return { t: "lit", v: "" };
    if (t[0] === "'" || t[0] === '"') return { t: "lit", v: t.slice(1, -1) };
    return { t: "key", k: t };
  };
  if (!toks.length) return null;
  try { return orE(); } catch { return null; }
}

const cache = new Map<string, Node | null>();
export function evalWhen(src: string | undefined, get: (k: string) => unknown): boolean {
  if (!src || !src.trim()) return true;
  let n = cache.get(src);
  if (n === undefined) { n = parse(src); cache.set(src, n); }
  if (!n) return false;
  const val = (x: Node): unknown => (x.t === "key" ? get(x.k) : x.t === "lit" ? x.v : ev(x));
  const ev = (x: Node): boolean => {
    switch (x.t) {
      case "key": return !!get(x.k);
      case "lit": return !!x.v;
      case "not": return !ev(x.a);
      case "and": return ev(x.a) && ev(x.b);
      case "or": return ev(x.a) || ev(x.b);
      case "eq": return String(val(x.a)) === String(val(x.b));
      case "ne": return String(val(x.a)) !== String(val(x.b));
    }
  };
  return ev(n);
}
