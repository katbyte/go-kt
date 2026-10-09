package replayproxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// bodies is what a cassette holds for each path.
func bodies(t *testing.T, dir, host string) map[string][]string {
	t.Helper()

	out := map[string][]string{}
	for path, is := range recorded(t, dir, host) {
		for _, i := range is {
			out[path] = append(out[path], i.Body)
		}
	}

	return out
}

// Record mode against a local upstream, so the recording path is checked on
// every run. A gzipped answer is stored as the text it decodes to, so a
// cassette can be read, grepped and compared, and its token redacted; a
// binary under octet-stream is elided, text under it kept; and a rate limit
// or a server error is passed on but never stored, since a replay of one
// would read as the service's answer.
func TestRecordDecodesGzipAndKeepsNoFailure(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/token/new":
			if offered := r.Header.Get("Accept-Encoding"); offered != "gzip" {
				t.Errorf("the service was offered %q, want gzip alone", offered)
			}
			w.Header().Set("Content-Type", "application/json;charset=utf-8")
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Authentication-Callback", "https://www.example.org/authenticate/tok-1")
			zw := gzip.NewWriter(w)
			_, _ = zw.Write([]byte(`{"success":true,"request_token":"tok-1"}`))
			_ = zw.Close()
		case "/packageFiles/Plugin.dll":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0x4d, 0x5a, 0x90, 0x00, 0xff, 0xfe})
		case "/list.txt":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("one\ntwo\n"))
		case "/limited":
			w.Header().Set("Content-Type", contentJSON)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429}}`)
		case "/broken":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "upstream fell over")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")

	dir := t.TempDir()
	p, err := New(Options{Mode: Record, CassetteDir: dir, Addr: loopback, Logger: quiet(), RedactBodyFields: []string{"request_token"}})
	if err != nil {
		t.Fatal(err)
	}
	// the server that asked is handed the token, which it needs for what it
	// asks next; the cassette is not (below)
	if _, got := get(t, p, upstream.URL+"/v1/token/new"); got != `{"success":true,"request_token":"tok-1"}` {
		t.Errorf("the answer handed to the server = %q, want it decoded with its token", got)
	}
	get(t, p, upstream.URL+"/packageFiles/Plugin.dll")
	get(t, p, upstream.URL+"/list.txt")
	if status, body := get(t, p, upstream.URL+"/limited"); status != http.StatusTooManyRequests || body != `{"error":{"code":429}}` {
		t.Errorf("the rate limit was answered %d %q, want it passed on", status, body)
	}
	if status, body := get(t, p, upstream.URL+"/broken"); status != http.StatusBadGateway || body != "upstream fell over" {
		t.Errorf("the server error was answered %d %q, want it passed on", status, body)
	}
	// an answer that is the service's own, such as a 404, is recorded
	if status, _ := get(t, p, upstream.URL+"/nothing-here"); status != http.StatusNotFound {
		t.Errorf("a 404 was answered %d", status)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	got := recorded(t, dir, host)
	token := got["/v1/token/new"]
	if len(token) != 1 || token[0].BodyBase64 != "" || token[0].Body != `{"success":true,"request_token":"`+redactedValue+`"}` || token[0].Headers["Content-Encoding"] != "" {
		t.Errorf("gzipped JSON = %+v, want it decoded, redacted and stored as text", token)
	}
	if len(token) == 1 && strings.Contains(token[0].Headers["Authentication-Callback"], "tok-1") {
		t.Errorf("the header still carries the token: %s", token[0].Headers["Authentication-Callback"])
	}
	if dll := got["/packageFiles/Plugin.dll"]; len(dll) != 1 || !dll[0].Elided || dll[0].ElidedSize != 6 {
		t.Errorf("a binary = %+v, want it elided", dll)
	}
	if txt := got["/list.txt"]; len(txt) != 1 || txt[0].Elided || txt[0].Body != "one\ntwo\n" {
		t.Errorf("text under octet-stream = %+v, want it kept", txt)
	}
	if missing := got["/nothing-here"]; len(missing) != 1 || missing[0].Status != http.StatusNotFound {
		t.Errorf("a 404 = %+v, want it recorded", missing)
	}
	if len(got["/limited"])+len(got["/broken"]) != 0 {
		t.Errorf("a failure was recorded: %+v %+v", got["/limited"], got["/broken"])
	}

	// and it replays, decoded, without the upstream; what was not recorded
	// is a miss, not a failure served as an answer
	upstream.Close()
	replay, err := New(Options{CassetteDir: dir, Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	if status, body := get(t, replay, "http://"+host+"/v1/token/new"); status != http.StatusOK || body != `{"success":true,"request_token":"`+redactedValue+`"}` {
		t.Errorf("replayed %d %q", status, body)
	}
	if status, _ := get(t, replay, "http://"+host+"/nothing-here"); status != http.StatusNotFound {
		t.Errorf("the recorded 404 replayed as %d", status)
	}
	if misses := replay.Misses(); len(misses) != 0 {
		t.Errorf("misses = %v, want none", misses)
	}
	if status, _ := get(t, replay, "http://"+host+"/limited"); status != http.StatusBadGateway || len(replay.Misses()) != 1 {
		t.Errorf("the rate limit replayed as %d with misses %v, want a miss", status, replay.Misses())
	}
}

// Record fills in only what the cassettes lack, so recording a new test's
// lookups leaves every other recording as it was; Rerecord refreshes what is
// recorded too: a request is fetched live the first time the proxy sees it,
// its recording replaced, and repeats in the same run are served that fresh
// answer without another fetch.
func TestRecordFillsInAndRerecordRefreshes(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	fetched := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", contentJSON)
		_, _ = io.WriteString(w, `{"runtime":118}`)
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")
	fetches := func(path string) int {
		mu.Lock()
		defer mu.Unlock()

		return fetched[path]
	}

	dir := oneRecording(t, host, "/v1/things/348", recordedAnswer)

	// Record: the recording answers, and only the request it lacks goes out
	p, err := New(Options{Mode: Record, CassetteDir: dir, Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	if _, got := get(t, p, upstream.URL+"/v1/things/348"); got != recordedAnswer || fetches("/v1/things/348") != 0 {
		t.Errorf("Record answered a recorded request with %s after %d fetches, want the recording", got, fetches("/v1/things/348"))
	}
	if _, got := get(t, p, upstream.URL+"/v1/things/78"); got != `{"runtime":118}` || fetches("/v1/things/78") != 1 {
		t.Errorf("Record answered a new request with %s", got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := bodies(t, dir, host); !slices.Equal(got["/v1/things/348"], []string{recordedAnswer}) || len(got["/v1/things/78"]) != 1 {
		t.Errorf("after Record the cassette holds %v", got)
	}

	// Rerecord: each request fetched once, the recording replaced in place
	p, err = New(Options{Mode: Rerecord, CassetteDir: dir, Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, got := get(t, p, upstream.URL+"/v1/things/348"); got != `{"runtime":118}` {
			t.Errorf("Rerecord answered %s, want the fresh answer", got)
		}
	}
	if n := fetches("/v1/things/348"); n != 1 {
		t.Errorf("Rerecord fetched a request %d times in one run, want once", n)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := bodies(t, dir, host); !slices.Equal(got["/v1/things/348"], []string{`{"runtime":118}`}) || len(got["/v1/things/78"]) != 1 {
		t.Errorf("after Rerecord the cassette holds %v, want each request once, refreshed", got)
	}
}

// A login is recorded with its token blanked out, and a recording run that
// replayed it would hand the server that blank for a token: every call the
// server then made that no cassette held went out with it, and the 401s were
// what got recorded. So while recording, a recording that holds a redacted
// credential is fetched again for the server that asked, and the cassette
// keeps the recording as it was.
func TestRecordFetchesARedactedLoginAgain(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	logins := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentJSON)
		switch r.URL.Path {
		case "/v4/login":
			mu.Lock()
			logins++
			n := logins
			mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"data":{"token":"tok-%d"},"status":"success"}`, n)
		default:
			// what the token authorises, which only a real token opens
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"message":"Unauthorized"}`)

				return
			}
			_, _ = io.WriteString(w, `{"data":{"id":1438}}`)
		}
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")

	dir := t.TempDir()
	writeCassette(t, dir, cassette{Host: host, Interactions: []*interaction{{
		Key: "POST " + host + "/v4/login", Method: http.MethodPost, Host: host, Path: "/v4/login",
		Status: http.StatusOK, Headers: map[string]string{"Content-Type": contentJSON},
		Body: `{"data":{"token":"` + redactedValue + `"},"status":"success"}`,
	}}})
	do := func(p *Proxy, method, path, token string) (int, string) {
		t.Helper()

		req, err := http.NewRequestWithContext(t.Context(), method, upstream.URL+path, strings.NewReader(`{"apikey":"k"}`))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := clientThrough(t, p).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(body)
	}
	tokenOf := func(body string) string {
		var login struct {
			Data struct{ Token string } `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &login)

		return login.Data.Token
	}

	for _, mode := range []Mode{Record, Rerecord} {
		p, err := New(Options{Mode: mode, CassetteDir: dir, Addr: loopback, Logger: quiet(), RedactBodyFields: []string{"token"}})
		if err != nil {
			t.Fatal(err)
		}
		// asked twice: Rerecord's second ask is served its fresh recording,
		// which is redacted too
		for range 2 {
			_, body := do(p, http.MethodPost, "/v4/login", "")
			token := tokenOf(body)
			if !strings.HasPrefix(token, "tok-") {
				t.Errorf("mode %d: the login handed to the server = %s, want a live token", mode, body)
			}
			if status, got := do(p, http.MethodGet, "/v4/series/79126", token); status != http.StatusOK {
				t.Errorf("mode %d: a call made with that token = %d %s, want it recorded as answered", mode, status, got)
			}
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		// the cassette holds the login redacted, and the call it authorised
		// as answered
		raw, err := os.ReadFile(filepath.Join(dir, hostFile(host))) //nolint:gosec // a path this test wrote
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "tok-") || strings.Contains(string(raw), "Unauthorized") || !strings.Contains(string(raw), redactedValue) {
			t.Errorf("mode %d: the cassette = %s, want the login redacted and no 401", mode, raw)
		}
	}
}

// A trimmed response is recorded and served trimmed: a service's list of
// everything it knows is cut to the entries a suite reads, and replay answers
// with exactly what was recorded. An answer that is no 200 is left as it came.
func TestRecordTrims(t *testing.T) {
	t.Parallel()

	const everything = `[{"id":1},{"id":2},{"id":3}]`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentJSON)
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusNotFound)
		}
		_, _ = io.WriteString(w, everything)
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")

	keepTwo := func(body []byte) ([]byte, error) {
		var all []map[string]int
		if err := json.Unmarshal(body, &all); err != nil {
			return nil, err
		}
		var kept []map[string]int
		for _, e := range all {
			if e["id"] == 2 {
				kept = append(kept, e)
			}
		}

		return json.Marshal(kept)
	}
	refuse := func([]byte) ([]byte, error) { return nil, errors.New("not the list this trim knows") }

	dir := t.TempDir()
	p, err := New(Options{
		Mode: Record, CassetteDir: dir, Addr: loopback, Logger: quiet(),
		Trim: map[string]func([]byte) ([]byte, error){host + "/list": keepTwo, host + "/gone": keepTwo, host + "/odd": refuse},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, got := get(t, p, upstream.URL+"/list"); got != `[{"id":2}]` {
		t.Errorf("recorded and served = %s", got)
	}
	if _, got := get(t, p, upstream.URL+"/gone"); got != everything {
		t.Errorf("an answer that is no 200 = %s, want it untrimmed", got)
	}
	if _, got := get(t, p, upstream.URL+"/untrimmed"); got != everything {
		t.Errorf("a path with no trim = %s", got)
	}
	// a trim that fails is a failed recording, not an untrimmed one
	if status, got := get(t, p, upstream.URL+"/odd"); status != http.StatusBadGateway || !strings.Contains(got, "not the list this trim knows") {
		t.Errorf("a trim that fails = %d %s", status, got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := bodies(t, dir, host); !slices.Equal(got["/list"], []string{`[{"id":2}]`}) || len(got["/odd"]) != 0 {
		t.Errorf("the cassette holds %v", got)
	}

	replay, err := New(Options{CassetteDir: dir, Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	if _, got := get(t, replay, upstream.URL+"/list"); got != `[{"id":2}]` {
		t.Errorf("replayed = %s", got)
	}
}

// A failure is logged on one line, whatever the request it names holds: a
// path can carry a line break, and the reason a fetch failed names the path,
// so a request could otherwise start a line of the log the proxy never
// wrote.
func TestAFailureIsLoggedOnOneLine(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentJSON)
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")

	var logged logBuffer
	p, err := New(Options{
		Mode: Record, CassetteDir: t.TempDir(), Addr: loopback, Logger: log.New(&logged, "", 0),
		Trim: map[string]func([]byte) ([]byte, error){
			host + "/list\nREPLAY MISS forged": func([]byte) ([]byte, error) { return nil, errors.New("not the list this trim knows") },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	if status, _ := get(t, p, upstream.URL+"/list%0AREPLAY%20MISS%20forged"); status != http.StatusBadGateway {
		t.Fatalf("a trim that fails answered %d", status)
	}
	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "record ") || !strings.Contains(lines[0], "not the list this trim knows") {
		t.Errorf("the failure was logged as %q, want the one line", lines)
	}
}

// Verify serves the recording, holds it against what the service answers
// now, and writes nothing: a field that went, came or changed type is a
// drift, a different value is not, and a request with no recording is a miss
// as in replay.
func TestVerifyReportsDriftAndWritesNothing(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentJSON)
		switch r.URL.Path {
		case "/v1/same":
			_, _ = io.WriteString(w, `{"runtime":999,"token":"live-token"}`)
		default:
			_, _ = io.WriteString(w, `{"runtime":"118","year":2000}`)
		}
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")

	dir := t.TempDir()
	writeCassette(t, dir, cassette{Host: host, Interactions: []*interaction{
		{Key: "GET " + host + "/v1/changed", Method: http.MethodGet, Host: host, Path: "/v1/changed", Status: http.StatusOK, Body: `{"runtime":117,"title":"x"}`},
		{Key: "GET " + host + "/v1/same", Method: http.MethodGet, Host: host, Path: "/v1/same", Status: http.StatusOK, Body: `{"runtime":117,"token":"` + redactedValue + `"}`},
	}})
	before, err := os.ReadFile(filepath.Join(dir, hostFile(host))) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}

	p, err := New(Options{Mode: Verify, CassetteDir: dir, Addr: loopback, Logger: quiet(), RedactBodyFields: []string{"token"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, got := get(t, p, upstream.URL+"/v1/changed"); got != `{"runtime":117,"title":"x"}` {
		t.Errorf("Verify served %s, want the recording", got)
	}
	if _, got := get(t, p, upstream.URL+"/v1/same"); got != `{"runtime":117,"token":"`+redactedValue+`"}` {
		t.Errorf("Verify served %s, want the recording", got)
	}
	if status, _ := get(t, p, upstream.URL+"/v1/never-recorded"); status != http.StatusBadGateway || len(p.Misses()) != 1 {
		t.Errorf("a request with no recording = %d, misses %v", status, p.Misses())
	}

	drifts := p.Drifts()
	if len(drifts) != 1 {
		t.Fatalf("drifts = %v, want the one answer that changed shape", drifts)
	}
	d := drifts[0]
	if d.Key != "GET "+host+"/v1/changed" || !slices.Equal(d.FieldsRemoved, []string{"runtime:number", "title:string"}) || !slices.Equal(d.FieldsAdded, []string{"runtime:string", "year:number"}) {
		t.Errorf("drift = %+v", d)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, hostFile(host))) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("Verify rewrote the cassette:\n%s", after)
	}
}

// Record mode against the real internet, through a real tunnel. Off by
// default so `go test ./...` stays hermetic; this is the check that the
// recording path still reaches a real https service.
//
//	REPLAYPROXY_TEST_LIVE=1 go test ./test/replayproxy/ -run Internet -v
func TestRecordAgainstTheInternet(t *testing.T) {
	t.Parallel()

	if os.Getenv("REPLAYPROXY_TEST_LIVE") == "" {
		t.Skip("set REPLAYPROXY_TEST_LIVE=1 to record against the real internet")
	}

	dir := t.TempDir()
	p, err := New(Options{Mode: Record, CassetteDir: dir, Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	status, body := get(t, p, "https://example.com/")
	if status != http.StatusOK || !strings.Contains(body, "Example Domain") {
		t.Fatalf("status = %d: %s", status, body)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	// and the cassette it wrote must replay without touching the network
	if got := bodies(t, dir, "example.com"); !slices.Equal(got["/"], []string{body}) {
		t.Errorf("the cassette holds %v", got)
	}
	replay, err := New(Options{CassetteDir: dir, Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	if _, again := get(t, replay, "https://example.com/"); again != body {
		t.Error("replayed body differs from the recorded one")
	}
	if misses := replay.Misses(); len(misses) != 0 {
		t.Errorf("replay missed: %v", misses)
	}
}
