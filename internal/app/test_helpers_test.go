package app

import (
	"runtime"
	"testing"
)

func waitGrepIdle(t *testing.T, s *SearchService) {
	t.Helper()
	for i := 0; i < 50000; i++ {
		s.mu.Lock()
		n := len(s.greps)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("grep goroutine did not finish after cancel")
}
