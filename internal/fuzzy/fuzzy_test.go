package fuzzy

import (
	"fmt"
	"reflect"
	"testing"
)

func TestOrderingMainPrefersBasename(t *testing.T) {
	cands := []string{"domain/admin.go", "main.go"}
	got := Rank("main", cands, 0)
	if len(got) != 2 || got[0].Index != 1 || got[1].Index != 0 {
		t.Fatalf("want main.go first, got %+v", got)
	}
}

func TestOrderingCamelBoundaries(t *testing.T) {
	score, pos, ok := Score("fb", "FooBar.ts")
	if !ok {
		t.Fatal("fb should match FooBar.ts")
	}
	if !reflect.DeepEqual(pos, []int{0, 3}) {
		t.Fatalf("positions %v, want [0 3]", pos)
	}
	if score <= 0 {
		t.Fatalf("score %d should be positive", score)
	}
	// Boundary matches must beat the same letters buried mid-word.
	boundary, _, _ := Score("fb", "FooBar.ts")
	buried, _, _ := Score("fb", "fxxxxxxxb.ts")
	if boundary <= buried {
		t.Fatalf("camel boundary %d should beat buried %d", boundary, buried)
	}
}

func TestOrderingSeparatorPath(t *testing.T) {
	score, pos, ok := Score("srcapp", "src/app.tsx")
	if !ok {
		t.Fatal("srcapp should match src/app.tsx")
	}
	if !reflect.DeepEqual(pos, []int{0, 1, 2, 4, 5, 6}) {
		t.Fatalf("positions %v", pos)
	}
	if score <= 0 {
		t.Fatalf("score %d", score)
	}
}

func TestPositionsSpreadMatch(t *testing.T) {
	_, pos, ok := Score("ac", "abc")
	if !ok || !reflect.DeepEqual(pos, []int{0, 2}) {
		t.Fatalf("ok=%v pos=%v", ok, pos)
	}
}

func TestPositionsAreRuneIndices(t *testing.T) {
	_, pos, ok := Score("é", "café")
	if !ok || !reflect.DeepEqual(pos, []int{3}) {
		t.Fatalf("ok=%v pos=%v", ok, pos)
	}
}

func TestCaseInsensitiveWithExactCaseBonus(t *testing.T) {
	if _, _, ok := Score("MAIN", "main.go"); !ok {
		t.Fatal("matching should be case-insensitive")
	}
	exact, _, _ := Score("Main", "Main.go")
	other, _, _ := Score("Main", "main.go")
	if exact <= other {
		t.Fatalf("exact case %d should beat %d", exact, other)
	}
}

func TestNoMatch(t *testing.T) {
	if _, pos, ok := Score("xyz", "main.go"); ok || pos != nil {
		t.Fatalf("want no match, got ok=%v pos=%v", ok, pos)
	}
	if _, _, ok := Score("abcd", "abc"); ok {
		t.Fatal("query longer than candidate cannot match")
	}
	if _, _, ok := Score("ba", "ab"); ok {
		t.Fatal("order must be respected")
	}
	if got := Rank("zzz", []string{"main.go", "app.ts"}, 0); len(got) != 0 {
		t.Fatalf("Rank returned %+v for no matches", got)
	}
}

func TestEmptyQueryKeepsOrder(t *testing.T) {
	cands := []string{"c", "a", "b", "d"}
	got := Rank("", cands, 0)
	if len(got) != 4 {
		t.Fatalf("len %d", len(got))
	}
	for i, m := range got {
		if m.Index != i {
			t.Fatalf("index %d at %d", m.Index, i)
		}
	}
	if got := Rank("", cands, 2); len(got) != 2 || got[1].Index != 1 {
		t.Fatalf("limit: %+v", got)
	}
}

func TestRankLimitAndTiesStable(t *testing.T) {
	cands := []string{"x/a1", "x/a2", "x/a3", "x/a4"}
	all := Rank("a", cands, 0)
	if len(all) != 4 {
		t.Fatalf("len %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Score == all[i].Score && all[i-1].Index > all[i].Index {
			t.Fatalf("ties not stable: %+v", all)
		}
	}
	top := Rank("a", cands, 2)
	if len(top) != 2 {
		t.Fatalf("limit gave %d", len(top))
	}
	for i := range top {
		if top[i].Index != all[i].Index || top[i].Score != all[i].Score {
			t.Fatalf("limited %+v != full %+v", top, all[:2])
		}
	}
}

func TestRankPositionsMatchScore(t *testing.T) {
	cands := []string{"internal/fuzzy/fuzzy.go", "docs/fuzzy.md", "fuzzy_test.go"}
	for _, m := range Rank("fuz", cands, 0) {
		s, pos, ok := Score("fuz", cands[m.Index])
		if !ok || s != m.Score || !reflect.DeepEqual(pos, m.Positions) {
			t.Fatalf("mismatch for %q: %+v vs %d %v", cands[m.Index], m, s, pos)
		}
	}
}

func syntheticPaths(n int) []string {
	dirs := []string{"internal", "app", "frontend/src", "pkg/store", "cmd", "lib/ui", "docs-spec/specs"}
	names := []string{"main", "server", "handler", "config", "model", "util", "stream", "fuzzy", "agent", "render"}
	exts := []string{".go", ".ts", ".tsx", ".md", ".json"}
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s/%s_%d%s", dirs[i%len(dirs)], names[(i*7)%len(names)], i, exts[i%len(exts)])
	}
	return out
}

func BenchmarkRank100k(b *testing.B) {
	paths := syntheticPaths(100_000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := Rank("main", paths, 100)
		if len(got) == 0 {
			b.Fatal("no matches")
		}
	}
}

func BenchmarkRank100kUnlimited(b *testing.B) {
	paths := syntheticPaths(100_000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := Rank("srcfz", paths, 0); len(got) == 0 {
			b.Fatal("no matches")
		}
	}
}
