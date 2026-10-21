package termlayout

import (
	"encoding/json"
	"math"
	"reflect"
	"sync"
	"testing"
)

// grid2x2 builds: col[ row[a,b], row[c,d] ] and returns ids a,b,c,d.
func grid2x2(t *testing.T) (*Layout, [4]PaneID) {
	t.Helper()
	l := New()
	_, a := l.NewTab(KindShell)
	c, err := l.Split(a, DirCol)
	must(t, err)
	b, err := l.Split(a, DirRow)
	must(t, err)
	d, err := l.Split(c, DirRow)
	must(t, err)
	return l, [4]PaneID{a, b, c, d}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestFocusDirGrid(t *testing.T) {
	_, ids := grid2x2(t)
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]
	tests := []struct {
		from  PaneID
		dir   Direction
		want  PaneID
		moved bool
	}{
		{a, Right, b, true}, {a, Down, c, true}, {a, Left, a, false}, {a, Up, a, false},
		{b, Left, a, true}, {b, Down, d, true}, {b, Right, b, false}, {b, Up, b, false},
		{c, Up, a, true}, {c, Right, d, true}, {c, Left, c, false}, {c, Down, c, false},
		{d, Up, b, true}, {d, Left, c, true}, {d, Right, d, false}, {d, Down, d, false},
	}
	for _, tc := range tests {
		l, ids := grid2x2(t)
		_ = ids
		must(t, l.Focus(tc.from))
		got, moved, err := l.FocusDir(tc.dir)
		must(t, err)
		if got != tc.want || moved != tc.moved {
			t.Errorf("from %s dir %d: got %s,%v want %s,%v", tc.from, tc.dir, got, moved, tc.want, tc.moved)
		}
	}
}

func TestFocusDirTieAndDistance(t *testing.T) {
	// row[ A, col[ B(0.7), C(0.3) ] ]: from A going right, B has more overlap than C.
	l := New()
	_, a := l.NewTab(KindShell)
	b, _ := l.Split(a, DirRow)
	c, _ := l.Split(b, DirCol)
	must(t, l.SetRatios("t1", []int{1}, []float64{0.7, 0.3}))
	must(t, l.Focus(a))
	got, _, _ := l.FocusDir(Right)
	if got != b {
		t.Errorf("want %s got %s", b, got)
	}
	must(t, l.Focus(c))
	got, _, _ = l.FocusDir(Left)
	if got != a {
		t.Errorf("want %s got %s", a, got)
	}
}

func TestSplitFlatten(t *testing.T) {
	l := New()
	_, a := l.NewTab(KindShell)
	b, _ := l.Split(a, DirRow)
	c, _ := l.Split(b, DirRow)
	root := l.Tabs[0].Root
	if root.Type != TypeSplit || len(root.Children) != 3 {
		t.Fatalf("not flattened: %+v", root)
	}
	want := []float64{0.5, 0.25, 0.25}
	if !reflect.DeepEqual(root.Ratios, want) {
		t.Errorf("ratios %v want %v", root.Ratios, want)
	}
	if root.Children[2].PaneID != c {
		t.Errorf("order wrong")
	}
	// different direction nests
	d, _ := l.Split(b, DirCol)
	if l.Tabs[0].Root.Children[1].Type != TypeSplit || l.Tabs[0].Root.Children[1].Dir != DirCol {
		t.Errorf("expected nested col split")
	}
	if l.Tabs[0].Focused != d {
		t.Errorf("new pane should be focused")
	}
}

func TestClose(t *testing.T) {
	l := New()
	tab, a := l.NewTab(KindShell)
	b, _ := l.Split(a, DirRow)
	c, _ := l.Split(b, DirRow) // ratios .5 .25 .25
	next, err := l.Close(c)
	must(t, err)
	if next != b {
		t.Errorf("focus next %s want %s", next, b)
	}
	r := l.Tabs[0].Root
	if len(r.Children) != 2 || !approx(r.Ratios[0], 2.0/3) || !approx(r.Ratios[1], 1.0/3) {
		t.Errorf("renormalise failed: %v", r.Ratios)
	}
	_, err = l.Close(b)
	must(t, err)
	if !l.Tabs[0].Root.isLeaf() || l.Tabs[0].Root.PaneID != a {
		t.Errorf("single child should collapse: %+v", l.Tabs[0].Root)
	}
	// collapse merges into same-direction grandparent
	b, _ = l.Split(a, DirRow)
	m, _ := l.Split(b, DirCol)
	if _, err := l.Close(m); err != nil {
		t.Fatal(err)
	}
	if len(l.Tabs[0].Root.Children) != 2 || !l.Tabs[0].Root.Children[1].isLeaf() {
		t.Errorf("bad collapse: %+v", l.Tabs[0].Root)
	}
	// last pane removes tab
	t2, p2 := l.NewTab(KindAgent)
	got, err := l.Close(p2)
	must(t, err)
	if l.tabIndex(t2) >= 0 || l.Active != tab || got != l.Tabs[0].Focused {
		t.Errorf("tab not removed: active=%s got=%s", l.Active, got)
	}
	if _, err := l.Close("nope"); err == nil {
		t.Error("want error")
	}
	for _, p := range l.Panes() {
		if _, err := l.Close(p.PaneID); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.Tabs) != 0 || l.Active != "" {
		t.Errorf("expected empty layout")
	}
}

func TestResizeClamp(t *testing.T) {
	l := New()
	_, a := l.NewTab(KindShell)
	b, _ := l.Split(a, DirRow)
	must(t, l.Focus(a))
	if ok, _ := l.ResizeDir(Up); ok {
		t.Error("no col ancestor: no-op expected")
	}
	ok, err := l.ResizeDir(Right)
	must(t, err)
	r := l.Tabs[0].Root.Ratios
	if !ok || !approx(r[0], 0.55) || !approx(r[1], 0.45) {
		t.Errorf("ratios %v", r)
	}
	for i := 0; i < 20; i++ {
		l.ResizeDir(Right)
	}
	r = l.Tabs[0].Root.Ratios
	if !approx(r[1], MinRatio) || !approx(r[0]+r[1], 1) {
		t.Errorf("clamp failed %v", r)
	}
	if ok, _ := l.ResizeDir(Right); ok {
		t.Error("should be no-op at clamp")
	}
	// last child: leading edge moves
	must(t, l.Focus(b))
	l.ResizeDir(Left)
	r = l.Tabs[0].Root.Ratios
	if !approx(r[0], 0.85) {
		t.Errorf("leading edge: %v", r)
	}
	// SetRatios clamps
	must(t, l.SetRatios("t1", nil, []float64{0.99, 0.01}))
	r = l.Tabs[0].Root.Ratios
	if !approx(r[1], MinRatio) || !approx(r[0], 0.9) {
		t.Errorf("SetRatios clamp %v", r)
	}
}

func TestSetRatiosErrors(t *testing.T) {
	l := New()
	tab, a := l.NewTab(KindShell)
	l.Split(a, DirRow)
	tests := []struct {
		name string
		tab  string
		path []int
		r    []float64
	}{
		{"bad tab", "zz", nil, []float64{1, 1}},
		{"leaf path", tab, []int{0}, []float64{1, 1}},
		{"bad index", tab, []int{5}, []float64{1, 1}},
		{"len mismatch", tab, nil, []float64{1}},
		{"nan", tab, nil, []float64{math.NaN(), 1}},
		{"zero", tab, nil, []float64{0, 0}},
	}
	for _, tc := range tests {
		if err := l.SetRatios(tc.tab, tc.path, tc.r); err == nil {
			t.Errorf("%s: want error", tc.name)
		}
	}
}

func TestMaximizeAndPanes(t *testing.T) {
	l, ids := grid2x2(t)
	if n := len(l.Panes()); n != 4 {
		t.Fatalf("panes %d", n)
	}
	must(t, l.Focus(ids[3]))
	must(t, l.ToggleMaximize())
	ps := l.Panes()
	if len(ps) != 1 || ps[0].PaneID != ids[3] || ps[0].Rect != (Rect{0, 0, 1, 1}) {
		t.Errorf("maximized panes: %+v", ps)
	}
	must(t, l.ToggleMaximize())
	for _, p := range l.Panes() {
		if !approx(p.Rect.W, 0.5) || !approx(p.Rect.H, 0.5) {
			t.Errorf("rect %+v", p.Rect)
		}
	}
	must(t, l.ToggleMaximize())
	l.Split(ids[3], DirRow)
	if l.Tabs[0].Maximized {
		t.Error("split should clear maximize")
	}
	if err := New().ToggleMaximize(); err == nil {
		t.Error("want error without tabs")
	}
}

func TestTabsOps(t *testing.T) {
	l := New()
	t1, _ := l.NewTab(KindShell)
	t2, _ := l.NewTab(KindSetup)
	t3, _ := l.NewTab(KindAgent)
	ids := func() []string {
		var o []string
		for _, x := range l.Tabs {
			o = append(o, x.ID)
		}
		return o
	}
	must(t, l.NextTab())
	if l.Active != t1 {
		t.Errorf("wrap next: %s", l.Active)
	}
	must(t, l.PrevTab())
	if l.Active != t3 {
		t.Errorf("wrap prev: %s", l.Active)
	}
	must(t, l.MoveTab(0, 2))
	if !reflect.DeepEqual(ids(), []string{t2, t3, t1}) || l.Active != t3 {
		t.Errorf("move: %v", ids())
	}
	must(t, l.MoveTab(2, 0))
	if !reflect.DeepEqual(ids(), []string{t1, t2, t3}) {
		t.Errorf("move back: %v", ids())
	}
	if l.MoveTab(0, 3) == nil || l.MoveTab(-1, 0) == nil {
		t.Error("want range errors")
	}
	must(t, l.RenameTab(t2, "build"))
	if l.Tabs[1].Title != "build" || l.RenameTab("x", "y") == nil {
		t.Error("rename")
	}
	if l.Tabs[1].Root.Kind != KindSetup {
		t.Error("kind")
	}
	p := l.Tabs[0].Focused
	must(t, l.AttachPty(p, "pty-1"))
	if l.Tabs[0].Root.PtyID != "pty-1" || l.AttachPty("x", "y") == nil {
		t.Error("attach")
	}
	if New().NextTab() == nil {
		t.Error("want error")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	l, ids := grid2x2(t)
	l.AttachPty(ids[0], "pty-1")
	l.NewTab(KindAgent)
	data, err := json.Marshal(l)
	must(t, err)
	var back Layout
	must(t, json.Unmarshal(data, &back))
	must(t, back.Validate())
	if !reflect.DeepEqual(*l, back) {
		t.Errorf("round trip mismatch:\n%s", data)
	}
	var raw map[string]any
	json.Unmarshal(data, &raw)
	root := raw["tabs"].([]any)[0].(map[string]any)["root"].(map[string]any)
	if root["type"] != "split" || root["dir"] != "col" {
		t.Errorf("shape: %v", root)
	}
	leaf := root["children"].([]any)[0].(map[string]any)["children"].([]any)[0].(map[string]any)
	if leaf["type"] != "leaf" || leaf["paneId"] != ids[0] || leaf["ptyId"] != "pty-1" || leaf["kind"] != "shell" {
		t.Errorf("leaf shape: %v", leaf)
	}
	// restored layout never reuses ids
	nt, np := back.NewTab(KindShell)
	if back.tabIndex(nt) != len(back.Tabs)-1 || back.findPane(np) < 0 {
		t.Error("new ids")
	}
	if e, _ := json.Marshal(New()); string(e) != `{"tabs":[],"active":"","seq":0}` {
		t.Errorf("empty: %s", e)
	}
	bad := Layout{Tabs: []Tab{{ID: "t1", Root: Node{Type: "weird"}}}}
	if bad.Validate() == nil {
		t.Error("want invalid")
	}
}

func TestInvalidIDs(t *testing.T) {
	l := New()
	l.NewTab(KindShell)
	if _, err := l.Split("nope", DirRow); err == nil {
		t.Error("split")
	}
	if _, err := l.Split("p2", "diag"); err == nil {
		t.Error("dir")
	}
	if l.Focus("nope") != nil == false {
		t.Error("focus")
	}
	if _, err := New().Close("p1"); err == nil {
		t.Error("close")
	}
}

func TestManager(t *testing.T) {
	m := NewManager()
	var mu sync.Mutex
	var got []Layout
	unsub := m.Subscribe("s1", func(l Layout) { mu.Lock(); got = append(got, l); mu.Unlock() })
	var pane PaneID
	must(t, m.Update("s1", func(l *Layout) error { _, pane = l.NewTab(KindShell); return nil }))
	must(t, m.Update("s1", func(l *Layout) error { _, err := l.Split(pane, DirRow); return err }))
	if err := m.Update("s1", func(l *Layout) error { return l.Focus("bad") }); err == nil {
		t.Error("want error")
	}
	m.Update("other", func(l *Layout) error { l.NewTab(KindShell); return nil })
	if len(got) != 2 {
		t.Fatalf("notifications %d", len(got))
	}
	// snapshots are deep copies
	got[1].Tabs[0].Root.Ratios[0] = 0.9
	if m.Get("s1").Tabs[0].Root.Ratios[0] != 0.5 {
		t.Error("snapshot aliased")
	}
	unsub()
	m.Update("s1", func(l *Layout) error { return l.NextTab() })
	if len(got) != 2 {
		t.Error("unsubscribed still notified")
	}
	data, _ := json.Marshal(m.Get("s1"))
	must(t, m.Restore("s2", data))
	if len(m.Get("s2").Tabs) != 1 || m.Restore("s3", []byte(`{"tabs":[{"id":"t1"}]}`)) == nil {
		t.Error("restore")
	}

	var wg sync.WaitGroup
	var cnt int
	m.Subscribe("c", func(Layout) { cnt++ }) // serialised by notify handoff
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Update("c", func(l *Layout) error { l.NewTab(KindShell); return nil })
		}()
	}
	wg.Wait()
	if cnt != 50 || len(m.Get("c").Tabs) != 50 {
		t.Errorf("cnt %d", cnt)
	}
}
