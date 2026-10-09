package lock

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// within fails the test when what it runs has not returned in five seconds:
// two callers waiting on each other, which no test should hang on.
func within(t *testing.T, what string, run func()) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish: callers are waiting on each other", what)
	}
}

// Callers holding one key take turns: a read and a write back of the same
// record never interleave, so no change is lost.
func TestHoldKeepsCallersOfOneKeyApart(t *testing.T) {
	t.Parallel()

	var l Locks
	tags := 0
	within(t, "eight edits of one record", func() {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				release := l.Hold("item:1")
				defer release()
				read := tags
				time.Sleep(time.Millisecond)
				tags = read + 1
			})
		}
		wg.Wait()
	})
	if tags != 8 {
		t.Errorf("eight edits left %d changes, want every one", tags)
	}
	if len(l.keys) != 0 {
		t.Errorf("%d locks are kept with nobody holding them", len(l.keys))
	}
}

// Another key is another record: its holder does not wait.
func TestHoldLetsOtherKeysThrough(t *testing.T) {
	t.Parallel()

	var l Locks
	release := l.Hold("item:1")
	defer release()
	within(t, "a hold of another key", func() { l.Hold("item:2")() })
	// a key named twice is taken once, and an empty hold takes nothing
	within(t, "a hold naming a key twice", func() { l.Hold("item:3", "item:3")() })
	within(t, "a hold of nothing", func() { l.Hold()() })
}

// Sets that overlap are taken in one order whatever order they are named
// in, so two callers each holding what the other wants cannot happen.
func TestHoldTakesOverlappingSetsInOneOrder(t *testing.T) {
	t.Parallel()

	var l Locks
	var done atomic.Int32
	within(t, "holds of overlapping sets named in opposite orders", func() {
		var wg sync.WaitGroup
		for i := range 200 {
			wg.Go(func() {
				keys := []string{"a", "b", "c"}
				if i%2 == 1 {
					keys = []string{"c", "b", "a"}
				}
				release := l.Hold(keys...)
				done.Add(1)
				release()
			})
		}
		wg.Wait()
	})
	if done.Load() != 200 {
		t.Errorf("%d of 200 holds were granted", done.Load())
	}
	if len(l.keys) != 0 {
		t.Errorf("%d locks are kept with nobody holding them", len(l.keys))
	}
}

// HoldAll waits for every hold to be released, and holds everything: no key
// is granted until it lets go.
func TestHoldAllHoldsEveryKey(t *testing.T) {
	t.Parallel()

	var l Locks
	one := l.Hold("item:1")
	var swept atomic.Bool
	sweeping := make(chan struct{})
	go func() {
		release := l.HoldAll()
		swept.Store(true)
		close(sweeping)
		time.Sleep(20 * time.Millisecond)
		swept.Store(false)
		release()
	}()
	time.Sleep(20 * time.Millisecond)
	if swept.Load() {
		t.Fatal("HoldAll was granted while a key was held")
	}
	one()
	<-sweeping

	// asked for while everything is held, a key waits until it is let go
	within(t, "a hold behind a HoldAll", func() {
		release := l.Hold("item:2")
		defer release()
		if swept.Load() {
			t.Error("a key was granted while everything was held")
		}
	})
}
