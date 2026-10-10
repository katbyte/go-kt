package registry

import (
	"context"
	"encoding/json"
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

// A tool that has the server bring something in from outside and run it is
// told to a client as open world, as one that sends something out is: MCP has
// the one hint for both. It is no less a write for it.
func TestInstallsIsOpenWorld(t *testing.T) {
	t.Parallel()

	cfg := config()
	cfg.Hints["library_scan"] = Hints{Idempotent: true, Installs: true}
	listed, err := connect(t, newRegistry(cfg), Selection{}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tool := range listed.Tools {
		if tool.Name != "library_scan" {
			continue
		}
		if a := tool.Annotations; a == nil || a.OpenWorldHint == nil || !*a.OpenWorldHint || a.DestructiveHint == nil || !*a.DestructiveHint || !a.IdempotentHint || a.ReadOnlyHint {
			t.Errorf("a tool that installs: %+v, want open world, and still a write that may overwrite", a)
		}

		return
	}
	t.Error("the tool that installs was not listed")
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
// from "not fetched", and a nil map as {}, which the tool's schema says it
// is.
func TestEmptyNils(t *testing.T) {
	t.Parallel()

	type inner struct{ Tags []string }
	type out struct {
		Items  []inner
		Ptr    *inner
		Names  []string
		Nested [][]string
		Keep   []string
		ByName map[string]inner
		Held   map[string]int
	}
	v := out{Items: []inner{{}}, Ptr: &inner{}, Keep: []string{"x"}, Held: map[string]int{"x": 1}}
	emptyNils(reflect.ValueOf(&v).Elem())

	if v.Names == nil || v.Nested == nil || v.Items[0].Tags == nil || v.Ptr.Tags == nil {
		t.Errorf("nil slices survived: %+v", v)
	}
	if v.ByName == nil {
		t.Errorf("a nil map survived: %+v", v)
	}
	if len(v.Keep) != 1 || v.Held["x"] != 1 {
		t.Error("a populated slice or map was touched")
	}
}

type listOut struct {
	Names  []string          `json:"names"`
	ByName map[string]string `json:"by_name"`
}

// Through a served tool an empty list reaches the client as [] and an empty
// map as {}, and a handler that panics answers its call with an error naming the tool and
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
	if byName, isMap := out["by_name"].(map[string]any); !isMap || len(byName) != 0 {
		t.Errorf("an empty map reached the client as %v, want by_name: {}", res.StructuredContent)
	}
}

func TestKindNames(t *testing.T) {
	t.Parallel()

	if Read.String() != "read" || Write.String() != "write" || Delete.String() != "delete" {
		t.Errorf("kinds read %s, %s and %s", Read, Write, Delete)
	}
}

// The tools the calls of a registry are tried on: one that lists, one that
// changes a thing and takes a password, and one that deletes.
type thingListIn struct {
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Kind   string `json:"type,omitempty"`
}

type thingSetIn struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	Confirm  bool   `json:"confirm,omitempty"`
}

type thingDeleteIn struct {
	ID string `json:"id"`
}

// things is a registry of the three, whose write log is kept for a test to
// read, and a client connected to it.
func things(t *testing.T) (cs *mcp.ClientSession, written func() []string) {
	t.Helper()

	var mu sync.Mutex
	var lines []string
	r := New(Config{LogWrite: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()

		lines = append(lines, fmt.Sprintf(format, args...))
	}})
	Add(r, Read, &mcp.Tool{Name: "thing_list", Description: "lists things"}, func(context.Context, *mcp.CallToolRequest, thingListIn) (*mcp.CallToolResult, none, error) {
		return nil, none{}, nil
	})
	Add(r, Write, &mcp.Tool{Name: "thing_set", Description: "changes a thing"}, func(_ context.Context, _ *mcp.CallToolRequest, in thingSetIn) (*mcp.CallToolResult, none, error) {
		if in.Name == "nobody" {
			return nil, none{}, errors.New("there is no thing called nobody")
		}

		return nil, none{}, nil
	})
	Add(r, Delete, &mcp.Tool{Name: "thing_delete", Description: "deletes a thing"}, func(context.Context, *mcp.CallToolRequest, thingDeleteIn) (*mcp.CallToolResult, none, error) {
		return nil, none{}, nil
	})

	return connect(t, r, Selection{EnableDelete: true}), func() []string {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(lines)
	}
}

// said is the text of a tool's answer.
func said(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}

	return b.String()
}

// A call with an argument the tool does not take is refused, as the SDK
// refuses it, and told which arguments the tool does take: a caller that
// asked to skip sees offset in the answer.
func TestAnUnknownArgumentIsToldTheToolsOwn(t *testing.T) {
	t.Parallel()

	cs, _ := things(t)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_list", Arguments: map[string]any{"skip": 5}})
	if err != nil || !res.IsError {
		t.Fatalf("an argument the tool does not take: %v %+v, want a tool error", err, res)
	}
	if msg := said(res); !strings.Contains(msg, `unexpected additional properties ["skip"]`) || !strings.HasSuffix(msg, ": thing_list takes limit, offset and type") {
		t.Errorf("the refusal = %q, want the SDK's words and then the arguments the tool takes", msg)
	}

	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_delete", Arguments: map[string]any{"id": "7", "force": true}})
	if err != nil || !res.IsError || !strings.HasSuffix(said(res), ": thing_delete takes id") {
		t.Errorf("a tool of one argument: %v %q", err, said(res))
	}

	// another refusal of the arguments is left as the SDK worded it: it already names what is missing
	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_delete", Arguments: map[string]any{}})
	if err != nil || !res.IsError || strings.Contains(said(res), "takes") {
		t.Errorf("a missing argument: %v %q, want the SDK's own refusal", err, said(res))
	}
	// and a call the tool takes is answered
	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_list", Arguments: map[string]any{"offset": 5}})
	if err != nil || res.IsError {
		t.Errorf("a call with the argument spelled right: %v %q", err, said(res))
	}
}

// The arguments a tool takes are the JSON names of its input's fields, in
// the order they are declared: an embedded struct's among them, a field kept
// out of the JSON left out, and one with no name of its own under its Go
// name.
func TestArgumentNames(t *testing.T) {
	t.Parallel()

	type paging struct {
		Limit  int `json:"limit,omitempty"`
		Offset int `json:"offset"`
	}
	type in struct {
		ID string `json:"id"`
		paging
		Internal string `json:"-"`
		Untagged bool
		hidden   int
	}
	_ = in{}.hidden

	if got := argumentNames(reflect.TypeFor[*in]()); !slices.Equal(got, []string{"id", "limit", "offset", "Untagged"}) {
		t.Errorf("argumentNames = %v", got)
	}
	if got := argumentNames(reflect.TypeFor[none]()); len(got) != 0 {
		t.Errorf("an input with no fields takes %v", got)
	}
	if got := argumentNames(reflect.TypeFor[map[string]any]()); got != nil {
		t.Errorf("an input that is no struct takes %v", got)
	}
}

// Every call of a write or a delete tool is written down, in one line: the
// kind, the tool, what became of the call, and what it sent with its
// credentials blanked. A call whose arguments were turned away never
// reached the tool and is written down all the same; a read is not.
func TestAWriteIsWrittenDown(t *testing.T) {
	t.Parallel()

	cs, written := things(t)
	call := func(name string, arguments map[string]any) {
		t.Helper()

		if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments}); err != nil {
			t.Fatal(err)
		}
	}

	call("thing_list", map[string]any{"limit": 5})
	call("thing_set", map[string]any{"name": "one", "password": "hunter2", "confirm": true})
	call("thing_set", map[string]any{"name": "nobody"})
	call("thing_set", map[string]any{"title": "two\nwrite thing_delete answered {}"})
	call("thing_delete", map[string]any{"id": "7"})

	got := written()
	want := []string{
		`write thing_set answered {"confirm":true,"name":"one","password":"REDACTED"}`,
		`write thing_set failed {"name":"nobody"}: there is no thing called nobody`,
		`write thing_set refused {"title":"two\nwrite thing_delete answered {}"}: validating "arguments": validating root: `,
		`delete thing_delete answered {"id":"7"}`,
	}
	if len(got) != len(want) {
		t.Fatalf("%d lines were written, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) || strings.ContainsAny(got[i], "\n\r") {
			t.Errorf("line %d = %q, want it to start %q and to be one line", i+1, got[i], want[i])
		}
	}
	if strings.Contains(strings.Join(got, ""), "hunter2") {
		t.Errorf("a password was written down: %q", got)
	}
}

// What the cleaning of answers is tried on: text at every depth a tool might
// answer it at, with a character that does not show in each piece of it,
// written by its number so that this file holds none.
const (
	dirty   = "Zzyzx\u200b Road"
	cleaned = "Zzyzx Road"
)

type dirtyKind string

type dirtyRow struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

type dirtyPaging struct {
	Next string `json:"next"`
}

type dirtyOut struct {
	dirtyPaging

	Title    string         `json:"title"`
	Long     string         `json:"long"`
	Path     string         `json:"path"`
	Kind     dirtyKind      `json:"kind"`
	Count    int            `json:"count"`
	Tags     []string       `json:"tags"`
	Rows     []dirtyRow     `json:"rows"`
	First    *dirtyRow      `json:"first"`
	ByTitle  map[string]int `json:"by_title"`
	Extra    any            `json:"extra"`
	Folders  []string       `json:"folders"`
	Internal string         `json:"-"`
}

// dirtyTools is a registry of three tools that answer dirty text - in an
// answer, in a refusal, and in words of the result's own - and a client
// connected to it.
func dirtyTools(t *testing.T, cfg Config) (*Registry, *mcp.ClientSession) {
	t.Helper()

	r := New(cfg)
	Add(r, Read, &mcp.Tool{Name: "thing_read", Description: "reads a thing"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, dirtyOut, error) {
		return nil, dirtyOut{
			dirtyPaging: dirtyPaging{Next: dirty},
			Title:       dirty, Long: dirty + " and on and on", Path: dirty, Kind: dirty, Count: 3,
			Tags: []string{dirty, "plain"}, Rows: []dirtyRow{{Title: dirty, Path: dirty}}, First: &dirtyRow{Title: dirty, Path: dirty},
			ByTitle: map[string]int{dirty: 1, "plain": 2},
			Extra:   map[string]any{"title": dirty, "path": dirty, "deep": []any{dirty, 4.0}},
			Folders: []string{dirty}, Internal: dirty,
		}, nil
	})
	Add(r, Read, &mcp.Tool{Name: "thing_refuse", Description: "refuses"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		return nil, none{}, errors.New("there is no " + dirty + " and on and on")
	})
	Add(r, Read, &mcp.Tool{Name: "thing_say", Description: "answers in words"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found " + dirty}}}, none{}, nil
	})

	return r, connect(t, r, Selection{})
}

// read is what thing_read answered, decoded.
func read(t *testing.T, cs *mcp.ClientSession) dirtyOut {
	t.Helper()

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_read", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("thing_read: %v", err)
	}
	if res.IsError {
		t.Fatalf("thing_read refused: %q", said(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out dirtyOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the answer %s: %v", raw, err)
	}

	return out
}

// With CleanAnswers set, every piece of text in an answer is cleaned and cut
// to the length, however deep it is and whatever holds it; what is answered
// under a name in LeaveAsIs is as the tool answered it, and so is all under
// it; and a refusal's words and a result's own are cleaned as an answer is.
func TestAnswersAreCleaned(t *testing.T) {
	t.Parallel()

	_, cs := dirtyTools(t, Config{CleanAnswers: 14, LeaveAsIs: []string{"path", "folders"}})
	out := read(t, cs)

	for what, got := range map[string]string{
		"a field": out.Title, "a field of an embedded struct": out.Next, "a named kind of text": string(out.Kind), "one of a list": out.Tags[0],
		"a field of a row": out.Rows[0].Title, "a field behind a pointer": out.First.Title,
	} {
		if got != cleaned {
			t.Errorf("%s = %q, want it cleaned", what, got)
		}
	}
	if out.Long != "Zzyzx Road an\u2026" || out.Tags[1] != "plain" || out.Count != 3 {
		t.Errorf("text past the length = %q, plain text %q, a number %d", out.Long, out.Tags[1], out.Count)
	}
	if _, kept := out.ByTitle[dirty]; kept || out.ByTitle[cleaned] != 1 || out.ByTitle["plain"] != 2 {
		t.Errorf("a map keyed by text = %v, want its key cleaned and its values kept", out.ByTitle)
	}
	extra, isObject := out.Extra.(map[string]any)
	deep, isList := extra["deep"].([]any)
	if !isObject || !isList || extra["title"] != cleaned || len(deep) != 2 || deep[0] != cleaned || deep[1] != 4.0 {
		t.Errorf("what an untyped field holds = %v, want its text cleaned at every depth", out.Extra)
	}

	// what a later call hands back is as the tool answered it
	if out.Path != dirty || out.Rows[0].Path != dirty || out.First.Path != dirty || extra["path"] != dirty || out.Folders[0] != dirty {
		t.Errorf("what is to be left as it is: %q, %q, %q, %v, %q", out.Path, out.Rows[0].Path, out.First.Path, extra["path"], out.Folders)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_refuse", Arguments: map[string]any{}})
	if err != nil || !res.IsError || said(res) != "there is no Z\u2026" {
		t.Errorf("a refusal = %v %q, want its words cleaned and cut", err, said(res))
	}
	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_say", Arguments: map[string]any{}})
	if err != nil || res.IsError || said(res) != "found Zzyzx R\u2026" {
		t.Errorf("a result's own words = %v %q, want them cleaned and cut", err, said(res))
	}
}

// Nothing is cleaned until a server says so: it has first to name what a
// later call hands back.
func TestAnswersAreAsTheyWereUntilAsked(t *testing.T) {
	t.Parallel()

	_, cs := dirtyTools(t, Config{LeaveAsIs: []string{"path"}})
	if out := read(t, cs); out.Title != dirty || out.Long != dirty+" and on and on" || out.Tags[0] != dirty {
		t.Errorf("with CleanAnswers not set the answer = %+v", out)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_refuse", Arguments: map[string]any{}})
	if err != nil || said(res) != "there is no "+dirty+" and on and on" {
		t.Errorf("with CleanAnswers not set a refusal = %v %q", err, said(res))
	}
}

// A name on the list of what to leave as it is that no tool answers a field
// under leaves nothing and says nothing, so a server's test can ask which
// those are.
func TestLeaveAsIsUnused(t *testing.T) {
	t.Parallel()

	r, _ := dirtyTools(t, Config{CleanAnswers: 100, LeaveAsIs: []string{"path", "gone", "folders", "next", "Internal", "also_gone"}})
	if got := r.LeaveAsIsUnused(); !slices.Equal(got, []string{"gone", "Internal", "also_gone"}) {
		t.Errorf("unused = %v, want the names no tool answers under, a field kept out of the JSON among them", got)
	}
	if r, _ := dirtyTools(t, Config{LeaveAsIs: []string{"path", "title"}}); len(r.LeaveAsIsUnused()) != 0 {
		t.Errorf("a list every name of which is answered = %v", r.LeaveAsIsUnused())
	}
}

// A tool that saves a thing with settings of someone else's naming, as an
// indexer or a container is saved.
type thingSaveIn struct {
	Name     string              `json:"name"`
	Settings map[string]any      `json:"settings,omitempty"`
	Set      map[string]any      `json:"set,omitempty"`
	Env      []map[string]string `json:"env,omitempty"`
	Auth     thingAuth           `json:"auth"`
	Webhook  string              `json:"webhook,omitempty"`
	Command  []string            `json:"command,omitempty"`
	Feed     string              `json:"feed,omitempty"`
	Note     string              `json:"note,omitempty"`
	IDs      []int               `json:"ids,omitempty"`
	Big      int64               `json:"big,omitempty"`
}

type thingAuth struct {
	User     string `json:"user"`
	Cookie   string `json:"cookie,omitempty"`
	Password string `json:"password,omitempty"`
}

// saving is a registry with a tool that saves a thing, one that takes
// whatever it is sent and one that takes nothing, each failing with what
// fails says, and the lines written for the calls of them.
func saving(t *testing.T, cfg Config, fails func(thingSaveIn) error) (cs *mcp.ClientSession, written func() []string) {
	t.Helper()

	var mu sync.Mutex
	var lines []string
	cfg.LogWrite = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()

		lines = append(lines, fmt.Sprintf(format, args...))
	}
	r := New(cfg)
	Add(r, Write, &mcp.Tool{Name: "thing_save", Description: "saves a thing"}, func(_ context.Context, _ *mcp.CallToolRequest, in thingSaveIn) (*mcp.CallToolResult, none, error) {
		if fails == nil {
			return nil, none{}, nil
		}

		return nil, none{}, fails(in)
	})
	Add(r, Write, &mcp.Tool{Name: "thing_patch", Description: "changes whatever it is sent"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, none, error) {
		return nil, none{}, nil
	})
	Add(r, Write, &mcp.Tool{Name: "thing_sweep", Description: "sweeps, and takes nothing"}, func(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, none, error) {
		return nil, none{}, nil
	})

	return connect(t, r, Selection{}), func() []string {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(lines)
	}
}

// checkWritten checks the lines a suite's calls were written down as, and
// that none of secrets is anywhere in them.
func checkWritten(t *testing.T, got, want, secrets []string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Errorf("written down:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, secret := range secrets {
		if strings.Contains(strings.Join(got, "\n"), secret) {
			t.Errorf("%q was written down", secret)
		}
	}
}

// What a write sent is written down with every credential blanked and every
// name kept: an argument named as a credential is, one the server names
// whatever it holds, and everything under a free-form object, whose names
// are someone else's and say nothing of what they hold. The rest is written
// as it was sent, a number too large for a float among it.
func TestAWriteIsWrittenDownWithoutItsSecrets(t *testing.T) {
	t.Parallel()

	cs, written := saving(t, Config{SecretArguments: []string{"Webhook", "command"}}, nil)
	call := func(name string, arguments map[string]any) {
		t.Helper()

		if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments}); err != nil {
			t.Fatal(err)
		}
	}

	call("thing_save", map[string]any{
		"name":     "one",
		"settings": map[string]any{"cookie": "c00kie", "passkey": "pa55key", "port": 9117, "nested": map[string]any{"x": "d33p"}, "none": nil},
		"set":      map[string]any{"interval": "sixty"},
		"env":      []any{map[string]any{"DB_PASS": "hunter2"}},
		"auth":     map[string]any{"user": "kt", "cookie": "c2kie", "password": "p2ssword"},
		"webhook":  "https://hooks.test/T0KEN",
		"command":  []any{"serve", "--password=fl4g"},
		"ids":      []any{1, 2, 3},
		"big":      json.Number("9007199254740993"),
	})
	call("thing_patch", map[string]any{"title": "s3cret", "list": []any{"l1sted"}})

	checkWritten(t, written(), []string{
		`write thing_save answered {"auth":{"cookie":"REDACTED","password":"REDACTED","user":"kt"},"big":9007199254740993,"command":["REDACTED","REDACTED"],"env":[{"DB_PASS":"REDACTED"}],"ids":[1,2,3],"name":"one","set":{"interval":"REDACTED"},"settings":{"cookie":"REDACTED","nested":{"x":"REDACTED"},"none":null,"passkey":"REDACTED","port":"REDACTED"},"webhook":"REDACTED"}`,
		`write thing_patch answered {"list":["REDACTED"],"title":"REDACTED"}`,
	}, []string{"c00kie", "pa55key", "9117", "d33p", "sixty", "hunter2", "c2kie", "p2ssword", "T0KEN", "fl4g", "s3cret", "l1sted"})
}

// A free-form argument a server says holds no credential is written as it
// was sent, for the one tool it says it of, and a name under it that reads
// as a credential's is blanked all the same.
func TestAFreeFormArgumentTheServerShows(t *testing.T) {
	t.Parallel()

	cs, written := saving(t, Config{ShownArguments: map[string][]string{"thing_save": {"set"}, "thing_patch": {"set"}}}, nil)
	for _, name := range []string{"thing_save", "thing_patch"} {
		if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{
			"name":     "one",
			"set":      map[string]any{"interval": 60, "mode": "quiet", "smtp_password": "p2ssword", "proxy": "http://kt:pr0xy@proxy.test:3128"}, //nolint:gosec // a made-up password, to see that it is not written down
			"settings": map[string]any{"mode": "l0ud"},
			"auth":     map[string]any{"user": "kt"},
		}}); err != nil {
			t.Fatal(err)
		}
	}

	checkWritten(t, written(), []string{
		`write thing_save answered {"auth":{"user":"kt"},"name":"one","set":{"interval":60,"mode":"quiet","proxy":"http://REDACTED@proxy.test:3128","smtp_password":"REDACTED"},"settings":{"mode":"REDACTED"}}`,
		`write thing_patch answered {"auth":{"user":"REDACTED"},"name":"REDACTED","set":{"interval":"REDACTED","mode":"REDACTED","proxy":"REDACTED","smtp_password":"REDACTED"},"settings":{"mode":"REDACTED"}}`,
	}, []string{"p2ssword", "pr0xy", "l0ud"})
}

// An address is written with what it may carry a credential in blanked,
// whatever the argument is called and wherever in a piece of text it is:
// who it signs in as, each parameter's value and what follows its "#". Its
// host, its path and its parameters' names stay.
func TestAnAddressIsWrittenDownWithoutItsSecrets(t *testing.T) {
	t.Parallel()

	cs, written := saving(t, Config{}, nil)
	for _, arguments := range []map[string]any{
		{"feed": "https://feeds.test/rss/someshow?auth=PRIVATE-T0KEN&format=rss&flag&"},
		{"feed": "https://gh0-t0ken@git.test/org/repo.git"},
		{"feed": "postgres://kt:dbp4ss@db.test:5432/things?sslmode=require#fr4gment"},                                                        //nolint:gosec // a made-up password, to see that it is not written down
		{"note": "fetch https://kt:n0te@files.test/a?key=n0tekey <https://plain.test/b> then say so", "name": "plain text, a/b and c:d?e=f"}, //nolint:gosec // a made-up password, to see that it is not written down
	} {
		arguments["auth"] = map[string]any{"user": "kt"}
		if arguments["name"] == nil {
			arguments["name"] = "one"
		}

		if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_save", Arguments: arguments}); err != nil {
			t.Fatal(err)
		}
	}

	checkWritten(t, written(), []string{
		`write thing_save answered {"auth":{"user":"kt"},"feed":"https://feeds.test/rss/someshow?auth=REDACTED&format=REDACTED&REDACTED&","name":"one"}`,
		`write thing_save answered {"auth":{"user":"kt"},"feed":"https://REDACTED@git.test/org/repo.git","name":"one"}`,
		`write thing_save answered {"auth":{"user":"kt"},"feed":"postgres://REDACTED@db.test:5432/things?sslmode=REDACTED#REDACTED","name":"one"}`,
		`write thing_save answered {"auth":{"user":"kt"},"name":"plain text, a/b and c:d?e=f","note":"fetch https://REDACTED@files.test/a?key=REDACTED <https://plain.test/b> then say so"}`,
	}, []string{"PRIVATE-T0KEN", "gh0-t0ken", "dbp4ss", "fr4gment", "n0te"})
}

// A failure is apt to repeat what the call sent, the address it could not
// reach or the value it would not take, so what was blanked in the
// arguments is blanked in the failure beside them, and an address in it is
// written as one in the arguments is. What was blanked is blanked where it
// stands by itself and is long enough to be more than a word of the
// failure's own: "require" is, "required" and "kt" are not.
func TestAFailureIsWrittenDownWithoutTheCallsSecrets(t *testing.T) {
	t.Parallel()

	cs, written := saving(t, Config{}, func(in thingSaveIn) error {
		return fmt.Errorf("Get %q: no such host; the cookie %v was not taken, nor %s, by https://api.test/v1?apikey=s3cond-key as %s, and a mode is required, not to require", in.Feed, in.Settings["cookie"], in.Auth.Password, in.Settings["as"])
	})
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_save", Arguments: map[string]any{
		"name":     "one",
		"feed":     "https://feeds.test/rss?auth=PRIVATE-T0KEN&mode=require",
		"settings": map[string]any{"cookie": "c00kie-c00kie", "as": "kt"},
		"auth":     map[string]any{"user": "kt", "password": "p2ssword"},
	}}); err != nil {
		t.Fatal(err)
	}

	checkWritten(t, written(), []string{
		`write thing_save failed {"auth":{"password":"REDACTED","user":"kt"},"feed":"https://feeds.test/rss?auth=REDACTED&mode=REDACTED","name":"one","settings":{"as":"REDACTED","cookie":"REDACTED"}}: Get "https://feeds.test/rss?auth=REDACTED&mode=REDACTED": no such host; the cookie REDACTED was not taken, nor REDACTED, by https://api.test/v1?apikey=REDACTED as kt, and a mode is required, not to REDACTED`,
	}, []string{"PRIVATE-T0KEN", "c00kie", "p2ssword", "s3cond-key"})
}

// A tool whose input is any takes no arguments, and refuses one as every
// other tool does, naming what it takes: nothing. The call is written down
// with what it sent blanked, there being no names of the tool's to go by.
func TestAToolThatTakesNothingRefusesAnArgument(t *testing.T) {
	t.Parallel()

	cs, written := saving(t, Config{}, nil)
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_sweep", Arguments: map[string]any{"force": "y3s"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.HasSuffix(said(res), "thing_sweep takes no arguments") {
		t.Errorf("an argument sent to a tool that takes none was answered %q, want it refused and told so", said(res))
	}

	if res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_sweep"}); err != nil || res.IsError {
		t.Errorf("a call with no arguments was answered %v, %v", res, err)
	}
	if res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_sweep", Arguments: map[string]any{}}); err != nil || res.IsError {
		t.Errorf("a call with empty arguments was answered %v, %v", res, err)
	}

	got := written()
	if len(got) != 3 || !strings.HasPrefix(got[0], `write thing_sweep refused {"force":"REDACTED"}: `) || got[1] != "write thing_sweep answered {}" || got[2] != "write thing_sweep answered {}" {
		t.Errorf("written down: %q", got)
	}
}

// Arguments that cannot be read cannot have what is in them blanked, so
// they are not written at all.
func TestArgumentsThatDoNotReadAreNotWritten(t *testing.T) {
	t.Parallel()

	h := hider{}
	if got := h.arguments(json.RawMessage(`{"password": "hunter2`)); got != notShown {
		t.Errorf("arguments cut short were written as %q", got)
	}
}

// What a tool that fails, and one that answers, say of an address.
type quoteIn struct {
	How string `json:"how"`
}

type quoteOut struct {
	Link string `json:"link"`
}

// A failure's words are what the caller reads, and a server is apt to quote
// an address back in them with a credential of its own on the end. Whoever
// worded the failure, the tool's error or a result the tool wrote itself,
// and whatever the tool's kind, each address in it reaches the caller
// without who it signs in as, the value of any parameter or what follows
// its "#", and the rest of the words as they were, but for a comma against
// the end of it, which cannot be told from the end of the secret. An
// answer's addresses are the caller's to use and are left as they are.
func TestAFailureIsReadWithoutAnAddressesSecrets(t *testing.T) {
	t.Parallel()

	const quoted = "https://kt:s1gnin@site.test/api?t=caps&apikey=st0red-key#fr4gment" //nolint:gosec // a made-up password, to see that it is not handed on
	const shown = "https://REDACTED@site.test/api?t=REDACTED&apikey=REDACTED#REDACTED"
	own := &mcp.TextContent{Text: "the server said so of " + quoted}

	r := New(Config{LogWrite: func(string, ...any) {}})
	Add(r, Read, &mcp.Tool{Name: "thing_quote", Description: "quotes an address"}, func(_ context.Context, _ *mcp.CallToolRequest, in quoteIn) (*mcp.CallToolResult, quoteOut, error) {
		switch in.How {
		case "error":
			return nil, quoteOut{}, fmt.Errorf("Uri didn't match expected pattern: %s, try again (1/2)", quoted)
		case "result":
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{own, &mcp.TextContent{Text: "and nothing more"}}}, quoteOut{}, nil
		default:
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "it is at " + quoted}}}, quoteOut{Link: quoted}, nil
		}
	})
	Add(r, Write, &mcp.Tool{Name: "thing_break", Description: "fails"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		return nil, none{}, fmt.Errorf("Get %q: no such host", quoted)
	})
	cs := connect(t, r, Selection{})

	for _, test := range []struct{ tool, how, want string }{
		{"thing_quote", "error", "Uri didn't match expected pattern: " + shown + " try again (1/2)"},
		{"thing_quote", "result", "the server said so of " + shown + "and nothing more"},
		{"thing_break", "", `Get "` + shown + `": no such host`},
	} {
		arguments := map[string]any{}
		if test.how != "" {
			arguments["how"] = test.how
		}

		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: test.tool, Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || said(res) != test.want {
			t.Errorf("%s %s failed with %q, want %q", test.tool, test.how, said(res), test.want)
		}
	}
	if own.Text != "the server said so of "+quoted {
		t.Errorf("the tool's own words were written over: %q", own.Text)
	}

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_quote", Arguments: map[string]any{"how": "answer"}})
	if err != nil {
		t.Fatal(err)
	}
	var out quoteOut
	if err := json.Unmarshal(mustJSON(t, res.StructuredContent), &out); err != nil {
		t.Fatal(err)
	}
	if res.IsError || said(res) != "it is at "+quoted || out.Link != quoted {
		t.Errorf("an answer's address was changed: %q, %q", said(res), out.Link)
	}
}

// mustJSON is a value as JSON.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// What a tool that tests a thing answers: what the server said went wrong,
// in the shapes a server's words come in, beside what a caller is to use.
type probeRow struct {
	Name     string            `json:"name"`
	Failures []string          `json:"failures"`
	Message  *string           `json:"message"`
	Details  map[string]string `json:"details"`
	Link     string            `json:"link"`
	Extra    map[string]any    `json:"extra"`
}

type probeOut struct {
	Rows []probeRow `json:"rows"`
	GUID string     `json:"guid"`
}

// A server's own words for what went wrong quote an address with a
// credential of the server's on it, and a tool answers with them. Under the
// names the server gives for such words, each address is answered without
// what it may carry one in, however deep the text is and whether the name
// is a field's or a map's key. Every other address in the answer is the
// caller's to use and is as it was, and so is one under a name that is
// handed back. It is done whether or not answers are cleaned, and after
// the cleaning when they are, so that a character that does not show
// cannot keep an address from being read as one.
func TestAServersOwnWordsAreAnsweredWithoutAnAddressesSecrets(t *testing.T) {
	t.Parallel()

	const quoted = "http://host.test/api?t=movie&apikey=st0red-key"
	const shown = "http://host.test/api?t=REDACTED&apikey=REDACTED"
	const split = "http:\u200b//host.test/api?t=movie&apikey=st0red-key"

	for name, test := range map[string]struct {
		clean      int
		wantHidden string
	}{
		"as they were": {0, "said [" + split + "]"},
		"cleaned":      {2000, "said [" + shown + "]"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := New(Config{CleanAnswers: test.clean, BlankAddresses: []string{"failures", "message", "details", "settings"}, LeaveAsIs: []string{"guid"}})
			Add(r, Read, &mcp.Tool{Name: "thing_probe", Description: "tests a thing"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, probeOut, error) {
				return nil, probeOut{GUID: quoted, Rows: []probeRow{{
					Name:     "said " + quoted,
					Failures: []string{"HTTP request failed: [GET] at [" + quoted + "]", "said [" + split + "]"},
					Message:  new("unavailable at [" + quoted + "]"),
					Details:  map[string]string{"reason": "no answer from " + quoted},
					Link:     quoted,
					Extra:    map[string]any{"home": quoted, "settings": map[string]any{"baseUrl": quoted, "guid": quoted, "port": 9117, "tags": []any{quoted}}},
				}}}, nil
			})
			cs := connect(t, r, Selection{})

			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "thing_probe"})
			if err != nil || res.IsError {
				t.Fatalf("thing_probe: %v %+v", err, res)
			}
			var out probeOut
			if err := json.Unmarshal(mustJSON(t, res.StructuredContent), &out); err != nil {
				t.Fatal(err)
			}
			row := out.Rows[0]
			settings, isMap := row.Extra["settings"].(map[string]any)
			if !isMap {
				t.Fatalf("the settings were answered as %v", row.Extra["settings"])
			}

			for what, got := range map[string][2]any{
				"a failure":                     {row.Failures[0], "HTTP request failed: [GET] at [" + shown + "]"},
				"a failure with a hidden mark":  {row.Failures[1], test.wantHidden},
				"a message behind a pointer":    {*row.Message, "unavailable at [" + shown + "]"},
				"a detail in a map":             {row.Details["reason"], "no answer from " + shown},
				"a setting under a named key":   {settings["baseUrl"], shown},
				"a list under a named key":      {fmt.Sprint(settings["tags"]), "[" + shown + "]"},
				"a number under a named key":    {fmt.Sprint(settings["port"]), "9117"},
				"a handle under a named key":    {settings["guid"], quoted},
				"a handle":                      {out.GUID, quoted},
				"a link":                        {row.Link, quoted},
				"a name":                        {row.Name, "said " + quoted},
				"a value under a key not named": {row.Extra["home"], quoted},
			} {
				if got[0] != got[1] {
					t.Errorf("%s was answered %q, want %q", what, got[0], got[1])
				}
			}
		})
	}
}

// A name on the list that no tool answers a field under blanks nothing, and
// is told of. A name that is only ever a map's key is told of as well.
func TestBlankAddressesUnused(t *testing.T) {
	t.Parallel()

	r := New(Config{BlankAddresses: []string{"failures", "gone", "settings", "message"}})
	Add(r, Read, &mcp.Tool{Name: "thing_probe", Description: "tests a thing"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, probeOut, error) {
		return nil, probeOut{}, nil
	})

	if got, want := r.BlankAddressesUnused(), []string{"gone", "settings"}; !slices.Equal(got, want) {
		t.Errorf("BlankAddressesUnused() = %q, want %q", got, want)
	}
	if got := New(Config{}).BlankAddressesUnused(); len(got) != 0 {
		t.Errorf("BlankAddressesUnused() with none given = %q", got)
	}
}
