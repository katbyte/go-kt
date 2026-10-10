// Package registry is how a katbyte MCP tool decides which of its tools a
// session gets, and what each tells a client about itself.
//
// Tools are named resource-first (library_*, item_*, audit_*) and each is
// added with a kind: a read tool never changes what the server holds, a
// write tool does, and a delete tool removes what cannot be put back. A
// read-only session gets the read tools alone; the delete tools are held
// back until the operator asks for them; toolsets pick the groups a session
// needs, so a client loads a working subset rather than every definition; an
// allow list adds tools by name to whatever the toolsets hold; and a deny
// list takes tools out of what is left.
//
// What is shared is the machinery. The tools, the toolsets they sit in and
// the hints each carries are the application's, given in a Config.
package registry

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/katbyte/go-kt/chttp"
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/internal/addresses"
	"github.com/katbyte/go-kt/outside"
)

// Kind is what a tool does to the server it works on.
type Kind int

const (
	// Read never changes what the server holds.
	Read Kind = iota
	// Write changes it, and is left out of a read-only session.
	Write
	// Delete removes what cannot be put back, and is registered only when
	// the operator asks for the delete tools.
	Delete
)

func (k Kind) String() string {
	return [...]string{"read", "write", "delete"}[k]
}

// Core is the toolset every other assumes - enough to find things and open
// them - and is added to whatever sets a session asks for, when the
// application has one of that name.
const Core = "core"

// All is the toolset name that asks for every tool.
const All = "all"

// Essential is the name an allow list asks for the application's preset by.
const Essential = "essential"

// Hints are what a tool tells a client about itself beyond its kind, in the
// terms of the MCP annotations. The zero value is the answer that warns: a
// write tool with no hints of its own is marked destructive.
type Hints struct {
	// Additive is for a write tool that only ever adds - a new library, a
	// new playlist - and never changes or takes away what was there: a
	// value written over, a file replaced, a history cleared. MCP reads
	// "not destructive" as exactly that.
	Additive bool
	// Idempotent is for a write tool that changes nothing more when called
	// again with the same arguments.
	Idempotent bool
	// SendsOut is for a tool that itself sends something to a person or a
	// service beyond the server it works on: an email, a notification. A
	// tool that makes the server fetch from its own providers, or hand
	// work to its own download client, does not. MCP's name for the hint
	// is "open world".
	SendsOut bool
	// Installs is for a tool that has the server bring something in from
	// outside and run it: an app, a plug-in. What it brings in is someone
	// else's to change, and MCP has the one hint, "open world", for that
	// and for SendsOut: a tool that sets either carries it.
	Installs bool
	// WritesHere is for a read tool that writes a file on the machine it
	// runs on. The server is only read, so it stays a read tool and a
	// read-only session keeps it, but it does not claim to change nothing.
	WritesHere bool
}

// Config is the application's side of a registry.
type Config struct {
	// Toolsets are the curated groups by name, each tool in one of them.
	// The one named Core is added to whatever else is asked for.
	Toolsets map[string][]string
	// Essential is the preset an allow list names as "essential".
	Essential []string
	// Hints are the tools' hints by name.
	Hints map[string]Hints
	// LogError is where a handler's panic and its stack are written; nil is
	// clog.Log.Errorf, which writes to stderr - stdout carries the protocol
	// itself when serving stdio.
	LogError func(format string, args ...any)
	// LogWrite is where one line is written for every call of a write or a
	// delete tool: the tool, what the call sent with its credentials
	// blanked, and what became of it. nil is clog.Log.Infof.
	LogWrite func(format string, args ...any)
	// SecretArguments are the names of arguments, at any depth, whose
	// values LogWrite's line blanks whole, whatever they are, a piece of
	// text, a list or an object, beside what it blanks unasked: an argument
	// named as a credential is (chttp.SecretName); everything under an
	// argument a tool takes as a free-form object, a settings map or a
	// container's environment, whose names are someone else's to choose and
	// say nothing of what they hold; and, in an address anywhere in what is
	// left, who it signs in as and whatever follows its "?" or its "#". It
	// is for a credential in a plain field under a name of its own: a
	// cookie, a file's whole text, a command and its flags, an address whose
	// path is the secret.
	SecretArguments []string
	// ShownArguments are the free-form arguments, by tool, that hold no
	// credential, and whose values LogWrite's line shows as they were sent:
	// the settings a tool changes by the server's own names, from a list it
	// checks them against. A name under one that reads as a credential's,
	// or is among SecretArguments, is blanked all the same.
	ShownArguments map[string][]string
	// CleanAnswers, when above 0, has every piece of text in every tool's
	// answer, and in its refusal, cleaned as text from outside is
	// (outside.Text) and cut to this many characters: here, once, around
	// every tool, and not by each tool where it remembers to. Most of what
	// a tool answers was written by someone else, and one field missed is
	// the way in. It is off until a server has named what to leave as it
	// is.
	CleanAnswers int
	// LeaveAsIs are the values in an answer that a later call hands back,
	// by the JSON names they are answered under: a path, a folder, a
	// release's id. Each has to find the same thing again, so it is
	// answered as the server holds it, and everything under such a name
	// with it. What is on this list is not cleaned, so it is for what is
	// handed back and nothing else (LeaveAsIsUnused).
	LeaveAsIs []string
	// BlankAddresses are the values in an answer that are a server's own
	// words for what went wrong, by the JSON names they are answered under:
	// a failed test's failures, a health check's message, a log's lines. A
	// server quotes an address in such words with a credential of its own
	// on it, a stored key, so each address under such a name, however deep,
	// is answered as one in a failure is (outside.BlankAddresses): without
	// who it signs in as, the value of any parameter or what follows its
	// "#". A tool that puts such words together with its own calls that
	// itself, on the words before it joins them. It is not for every
	// answer: a link a caller is to follow needs what follows its "?", and
	// what is under a name in LeaveAsIs is left as it is here too. A name
	// no tool answers under blanks nothing (BlankAddressesUnused).
	BlankAddresses []string
}

// Selection is what one session asks for.
type Selection struct {
	// ReadOnly keeps the read tools alone.
	ReadOnly bool
	// EnableDelete lets the delete tools in.
	EnableDelete bool
	// Toolsets are the sets asked for, each entry a name or several joined
	// by commas: a curated set, All, or a resource family (every tool whose
	// name begins with it, "library" for library_*). None is every tool.
	Toolsets []string
	// Allow asks for tools by name: exact names, a pattern with one "*" at
	// either end (library_*, *_delete), or Essential. Beside Toolsets it
	// adds to them, "these sets and these tools as well"; on its own it is
	// only the tools it names, with no Core added. It lets nothing past
	// ReadOnly or EnableDelete.
	Allow []string
	// Deny takes out the tools it names, the same way, from whatever the
	// toolsets and the allow list asked for: it is how a toolset is
	// narrowed.
	Deny []string
}

// Info describes a tool a selection would register.
type Info struct {
	Name string
	// Kind is read, write or delete
	Kind string
	// Toolset is the curated set the tool belongs to
	Toolset     string
	Description string
}

type pending struct {
	name        string
	kind        Kind
	description string
	// arguments are the names of the arguments the tool takes, in the
	// order its input declares them
	arguments []string
	// loose are the names of the arguments, at any depth, the tool takes
	// as a free-form object, less the ones Config.ShownArguments says hold
	// no credential, and looseInput is a tool whose whole input is one
	loose      map[string]bool
	looseInput bool
	// answers are the JSON names of the fields the tool answers with,
	// however deep
	answers  map[string]bool
	register func(*mcp.Server)
}

// Registry collects an application's tools before any is registered, so a
// selection can be checked against all of them: a pattern that names no
// tool is refused rather than quietly hiding one.
type Registry struct {
	cfg     Config
	pending []pending
	// leave is Config.LeaveAsIs, and quoted Config.BlankAddresses, to look
	// a name up in
	leave  map[string]bool
	quoted map[string]bool
}

// New makes an empty registry.
func New(cfg Config) *Registry {
	r := &Registry{cfg: cfg, leave: map[string]bool{}, quoted: map[string]bool{}}
	for _, name := range cfg.LeaveAsIs {
		r.leave[name] = true
	}
	for _, name := range cfg.BlankAddresses {
		r.quoted[name] = true
	}

	return r
}

// Add queues a typed tool. It sets the tool's MCP annotations from its kind
// and its hints, so a client can tell a read from a destructive write
// without parsing descriptions; turns a panic in the handler into an
// ordinary tool error; sends every empty list in an answer as [] and every
// empty map as {} rather than null, which a client cannot tell from "not
// fetched" and the SDK refuses a map for; and, when the registry is set to,
// cleans every piece of text in the answer or the refusal
// (Config.CleanAnswers) and takes out of a server's own words in an answer
// what an address may carry a credential in (Config.BlankAddresses). Once
// registered, a failure's words reach the caller with what an address in
// them may carry a credential in taken out (hideAddresses). A tool whose
// input is any takes no arguments, and is given a schema that says so unless
// it brings its own: the SDK's for such a tool takes whatever it is sent,
// and an argument nobody takes is a mistake a caller is owed a refusal for.
func Add[In, Out any](r *Registry, kind Kind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	hints := r.cfg.Hints[t.Name]
	openWorld := hints.SendsOut || hints.Installs
	switch kind {
	case Read:
		t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: !hints.WritesHere, DestructiveHint: new(false), OpenWorldHint: new(openWorld)}
	case Write:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: new(!hints.Additive), IdempotentHint: hints.Idempotent, OpenWorldHint: new(openWorld)}
	case Delete:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: new(openWorld)}
	}

	if reflect.TypeFor[In]() == reflect.TypeFor[any]() && t.InputSchema == nil {
		t.InputSchema = map[string]any{"type": "object", "additionalProperties": false}
	}

	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := recovered(ctx, r, t.Name, h, req, in)
		if err != nil {
			return res, out, r.cleanFailure(err)
		}

		answer := reflect.ValueOf(&out).Elem()
		emptyNils(answer)
		r.cleanAnswer(res, answer)

		return res, out, nil
	}

	loose := looseNames(reflect.TypeFor[In](), map[string]bool{}, map[reflect.Type]bool{})
	for _, name := range r.cfg.ShownArguments[t.Name] {
		delete(loose, name)
	}

	r.pending = append(r.pending, pending{
		name:        t.Name,
		kind:        kind,
		description: t.Description,
		arguments:   argumentNames(reflect.TypeFor[In]()),
		loose:       loose,
		looseInput:  freeForm(reflect.TypeFor[In]()),
		answers:     answerNames(reflect.TypeFor[Out](), map[string]bool{}, map[reflect.Type]bool{}),
		register:    func(server *mcp.Server) { mcp.AddTool(server, t, wrapped) },
	})
}

// freeForm reports whether a type is an object whose names are not the
// tool's own: a map, or a value of no type at all, or a list of either.
func freeForm(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}

	return t.Kind() == reflect.Map || t.Kind() == reflect.Interface
}

// looseNames is the JSON names of the fields of an input, however deep, that
// are free-form objects, added to names. seen keeps a type that holds itself
// from being walked for ever.
func looseNames(t reflect.Type, names map[string]bool, seen map[reflect.Type]bool) map[string]bool {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return names
	}
	seen[t] = true

	for f := range t.Fields() {
		name := jsonName(f)
		switch {
		case name == "-", !f.IsExported() && !f.Anonymous:
		case freeForm(f.Type):
			names[name] = true
		default:
			looseNames(f.Type, names, seen)
		}
	}

	return names
}

// jsonName is the name a struct's field is answered under: its JSON tag's,
// its own when it has no tag, "" for an embedded struct that has none and
// "-" for a field kept out of the JSON.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" && !f.Anonymous {
		return f.Name
	}

	return name
}

// answerNames is the JSON names of the fields a type answers with, however
// deep, added to names. seen keeps a type that holds itself from being
// walked for ever.
func answerNames(t reflect.Type, names map[string]bool, seen map[reflect.Type]bool) map[string]bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		answerNames(t.Elem(), names, seen)
	case reflect.Struct:
		if seen[t] {
			return names
		}
		seen[t] = true
		for f := range t.Fields() {
			name := jsonName(f)
			if name == "-" || !f.IsExported() && !f.Anonymous {
				continue
			}
			if name != "" {
				names[name] = true
			}
			answerNames(f.Type, names, seen)
		}
	default:
	}

	return names
}

// LeaveAsIsUnused is the names in Config.LeaveAsIs that no tool answers a
// field under, in the order they were given. A name left on the list after
// its field was renamed or dropped leaves nothing as it is and says
// nothing, so a server's test holds this to none. Only fields are looked
// for: a value answered under the key of a map is not one, and a name kept
// for such a key is reported here all the same.
func (r *Registry) LeaveAsIsUnused() []string { return r.unused(r.cfg.LeaveAsIs) }

// BlankAddressesUnused is the names in Config.BlankAddresses that no tool
// answers a field under, in the order they were given. A name left on the
// list after its field was renamed blanks nothing and says nothing, and
// what the field held is answered as the server wrote it, so a server's
// test holds this to none. Only fields are looked for, as by
// LeaveAsIsUnused.
func (r *Registry) BlankAddressesUnused() []string { return r.unused(r.cfg.BlankAddresses) }

// unused is the names among these that no tool answers a field under.
func (r *Registry) unused(names []string) []string {
	var unused []string
	for _, name := range names {
		if !slices.ContainsFunc(r.pending, func(p pending) bool { return p.answers[name] }) {
			unused = append(unused, name)
		}
	}

	return unused
}

// cleanAnswer does to a tool's answer, in place, what the registry is set
// to: cleans every piece of text in it, the words of a result the tool
// wrote itself and everything the answer holds (Config.CleanAnswers), and
// blanks what an address carries in a server's own words
// (Config.BlankAddresses).
func (r *Registry) cleanAnswer(res *mcp.CallToolResult, answer reflect.Value) {
	if r.cfg.CleanAnswers <= 0 && len(r.quoted) == 0 {
		return
	}

	if res != nil {
		for _, c := range res.Content {
			if text, ok := c.(*mcp.TextContent); ok {
				text.Text = r.cleaned(text.Text)
			}
		}
	}
	r.clean(answer, r.cleaned)
}

// cleaned is a piece of an answer's text cleaned, when the registry is set
// to, and as it was when it is not.
func (r *Registry) cleaned(text string) string {
	if r.cfg.CleanAnswers <= 0 {
		return text
	}

	return outside.Text(text, r.cfg.CleanAnswers)
}

// unquoted is a piece of a server's own words cleaned, and then without
// what an address in it may carry a credential in: cleaned first, so that a
// character that does not show cannot keep an address from being read as
// one.
func (r *Registry) unquoted(text string) string { return outside.BlankAddresses(r.cleaned(text)) }

// clean does text to each piece of text a value holds, however deep,
// leaving what is answered under a name in Config.LeaveAsIs, and all that
// is under it, as it is. Under a name in Config.BlankAddresses the text is
// done unquoted to. Raw JSON a tool passes on unread is bytes, not text, and
// is not looked into.
func (r *Registry) clean(v reflect.Value, text func(string) string) {
	switch v.Kind() {
	case reflect.String:
		if s := text(v.String()); v.CanSet() && s != v.String() {
			v.SetString(s)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			r.clean(v.Elem(), text)
		}
	case reflect.Interface:
		// what an interface holds cannot be changed where it is, so a copy
		// is cleaned and put in its place
		if v.IsNil() || !v.CanSet() {
			return
		}
		held := reflect.New(v.Elem().Type()).Elem()
		held.Set(v.Elem())
		r.clean(held, text)
		v.Set(held)
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if name := jsonName(f); name != "-" && (f.IsExported() || f.Anonymous) && !r.leave[name] {
				r.clean(v.Field(i), r.under(name, text))
			}
		}
	case reflect.Slice, reflect.Array:
		if holdsText(v.Type().Elem()) {
			for i := range v.Len() {
				r.clean(v.Index(i), text)
			}
		}
	case reflect.Map:
		r.cleanMap(v, text)
	default:
		// a number, a flag: nothing to clean
	}
}

// under is what is done to the text under a name: unquoted when the name
// is in Config.BlankAddresses, and otherwise what was being done above it.
func (r *Registry) under(name string, text func(string) string) func(string) string {
	if r.quoted[name] {
		return r.unquoted
	}

	return text
}

// holdsText reports whether a list of these could hold text: a list of
// bytes or of numbers is not walked an element at a time to find none.
func holdsText(elem reflect.Type) bool {
	switch elem.Kind() {
	case reflect.String, reflect.Pointer, reflect.Interface, reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
		return true
	default:
		return false
	}
}

// cleanMap cleans what a map holds, and its keys when they are text: a
// count kept by title has the title for a key. What a map holds cannot be
// changed where it is, so each value is cleaned as a copy and put back; a
// key in Config.LeaveAsIs or Config.BlankAddresses is a name like a
// field's, and what is under it is left, or done unquoted to. The keys are
// taken in order so that two that clean to the same one leave the same one
// of them each time.
func (r *Registry) cleanMap(v reflect.Value, text func(string) string) {
	if v.IsNil() {
		return
	}

	named := v.Type().Key().Kind() == reflect.String
	keys := v.MapKeys()
	if named {
		slices.SortFunc(keys, func(a, b reflect.Value) int { return cmp.Compare(a.String(), b.String()) })
	}
	for _, k := range keys {
		if named && r.leave[k.String()] {
			continue
		}

		below := text
		if named {
			below = r.under(k.String(), text)
		}
		held := reflect.New(v.Type().Elem()).Elem()
		held.Set(v.MapIndex(k))
		r.clean(held, below)

		if named {
			if cleaned := reflect.ValueOf(r.cleaned(k.String())).Convert(k.Type()); cleaned.String() != k.String() && !v.MapIndex(cleaned).IsValid() {
				v.SetMapIndex(k, reflect.Value{})
				k = cleaned
			}
		}
		v.SetMapIndex(k, held)
	}
}

// cleanedError is a tool's refusal with its words cleaned: an error quotes
// what it was given and what the server said, and its text is an answer
// too.
type cleanedError struct {
	err   error
	limit int
}

func (e cleanedError) Error() string { return outside.Text(e.err.Error(), e.limit) }
func (e cleanedError) Unwrap() error { return e.err }

// cleanFailure is a tool's error with its words cleaned, when the registry
// is set to, and the error itself when there is nothing in them to clean.
func (r *Registry) cleanFailure(err error) error {
	limit := r.cfg.CleanAnswers
	if limit <= 0 || outside.Text(err.Error(), limit) == err.Error() {
		return err
	}

	return cleanedError{err: err, limit: limit}
}

// argumentNames is the arguments a tool takes: the JSON names of its input's
// fields, in the order they are declared, those of an embedded struct among
// them. An input that is no struct names none.
func argumentNames(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	var names []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-", !f.IsExported() && !f.Anonymous:
		case f.Anonymous && name == "":
			names = append(names, argumentNames(f.Type)...)
		case name == "":
			names = append(names, f.Name)
		default:
			names = append(names, name)
		}
	}

	return names
}

// recovered calls a handler, turning a panic into an ordinary tool error.
// Nothing above the handler recovers one - not the MCP SDK, not a CLI - so
// one nil dereference in one tool would otherwise end the whole session. The
// caller is told which tool failed, and the stack goes to the log.
func recovered[In, Out any](ctx context.Context, r *Registry, name string, h mcp.ToolHandlerFor[In, Out], req *mcp.CallToolRequest, in In) (res *mcp.CallToolResult, out Out, err error) {
	defer func() {
		if p := recover(); p != nil {
			r.logError("internal error in %s: %v\n%s", name, p, debug.Stack())
			var zero Out
			res, out, err = nil, zero, fmt.Errorf("internal error in %s: %v", name, p)
		}
	}()

	return h(ctx, req, in)
}

func (r *Registry) logError(format string, args ...any) {
	if r.cfg.LogError != nil {
		r.cfg.LogError(format, args...)

		return
	}
	clog.Log.Errorf(format, args...)
}

// emptyNils walks v (structs, pointers, slices) and replaces every settable
// nil slice with an empty one, and every nil map: a nil slice is sent as
// null, which a client cannot tell from "not fetched", and a nil map is
// sent as null where the tool's own schema says an object, which the SDK
// refuses the whole call for. What a map holds is not walked.
func emptyNils(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			emptyNils(v.Elem())
		}
	case reflect.Struct:
		for _, f := range v.Fields() {
			emptyNils(f)
		}
	case reflect.Slice:
		if v.IsNil() {
			if v.CanSet() {
				v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			}

			return
		}
		for i := range v.Len() {
			emptyNils(v.Index(i))
		}
	case reflect.Map:
		if v.IsNil() && v.CanSet() {
			v.Set(reflect.MakeMap(v.Type()))
		}
	default:
	}
}

// Register adds every tool the selection permits to the server and answers
// with the names registered, sorted. It fails when the selection names a
// toolset or a pattern that reaches no tool, so a typo cannot hide one.
func (r *Registry) Register(server *mcp.Server, sel Selection) ([]string, error) {
	keep, err := r.selected(sel)
	if err != nil {
		return nil, err
	}

	var registered []string
	tools := map[string]pending{}
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		p.register(server)
		registered = append(registered, p.name)
		tools[p.name] = p
	}
	slices.Sort(registered)
	server.AddReceivingMiddleware(r.calls(tools))

	return registered, nil
}

// The ways a call of a write or a delete tool ends, as LogWrite names them.
// Whether a call that was answered changed anything or showed what it would
// is the tool's to say, with an argument of its own such as confirm or
// dry_run, which the line carries.
const (
	// outcomeRefused is a call whose arguments were turned away before the
	// tool ran.
	outcomeRefused = "refused"
	// outcomeFailed is a call the tool ran and answered with an error.
	outcomeFailed = "failed"
	// outcomeAnswered is a call the tool ran and answered.
	outcomeAnswered = "answered"
)

// The most of a call's arguments, and of an error, that a line of LogWrite
// carries.
const (
	loggedArguments = 2000
	loggedError     = 300
)

// calls stands between a client and every call of a tool, which is the one
// place that sees a call whose arguments never reached the tool, and every
// failure whoever worded it. It tells a caller who sent an argument the tool
// does not take which ones it does, it takes out of a failure's words what
// an address in them may carry a credential in (hideAddresses), and it
// writes a line for every call of a write or a delete tool.
func (r *Registry) calls(tools map[string]pending) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || call.Params == nil {
				return next(ctx, method, req)
			}
			p, known := tools[call.Params.Name]
			if !known {
				return next(ctx, method, req)
			}

			result, err := next(ctx, method, req)
			outcome, failure := outcomeAnswered, err
			if err != nil {
				outcome = outcomeFailed
			}
			if res, answered := result.(*mcp.CallToolResult); answered && err == nil && res != nil && res.IsError {
				outcome, failure = outcomeFailed, res.GetError()
				if failure != nil && strings.HasPrefix(failure.Error(), `validating "arguments":`) {
					outcome = outcomeRefused
					nameArguments(res, p)
				}
				hideAddresses(res)
			}
			if p.kind != Read {
				r.logWrite(p, call.Params.Arguments, outcome, failure)
			}

			return result, err
		}
	}
}

// hideAddresses rewrites the words of a tool's failure, which its caller
// reads, with each address in them as LogWrite's line shows one
// (outside.BlankAddresses): without who it signs in as, the value of any
// parameter, or what follows its "#". A server is apt to quote an address
// back with a credential of its own on the end of it, an indexer's stored
// key, and that was never the caller's to be handed. An answer's addresses
// are left as they are, but for the ones under a name the server gives
// (Config.BlankAddresses): a caller needs the rest, and a stored secret in
// an answer is the tool's own to hide.
func hideAddresses(res *mcp.CallToolResult) {
	for i, c := range res.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}

		if shown := outside.BlankAddresses(text.Text); shown != text.Text {
			rewritten := *text
			rewritten.Text = shown
			res.Content[i] = &rewritten
		}
	}
}

// blanked stands for a value that is not shown, in LogWrite's line and in a
// failure's words, as it does in a trace of a request.
const blanked = addresses.Blanked

// notShown stands in LogWrite's line for arguments that could not be read to
// blank what is in them, and so are not written at all.
const notShown = "(arguments that do not read as JSON, not shown)"

// repeated is the least a blanked piece of text is long for a failure that
// repeats it to have it blanked there too: shorter, and it is a word or a
// number the failure may well have had anyway, a port or a "true".
const repeated = 6

// hider blanks what LogWrite's line may not show of one call, and keeps
// what it blanked.
type hider struct {
	// loose are the names everything under which is blanked, and secret
	// the ones a server names beside those that read as a credential's
	loose  map[string]bool
	secret []string
	// all is a tool whose whole input is free-form, with no names of its
	// own to go by
	all bool
	// hidden are the pieces of text it blanked, to blank again where the
	// call's failure repeats them
	hidden []string
}

// arguments is what a call sent as LogWrite's line may show it. Blanked are
// each value that is a credential by its name (chttp.SecretName, and
// Config.SecretArguments), each that is under a free-form object, whose
// names are not the tool's and cannot be read for what they hold, and what
// an address carries a credential in (outside.BlankAddresses). The names
// stay, so the line still says what was sent.
func (h *hider) arguments(sent json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(sent))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return notShown
	}

	if h.all {
		v = h.blank(v)
	}

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(h.hide(v)); err != nil {
		return notShown
	}

	return strings.TrimSuffix(out.String(), "\n")
}

// hide is a value with what LogWrite's line may not show blanked: what is
// under a name that hides it (blank), and what an address in its text
// carries.
func (h *hider) hide(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for name, held := range v {
			if h.loose[name] || chttp.SecretName(name, h.secret...) {
				v[name] = h.blank(held)

				continue
			}
			v[name] = h.hide(held)
		}

		return v
	case []any:
		for i, held := range v {
			v[i] = h.hide(held)
		}

		return v
	case string:
		return addresses.Blank(v, h.keep)
	default:
		return v
	}
}

// blank is a value with everything in it blanked and its names kept, so the
// line says what was sent and not what it held. Nothing stays nothing.
func (h *hider) blank(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for name, held := range v {
			v[name] = h.blank(held)
		}

		return v
	case []any:
		for i, held := range v {
			v[i] = h.blank(held)
		}

		return v
	case string:
		h.keep(v)

		return blanked
	case nil:
		return nil
	default:
		return blanked
	}
}

// keep takes a piece of text that was blanked, to blank again where the
// call's failure repeats it.
func (h *hider) keep(piece string) { h.hidden = append(h.hidden, piece) }

// failure is what a call failed with as LogWrite's line may show it: each
// address in it blanked as one in the arguments is, and each piece of text
// that was blanked in the arguments blanked here too, since a failure is apt
// to repeat what it was sent: the address it could not reach, the value it
// would not take.
func (h *hider) failure(text string) string {
	text = addresses.Blank(text, h.keep)

	slices.SortFunc(h.hidden, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, secret := range h.hidden {
		if len(secret) >= repeated {
			text = blankWhole(text, secret)
		}
	}

	return text
}

// blankWhole is text with secret blanked wherever it stands by itself, with
// no letter or digit against an end of it that is one: "require" where a
// failure repeats "sslmode=require", and not in "required".
func blankWhole(text, secret string) string {
	first, _ := utf8.DecodeRuneInString(secret)
	last, _ := utf8.DecodeLastRuneInString(secret)

	var out strings.Builder
	for {
		at := strings.Index(text, secret)
		if at < 0 {
			break
		}
		end := at + len(secret)

		before, _ := utf8.DecodeLastRuneInString(text[:at])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if (wordy(first) && wordy(before)) || (wordy(last) && wordy(after)) {
			out.WriteString(text[:end])
		} else {
			out.WriteString(text[:at] + blanked)
		}
		text = text[end:]
	}
	out.WriteString(text)

	return out.String()
}

// wordy reports whether a character is part of a word: a letter or a digit.
func wordy(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// nameArguments adds the arguments a tool takes to its refusal of one it
// does not: the SDK says which argument was not expected, and a caller that
// mistyped offset is not helped by that alone.
func nameArguments(res *mcp.CallToolResult, p pending) {
	refusal := res.GetError().Error()
	if !strings.Contains(refusal, "unexpected additional properties") {
		return
	}

	takes := "no arguments"
	switch n := len(p.arguments); {
	case n == 1:
		takes = p.arguments[0]
	case n > 1:
		takes = strings.Join(p.arguments[:n-1], ", ") + " and " + p.arguments[n-1]
	}
	res.Content = []mcp.Content{&mcp.TextContent{Text: refusal + ": " + p.name + " takes " + takes}}
}

// logWrite writes the line for one call of a write or a delete tool. What
// the call sent, and what it failed with, are written with their
// credentials blanked (hider), and with nothing in them that does not show,
// so a line is a line whatever was sent. The blanking comes before the
// cutting to length, which could otherwise cut a name from its value.
func (r *Registry) logWrite(p pending, arguments json.RawMessage, outcome string, failure error) {
	log := r.cfg.LogWrite
	if log == nil {
		log = clog.Log.Infof
	}

	h := hider{loose: p.loose, secret: r.cfg.SecretArguments, all: p.looseInput}
	sent := "{}"
	if len(arguments) > 0 {
		sent = outside.Text(h.arguments(arguments), loggedArguments)
	}
	if failure != nil {
		log("%s %s %s %s: %s", p.kind, p.name, outcome, sent, outside.Text(h.failure(failure.Error()), loggedError))

		return
	}
	log("%s %s %s %s", p.kind, p.name, outcome, sent)
}

// Describe lists the tools a selection would register, by name, with
// nothing registered: the same choice Register makes, so what a tool says it
// would serve cannot drift from what it serves.
func (r *Registry) Describe(sel Selection) ([]Info, error) {
	keep, err := r.selected(sel)
	if err != nil {
		return nil, err
	}

	set := map[string]string{}
	for name, members := range r.cfg.Toolsets {
		for _, m := range members {
			// core wins: it is the set a tool is reached through most often
			if set[m] == "" || name == Core {
				set[m] = name
			}
		}
	}

	out := make([]Info, 0, len(r.pending))
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		out = append(out, Info{Name: p.name, Kind: p.kind.String(), Toolset: set[p.name], Description: p.description})
	}
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(a.Name, b.Name) })

	return out, nil
}

// Names is every tool added, whatever a selection would make of it, in the
// order added.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.pending))
	for _, p := range r.pending {
		out = append(out, p.name)
	}

	return out
}

// ToolsetNames lists the curated toolsets, sorted, for help output.
func (r *Registry) ToolsetNames() []string {
	out := make([]string, 0, len(r.cfg.Toolsets))
	for k := range r.cfg.Toolsets {
		out = append(out, k)
	}
	slices.Sort(out)

	return out
}

// FamilyNames lists the resource families a toolset entry may name - the
// part of each tool's name before its first underscore - sorted. They are
// read off the tools added, so they cannot go stale.
func (r *Registry) FamilyNames() []string { return families(r.Names()) }

// selected applies the kind gates, then what the toolsets and the allow list
// ask for between them, then the deny list. Toolsets and an allow list both
// ask for tools, and a session gets what either names; with neither it gets
// every tool.
func (r *Registry) selected(sel Selection) (map[string]bool, error) {
	names := r.Names()
	sets, err := r.compileToolsets(sel.Toolsets, names)
	if err != nil {
		return nil, err
	}
	allow, err := r.compilePatterns(sel.Allow, names, "allow")
	if err != nil {
		return nil, err
	}
	deny, err := r.compilePatterns(sel.Deny, names, "deny")
	if err != nil {
		return nil, err
	}
	// what was asked for is read off the lists as they were given: an allow
	// list of a preset the application left empty asks for nothing, which
	// is not the same as no allow list
	everything := len(entries(sel.Toolsets)) == 0 && len(entries(sel.Allow)) == 0

	keep := make(map[string]bool, len(r.pending))
	for _, p := range r.pending {
		switch {
		case p.kind == Delete && !sel.EnableDelete:
		case p.kind != Read && sel.ReadOnly:
		case !everything && !sets[p.name] && !matchesAny(allow, p.name):
		case matchesAny(deny, p.name):
		default:
			keep[p.name] = true
		}
	}

	return keep, nil
}

// entries splits a list whose entries may each hold several names joined
// by commas, as a flag given twice and an environment variable both arrive.
func entries(raw []string) []string {
	var out []string
	for _, entry := range raw {
		for name := range strings.SplitSeq(entry, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
	}

	return out
}

// compileToolsets turns the set names asked for into the tools they hold,
// always with the core set. An unknown name is an error naming the valid
// ones, the way a pattern that matches nothing is.
func (r *Registry) compileToolsets(raw, known []string) (map[string]bool, error) {
	asked := entries(raw)
	if len(asked) == 0 {
		return nil, nil
	}

	out := map[string]bool{}
	for _, name := range asked {
		if name == All {
			for _, t := range known {
				out[t] = true
			}

			continue
		}
		if tools, ok := r.cfg.Toolsets[name]; ok {
			for _, t := range tools {
				out[t] = true
			}

			continue
		}
		// not a named set: a resource family, every tool with that prefix
		found := false
		for _, t := range known {
			if strings.HasPrefix(t, name+"_") {
				out[t], found = true, true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown toolset %q (sets: %s, %s; or a resource family: %s)",
				name, All, strings.Join(r.ToolsetNames(), ", "), strings.Join(families(known), ", "))
		}
	}
	// core is what every other set assumes: without it there is no way to
	// find anything or open it
	for _, t := range r.cfg.Toolsets[Core] {
		out[t] = true
	}

	return out, nil
}

// families lists the resource prefixes in use, sorted.
func families(known []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range known {
		if i := strings.Index(t, "_"); i > 0 && !seen[t[:i]] {
			seen[t[:i]] = true
			out = append(out, t[:i])
		}
	}
	slices.Sort(out)

	return out
}

// compilePatterns expands the essential preset and checks that every other
// pattern matches at least one tool.
func (r *Registry) compilePatterns(raw, known []string, which string) ([]string, error) {
	var out []string
	for _, pat := range entries(raw) {
		if pat == Essential {
			out = append(out, r.cfg.Essential...)

			continue
		}
		if !slices.ContainsFunc(known, func(n string) bool { return matchPattern(pat, n) }) {
			return nil, fmt.Errorf("%s-tools pattern %q matches no tool (have: %s)", which, pat, strings.Join(known, ", "))
		}
		out = append(out, pat)
	}

	return out, nil
}

func matchesAny(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return matchPattern(p, name) })
}

// matchPattern takes an exact name, or one with a single "*" at its start
// or its end.
func matchPattern(pattern, name string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(name, strings.TrimPrefix(pattern, "*"))
	default:
		return pattern == name
	}
}
