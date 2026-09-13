package pointer

import "testing"

func TestFrom(t *testing.T) {
	t.Parallel()

	t.Run("nil returns the zero value", func(t *testing.T) {
		t.Parallel()
		if got := From[string](nil); got != "" {
			t.Errorf("From[string](nil) = %q, want empty", got)
		}
		if got := From[int](nil); got != 0 {
			t.Errorf("From[int](nil) = %d, want 0", got)
		}
		type s struct{ A, B int }
		if got := From[s](nil); got != (s{}) {
			t.Errorf("From[s](nil) = %+v, want zero struct", got)
		}
		if got := From[[]int](nil); got != nil {
			t.Errorf("From[[]int](nil) = %v, want nil slice", got)
		}
	})

	t.Run("non-nil returns a copy of the value", func(t *testing.T) {
		t.Parallel()
		v := 42
		got := From(&v)
		if got != 42 {
			t.Fatalf("From(&42) = %d, want 42", got)
		}
		v = 7
		if got != 42 {
			t.Errorf("From returned an alias: changed to %d after mutating the source", got)
		}
	})
}

func TestTo(t *testing.T) {
	t.Parallel()

	p := To("hello")
	if p == nil || *p != "hello" {
		t.Fatalf("To(\"hello\") = %v, want pointer to \"hello\"", p)
	}

	// each call allocates its own copy, so two pointers to equal values are distinct
	a, b := To(1), To(1)
	if a == b {
		t.Error("To(1) returned the same pointer twice")
	}
	*a = 2
	if *b != 1 {
		t.Errorf("mutating one To result changed the other: %d", *b)
	}

	// round trip
	if got := From(To(3.5)); got != 3.5 {
		t.Errorf("From(To(3.5)) = %v, want 3.5", got)
	}
}
