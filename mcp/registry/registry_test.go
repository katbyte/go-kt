package registry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type none struct{}

// tools are a small application's: a core to find things with, a set for
// who watched what, and a set for running the server, with one tool that
// deletes and one read tool that writes a file where it runs.
var tools = []struct {
	name string
	kind Kind
}{
	{"library_list", Read},
	{"library_get", Read},
	{"library_scan", Write},
	{"library_create", Write},
	{"library_export", Read},
	{"item_get", Read},
	{"item_set_state", Write},
	{"item_send", Write},
	{"item_delete", Delete},
	{"user_list", Read},
	{"user_next_up", Read},
}

func config() Config {
	return Config{
		Toolsets: map[string][]string{
			"core":     {"library_list", "library_get", "item_get"},
			"watching": {"user_list", "user_next_up", "item_set_state"},
			"admin":    {"library_scan", "library_create", "library_export", "item_send", "item_delete"},
		},
		Essential: []string{"library_list", "item_get", "user_next_up", "item_set_state"},
		Hints: map[string]Hints{
			"library_create": {Additive: true},
			"item_set_state": {Idempotent: true},
			"item_send":      {Additive: true, SendsOut: true},
			"library_export": {WritesHere: true},
		},
	}
}

func newRegistry(cfg Config) *Registry {
	r := New(cfg)
	for _, tool := range tools {
		Add(r, tool.kind, &mcp.Tool{Name: tool.name, Description: "does " + tool.name}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
			return nil, none{}, nil
		})
	}

	return r
}

func register(t *testing.T, sel Selection) []string {
	t.Helper()

	got, err := newRegistry(config()).Register(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), sel)
	if err != nil {
		t.Fatalf("%+v: %v", sel, err)
	}

	return got
}

func refused(t *testing.T, sel Selection) string {
	t.Helper()

	_, err := newRegistry(config()).Register(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), sel)
	if err == nil {
		t.Errorf("%+v was accepted", sel)

		return ""
	}

	return err.Error()
}

// connect serves a registry's selection to a client in memory.
func connect(t *testing.T, r *Registry, sel Selection) *mcp.ClientSession {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	if _, err := r.Register(srv, sel); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// With nothing asked for, every tool is registered but the ones that
// delete; a read-only session gets the read tools alone, whatever else is
// asked for; and the delete tools come only when asked for.
func TestRegisterByKind(t *testing.T) {
	t.Parallel()

	every := []string{"item_get", "item_send", "item_set_state", "library_create", "library_export", "library_get", "library_list", "library_scan", "user_list", "user_next_up"}
	if got := register(t, Selection{}); !slices.Equal(got, every) {
		t.Errorf("with nothing asked for = %v, want every tool but the one that deletes", got)
	}
	if got := register(t, Selection{EnableDelete: true}); !slices.Contains(got, "item_delete") || len(got) != len(every)+1 {
		t.Errorf("with the delete tools = %v", got)
	}
	reads := []string{"item_get", "library_export", "library_get", "library_list", "user_list", "user_next_up"}
	for _, sel := range []Selection{{ReadOnly: true}, {ReadOnly: true, EnableDelete: true}} {
		if got := register(t, sel); !slices.Equal(got, reads) {
			t.Errorf("%+v = %v, want the read tools alone", sel, got)
		}
	}
}

// A toolset is a curated set, every tool, or a resource family, and core
// comes with whichever is asked for. A name that is none of them is refused,
// with what it could have been.
func TestToolsets(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		asked []string
		want  []string
	}{
		{[]string{"core"}, []string{"item_get", "library_get", "library_list"}},
		{[]string{"watching"}, []string{"item_get", "item_set_state", "library_get", "library_list", "user_list", "user_next_up"}},
		// several, joined by commas or given apart, with space around them
		{[]string{" watching , admin "}, []string{"item_get", "item_send", "item_set_state", "library_create", "library_export", "library_get", "library_list", "library_scan", "user_list", "user_next_up"}},
		{[]string{"watching", "admin"}, []string{"item_get", "item_send", "item_set_state", "library_create", "library_export", "library_get", "library_list", "library_scan", "user_list", "user_next_up"}},
		{[]string{"all"}, []string{"item_get", "item_send", "item_set_state", "library_create", "library_export", "library_get", "library_list", "library_scan", "user_list", "user_next_up"}},
		// a family is every tool of that prefix, with core
		{[]string{"user"}, []string{"item_get", "library_get", "library_list", "user_list", "user_next_up"}},
		{[]string{"", " "}, []string{"item_get", "item_send", "item_set_state", "library_create", "library_export", "library_get", "library_list", "library_scan", "user_list", "user_next_up"}},
	} {
		if got := register(t, Selection{Toolsets: tc.asked}); !slices.Equal(got, tc.want) {
			t.Errorf("toolsets %q = %v, want %v", tc.asked, got, tc.want)
		}
	}
	// a set still answers to the kinds
	if got := register(t, Selection{Toolsets: []string{"admin"}, EnableDelete: true, ReadOnly: true}); !slices.Equal(got, []string{"item_get", "library_export", "library_get", "library_list"}) {
		t.Errorf("admin, read-only = %v", got)
	}
	if got := register(t, Selection{Toolsets: []string{"admin"}, EnableDelete: true}); !slices.Contains(got, "item_delete") {
		t.Errorf("admin with the delete tools = %v", got)
	}

	if msg := refused(t, Selection{Toolsets: []string{"watchin"}}); msg != `unknown toolset "watchin" (sets: all, admin, core, watching; or a resource family: item, library, user)` {
		t.Errorf("an unknown toolset = %q", msg)
	}
	r := newRegistry(config())
	if got := r.ToolsetNames(); !slices.Equal(got, []string{"admin", "core", "watching"}) {
		t.Errorf("ToolsetNames = %v", got)
	}
	if got := r.FamilyNames(); !slices.Equal(got, []string{"item", "library", "user"}) {
		t.Errorf("FamilyNames = %v", got)
	}
	if got := r.Names(); len(got) != len(tools) || got[0] != "library_list" {
		t.Errorf("Names = %v, want every tool in the order added", got)
	}
}

// On its own an allow list is only the tools it names, and a deny list takes
// out what it names, by exact name, by a pattern, or by the essential
// preset; a name or pattern that reaches no tool is refused, so a typo
// cannot hide one.
func TestAllowAndDeny(t *testing.T) {
	t.Parallel()

	if got := register(t, Selection{Allow: []string{"essential"}}); !slices.Equal(got, []string{"item_get", "item_set_state", "library_list", "user_next_up"}) {
		t.Errorf("essential = %v", got)
	}
	// no core is added to an allow list: user_list alone is one tool
	if got := register(t, Selection{Allow: []string{"user_list"}}); !slices.Equal(got, []string{"user_list"}) {
		t.Errorf("user_list alone = %v", got)
	}
	if got := register(t, Selection{Allow: []string{"library_*,user_list"}, Deny: []string{"*_scan"}}); !slices.Equal(got, []string{"library_create", "library_export", "library_get", "library_list", "user_list"}) {
		t.Errorf("library_* and user_list, less *_scan = %v", got)
	}
	if got := register(t, Selection{Deny: []string{"*_delete", "item_*"}, EnableDelete: true}); slices.ContainsFunc(got, func(n string) bool { return strings.HasPrefix(n, "item_") }) {
		t.Errorf("with item_* denied = %v", got)
	}
	if msg := refused(t, Selection{Allow: []string{"bogus_*"}}); !strings.HasPrefix(msg, `allow-tools pattern "bogus_*" matches no tool (have: library_list, library_get, `) {
		t.Errorf("an allow pattern that matches nothing = %q", msg)
	}
	if msg := refused(t, Selection{Deny: []string{"nope"}}); !strings.HasPrefix(msg, `deny-tools pattern "nope" matches no tool`) {
		t.Errorf("a deny pattern that matches nothing = %q", msg)
	}
}

// Beside toolsets an allow list adds to them: the session gets what the sets
// hold and the tools named as well, wherever those tools live. A deny list
// is what narrows a set.
func TestAnAllowListBesideToolsets(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		sel  Selection
		want []string
	}{
		"a tool of another set":                       {Selection{Toolsets: []string{"core"}, Allow: []string{"library_scan"}}, []string{"item_get", "library_get", "library_list", "library_scan"}},
		"a pattern reaching other sets":               {Selection{Toolsets: []string{"core"}, Allow: []string{"user_*"}}, []string{"item_get", "library_get", "library_list", "user_list", "user_next_up"}},
		"the preset beside core":                      {Selection{Toolsets: []string{"core"}, Allow: []string{"essential"}}, []string{"item_get", "item_set_state", "library_get", "library_list", "user_next_up"}},
		"the preset beside its own set":               {Selection{Toolsets: []string{"watching"}, Allow: []string{"essential"}}, []string{"item_get", "item_set_state", "library_get", "library_list", "user_list", "user_next_up"}},
		"a pattern the set already holds":             {Selection{Toolsets: []string{"core"}, Allow: []string{"library_get"}}, []string{"item_get", "library_get", "library_list"}},
		"a pattern beside a set no longer narrows it": {Selection{Toolsets: []string{"core"}, Allow: []string{"library_*"}}, []string{"item_get", "library_create", "library_export", "library_get", "library_list", "library_scan"}},
		"a deny list narrows a set":                   {Selection{Toolsets: []string{"core"}, Deny: []string{"item_*"}}, []string{"library_get", "library_list"}},
		"a deny list takes out what was allowed":      {Selection{Toolsets: []string{"core"}, Allow: []string{"user_*"}, Deny: []string{"user_list"}}, []string{"item_get", "library_get", "library_list", "user_next_up"}},
		"every set, and a tool as well":               {Selection{Toolsets: []string{"all"}, Allow: []string{"user_list"}, EnableDelete: true}, []string{"item_delete", "item_get", "item_send", "item_set_state", "library_create", "library_export", "library_get", "library_list", "library_scan", "user_list", "user_next_up"}},
		"a delete tool allowed by name stays out":     {Selection{Toolsets: []string{"core"}, Allow: []string{"item_delete"}}, []string{"item_get", "library_get", "library_list"}},
		"a delete tool allowed, with deletes on":      {Selection{Toolsets: []string{"core"}, Allow: []string{"item_delete"}, EnableDelete: true}, []string{"item_delete", "item_get", "library_get", "library_list"}},
		"a write tool allowed in a read-only session": {Selection{Toolsets: []string{"core"}, Allow: []string{"library_scan,user_list"}, ReadOnly: true}, []string{"item_get", "library_get", "library_list", "user_list"}},
	} {
		if got := register(t, c.sel); !slices.Equal(got, c.want) {
			t.Errorf("%s: %+v registered %v, want %v", name, c.sel, got, c.want)
		}
	}

	// a name that reaches no tool at all is still refused beside a set
	if msg := refused(t, Selection{Toolsets: []string{"core"}, Allow: []string{"bogus"}}); !strings.HasPrefix(msg, `allow-tools pattern "bogus" matches no tool`) {
		t.Errorf("a name that matches nothing, beside core = %q", msg)
	}
}

// An allow list that asks for nothing is not the same as no allow list: the
// preset of an application that left it empty registers no tool, where no
// list at all registers every one.
func TestAnAllowListThatNamesNothing(t *testing.T) {
	t.Parallel()

	cfg := config()
	cfg.Essential = nil
	got, err := newRegistry(cfg).Register(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), Selection{Allow: []string{"essential"}})
	if err != nil || len(got) != 0 {
		t.Errorf("an empty preset registered %v (%v), want no tool", got, err)
	}

	if got := register(t, Selection{Allow: []string{" , "}}); len(got) != 10 {
		t.Errorf("an allow list of blanks registered %d tools, want all ten that need no delete gate, as with no list", len(got))
	}
}

// Describe makes the choice Register makes, with nothing to register on,
// and says each tool's kind and the set it sits in.
func TestDescribe(t *testing.T) {
	t.Parallel()

	for _, sel := range []Selection{{}, {ReadOnly: true}, {EnableDelete: true}, {Toolsets: []string{"watching"}}, {Allow: []string{"essential"}, Deny: []string{"item_*"}}} {
		got, err := newRegistry(config()).Describe(sel)
		if err != nil {
			t.Fatalf("%+v: %v", sel, err)
		}
		names := make([]string, 0, len(got))
		for _, info := range got {
			names = append(names, info.Name)
		}
		if want := register(t, sel); !slices.Equal(names, want) {
			t.Errorf("%+v: Describe lists %v, Register registers %v", sel, names, want)
		}
	}

	got, err := newRegistry(config()).Describe(Selection{EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Info{}
	for _, info := range got {
		by[info.Name] = info
	}
	for name, want := range map[string]Info{
		"library_list":   {Name: "library_list", Kind: "read", Toolset: "core", Description: "does library_list"},
		"item_set_state": {Name: "item_set_state", Kind: "write", Toolset: "watching", Description: "does item_set_state"},
		"item_delete":    {Name: "item_delete", Kind: "delete", Toolset: "admin", Description: "does item_delete"},
	} {
		if by[name] != want {
			t.Errorf("%s = %+v, want %+v", name, by[name], want)
		}
	}
	if _, err := newRegistry(config()).Describe(Selection{Allow: []string{"bogus"}}); err == nil {
		t.Error("Describe took a pattern that matches nothing")
	}
}

// What a client is told of each tool follows its kind and its hints: a
// read is read-only unless it writes a file where it runs, a write is
// destructive unless it only adds, and a delete is destructive always.
func TestAnnotations(t *testing.T) {
	t.Parallel()

	cs := connect(t, newRegistry(config()), Selection{EnableDelete: true})
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type hint struct{ readOnly, destructive, idempotent, openWorld bool }
	want := map[string]hint{
		"library_list":   {readOnly: true},
		"library_export": {},
		"library_scan":   {destructive: true},
		"library_create": {},
		"item_set_state": {destructive: true, idempotent: true},
		"item_send":      {openWorld: true},
		"item_delete":    {destructive: true},
	}
	seen := 0
	for _, tool := range listed.Tools {
		w, checked := want[tool.Name]
		if !checked {
			continue
		}
		seen++
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil || a.OpenWorldHint == nil {
			t.Errorf("%s: annotations %+v, want the destructive and open-world hints said either way", tool.Name, a)

			continue
		}
		if got := (hint{a.ReadOnlyHint, *a.DestructiveHint, a.IdempotentHint, *a.OpenWorldHint}); got != w {
			t.Errorf("%s: %+v, want %+v", tool.Name, got, w)
		}
	}
	if seen != len(want) {
		t.Errorf("%d of %d tools were listed", seen, len(want))
	}
}

func TestMatchPattern(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"item_get", "item_get", true},
		{"item_get", "item_gets", false},
		{"item_*", "item_get", true},
		{"item_*", "library_items", false},
		{"*_delete", "item_delete", true},
		// the way to turn every tool that deletes off has to reach this one
		{"*_delete", "item_orphans_delete", true},
		{"*", "anything", true},
	} {
		if got := matchPattern(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v", tc.pattern, tc.name, got)
		}
	}
}

// A nil slice in an answer must be sent as [], so a client can tell "none"
// from "not fetched".
func TestEmptyNilSlices(t *testing.T) {
	t.Parallel()

	type inner struct{ Tags []string }
	type out struct {
		Items  []inner
		Ptr    *inner
		Names  []string
		Nested [][]string
		Keep   []string
	}
	v := out{Items: []inner{{}}, Ptr: &inner{}, Keep: []string{"x"}}
	emptyNilSlices(reflect.ValueOf(&v).Elem())

	if v.Names == nil || v.Nested == nil || v.Items[0].Tags == nil || v.Ptr.Tags == nil {
		t.Errorf("nil slices survived: %+v", v)
	}
	if len(v.Keep) != 1 {
		t.Error("a populated slice was touched")
	}
}

type listOut struct {
	Names []string `json:"names"`
}

// Through a served tool an empty list reaches the client as [], and a
// handler that panics answers its call with an error naming the tool and
// logs the stack, rather than ending the session: the next call is served.
func TestAServedToolSendsEmptyListsAndSurvivesAPanic(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		logged strings.Builder
	)
	r := New(Config{LogError: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(&logged, format, args...)
	}})
	Add(r, Write, &mcp.Tool{Name: "zzyzx_panic"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		var found map[string]*Info

		return nil, none{}, errors.New(found["9"].Name) // a nil dereference
	})
	Add(r, Read, &mcp.Tool{Name: "zzyzx_list"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, listOut, error) {
		return nil, listOut{}, nil
	})
	cs := connect(t, r, Selection{})

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "zzyzx_panic", Arguments: map[string]any{}})
	if err != nil || !res.IsError {
		t.Fatalf("a tool that panics: %v %+v, want a tool error", err, res)
	}
	var said strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			said.WriteString(text.Text)
		}
	}
	if msg := said.String(); !strings.Contains(msg, "internal error in zzyzx_panic") || !strings.Contains(msg, "nil pointer") {
		t.Errorf("the refusal = %q, want the tool and the panic named", msg)
	}
	mu.Lock()
	log := logged.String()
	mu.Unlock()
	if !strings.Contains(log, "internal error in zzyzx_panic") || !strings.Contains(log, "goroutine") {
		t.Errorf("the log = %q, want the panic and its stack", log)
	}

	// and the session goes on
	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "zzyzx_list", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("the call after the panic: %v %+v", err, res)
	}
	out, ok := res.StructuredContent.(map[string]any)
	if names, isList := out["names"].([]any); !ok || !isList || len(names) != 0 {
		t.Errorf("an empty list reached the client as %v, want names: []", res.StructuredContent)
	}
}

func TestKindNames(t *testing.T) {
	t.Parallel()

	if Read.String() != "read" || Write.String() != "write" || Delete.String() != "delete" {
		t.Errorf("kinds read %s, %s and %s", Read, Write, Delete)
	}
}
