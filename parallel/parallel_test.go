package parallel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every job runs once, and never more at a time than asked for.
func TestEachRunsEveryJobAFewAtATime(t *testing.T) {
	t.Parallel()

	for _, workers := range []int{1, 3, 8} {
		var (
			mu      sync.Mutex
			ran     = map[int]int{}
			running atomic.Int32
			most    atomic.Int32
		)
		if err := Each(t.Context(), 40, workers, func(_ context.Context, i int) error {
			now := running.Add(1)
			for {
				seen := most.Load()
				if now <= seen || most.CompareAndSwap(seen, now) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			mu.Lock()
			ran[i]++
			mu.Unlock()
			running.Add(-1)

			return nil
		}); err != nil {
			t.Fatalf("%d workers: %v", workers, err)
		}
		if len(ran) != 40 {
			t.Errorf("%d workers: %d jobs ran, want 40", workers, len(ran))
		}
		for i, n := range ran {
			if n != 1 {
				t.Errorf("%d workers: job %d ran %d times", workers, i, n)
			}
		}
		if got := int(most.Load()); got > workers || got < 1 {
			t.Errorf("%d workers: %d ran at once", workers, got)
		}
	}
}

// No workers, or fewer, is one: the jobs run one after another, in order.
func TestEachWithNoWorkersRunsOneAtATime(t *testing.T) {
	t.Parallel()

	for _, workers := range []int{0, -3} {
		var order []int
		if err := Each(t.Context(), 5, workers, func(_ context.Context, i int) error {
			order = append(order, i)

			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for i, got := range order {
			if got != i {
				t.Errorf("%d workers: ran in the order %v", workers, order)

				break
			}
		}
		if len(order) != 5 {
			t.Errorf("%d workers: %d jobs ran", workers, len(order))
		}
	}

	if err := Each(t.Context(), 0, 4, func(context.Context, int) error { return errors.New("never called") }); err != nil {
		t.Errorf("no jobs: %v", err)
	}
}

// The first error is the answer, the jobs not yet started are not started,
// and the ones running see their context end with that error as its cause.
func TestEachStopsAtTheFirstError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	var started atomic.Int32
	var cause error
	err := Each(t.Context(), 1000, 2, func(ctx context.Context, i int) error {
		started.Add(1)
		switch i {
		case 0:
			// still running when the second job fails
			<-ctx.Done()
			cause = context.Cause(ctx)
		case 1:
			return boom
		}

		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the job's own", err)
	}
	if n := started.Load(); n > 3 {
		t.Errorf("%d jobs started, want none after the second failed", n)
	}
	if !errors.Is(cause, boom) {
		t.Errorf("a running job's context ended with %v, want the error that ended the run", cause)
	}
}

// A run the caller's context ends is said to have been ended, by that
// context's cause, and is not read as one that finished.
func TestEachSaysWhenTheCallerEndedIt(t *testing.T) {
	t.Parallel()

	stopped := errors.New("the caller stopped")
	ctx, cancel := context.WithCancelCause(t.Context())
	var started atomic.Int32
	err := Each(ctx, 100, 1, func(context.Context, int) error {
		if started.Add(1) == 2 {
			cancel(stopped)
		}

		return nil
	})
	if !errors.Is(err, stopped) {
		t.Errorf("err = %v, want the caller's cause", err)
	}
	if n := started.Load(); n > 3 {
		t.Errorf("%d jobs started after the caller stopped", n)
	}
}
