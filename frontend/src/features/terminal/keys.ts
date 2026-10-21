// Default chords for terminal commands. Also used by xterm's custom key handler so the
// shell never receives these chords.
export const CHORDS: Record<string, string[]> = {
  "terminal.split.right": ["cmd+d", "cmd+\\"],
  "terminal.split.down": ["cmd+shift+d"],
  "terminal.focus.left": ["cmd+alt+left"],
  "terminal.focus.right": ["cmd+alt+right"],
  "terminal.focus.up": ["cmd+alt+up"],
  "terminal.focus.down": ["cmd+alt+down"],
  "terminal.resize.left": ["ctrl+cmd+left"],
  "terminal.resize.right": ["ctrl+cmd+right"],
  "terminal.resize.up": ["ctrl+cmd+up"],
  "terminal.resize.down": ["ctrl+cmd+down"],
  "terminal.maximize": ["cmd+shift+enter"],
  "terminal.close": ["cmd+w"],
  "terminal.newTab": ["cmd+t"],
  "terminal.nextTab": ["cmd+shift+]"],
  "terminal.prevTab": ["cmd+shift+["],
  "terminal.clear": ["cmd+k"],
  "terminal.find": ["cmd+f"],
  "terminal.sendSelectionToAgent": ["cmd+l"],
};

const CODE: Record<string, string> = {
  "[": "BracketLeft", "]": "BracketRight", "\\": "Backslash", left: "ArrowLeft", right: "ArrowRight",
  up: "ArrowUp", down: "ArrowDown", enter: "Enter",
};

type Parsed = { meta: boolean; ctrl: boolean; alt: boolean; shift: boolean; code: string };
const parsed: Parsed[] = Object.values(CHORDS).flat().map((c) => {
  const parts = c.split("+");
  const k = parts[parts.length - 1];
  const mods = parts.slice(0, -1);
  return {
    meta: mods.includes("cmd"), ctrl: mods.includes("ctrl"), alt: mods.includes("alt"), shift: mods.includes("shift"),
    code: CODE[k] ?? (k.length === 1 && /[a-z]/.test(k) ? "Key" + k.toUpperCase() : k),
  };
});

export function isReservedChord(e: KeyboardEvent): boolean {
  return parsed.some((p) => p.code === e.code && p.meta === e.metaKey && p.ctrl === e.ctrlKey && p.alt === e.altKey && p.shift === e.shiftKey);
}
