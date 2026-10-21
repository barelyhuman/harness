package app

import "strings"

type modeDef struct{ Native, Display string }

// modeTables is the single mapping of provider-native permission modes to
// display names. The first entry is the default; CycleMode walks the order.
var modeTables = map[string][]modeDef{
	"claude": {{"default", "Default"}, {"acceptEdits", "Accept edits"}, {"plan", "Plan"}},
	"cursor": {{"agent", "Agent"}, {"plan", "Plan"}, {"ask", "Ask"}},
}

// normalizeMode accepts a native or display name. Providers without a table
// accept anything.
func normalizeMode(provider, m string) (string, bool) {
	t := modeTables[provider]
	if t == nil {
		return m, m != ""
	}
	for _, d := range t {
		if strings.EqualFold(m, d.Native) || strings.EqualFold(m, d.Display) {
			return d.Native, true
		}
	}
	return "", false
}

func defaultMode(provider string) string {
	if t := modeTables[provider]; len(t) > 0 {
		return t[0].Native
	}
	return ""
}

func nextMode(provider, cur string) (string, bool) {
	t := modeTables[provider]
	if len(t) == 0 {
		return "", false
	}
	i := 0
	for j, d := range t {
		if d.Native == cur {
			i = j
		}
	}
	return t[(i+1)%len(t)].Native, true
}
