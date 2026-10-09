package acctest

import (
	"context"
	"errors"
	"flag"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer is an MCP server of four tools: one that answers, one that
// refuses, one nobody calls, and one that refuses until it has been asked a
// few times.
func fakeServer(t *testing.T) (*Suite, *atomic.Int32) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	type in struct {
		Name string `json:"name,omitempty"`
	}
	type out struct {
		Greeting string `json:"greeting"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "greet"}, func(_ context.Context, _ *mcp.CallToolRequest, in in) (*mcp.CallToolResult, out, error) {
		return nil, out{Greeting: "hello " + in.Name}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "refuse"}, func(_ context.Context, _ *mcp.CallToolRequest, _ in) (*mcp.CallToolResult, out, error) {
		return nil, out{}, errors.New("no: not like that")
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "never"}, func(_ context.Context, _ *mcp.CallToolRequest, _ in) (*mcp.CallToolResult, out, error) {
		return nil, out{}, nil
	})
	var asked atomic.Int32
	mcp.AddTool(srv, &mcp.Tool{Name: "busy"}, func(_ context.Context, _ *mcp.CallToolRequest, in in) (*mcp.CallToolResult, out, error) {
		if asked.Add(1) < 3 {
			return nil, out{}, errors.New("busy: " + in.Name + " is held by a refresh")
		}

		return nil, out{Greeting: "done"}, nil
	})

	// a suite's context outlives its tests, whose own is cancelled before
	// their clean-ups run, which is when a put-back calls
	s, err := Connect(context.WithoutCancel(t.Context()), srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Session.Close() })
	s.NotReady, s.CannotAnswer = "no server", map[string]string{"refuse": "it never does"}

	return s, &asked
}

// Every call is counted as an answer or a failure, a tool never seen to
// answer is uncovered - unless it is one that cannot - and a refusal comes
// back as its text.
func TestCallsAreCounted(t *testing.T) {
	t.Parallel()

	s, _ := fakeServer(t)
	if !s.Ready {
		t.Fatal("a connected suite is not ready")
	}
	if out := s.Call(t, "greet", map[string]any{"name": "you"}); out["greeting"] != "hello you" {
		t.Errorf("greet = %v", out)
	}
	if out := s.Call(t, "greet", nil); out["greeting"] != "hello " {
		t.Errorf("greet with nothing = %v", out)
	}
	if msg := s.CallErr(t, "refuse", nil); !strings.HasPrefix(msg, "refuse: ") || !strings.Contains(msg, "not like that") {
		t.Errorf("refuse = %q", msg)
	}
	if _, err := s.Invoke("nope", nil); err == nil || !strings.HasPrefix(err.Error(), "nope: ") {
		t.Errorf("an unknown tool = %v", err)
	}
	if c := s.Calls("greet"); c != (Calls{Answered: 2}) {
		t.Errorf("greet's calls = %+v", c)
	}
	if c := s.Calls("refuse"); c != (Calls{Failed: 1}) {
		t.Errorf("refuse's calls = %+v", c)
	}
	never, onlyFailed, err := s.Uncovered()
	if err != nil || !slices.Equal(never, []string{"busy", "never"}) || len(onlyFailed) != 0 {
		t.Errorf("uncovered = %v, %v, %v; want busy and never, and refuse excused", never, onlyFailed, err)
	}
	if names := s.ToolNames(t); !slices.Equal(names, []string{"busy", "greet", "never", "refuse"}) {
		t.Errorf("tool names = %v", names)
	}

	// what a suite prints at the end of a run: the tools nobody called, and
	// with no excuse the one that only ever refused
	report, err := s.CoverageReport()
	if err != nil || report != "\n2 registered tool(s) are never called by this suite:\n  busy\n  never\nevery tool needs a test that it answers; add one or remove the tool\n" {
		t.Errorf("the report = %q, %v", report, err)
	}
	s.CannotAnswer = nil
	if _, onlyFailed, _ := s.Uncovered(); !slices.Equal(onlyFailed, []string{"refuse"}) {
		t.Errorf("with no excuse, only failed = %v", onlyFailed)
	}
	if report, _ := s.CoverageReport(); !strings.Contains(report, "\n1 registered tool(s) only ever failed in this suite, so nothing shows they work:\n  refuse (1 failed calls)\n") {
		t.Errorf("the report with no excuse = %q", report)
	}
	// once every tool has answered there is nothing to say
	s.CannotAnswer = map[string]string{"refuse": "it never does"}
	s.Call(t, "never", nil)
	if err := s.retryWith([]time.Duration{0, 0, 0}, "busy", nil); err != nil {
		t.Fatal(err)
	}
	if report, err := s.CoverageReport(); report != "" || err != nil {
		t.Errorf("with every tool answered the report = %q, %v", report, err)
	}

	// a session that has gone cannot say what is registered
	_ = s.Session.Close()
	if _, err := s.CoverageReport(); err == nil || !strings.Contains(err.Error(), "could not list tools") {
		t.Errorf("the report of a closed session = %v", err)
	}
}

// A suite whose server is not there skips the test rather than failing it.
func TestNotReadySkips(t *testing.T) {
	t.Parallel()

	s := &Suite{Ready: false, NotReady: "APP_SERVER is not set"}
	t.Run("call", func(t *testing.T) {
		t.Parallel()
		s.Call(t, "greet", nil)
		t.Error("a call with no server did not skip")
	})
	t.Run("refusal", func(t *testing.T) {
		t.Parallel()
		s.CallErr(t, "refuse", nil)
		t.Error("a refusal with no server did not skip")
	})
	t.Run("names", func(t *testing.T) {
		t.Parallel()
		s.ToolNames(t)
		t.Error("a listing with no server did not skip")
	})
}

// A call the server refuses while it is busy is tried again until it takes;
// one that never takes is given up on with the last refusal; and what a test
// changed is put back, or deleted unless it has gone, once the test ends.
func TestRetriesAndPutBacks(t *testing.T) {
	t.Parallel()

	s, asked := fakeServer(t)
	// Retried gives up after its waits, with the last error
	if err := s.retryWith([]time.Duration{0, 0}, "refuse", nil); err == nil || !strings.Contains(err.Error(), "not like that") {
		t.Errorf("retried = %v", err)
	}
	if c := s.Calls("refuse"); c.Failed != 2 {
		t.Errorf("retried called refuse %d times, want 2", c.Failed)
	}
	// and stops at the first try that takes
	if err := s.retryWith([]time.Duration{0, 0, 0, 0, 0}, "busy", map[string]any{"name": "it"}); err != nil || asked.Load() != 3 {
		t.Errorf("retried a busy tool: %v after %d asks, want it to take on the third", err, asked.Load())
	}

	t.Run("put back", func(t *testing.T) {
		t.Parallel()

		s, _ := fakeServer(t)
		t.Cleanup(func() {
			if c := s.Calls("greet"); c.Answered != 2 {
				t.Errorf("after the test, greet was called %d times, want once to put back and once to undo", c.Answered)
			}
			// the delete was tried once, answered that the thing had gone,
			// and was not tried again
			if c := s.Calls("refuse"); c.Failed != 1 {
				t.Errorf("after the test, the delete was tried %d times, want once", c.Failed)
			}
		})
		s.PutBack(t, "greet", map[string]any{"name": "back"})
		s.Undo(t, "greet", map[string]any{"name": "now"})
		s.DeleteLaterIfThere(t, "refuse", nil, "not like that")
	})
}

// A run asked for every test is a whole run, and one filtered with -run or
// -skip is not: a suite's coverage check is only fair on the first.
func TestWholeRun(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		flags map[string]string
		want  bool
	}{
		{nil, true},
		{map[string]string{"test.run": "TestOne"}, false},
		{map[string]string{"test.skip": "TestSlow"}, false},
		{map[string]string{"test.v": "true"}, true},
	} {
		if got := unfiltered(func(name string) string { return tc.flags[name] }); got != tc.want {
			t.Errorf("with %v a whole run = %v, want %v", tc.flags, got, tc.want)
		}
	}
	// the binary this test runs in has the flags the answer is read from
	if flag.Lookup("test.run") == nil || flag.Lookup("test.skip") == nil {
		t.Error("the test binary has no -run or -skip flag to read")
	}
	if got, want := WholeRun(), flag.Lookup("test.run").Value.String() == "" && flag.Lookup("test.skip").Value.String() == ""; got != want {
		t.Errorf("WholeRun() = %v, want %v", got, want)
	}
}

// The readers name the field a wrong shape was found in.
func TestReaders(t *testing.T) {
	t.Parallel()

	answer := map[string]any{
		"items": []any{map[string]any{"name": "Zzyzx", "n": 3.0}, map[string]any{"name": "Quux"}},
		"names": []any{"b", "a"},
		"n":     2.0,
		"ratio": 1.5,
		"flag":  true,
		"mixed": []any{"x", 1.0},
	}
	if rows := Rows(t, answer["items"], "items"); len(rows) != 2 || Str(rows[0]["name"]) != "Zzyzx" || Num(t, rows[0]["n"], "n") != 3 || NumOr0(rows[1]["n"]) != 0 {
		t.Errorf("rows = %v", rows)
	}
	if got := Strs(t, answer["names"], "names"); !slices.Equal(got, []string{"b", "a"}) || !slices.Equal(Sorted(got), []string{"a", "b"}) || !slices.Equal(Reversed(got), []string{"a", "b"}) {
		t.Errorf("strs = %v", got)
	}
	if Num(t, answer["n"], "n") != 2 || Decimal(t, answer["ratio"], "ratio") != 1.5 || !BoolOf(answer["flag"]) || !IsBool(answer["flag"], true) || IsBool(answer["gone"], false) {
		t.Error("the numbers and the flag were read wrong")
	}
	if Str(answer["n"]) != "" || NumOr0(answer["names"]) != 0 || BoolOf(answer["n"]) {
		t.Error("a field of another kind was read as if it were there")
	}
	if got := Texts(answer["mixed"]); !slices.Equal(got, []string{"x", ""}) {
		t.Errorf("texts = %v", got)
	}
	if got := RowsOf(answer["mixed"]); len(got) != 0 {
		t.Errorf("rows of a list with no objects = %v", got)
	}
	if RowsOfAny(answer["n"]) != nil || len(RowsOfAny(answer["names"])) != 2 {
		t.Error("RowsOfAny")
	}
	if l, ok := OrEmptyList(nil).([]any); !ok || l == nil || OrEmptyList(answer["names"]) == nil {
		t.Error("OrEmptyList")
	}
	if got := WithoutName([]string{"a", "b", "a"}, "a"); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("WithoutName = %v", got)
	}
	if got := SortedKeys(map[string]int{"b": 1, "a": 2}); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("SortedKeys = %v", got)
	}
	if m := Object(t, RowsOfAny(answer["items"])[0], "items[0]"); m["name"] != "Zzyzx" {
		t.Errorf("object = %v", m)
	}
}

// Waits: a check that comes true is seen, one that never does is given up on
// within the patience, and a check that stops holding is caught.
func TestWaits(t *testing.T) {
	t.Parallel()

	n := 0
	if !EventuallyWithin(5*time.Second, func() bool { n++; return n > 2 }) {
		t.Error("a check that comes true was not seen")
	}
	start := time.Now()
	if EventuallyWithin(time.Second, func() bool { return false }) || time.Since(start) > 3*time.Second {
		t.Error("a check that never comes true was not given up on in time")
	}
	m := 0
	if Holds(func() bool { m++; return m < 3 }) {
		t.Error("a check that stops holding was not caught")
	}
	if !Eventually(func() bool { return true }) {
		t.Error("a check that is true at once was not seen")
	}
}
