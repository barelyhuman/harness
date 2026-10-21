// Package termlayout is the pure state machine for terminal tabs and split
// panes in one scope. It performs no I/O and never spawns PTYs: PTY ids are
// recorded with AttachPty. Go owns all layout logic; the frontend sends
// intents and renders the Layout pushed on "termlayout/<scope>".
//
// Geometry: Dir "row" lays children out left to right, Dir "col" stacks them
// top to bottom. Split(pane, DirRow) puts the new pane to the right of the
// old one, Split(pane, DirCol) puts it below.
//
// JSON shape (stable, persisted to ui_state):
//
//	{"tabs":[{"id":"t1","title":"Shell","focused":"p2","maximized":false,
//	  "root":{"type":"split","dir":"row","ratios":[0.5,0.5],"children":[
//	    {"type":"leaf","paneId":"p1","ptyId":"pty-9","kind":"shell"},
//	    {"type":"leaf","paneId":"p2","kind":"shell"}]}}],
//	 "active":"t1","seq":2}
//
// Layout and Tab methods are not synchronised; use Manager for concurrent use.
package termlayout
