package chttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recorder is a logger that keeps what it is told, and formats a trace only
// while tracing is on, as logrus does.
type recorder struct {
	mu      sync.Mutex
	tracing bool
	lines   []string
}

func (r *recorder) Debugf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, "DEBUG "+fmt.Sprintf(format, args...))
}

func (r *recorder) Tracef(format string, args ...any) {
	if !r.tracing {
		return
	}
	text := fmt.Sprintf(format, args...)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, "TRACE "+text)
}

func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return strings.Join(r.lines, "\n")
}

// countedBody is a body that says how much of it was read, and ends with the
// error it was given where a real one would end with io.EOF. It says how it
// ended once, as a connection does: asked again, it has nothing more to say.
type countedBody struct {
	data   []byte
	end    error
	read   int
	closed bool
}

func (c *countedBody) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		if end := c.end; end != nil {
			c.end = nil

			return 0, end
		}

		return 0, io.EOF
	}
	n := copy(p, c.data)
	c.data = c.data[n:]
	c.read += n

	return n, nil
}

func (c *countedBody) Close() error {
	c.closed = true

	return nil
}

// answering is a transport that answers every request with one response.
type answering struct {
	contentType string
	body        io.ReadCloser
	length      int64
}

func (a answering) RoundTrip(req *http.Request) (*http.Response, error) {
	h := http.Header{}
	if a.contentType != "" {
		h.Set("Content-Type", a.contentType)
	}

	return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: h, Body: a.body, ContentLength: a.length, Request: req}, nil
}

// traced sends one request through a tracing transport and hands back the
// response, unread, and what was logged. The body is closed when the test
// ends.
func traced(t *testing.T, o Options, next http.RoundTripper, req *http.Request) (resp *http.Response, logged string) {
	t.Helper()

	log := &recorder{tracing: true}
	o.Name, o.Log = "Widget", log
	resp, err := NewTransport(o, next).RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp, log.text()
}

// traceOf is what was logged of one request sent through a tracing
// transport, for a test with no use for the response.
func traceOf(t *testing.T, o Options, next http.RoundTripper, req *http.Request) string {
	t.Helper()

	_, logged := traced(t, o, next, req) //nolint:bodyclose // closed by traced when the test ends

	return logged
}

func get(t *testing.T, url string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	return req
}

// With tracing off nothing is put together and no body is touched; with it
// on the request and the answer are both there, JSON pretty-printed.
func TestTransportTraces(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	log := &recorder{}
	client := &http.Client{Transport: NewTransport(Options{Name: "Widget", Log: log}, http.DefaultTransport)}
	do := func() string {
		t.Helper()

		resp, err := client.Do(get(t, srv.URL+"/things?page=2"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}

		return string(body)
	}

	if body := do(); body != `{"hello":"world"}` || log.text() != "" {
		t.Fatalf("with tracing off the caller read %q and the log holds %q, want the body and nothing logged", body, log.text())
	}

	log.tracing = true
	if body := do(); body != `{"hello":"world"}` {
		t.Errorf("with tracing on the caller read %q, want every byte of the answer", body)
	}
	out := log.text()
	for _, want := range []string{"TRACE Widget API Request Details", "GET /things?page=2 HTTP/1.1\nHost: " + strings.TrimPrefix(srv.URL, "http://") + "\n", "TRACE Widget API Response Details", "HTTP/1.1 200 OK\n", "Content-Type: application/json\n", "\n{\n \"hello\": \"world\"\n}\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the trace is missing %q:\n%s", want, out)
		}
	}
}

// A request that gets no answer is still traced, and its error comes back
// as it was.
func TestTransportTracesARequestThatFails(t *testing.T) {
	t.Parallel()

	log := &recorder{tracing: true}
	failure := errors.New("dial failed")
	resp, err := NewTransport(Options{Name: "Widget", Log: log}, failing{failure}).RoundTrip(get(t, "https://example.test/")) //nolint:bodyclose // nil on the error path
	if !errors.Is(err, failure) || resp != nil {
		t.Fatalf("RoundTrip = (%v, %v), want (nil, the dial's error)", resp, err)
	}
	if out := log.text(); !strings.Contains(out, "Widget API Request Details") || strings.Contains(out, "Response") {
		t.Errorf("the trace of a request that failed:\n%s", out)
	}
}

// With no logger nothing is traced and nothing is read.
func TestTransportWithNoLogger(t *testing.T) {
	t.Parallel()

	body := &countedBody{data: []byte(`{"a":1}`)}
	resp, err := NewTransport(Options{}, answering{contentType: "application/json", body: body}).RoundTrip(get(t, "https://example.test/"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if body.read != 0 || resp.Body != io.ReadCloser(body) {
		t.Errorf("with no logger %d bytes of the answer were read and its body was replaced: %v", body.read, resp.Body != io.ReadCloser(body))
	}
}

// What is secret is not in a trace, wherever it travels: the headers every
// API uses and the application's own, a query parameter, a form field and a
// JSON field, in the request and in the answer.
func TestTraceBlanksWhatIsSecret(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Add("Set-Cookie", "session=s3cret-cookie")
		_, _ = w.Write([]byte(`{"user":{"name":"kt","token":"s3cret-token","accessToken":"s3cret-access","pin":"s3cret-pin","note":"a \"quoted\" word"},"refresh_token":"s3cret-refresh"}`))
	}))
	defer srv.Close()

	o := Options{SecretHeaders: []string{"X-Widget-Token"}, SecretNames: []string{"PIN", "pw"}}
	for name, send := range map[string]struct {
		contentType, body string
	}{
		"a JSON login": {"application/json", `{"username":"kt","password":"s3cret-password","pw" : "s3cret-pw"}`},
		"a form login": {"application/x-www-form-urlencoded", "user=kt&password=s3cret-password&pw=s3cret-pw&keep=1"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/login?api_key=s3cret-key&pin=s3cret-pin&lang=en", strings.NewReader(send.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", send.contentType)
		req.Header.Set("Authorization", "Bearer s3cret-bearer")
		req.Header.Set("Cookie", "session=s3cret-cookie")
		req.Header.Set("X-Widget-Token", "s3cret-widget")
		req.Header.Set("X-Request-ID", "42")

		resp, out := traced(t, o, http.DefaultTransport, req) //nolint:bodyclose // closed by traced when the test ends
		if strings.Contains(out, "s3cret") {
			t.Errorf("%s: the trace shows a secret:\n%s", name, out)
		}
		for _, want := range []string{"/login?api_key=REDACTED&pin=REDACTED&lang=en", "Authorization: REDACTED\n", "Cookie: REDACTED\n", "X-Widget-Token: REDACTED\n", "X-Request-Id: 42\n", "Set-Cookie: REDACTED\n", `"name": "kt"`, `"token": "REDACTED"`, `"accessToken": "REDACTED"`, `"pin": "REDACTED"`, `"refresh_token": "REDACTED"`, `"note": "a \"quoted\" word"`} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the trace is missing %q:\n%s", name, want, out)
			}
		}
		// what the server was sent, and what the caller reads, are as they were
		if got, err := io.ReadAll(resp.Body); err != nil || !bytes.Contains(got, []byte(`"token":"s3cret-token"`)) {
			t.Errorf("%s: the caller read %q (%v), want the answer as the server sent it", name, got, err)
		}
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader(`{"username":"kt","password":"s3cret-password","pw" : "s3cret-pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if out := traceOf(t, o, http.DefaultTransport, req); !strings.Contains(out, "{\n \"username\": \"kt\",\n \"password\": \"REDACTED\",\n \"pw\": \"REDACTED\"\n}") {
		t.Errorf("a JSON login is not traced with its secrets blanked and the rest in place:\n%s", out)
	}
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader("user=kt&password=s3cret-password&pw=s3cret-pw&keep=1"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if out := traceOf(t, o, http.DefaultTransport, req); !strings.Contains(out, "\nuser=kt&password=REDACTED&pw=REDACTED&keep=1\n") {
		t.Errorf("a form login is not traced with its secrets blanked and the rest in place:\n%s", out)
	}
}

// Only the start of a long body is read to show it, the caller still reads
// every byte, and a secret in a body cut off part way is still blanked.
func TestTraceShowsTheStartOfALongBody(t *testing.T) {
	t.Parallel()

	long := `{"token":"s3cret-token","names":["` + strings.Repeat("é", 4000) + `"],"password":"s3cret-` + strings.Repeat("x", 4000)
	body := &countedBody{data: []byte(long)}
	resp, out := traced(t, Options{TraceBody: 101}, answering{contentType: "application/json", body: body}, get(t, "https://example.test/")) //nolint:bodyclose // closed by traced when the test ends

	if body.read != 102 {
		t.Errorf("%d bytes were read to show the first 101, want one more than is shown and no more", body.read)
	}
	// the cut fell inside a two-byte letter, which is left out whole
	if !strings.Contains(out, "\n"+`{"token":"REDACTED","names":["`+strings.Repeat("é", 33)+"\n... (the first 100 bytes; the rest is not shown)") || strings.Contains(out, "s3cret") {
		t.Errorf("the trace of a long body:\n%s", out)
	}
	if got, err := io.ReadAll(resp.Body); err != nil || string(got) != long {
		t.Errorf("the caller read %d bytes (%v), want all %d as the server sent them", len(got), err, len(long))
	}

	// a secret that the cut runs through is blanked to the end of what is shown
	cut := `{"password":"s3cret-` + strings.Repeat("x", 200)
	if out := traceOf(t, Options{TraceBody: 40}, answering{contentType: "application/json", body: &countedBody{data: []byte(cut)}}, get(t, "https://example.test/")); strings.Contains(out, "s3cret") || !strings.Contains(out, `{"password":"REDACTED"`+"\n... (the first 40 bytes") {
		t.Errorf("the trace of a body cut inside a secret:\n%s", out)
	}
}

// A body that is not text - a file being downloaded - is not read at all to
// trace it, and neither is a stream of events, which would hold its reader
// up; no body is read when the trace was told to show none.
func TestTraceLeavesAStreamAlone(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		o           Options
		contentType string
		want        string
	}{
		"an audio file":            {Options{}, "audio/mpeg", "(a body of audio/mpeg, 734003200 bytes, not shown)"},
		"an unnamed download":      {Options{}, "application/octet-stream", "(a body of application/octet-stream, 734003200 bytes, not shown)"},
		"a stream of events":       {Options{}, "text/event-stream", "(a body of text/event-stream, 734003200 bytes, not shown)"},
		"any body, told to show 0": {Options{TraceBody: -1}, "application/json", "(a body, not shown)"},
	} {
		body := &countedBody{data: []byte(`{"a":1}`)}
		resp, out := traced(t, c.o, answering{contentType: c.contentType, body: body, length: 734003200}, get(t, "https://example.test/")) //nolint:bodyclose // closed by traced when the test ends
		if body.read != 0 || resp.Body != io.ReadCloser(body) {
			t.Errorf("%s: %d bytes were read to trace it, want none", name, body.read)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: the trace does not say %q:\n%s", name, c.want, out)
		}
	}

	// text that turns out not to be is read no further than the rest, and not shown
	body := &countedBody{data: append([]byte{0xff, 0xfe, 0x00}, bytes.Repeat([]byte{0x01}, 500)...)}
	if out := traceOf(t, Options{TraceBody: 64}, answering{body: body}, get(t, "https://example.test/")); body.read != 65 || !strings.Contains(out, "(a body that is not text, not shown)") {
		t.Errorf("a body with no type that is not text: %d bytes read, traced as:\n%s", body.read, out)
	}
}

// A request body that can only be read once is not read to trace it: that
// would take it from the server it is being sent to.
func TestTraceLeavesAStreamedRequestAlone(t *testing.T) {
	t.Parallel()

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	defer srv.Close()

	upload := &countedBody{data: []byte("the bytes of a file")}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, upload)
	if err != nil {
		t.Fatal(err)
	}
	if out := traceOf(t, Options{}, http.DefaultTransport, req); !strings.Contains(out, "(a body sent as a stream, not shown)") || got != "the bytes of a file" {
		t.Errorf("the server was sent %q, and the trace reads:\n%s", got, out)
	}

	// one that can be read again is shown, and still sent whole
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader("plain words"))
	if err != nil {
		t.Fatal(err)
	}
	if out := traceOf(t, Options{}, http.DefaultTransport, req); !strings.Contains(out, "\nplain words\n") || got != "plain words" {
		t.Errorf("the server was sent %q, and the trace reads:\n%s", got, out)
	}
}

// An answer that stops part way still reads as one after it was traced: the
// bytes that came, then the error, where a clean end would hide that the
// answer is incomplete.
func TestTraceKeepsHowABodyEnds(t *testing.T) {
	t.Parallel()

	for name, end := range map[string]error{"cut short": io.ErrUnexpectedEOF, "whole": nil} {
		body := &countedBody{data: []byte(`{"a":`), end: end}
		resp, _ := traced(t, Options{}, answering{contentType: "application/json", body: body}, get(t, "https://example.test/"))
		got, err := io.ReadAll(resp.Body)
		if string(got) != `{"a":` || !errors.Is(err, end) || (end == nil && err != nil) {
			t.Errorf("%s: after the trace the caller read %q and %v, want what came and %v", name, got, err, end)
		}
		if err := resp.Body.Close(); err != nil || !body.closed {
			t.Errorf("%s: closing the traced body did not close the answer's own (%v)", name, err)
		}
	}
}

// handedOver is the body of a connection switched to another protocol,
// which is written to as well as read.
type handedOver struct{ countedBody }

func (*handedOver) Write(p []byte) (int, error) { return len(p), nil }

// A connection handed over to the caller is not an answer to show.
func TestTraceLeavesAHandedOverConnectionAlone(t *testing.T) {
	t.Parallel()

	body := &handedOver{countedBody{data: []byte("frames")}}
	resp, _ := traced(t, Options{}, answering{body: body}, get(t, "https://example.test/")) //nolint:bodyclose // closed by traced when the test ends
	if _, writes := resp.Body.(io.Writer); body.read != 0 || !writes {
		t.Errorf("%d bytes of a handed-over connection were read, and it can still be written to: %v", body.read, writes)
	}
}

// Which bodies are worth showing as text.
func TestTextual(t *testing.T) {
	t.Parallel()

	for contentType, want := range map[string]bool{
		"application/json":                  true,
		"application/json; charset=utf-8":   true,
		"application/problem+json":          true,
		"application/xml":                   true,
		"application/rss+xml":               true,
		"text/plain":                        true,
		"text/html; charset=utf-8":          true,
		"application/x-www-form-urlencoded": true,
		"":                                  true,
		"TEXT/Plain":                        true,
		"text/event-stream":                 false,
		"audio/mpeg":                        false,
		"image/jpeg":                        false,
		"application/octet-stream":          false,
		"application/zip":                   false,
		"multipart/form-data; boundary=x":   false,
	} {
		if got := textual(contentType); got != want {
			t.Errorf("textual(%q) = %v, want %v", contentType, got, want)
		}
	}
}

// A credential is known by how its name ends, whatever an API puts in front
// of it, so one nobody listed is still not in a trace; a name that only
// looks like one is left alone.
func TestTraceBlanksANameThatEndsAsACredentialDoes(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"proxyPassword":"s3cret-1","tsigKeys":[{"keyName":"k","sharedSecret":"s3cret-2"}],"partialToken":"s3cret-3","REFRESH_TOKEN":"s3cret-4","apiKey":"s3cret-5","TmdbApiKey":"s3cret-11","tokens":"shown-1","passwordHint":"shown-2","secretary":"shown-3","token_count":"shown-4","sortKey":"shown-5","password":null}`))
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/join?sessionToken=s3cret-6&page_token=s3cret-7&tokens=shown-6&flag&Api%5FKey=s3cret-8&x_api_key=s3cret-12", strings.NewReader("node=two&primaryNodePassword=s3cret-9&client_secret=s3cret-10&secrets=shown-7"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	out := traceOf(t, Options{}, http.DefaultTransport, req)
	if strings.Contains(out, "s3cret") {
		t.Errorf("the trace shows a credential:\n%s", out)
	}
	for i := 1; i <= 7; i++ {
		if !strings.Contains(out, fmt.Sprintf("shown-%d", i)) {
			t.Errorf("the trace hides shown-%d, under a name that only looks like a credential's:\n%s", i, out)
		}
	}
	for _, want := range []string{"/join?sessionToken=REDACTED&page_token=REDACTED&tokens=shown-6&flag&Api%5FKey=REDACTED&x_api_key=REDACTED ", `"TmdbApiKey": "REDACTED"`, "\nnode=two&primaryNodePassword=REDACTED&client_secret=REDACTED&secrets=shown-7\n", `"sharedSecret": "REDACTED"`, `"REFRESH_TOKEN": "REDACTED"`, `"apiKey": "REDACTED"`, `"password": null`} {
		if !strings.Contains(out, want) {
			t.Errorf("the trace is missing %q:\n%s", want, out)
		}
	}
}

// What else writes down what it was sent - a tool's arguments, say - blanks
// a credential by the rule a trace does: a string field of a name an API
// passes one under, of a name that ends as a credential's does, or of a name
// the caller adds, whether the JSON is whole or cut short. Nothing else is
// touched.
func TestRedactJSON(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		also []string
		want string
	}{
		{`{"name":"one","password":"hunter2","confirm":true}`, nil, `{"name":"one","password":"REDACTED","confirm":true}`},
		{`{"apikey":"abc","api_key":"abc","token":"abc","access_token":"abc"}`, nil, `{"apikey":"REDACTED","api_key":"REDACTED","token":"REDACTED","access_token":"REDACTED"}`},
		{`{"primaryNodePassword": "x", "sharedSecret" : "y", "TmdbApiKey":"z"}`, nil, `{"primaryNodePassword": "REDACTED", "sharedSecret" : "REDACTED", "TmdbApiKey":"REDACTED"}`},
		{`{"pin":"1234","name":"one"}`, nil, `{"pin":"1234","name":"one"}`},
		{`{"pin":"1234","name":"one"}`, []string{"PIN"}, `{"pin":"REDACTED","name":"one"}`},
		{`{"password":"a \"quoted\" one","next":1}`, nil, `{"password":"REDACTED","next":1}`},
		{`{"name":"one","password":"cut sho`, nil, `{"name":"one","password":"REDACTED"`},
		{`{"passwords":3,"password":null}`, nil, `{"passwords":3,"password":null}`},
		{``, nil, ``},
	} {
		if got := RedactJSON(tc.in, tc.also...); got != tc.want {
			t.Errorf("RedactJSON(%s, %v) = %s, want %s", tc.in, tc.also, got, tc.want)
		}
	}
}

// A name holds a credential when it is one an API passes a credential
// under, one the caller adds, or one that ends as a credential's does,
// whatever is in front and in whatever case.
func TestSecretName(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"password": true, "Password": true, "primaryNodePassword": true, "sharedSecret": true, "refresh_token": true, "TmdbApiKey": true, "api_key": true, "apikey": true, "token": true, "ssh_passphrase": true, "ssh_private_key": true, "sshPrivateKey": true, "cookie": true, "sessionCookie": true, "passkey": true, "Passkey": true,
		"name": false, "passwords": false, "tokens": false, "cookies": false, "key": false, "userKey": false, "tls_key": false, "sort_key": false, "pass": false, "": false,
	} {
		if got := SecretName(name); got != want {
			t.Errorf("SecretName(%q) = %v, want %v", name, got, want)
		}
	}
	if !SecretName("Webhook", "feed_url", "webhook") || SecretName("webhooks", "webhook") {
		t.Error("a name the caller adds is matched whole, in whatever case")
	}
}
