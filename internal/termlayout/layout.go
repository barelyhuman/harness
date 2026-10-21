package termlayout

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Kind is the terminal kind of a leaf.
type Kind string

const (
	KindShell Kind = "shell"
	KindSetup Kind = "setup"
	KindAgent Kind = "agent"
)

// Dir is a split direction. Row lays children out left to right, Col top to bottom.
type Dir string

const (
	DirRow Dir = "row"
	DirCol Dir = "col"
)

// Direction is a focus/resize direction.
type Direction int

const (
	Left Direction = iota
	Right
	Up
	Down
)

const (
	// ResizeStep is the ratio moved by one ResizeDir call.
	ResizeStep = 0.05
	// MinRatio is the smallest share a child may have.
	MinRatio = 0.1
	eps      = 1e-9
)

// Node types used as the JSON "type" discriminator.
const (
	TypeLeaf  = "leaf"
	TypeSplit = "split"
)

var (
	ErrNoTab     = errors.New("termlayout: no such tab")
	ErrNoPane    = errors.New("termlayout: no such pane")
	ErrBadArg    = errors.New("termlayout: invalid argument")
	ErrNoActive  = errors.New("termlayout: no active tab")
	ErrBadLayout = errors.New("termlayout: invalid layout")
	ErrNoSplitAt = errors.New("termlayout: path does not address a split")
)

// Node is a leaf or a split. Leaf fields and split fields are mutually exclusive.
type Node struct {
	Type string `json:"type"` // "leaf" | "split"

	PaneID PaneID `json:"paneId,omitempty"`
	PtyID  string `json:"ptyId,omitempty"`
	Kind   Kind   `json:"kind,omitempty"`

	Dir      Dir       `json:"dir,omitempty"`
	Ratios   []float64 `json:"ratios,omitempty"`
	Children []Node    `json:"children,omitempty"`
}

// PaneID identifies a leaf; it is unique within a Layout.
type PaneID = string

// Tab is one terminal tab with its split tree.
type Tab struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Root      Node   `json:"root"`
	Focused   PaneID `json:"focused"`
	Maximized bool   `json:"maximized"`
}

// Layout is all terminal tabs of one scope.
type Layout struct {
	Tabs   []Tab  `json:"tabs"`
	Active string `json:"active"` // active tab id, "" when there are no tabs
	Seq    int    `json:"seq"`    // id counter, persisted so restored layouts never reuse ids
}

// Rect is a rectangle in the unit square.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// PaneInfo is one flattened leaf with its rect.
type PaneInfo struct {
	TabID   string `json:"tabId"`
	PaneID  PaneID `json:"paneId"`
	PtyID   string `json:"ptyId,omitempty"`
	Kind    Kind   `json:"kind"`
	Rect    Rect   `json:"rect"`
	Focused bool   `json:"focused"`
}

// New returns an empty layout.
func New() *Layout { return &Layout{Tabs: []Tab{}} }

func (n Node) isLeaf() bool { return n.Type == TypeLeaf }

func (n Node) clone() Node {
	c := n
	if n.Ratios != nil {
		c.Ratios = append([]float64(nil), n.Ratios...)
	}
	if n.Children != nil {
		c.Children = make([]Node, len(n.Children))
		for i, ch := range n.Children {
			c.Children[i] = ch.clone()
		}
	}
	return c
}

// Clone returns a deep copy.
func (l *Layout) Clone() Layout {
	c := Layout{Tabs: make([]Tab, len(l.Tabs)), Active: l.Active, Seq: l.Seq}
	for i, t := range l.Tabs {
		t.Root = t.Root.clone()
		c.Tabs[i] = t
	}
	return c
}

func (l *Layout) nextID(prefix string) string {
	l.Seq++
	return prefix + strconv.Itoa(l.Seq)
}

func (l *Layout) tabIndex(id string) int {
	for i := range l.Tabs {
		if l.Tabs[i].ID == id {
			return i
		}
	}
	return -1
}

// findPane returns the tab index containing the pane, or -1.
func (l *Layout) findPane(id PaneID) int {
	if id == "" {
		return -1
	}
	for i := range l.Tabs {
		if findLeaf(&l.Tabs[i].Root, id) != nil {
			return i
		}
	}
	return -1
}

func findLeaf(n *Node, id PaneID) *Node {
	if n.isLeaf() {
		if n.PaneID == id {
			return n
		}
		return nil
	}
	for i := range n.Children {
		if r := findLeaf(&n.Children[i], id); r != nil {
			return r
		}
	}
	return nil
}

func (l *Layout) activeTab() (*Tab, error) {
	i := l.tabIndex(l.Active)
	if i < 0 {
		return nil, ErrNoActive
	}
	return &l.Tabs[i], nil
}

// NewTab appends and activates a tab holding one pane of the given kind.
// An unknown kind falls back to shell.
func (l *Layout) NewTab(kind Kind) (tabID string, paneID PaneID) {
	switch kind {
	case KindShell, KindSetup, KindAgent:
	default:
		kind = KindShell
	}
	tabID, paneID = l.nextID("t"), l.nextID("p")
	title := strings.ToUpper(string(kind[:1])) + string(kind[1:])
	l.Tabs = append(l.Tabs, Tab{
		ID: tabID, Title: title, Focused: paneID,
		Root: Node{Type: TypeLeaf, PaneID: paneID, Kind: kind},
	})
	l.Active = tabID
	return
}

// Split splits the pane in direction dir and returns the new pane's id. The new
// pane has the same kind as the old one, is placed after it and gets focus.
// If the parent already splits in dir, a sibling is inserted instead of nesting
// and the old pane's share is divided equally. Splitting clears maximize.
func (l *Layout) Split(paneID PaneID, dir Dir) (PaneID, error) {
	if dir != DirRow && dir != DirCol {
		return "", fmt.Errorf("%w: dir %q", ErrBadArg, dir)
	}
	ti := l.findPane(paneID)
	if ti < 0 {
		return "", fmt.Errorf("%w: %q", ErrNoPane, paneID)
	}
	t := &l.Tabs[ti]
	newID := l.nextID("p")
	split(&t.Root, nil, -1, paneID, newID, dir)
	t.Focused = newID
	t.Maximized = false
	l.Active = t.ID
	return newID, nil
}

func split(n *Node, parent *Node, idx int, id, newID PaneID, dir Dir) bool {
	if n.isLeaf() {
		if n.PaneID != id {
			return false
		}
		nl := Node{Type: TypeLeaf, PaneID: newID, Kind: n.Kind}
		if parent != nil && parent.Dir == dir {
			r := parent.Ratios[idx] / 2
			parent.Ratios = insertF(parent.Ratios, idx, r)
			parent.Ratios[idx+1] = r
			parent.Children = append(parent.Children, Node{})
			copy(parent.Children[idx+2:], parent.Children[idx+1:])
			parent.Children[idx+1] = nl
			return true
		}
		old := *n
		*n = Node{Type: TypeSplit, Dir: dir, Ratios: []float64{0.5, 0.5}, Children: []Node{old, nl}}
		return true
	}
	for i := range n.Children {
		if split(&n.Children[i], n, i, id, newID, dir) {
			return true
		}
	}
	return false
}

func insertF(s []float64, i int, v float64) []float64 {
	s = append(s, 0)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// Close removes a pane and returns the pane that has focus afterwards. If the
// closed pane was not focused, focus is unchanged. Closing a tab's last pane
// removes the tab; the returned pane is then the new active tab's focus ("" if
// no tabs remain).
func (l *Layout) Close(paneID PaneID) (PaneID, error) {
	ti := l.findPane(paneID)
	if ti < 0 {
		return "", fmt.Errorf("%w: %q", ErrNoPane, paneID)
	}
	t := &l.Tabs[ti]
	if t.Root.isLeaf() {
		wasActive := l.Active == t.ID
		l.Tabs = append(l.Tabs[:ti], l.Tabs[ti+1:]...)
		if wasActive {
			l.Active = ""
			if len(l.Tabs) > 0 {
				ni := ti
				if ni >= len(l.Tabs) {
					ni = len(l.Tabs) - 1
				}
				l.Active = l.Tabs[ni].ID
			}
		}
		if at, err := l.activeTab(); err == nil {
			return at.Focused, nil
		}
		return "", nil
	}
	next := removeLeaf(&t.Root, paneID)
	normalize(&t.Root)
	if t.Focused == paneID {
		t.Focused = next
	}
	return t.Focused, nil
}

// removeLeaf deletes the leaf, returning the first leaf of the sibling that
// inherits focus. n must be a split containing the leaf.
func removeLeaf(n *Node, id PaneID) PaneID {
	for i := range n.Children {
		c := &n.Children[i]
		if c.isLeaf() && c.PaneID == id {
			n.Children = append(n.Children[:i], n.Children[i+1:]...)
			n.Ratios = append(n.Ratios[:i], n.Ratios[i+1:]...)
			renorm(n.Ratios)
			j := i - 1
			if j < 0 {
				j = 0
			}
			return firstLeaf(&n.Children[j])
		}
		if !c.isLeaf() {
			if r := removeLeaf(c, id); r != "" {
				return r
			}
		}
	}
	return ""
}

func firstLeaf(n *Node) PaneID {
	for !n.isLeaf() {
		n = &n.Children[0]
	}
	return n.PaneID
}

// normalize collapses single-child splits and merges same-direction nested
// splits into their parent (scaling ratios).
func normalize(n *Node) {
	if n.isLeaf() {
		return
	}
	for i := range n.Children {
		normalize(&n.Children[i])
	}
	if len(n.Children) == 1 {
		*n = n.Children[0]
		return
	}
	var ch []Node
	var rs []float64
	for i, c := range n.Children {
		if !c.isLeaf() && c.Dir == n.Dir {
			for j, cc := range c.Children {
				ch = append(ch, cc)
				rs = append(rs, n.Ratios[i]*c.Ratios[j])
			}
		} else {
			ch = append(ch, c)
			rs = append(rs, n.Ratios[i])
		}
	}
	n.Children, n.Ratios = ch, rs
}

func renorm(r []float64) {
	var s float64
	for _, v := range r {
		s += v
	}
	if s <= 0 {
		for i := range r {
			r[i] = 1 / float64(len(r))
		}
		return
	}
	for i := range r {
		r[i] /= s
	}
}

// clampRatios normalises r to sum 1 with every entry >= min(MinRatio, 1/n).
func clampRatios(r []float64) {
	renorm(r)
	min := math.Min(MinRatio, 1/float64(len(r)))
	for iter := 0; iter < len(r); iter++ {
		var low, free float64
		var nfree int
		for _, v := range r {
			if v < min-eps {
				low += min
			} else {
				free += v
				nfree++
			}
		}
		if low == 0 {
			return
		}
		scale := (1 - low) / free
		for i, v := range r {
			if v < min-eps {
				r[i] = min
			} else {
				r[i] = v * scale
			}
		}
		_ = nfree
	}
}

// Focus focuses a pane and activates its tab.
func (l *Layout) Focus(paneID PaneID) error {
	ti := l.findPane(paneID)
	if ti < 0 {
		return fmt.Errorf("%w: %q", ErrNoPane, paneID)
	}
	l.Tabs[ti].Focused = paneID
	l.Active = l.Tabs[ti].ID
	return nil
}

func leafRects(n *Node, r Rect, out *[]PaneInfo) {
	if n.isLeaf() {
		*out = append(*out, PaneInfo{PaneID: n.PaneID, PtyID: n.PtyID, Kind: n.Kind, Rect: r})
		return
	}
	off := 0.0
	for i := range n.Children {
		c := r
		if n.Dir == DirRow {
			c.X, c.W = r.X+off*r.W, r.W*n.Ratios[i]
		} else {
			c.Y, c.H = r.Y+off*r.H, r.H*n.Ratios[i]
		}
		off += n.Ratios[i]
		leafRects(&n.Children[i], c, out)
	}
}

func (t *Tab) allPanes() []PaneInfo {
	var out []PaneInfo
	leafRects(&t.Root, Rect{0, 0, 1, 1}, &out)
	for i := range out {
		out[i].TabID = t.ID
		out[i].Focused = out[i].PaneID == t.Focused
	}
	return out
}

// Panes returns the active tab's panes with rects in the unit square. When the
// tab is maximized only the focused pane is returned, filling the square.
func (l *Layout) Panes() []PaneInfo {
	i := l.tabIndex(l.Active)
	if i < 0 {
		return []PaneInfo{}
	}
	ps, _ := l.PanesOf(l.Tabs[i].ID)
	return ps
}

// PanesOf is Panes for a specific tab.
func (l *Layout) PanesOf(tabID string) ([]PaneInfo, error) {
	i := l.tabIndex(tabID)
	if i < 0 {
		return nil, fmt.Errorf("%w: %q", ErrNoTab, tabID)
	}
	t := &l.Tabs[i]
	ps := t.allPanes()
	if t.Maximized {
		for _, p := range ps {
			if p.PaneID == t.Focused {
				p.Rect = Rect{0, 0, 1, 1}
				return []PaneInfo{p}, nil
			}
		}
	}
	return ps, nil
}

// FocusDir moves focus to the nearest pane in direction d (see package doc for
// the rule). It returns the focused pane afterwards and whether focus moved.
func (l *Layout) FocusDir(d Direction) (PaneID, bool, error) {
	t, err := l.activeTab()
	if err != nil {
		return "", false, err
	}
	if d < Left || d > Down {
		return "", false, fmt.Errorf("%w: direction %d", ErrBadArg, d)
	}
	ps := t.allPanes()
	var f *PaneInfo
	for i := range ps {
		if ps[i].PaneID == t.Focused {
			f = &ps[i]
		}
	}
	if f == nil {
		return t.Focused, false, nil
	}
	var best *PaneInfo
	bestDist, bestOv := math.Inf(1), 0.0
	for i := range ps {
		c := &ps[i]
		if c.PaneID == f.PaneID {
			continue
		}
		var dist, ov float64
		switch d {
		case Right:
			dist = c.Rect.X - (f.Rect.X + f.Rect.W)
			ov = overlap(f.Rect.Y, f.Rect.H, c.Rect.Y, c.Rect.H)
		case Left:
			dist = f.Rect.X - (c.Rect.X + c.Rect.W)
			ov = overlap(f.Rect.Y, f.Rect.H, c.Rect.Y, c.Rect.H)
		case Down:
			dist = c.Rect.Y - (f.Rect.Y + f.Rect.H)
			ov = overlap(f.Rect.X, f.Rect.W, c.Rect.X, c.Rect.W)
		case Up:
			dist = f.Rect.Y - (c.Rect.Y + c.Rect.H)
			ov = overlap(f.Rect.X, f.Rect.W, c.Rect.X, c.Rect.W)
		}
		if dist < -eps || ov <= eps {
			continue
		}
		if dist < bestDist-eps || (math.Abs(dist-bestDist) <= eps && ov > bestOv+eps) {
			best, bestDist, bestOv = c, dist, ov
		}
	}
	if best == nil {
		return t.Focused, false, nil
	}
	t.Focused = best.PaneID
	return t.Focused, true, nil
}

func overlap(a, al, b, bl float64) float64 {
	return math.Min(a+al, b+bl) - math.Max(a, b)
}

// ResizeDir moves a boundary of the focused pane by ResizeStep. The boundary
// belongs to the nearest ancestor split along the direction's axis (Left/Right
// -> row, Up/Down -> col): it is the focused child's trailing edge, or its
// leading edge if it is the last child. Right/Down move the boundary towards
// larger coordinates, Left/Up towards smaller. Ratios never drop below
// MinRatio; the move is reduced to fit. Returns whether anything changed.
func (l *Layout) ResizeDir(d Direction) (bool, error) {
	t, err := l.activeTab()
	if err != nil {
		return false, err
	}
	if d < Left || d > Down {
		return false, fmt.Errorf("%w: direction %d", ErrBadArg, d)
	}
	axis := DirRow
	if d == Up || d == Down {
		axis = DirCol
	}
	// path of splits from root to the focused leaf
	var chain []*Node
	var idxs []int
	var walk func(n *Node) bool
	walk = func(n *Node) bool {
		if n.isLeaf() {
			return n.PaneID == t.Focused
		}
		for i := range n.Children {
			chain, idxs = append(chain, n), append(idxs, i)
			if walk(&n.Children[i]) {
				return true
			}
			chain, idxs = chain[:len(chain)-1], idxs[:len(idxs)-1]
		}
		return false
	}
	if !walk(&t.Root) {
		return false, fmt.Errorf("%w: %q", ErrNoPane, t.Focused)
	}
	for k := len(chain) - 1; k >= 0; k-- {
		s, i := chain[k], idxs[k]
		if s.Dir != axis {
			continue
		}
		a, b := i, i+1 // boundary between children a and b
		if i == len(s.Children)-1 {
			a, b = i-1, i
		}
		delta := ResizeStep
		if d == Left || d == Up {
			delta = -delta
		}
		// delta>0 grows a and shrinks b
		if delta > 0 {
			delta = math.Min(delta, s.Ratios[b]-MinRatio)
		} else {
			delta = -math.Min(-delta, s.Ratios[a]-MinRatio)
		}
		if math.Abs(delta) <= eps {
			return false, nil
		}
		s.Ratios[a] += delta
		s.Ratios[b] -= delta
		return true, nil
	}
	return false, nil
}

// SetRatios sets the ratios of the split addressed by path in tab tabID. The
// path is the list of child indices from the tab root down to the split (empty
// means the root), matching the tree the frontend renders. Ratios are
// normalised to sum 1 and clamped to MinRatio; their count must equal the
// split's child count.
func (l *Layout) SetRatios(tabID string, path []int, ratios []float64) error {
	ti := l.tabIndex(tabID)
	if ti < 0 {
		return fmt.Errorf("%w: %q", ErrNoTab, tabID)
	}
	n := &l.Tabs[ti].Root
	for _, i := range path {
		if n.isLeaf() || i < 0 || i >= len(n.Children) {
			return ErrNoSplitAt
		}
		n = &n.Children[i]
	}
	if n.isLeaf() {
		return ErrNoSplitAt
	}
	if len(ratios) != len(n.Children) {
		return fmt.Errorf("%w: want %d ratios, got %d", ErrBadArg, len(n.Children), len(ratios))
	}
	for _, r := range ratios {
		if math.IsNaN(r) || math.IsInf(r, 0) || r < 0 {
			return fmt.Errorf("%w: ratio %v", ErrBadArg, r)
		}
	}
	rs := append([]float64(nil), ratios...)
	var s float64
	for _, r := range rs {
		s += r
	}
	if s <= 0 {
		return fmt.Errorf("%w: ratios sum to zero", ErrBadArg)
	}
	clampRatios(rs)
	n.Ratios = rs
	return nil
}

// ToggleMaximize toggles the maximize flag of the active tab.
func (l *Layout) ToggleMaximize() error {
	t, err := l.activeTab()
	if err != nil {
		return err
	}
	t.Maximized = !t.Maximized
	return nil
}

// NextTab activates the next tab, wrapping around.
func (l *Layout) NextTab() error { return l.stepTab(1) }

// PrevTab activates the previous tab, wrapping around.
func (l *Layout) PrevTab() error { return l.stepTab(-1) }

func (l *Layout) stepTab(d int) error {
	i := l.tabIndex(l.Active)
	if i < 0 {
		return ErrNoActive
	}
	n := len(l.Tabs)
	l.Active = l.Tabs[((i+d)%n+n)%n].ID
	return nil
}

// ActivateTab makes a tab active.
func (l *Layout) ActivateTab(tabID string) error {
	if l.tabIndex(tabID) < 0 {
		return fmt.Errorf("%w: %q", ErrNoTab, tabID)
	}
	l.Active = tabID
	return nil
}

// RenameTab sets a tab title.
func (l *Layout) RenameTab(tabID, title string) error {
	i := l.tabIndex(tabID)
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrNoTab, tabID)
	}
	l.Tabs[i].Title = title
	return nil
}

// MoveTab moves the tab at index from to index to. The active tab is unchanged.
func (l *Layout) MoveTab(from, to int) error {
	n := len(l.Tabs)
	if from < 0 || from >= n || to < 0 || to >= n {
		return fmt.Errorf("%w: move %d -> %d with %d tabs", ErrBadArg, from, to, n)
	}
	t := l.Tabs[from]
	l.Tabs = append(l.Tabs[:from], l.Tabs[from+1:]...)
	l.Tabs = append(l.Tabs, Tab{})
	copy(l.Tabs[to+1:], l.Tabs[to:])
	l.Tabs[to] = t
	return nil
}

// AttachPty records the PTY id of a pane (empty detaches).
func (l *Layout) AttachPty(paneID PaneID, ptyID string) error {
	ti := l.findPane(paneID)
	if ti < 0 {
		return fmt.Errorf("%w: %q", ErrNoPane, paneID)
	}
	findLeaf(&l.Tabs[ti].Root, paneID).PtyID = ptyID
	return nil
}

// Validate checks structural invariants (use after decoding persisted JSON).
// It also raises Seq above any numeric id so new ids never collide.
func (l *Layout) Validate() error {
	if l.Tabs == nil {
		l.Tabs = []Tab{}
	}
	seen := map[string]bool{}
	for i := range l.Tabs {
		t := &l.Tabs[i]
		if t.ID == "" || seen["t:"+t.ID] {
			return fmt.Errorf("%w: bad tab id %q", ErrBadLayout, t.ID)
		}
		seen["t:"+t.ID] = true
		bump(&l.Seq, t.ID)
		if err := validateNode(&t.Root, seen, &l.Seq); err != nil {
			return err
		}
		if findLeaf(&t.Root, t.Focused) == nil {
			return fmt.Errorf("%w: tab %q focused pane %q missing", ErrBadLayout, t.ID, t.Focused)
		}
	}
	if len(l.Tabs) == 0 {
		l.Active = ""
	} else if l.tabIndex(l.Active) < 0 {
		return fmt.Errorf("%w: active tab %q missing", ErrBadLayout, l.Active)
	}
	return nil
}

func bump(seq *int, id string) {
	if len(id) > 1 {
		if v, err := strconv.Atoi(id[1:]); err == nil && v > *seq {
			*seq = v
		}
	}
}

func validateNode(n *Node, seen map[string]bool, seq *int) error {
	switch n.Type {
	case TypeLeaf:
		if n.PaneID == "" || seen["p:"+n.PaneID] {
			return fmt.Errorf("%w: bad pane id %q", ErrBadLayout, n.PaneID)
		}
		seen["p:"+n.PaneID] = true
		bump(seq, n.PaneID)
	case TypeSplit:
		if (n.Dir != DirRow && n.Dir != DirCol) || len(n.Children) < 2 || len(n.Ratios) != len(n.Children) {
			return fmt.Errorf("%w: bad split", ErrBadLayout)
		}
		var s float64
		for _, r := range n.Ratios {
			if !(r > 0) {
				return fmt.Errorf("%w: bad ratio %v", ErrBadLayout, r)
			}
			s += r
		}
		if math.Abs(s-1) > 1e-6 {
			return fmt.Errorf("%w: ratios sum to %v", ErrBadLayout, s)
		}
		for i := range n.Children {
			if err := validateNode(&n.Children[i], seen, seq); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: node type %q", ErrBadLayout, n.Type)
	}
	return nil
}
