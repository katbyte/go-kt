package lock

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// item and playlist are two kinds of record that can share an id.
type item struct{ id string }

func (i *item) LockID() string { return i.id }

type playlist struct{ id string }

func (p playlist) LockID() string { return p.id }

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

// Callers locking one thing take turns: a read and a write back of the same
// record never interleave, so no change is lost.
func TestByKeepsCallersOfOneThingApart(t *testing.T) {
	t.Parallel()

	s := NewSet()
	tags := 0
	within(t, "eight edits of one record", func() {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				// each caller has its own copy of the record, as each call that fetches it does
				unlock := s.By(&item{id: "1"})
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
	if !s.Idle() {
		t.Error("the set is not idle with every edit done")
	}
	if len(s.keys) != 0 {
		t.Errorf("%d locks are kept with nobody holding them", len(s.keys))
	}
}

// Another record is another lock: its caller does not wait.
func TestByLetsOtherThingsThrough(t *testing.T) {
	t.Parallel()

	s := NewSet()
	unlock := s.By(&item{id: "1"})
	defer unlock()

	within(t, "a lock of another record", func() { s.By(&item{id: "2"})() })
	// a thing named twice is taken once, and naming nothing takes nothing
	within(t, "a lock naming a record twice", func() { s.By(&item{id: "3"}, ID[item]("3"))() })
	within(t, "a lock of nothing", func() { s.By()() })
}

// What a thing is locked as: its type and its id, however it is named.
func TestWhatOneThingIs(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		held, asked Thing
		same        bool
	}{
		"two copies of one record":               {&item{id: "12"}, &item{id: "12"}, true},
		"a record and its id":                    {&item{id: "12"}, ID[item]("12"), true},
		"its id written for a pointer":           {&item{id: "12"}, ID[*item]("12"), true},
		"a value of a type that locks by value":  {playlist{id: "12"}, ID[playlist]("12"), true},
		"a pointer to such a value":              {playlist{id: "12"}, &playlist{id: "12"}, true},
		"one field of a record, named twice":     {Field(&item{id: "12"}, "notes"), Field(ID[item]("12"), "notes"), true},
		"one string":                             {String("settings"), String("settings"), true},
		"another record of the type":             {&item{id: "12"}, &item{id: "13"}, false},
		"the same id of another type":            {&item{id: "12"}, playlist{id: "12"}, false},
		"a record and a field of it":             {&item{id: "12"}, Field(&item{id: "12"}, "notes"), false},
		"two fields of one record":               {Field(&item{id: "12"}, "notes"), Field(&item{id: "12"}, "marks"), false},
		"one field of two records":               {Field(&item{id: "12"}, "notes"), Field(&item{id: "13"}, "notes"), false},
		"a record and a string that reads as it": {&item{id: "12"}, String(ID[item]("12").LockID()), false},
		"a field and a field of that field":      {Field(&item{id: "12"}, "notes"), Field(Field(&item{id: "12"}, "notes"), "first"), false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := NewSet()
			unlock := s.By(c.held)
			if c.same {
				waitsFor(t, "a lock of the same thing", unlock, func() func() { return s.By(c.asked) })
			} else {
				within(t, "a lock of another thing", func() { s.By(c.asked)() })
				unlock()
			}
			if !s.Idle() {
				t.Error("the set is not idle with everything unlocked")
			}
		})
	}
}

// A value that cannot say which record it is has no lock to take.
func TestByRefusesNothing(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("a nil thing was locked, want a panic saying it names no record")
		}
	}()
	NewSet().By(nil)
}

// Things that overlap are taken in one order whatever order they are named
// in, so two callers each holding what the other wants cannot happen.
func TestByTakesOverlappingThingsInOneOrder(t *testing.T) {
	t.Parallel()

	s := NewSet()
	var done atomic.Int32
	within(t, "locks of overlapping things named in opposite orders", func() {
		var wg sync.WaitGroup
		for i := range 200 {
			wg.Go(func() {
				things := []Thing{&item{id: "a"}, playlist{id: "a"}, Field(&item{id: "a"}, "notes"), String("a")}
				if i%2 == 1 {
					things = []Thing{String("a"), Field(&item{id: "a"}, "notes"), playlist{id: "a"}, &item{id: "a"}}
				}
				unlock := s.By(things...)
				done.Add(1)
				unlock()
			})
		}
		wg.Wait()
	})
	if done.Load() != 200 {
		t.Errorf("%d of 200 locks were granted", done.Load())
	}
	if !s.Idle() {
		t.Error("the set is not idle with every lock unlocked")
	}
	if len(s.keys) != 0 {
		t.Errorf("%d locks are kept with nobody holding them", len(s.keys))
	}
}

// All waits for every lock to be unlocked, and holds everything: nothing is
// granted until it is unlocked.
func TestAllHoldsEverything(t *testing.T) {
	t.Parallel()

	s := NewSet()
	one := s.By(&item{id: "1"})
	var swept atomic.Bool
	sweeping := make(chan struct{})
	go func() {
		unlock := s.All()
		swept.Store(true)
		close(sweeping)
		time.Sleep(20 * time.Millisecond)
		swept.Store(false)
		unlock()
	}()
	time.Sleep(20 * time.Millisecond)
	if swept.Load() {
		t.Fatal("All was granted while a record was locked")
	}
	one()
	<-sweeping

	// asked for while everything is held, a record waits until it is let go
	within(t, "a lock behind an All", func() {
		unlock := s.ByString("settings")
		defer unlock()
		if swept.Load() {
			t.Error("a lock was granted while everything was held")
		}
	})
}

// What is locked in one set holds nobody up in another, All included.
func TestSetsShareNothing(t *testing.T) {
	t.Parallel()

	one, other := NewSet(), NewSet()
	unlock := one.By(&item{id: "1"})
	within(t, "a lock of the same record in another set", func() { other.By(&item{id: "1"})() })
	within(t, "an All in another set", func() { other.All()() })
	unlock()

	unlock = one.All()
	within(t, "a lock in another set behind this set's All", func() { other.By(&item{id: "1"})() })
	within(t, "a lock in the process's set behind this set's All", func() { By(ID[item](t.Name()))() })
	unlock()

	// the zero value is a set too
	var zero Set
	unlock = zero.By(&item{id: "1"})
	within(t, "a lock of the same record in a made set", func() { one.By(&item{id: "1"})() })
	unlock()
}

// Idle is what a test asks to see that a call left nothing locked.
func TestIdle(t *testing.T) {
	t.Parallel()

	s := NewSet()
	if !s.Idle() {
		t.Error("a new set is not idle")
	}

	unlock := s.By(&item{id: "1"})
	if s.Idle() {
		t.Error("the set is idle with a record locked")
	}
	unlock()

	unlock = s.All()
	if s.Idle() {
		t.Error("the set is idle with everything locked")
	}
	unlock()

	unlock = s.By()
	if s.Idle() {
		t.Error("the set is idle while a caller that named nothing has not unlocked")
	}
	unlock()

	if !s.Idle() {
		t.Error("the set is not idle with everything unlocked")
	}

	// a lock the set still keeps is not nothing, though nobody holds it:
	// that is a lock left behind, which Idle is asked in order to find
	s.keys[ID[item]("left behind")] = &keyLock{}
	if s.Idle() {
		t.Error("the set is idle while it keeps a lock for a record")
	}
}

// The package's own functions lock in the one set the whole process shares,
// All among them, so they are tried here in turn and not as tests running at
// once: a test that holds one lock while it waits to see another caller
// granted a second would wait for ever behind another test's All, which is
// the one-call-at-a-time rule the package states.
func TestProcessWideLocks(t *testing.T) {
	t.Parallel()

	// every key carries the test's name, so nothing else in this process asks for it
	mine := &item{id: t.Name()}
	edits := 0
	within(t, "eight edits of one record", func() {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				unlock := By(mine)
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

	unlock := By(mine)
	within(t, "a lock of another record", func() { By(&item{id: t.Name() + "/other"})() })
	within(t, "a lock of the same id of another type", func() { By(playlist{id: t.Name()})() })
	waitsFor(t, "a second lock of one record", unlock, func() func() { return By(ID[item](t.Name())) })

	// a record and a field of another, in one call
	me := playlist{id: t.Name() + "/me"}
	unlock = By(mine, Field(me, "bookmarks"))
	within(t, "a lock of the thing whose field is locked", func() { By(me)() })
	waitsFor(t, "a lock of a field locked beside a record", unlock, func() func() { return By(Field(me, "bookmarks")) })

	// by a string of the caller's own
	unlock = ByString(t.Name() + " settings")
	within(t, "a lock of another string", func() { ByString(t.Name() + " other")() })
	waitsFor(t, "a second lock of one string", unlock, func() func() { return By(String(t.Name() + " settings")) })

	// everything: a record and a string both wait for All, and All waits
	// for a lock already held
	unlock = All()
	waitsFor(t, "a record behind All", unlock, func() func() { return By(mine) })
	unlock = All()
	waitsFor(t, "a string behind All", unlock, func() func() { return ByString(t.Name()) })
	unlock = By(mine)
	waitsFor(t, "All behind a locked record", unlock, All)
}

// A key says what it names, for a log.
func TestKeyReadsAsWhatItNames(t *testing.T) {
	t.Parallel()

	for want, key := range map[string]Key{
		"github.com/katbyte/go-kt/lock.item 12":        ID[item]("12"),
		"github.com/katbyte/go-kt/lock.item 12 notes":  Field(&item{id: "12"}, "notes"),
		"github.com/katbyte/go-kt/lock.playlist 7 a.b": Field(Field(playlist{id: "7"}, "a"), "b"),
		"server settings":                                 String("server settings"),
		"struct { ID string } 3":                          ID[struct{ ID string }]("3"),
		"github.com/katbyte/go-kt/lock.Key 9":             ID[Key]("9"),
		"github.com/katbyte/go-kt/lock.item 12 bookmarks": Field(ID[**item]("12"), "bookmarks"),
	} {
		if got := key.LockID(); got != want {
			t.Errorf("the key reads %q, want %q", got, want)
		}
	}
}
