package fuzzy

import (
	"slices"
	"unicode"
)

// Match is one ranked candidate. Positions are the matched rune indices in the
// candidate, for highlighting; they are only filled in by Rank and Score.
type Match struct {
	Index     int
	Score     int
	Positions []int
}

// Scoring constants, modelled on fzf v1.
const (
	scoreMatch     = 16
	scoreGapStart  = -3
	scoreGapExt    = -1
	bonusSpace     = 10 // after a space or at the start of the string
	bonusDelimiter = 9  // after '/'
	bonusBoundary  = 8  // after _ - . or other punctuation
	bonusCamel     = 7  // lower->upper, or letter->digit
	bonusConsec    = 4  // minimum bonus for a consecutive match
	bonusCase      = 2  // exact-case match
	bonusBasename  = 8  // per matched rune inside the basename
	negInf         = -1 << 40
)

// matcher holds reusable scratch buffers so Rank does not allocate per
// candidate.
type matcher struct {
	query []rune // original query
	ql    []rune // lowercased query
	t     []rune // candidate
	tl    []rune // lowercased candidate
	bonus []int  // position bonus per window column
	m     []int  // M[i*w+x]: q[i] matched at window column x
	b     []int  // B[i*w+x]: best q[0..i] ending at or before x, decayed per gap
}

func newMatcher(query string) *matcher {
	q := []rune(query)
	ql := make([]rune, len(q))
	for i, r := range q {
		ql[i] = unicode.ToLower(r)
	}
	return &matcher{query: q, ql: ql}
}

// load converts the candidate to runes (reusing buffers).
func (mt *matcher) load(s string) {
	mt.t = mt.t[:0]
	mt.tl = mt.tl[:0]
	for _, r := range s {
		mt.t = append(mt.t, r)
		mt.tl = append(mt.tl, unicode.ToLower(r))
	}
}

// boundaryBonus scores a match at a position whose predecessor is prev.
func boundaryBonus(prev, cur rune) int {
	switch {
	case prev == ' ' || prev == '\t':
		return bonusSpace
	case prev == '/':
		return bonusDelimiter
	case prev == '_' || prev == '-' || prev == '.':
		return bonusBoundary
	case unicode.IsLower(prev) && unicode.IsUpper(cur):
		return bonusCamel
	case !unicode.IsDigit(prev) && unicode.IsDigit(cur):
		return bonusCamel
	case !unicode.IsLetter(prev) && !unicode.IsDigit(prev):
		return bonusBoundary
	}
	return 0
}

// window finds the leftmost start that can still complete the query and the
// earliest end. ok is false when the query is not a subsequence.
func window(ql, tl []rune) (start int, ok bool) {
	end := -1
	qi := 0
	for j, c := range tl {
		if c == ql[qi] {
			qi++
			if qi == len(ql) {
				end = j
				break
			}
		}
	}
	if end < 0 {
		return 0, false
	}
	qi = len(ql) - 1
	for j := end; j >= 0; j-- {
		if tl[j] == ql[qi] {
			qi--
			if qi < 0 {
				return j, true
			}
		}
	}
	return 0, false
}

// prepare fills mt.bonus for columns of the window starting at start.
func (mt *matcher) prepare(start int) {
	w := len(mt.t) - start
	mt.bonus = mt.bonus[:0]
	for x := 0; x < w; x++ {
		j := start + x
		prev := ' '
		if j > 0 {
			prev = mt.t[j-1]
		}
		mt.bonus = append(mt.bonus, boundaryBonus(prev, mt.t[j]))
	}
}

// cell computes M[i][x] for the window column x (absolute index start+x).
// consec reports whether the best predecessor is the consecutive one.
func (mt *matcher) cell(i, x, start, baseStart, w int) (v int, consec bool) {
	j := start + x
	if mt.tl[j] != mt.ql[i] {
		return negInf, false
	}
	base := scoreMatch
	if mt.t[j] == mt.query[i] {
		base += bonusCase
	}
	if j >= baseStart {
		base += bonusBasename
	}
	if i == 0 {
		// Leading gaps are free; the first character gets its bonus doubled.
		return base + 2*mt.bonus[x], false
	}
	v = negInf
	if x >= 1 {
		if p := mt.m[(i-1)*w+x-1]; p > negInf {
			c := p + base + max(mt.bonus[x], bonusConsec)
			v, consec = c, true
		}
	}
	if x >= 2 {
		if p := mt.b[(i-1)*w+x-2]; p > negInf {
			if c := p + scoreGapStart + mt.bonus[x] + base; c > v {
				v, consec = c, false
			}
		}
	}
	return v, consec
}

// run fills the DP tables for the window starting at start and returns the
// best final score, or ok=false if the query cannot be matched there.
func (mt *matcher) run(start, baseStart int) (best int, ok bool) {
	m := len(mt.ql)
	w := len(mt.t) - start
	need := m * w
	if cap(mt.m) < need {
		mt.m = make([]int, need)
		mt.b = make([]int, need)
	}
	mt.m = mt.m[:need]
	mt.b = mt.b[:need]
	mt.prepare(start)
	for i := 0; i < m; i++ {
		prevB := negInf
		for x := 0; x < w; x++ {
			v, _ := mt.cell(i, x, start, baseStart, w)
			mt.m[i*w+x] = v
			bv := v
			if x >= 1 && prevB > negInf && prevB+scoreGapExt > bv {
				bv = prevB + scoreGapExt
			}
			mt.b[i*w+x] = bv
			prevB = bv
		}
	}
	best = negInf
	for x := 0; x < w; x++ {
		if v := mt.m[(m-1)*w+x]; v > best {
			best = v
		}
	}
	return best, best > negInf
}

// score loads s and returns its score; positions are only computed on demand.
func (mt *matcher) score(s string) (int, bool) {
	mt.load(s)
	start, ok := window(mt.ql, mt.tl)
	if !ok {
		return 0, false
	}
	return mt.run(start, baseStartOf(mt.t))
}

// positions loads s and returns its score and matched rune indices.
func (mt *matcher) positions(s string) (int, []int, bool) {
	mt.load(s)
	start, ok := window(mt.ql, mt.tl)
	if !ok {
		return 0, nil, false
	}
	baseStart := baseStartOf(mt.t)
	best, ok := mt.run(start, baseStart)
	if !ok {
		return 0, nil, false
	}
	w := len(mt.t) - start
	m := len(mt.ql)
	pos := make([]int, m)

	// Pick the best final column (leftmost on ties).
	x := 0
	for c := 0; c < w; c++ {
		if mt.m[(m-1)*w+c] == best {
			x = c
			break
		}
	}
	pos[m-1] = start + x
	for i := m - 1; i >= 1; i-- {
		_, consec := mt.cell(i, x, start, baseStart, w)
		if consec {
			x--
		} else {
			// B[i-1][x-2] = M[i-1][y] + decay; walk back to the match y.
			y := x - 2
			for mt.b[(i-1)*w+y] != mt.m[(i-1)*w+y] {
				y--
			}
			x = y
		}
		pos[i-1] = start + x
	}
	return best, pos, true
}

// baseStartOf returns the rune index where the basename begins.
func baseStartOf(t []rune) int {
	for j := len(t) - 1; j >= 0; j-- {
		if t[j] == '/' {
			return j + 1
		}
	}
	return 0
}

// Score reports whether query is a fuzzy subsequence of candidate and, if so,
// its score and the matched rune indices. An empty query matches everything
// with score 0.
func Score(query, candidate string) (score int, positions []int, ok bool) {
	if query == "" {
		return 0, nil, true
	}
	return newMatcher(query).positions(candidate)
}

// Rank returns the candidates matching query, best first. Ties keep their
// original order. limit <= 0 means no limit. An empty query returns all
// candidates in their original order, up to limit, with zero scores and nil
// positions.
func Rank(query string, candidates []string, limit int) []Match {
	if query == "" {
		n := len(candidates)
		if limit > 0 && limit < n {
			n = limit
		}
		out := make([]Match, n)
		for i := range out {
			out[i] = Match{Index: i}
		}
		return out
	}

	mt := newMatcher(query)
	var kept []Match
	for i, c := range candidates {
		s, ok := mt.score(c)
		if !ok {
			continue
		}
		m := Match{Index: i, Score: s}
		switch {
		case limit <= 0:
			kept = append(kept, m)
		case len(kept) < limit:
			kept = append(kept, m)
			siftUp(kept, len(kept)-1)
		case better(m, kept[0]):
			kept[0] = m
			siftDown(kept, 0)
		}
	}

	slices.SortFunc(kept, func(a, b Match) int {
		if better(a, b) {
			return -1
		}
		if better(b, a) {
			return 1
		}
		return 0
	})
	for k := range kept {
		_, pos, _ := mt.positions(candidates[kept[k].Index])
		kept[k].Positions = pos
	}
	return kept
}

// better orders matches best-first: higher score, then lower original index.
func better(a, b Match) bool {
	return a.Score > b.Score || (a.Score == b.Score && a.Index < b.Index)
}

// The heap keeps the worst kept match at the root so it can be evicted.
func siftUp(h []Match, i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !better(h[p], h[i]) {
			break
		}
		h[p], h[i] = h[i], h[p]
		i = p
	}
}

func siftDown(h []Match, i int) {
	n := len(h)
	for {
		l, r := 2*i+1, 2*i+2
		worst := i
		if l < n && better(h[worst], h[l]) {
			worst = l
		}
		if r < n && better(h[worst], h[r]) {
			worst = r
		}
		if worst == i {
			return
		}
		h[i], h[worst] = h[worst], h[i]
		i = worst
	}
}
