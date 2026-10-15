package registry_test

import (
	"testing"
	"time"

	"github.com/katbyte/go-kt/mcp/registry"
)

func TestLimitOr(t *testing.T) {
	t.Parallel()

	for limit, want := range map[int]int{-1: 50, 0: 50, 1: 1, 200: 200} {
		if got := registry.LimitOr(limit, 50); got != want {
			t.Errorf("LimitOr(%d, 50) = %d, want %d", limit, got, want)
		}
	}
}

// The next page starts after this one, and there is none after the last page, a short one, or an empty one.
func TestNextOffset(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ offset, n, total, want int }{
		{0, 50, 120, 50},
		{50, 50, 120, 100},
		{100, 20, 120, 0},
		{0, 50, 50, 0},
		{0, 0, 120, 0},
	} {
		if got := registry.NextOffset(c.offset, c.n, c.total); got != c.want {
			t.Errorf("NextOffset(%d, %d, %d) = %d, want %d", c.offset, c.n, c.total, got, c.want)
		}
	}
}

func TestWaitFor(t *testing.T) {
	t.Parallel()

	for seconds, want := range map[int]time.Duration{-1: 0, 0: 2 * time.Minute, 30: 30 * time.Second} {
		if got := registry.WaitFor(seconds, 2*time.Minute); got != want {
			t.Errorf("WaitFor(%d) = %s, want %s", seconds, got, want)
		}
	}
}
