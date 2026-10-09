package lock

import (
	"slices"
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

// waitsFor fails the test when take is granted before held is unlocked, or
// is never granted after it.
func waitsFor(t *testing.T, what string, held func(), take func() (unlock func())) {
	t.Helper()

	var granted atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		unlock := take()
		granted.Store(true)
		unlock()
	}()
	time.Sleep(20 * time.Millisecond)
	if granted.Load() {
		t.Errorf("%s was granted while what it names was locked", what)
	}
	held()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s was never granted", what)
	}
}

// Callers holding one key take turns: a read and a write back of the same
// record never interleave, so no change is lost.
func TestHoldKeepsCallersOfOneKeyApart(t *testing.T) {
	t.Parallel()

	var s set
	tags := 0
	within(t, "eight edits of one record", func() {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				unlock := s.hold("item:1")
				defer unlock()
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
	if len(s.keys) != 0 {
		t.Errorf("%d locks are kept with nobody holding them", len(s.keys))
	}
}

// Another key is another record: its holder does not wait.
func TestHoldLetsOtherKeysThrough(t *testing.T) {
	t.Parallel()

	var s set
	unlock := s.hold("item:1")
	defer unlock()
	within(t, "a hold of another key", func() { s.hold("item:2")() })
	// a key named twice is taken once, and an empty hold takes nothing
	within(t, "a hold naming a key twice", func() { s.hold("item:3", "item:3")() })
	within(t, "a hold of nothing", func() { s.hold()() })
}

// Sets that overlap are taken in one order whatever order they are named
// in, so two callers each holding what the other wants cannot happen.
func TestHoldTakesOverlappingSetsInOneOrder(t *testing.T) {
	t.Parallel()

	var s set
	var done atomic.Int32
	within(t, "holds of overlapping sets named in opposite orders", func() {
		var wg sync.WaitGroup
		for i := range 200 {
			wg.Go(func() {
				keys := []string{"a", "b", "c"}
				if i%2 == 1 {
					keys = []string{"c", "b", "a"}
				}
				unlock := s.hold(keys...)
				done.Add(1)
				unlock()
			})
		}
		wg.Wait()
	})
	if done.Load() != 200 {
		t.Errorf("%d of 200 holds were granted", done.Load())
	}
	if len(s.keys) != 0 {
		t.Errorf("%d locks are kept with nobody holding them", len(s.keys))
	}
}

// holdAll waits for every hold to be let go, and holds everything: no key is
// granted until it lets go.
func TestHoldAllHoldsEveryKey(t *testing.T) {
	t.Parallel()

	var s set
	one := s.hold("item:1")
	var swept atomic.Bool
	sweeping := make(chan struct{})
	go func() {
		unlock := s.holdAll()
		swept.Store(true)
		close(sweeping)
		time.Sleep(20 * time.Millisecond)
		swept.Store(false)
		unlock()
	}()
	time.Sleep(20 * time.Millisecond)
	if swept.Load() {
		t.Fatal("holdAll was granted while a key was held")
	}
	one()
	<-sweeping

	// asked for while everything is held, a key waits until it is let go
	within(t, "a hold behind a holdAll", func() {
		unlock := s.hold("item:2")
		defer unlock()
		if swept.Load() {
			t.Error("a key was granted while everything was held")
		}
	})
}

// The package's own functions lock in the one set the whole process shares,
// All among them, so they are tried here in turn and not as tests running at
// once: a test that holds one lock while it waits to see another caller
// granted a second would wait for ever behind another test's All, which is
// the one-lock-at-a-time rule the package states.
func TestProcessWideLocks(t *testing.T) {
	t.Parallel()

	// by id: callers of one id take turns, and another id does not wait
	id := t.Name()
	edits := 0
	within(t, "eight edits of one id", func() {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				unlock := ByID(id)
				defer unlock()
				read := edits
				time.Sleep(time.Millisecond)
				edits = read + 1
			})
		}
		wg.Wait()
	})
	if edits != 8 {
		t.Errorf("eight edits left %d changes, want every one", edits)
	}
	unlock := ByID(id)
	within(t, "a lock of another id", func() { ByID(id + "/other")() })
	waitsFor(t, "a second lock of one id", unlock, func() func() { return ByID(id) })

	// by name: a name is locked within its kind, so the same name of
	// another kind is another thing
	playlists, collections := t.Name()+"/playlist", t.Name()+"/collection"
	unlock = ByName("12", playlists)
	within(t, "a lock of the same name of another kind", func() { ByName("12", collections)() })
	within(t, "a lock of another name of the same kind", func() { ByName("13", playlists)() })
	waitsFor(t, "a second lock of one name of one kind", unlock, func() func() { return ByName("12", playlists) })

	// the id of a name is what ByName locks, so a call locking things of
	// several kinds by id waits for a lock by name
	unlock = ByName("12", playlists)
	waitsFor(t, "a lock by the id of a locked name", unlock, func() func() {
		return MultipleByID([]string{t.Name() + "/bookmarks", NameID("12", playlists)})
	})
	if NameID("12", "playlist") == NameID("12", "collection") {
		t.Error("one name of two kinds has one id")
	}

	// several in one call: each is held, one named twice is taken once and
	// the caller's list is left as it was
	names := []string{"c", "a", "b", "a"}
	unlock = MultipleByName(names, playlists)
	if !slices.Equal(names, []string{"c", "a", "b", "a"}) {
		t.Errorf("the caller's names came back as %q", names)
	}
	within(t, "a lock of a name not in the list", func() { ByName("d", playlists)() })
	waitsFor(t, "a lock of a name in a locked list", unlock, func() func() { return ByName("b", playlists) })

	ids := []string{id + "/2", id + "/1"}
	unlock = MultipleByID(ids)
	if !slices.Equal(ids, []string{id + "/2", id + "/1"}) {
		t.Errorf("the caller's ids came back as %q", ids)
	}
	waitsFor(t, "a lock of an id in a locked list", unlock, func() func() { return ByID(id + "/1") })

	within(t, "a lock of no names", func() { MultipleByName(nil, playlists)() })
	within(t, "a lock of no ids", func() { MultipleByID(nil)() })

	// lists that overlap, named in opposite orders, do not wait on each other
	var done atomic.Int32
	within(t, "locks of overlapping lists named in opposite orders", func() {
		var wg sync.WaitGroup
		for i := range 200 {
			wg.Go(func() {
				list := []string{"x", "y", "z"}
				if i%2 == 1 {
					list = []string{"z", "y", "x"}
				}
				letGo := MultipleByName(list, playlists)
				done.Add(1)
				letGo()
			})
		}
		wg.Wait()
	})
	if done.Load() != 200 {
		t.Errorf("%d of 200 locks were granted", done.Load())
	}

	// everything: a lock by id and one by name both wait for All, and All
	// waits for a lock already held
	unlock = All()
	waitsFor(t, "a lock by id behind All", unlock, func() func() { return ByID(id) })
	unlock = All()
	waitsFor(t, "a lock by name behind All", unlock, func() func() { return ByName("12", playlists) })
	unlock = ByID(id)
	waitsFor(t, "All behind a lock by id", unlock, All)

	if len(process.keys) != 0 {
		t.Errorf("%d locks are kept with nobody holding them", len(process.keys))
	}
}
