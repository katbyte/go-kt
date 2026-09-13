package chttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/katbyte/go-kt/clog"
)

// fakeTransport returns each queued outcome in order, counting attempts and
// recording the Retry-After it was told to send with each response.
type fakeTransport struct {
	mu       sync.Mutex
	attempts int
	statuses []int // 0 means return an error instead of a response
	headers  http.Header
}

func (t *fakeTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	i := min(t.attempts, len(t.statuses)-1)
	t.attempts++
	if t.statuses[i] == 0 {
		return nil, errors.New("connection reset")
	}
	h := http.Header{}
	if t.headers != nil {
		h = t.headers.Clone()
	}
	return &http.Response{StatusCode: t.statuses[i], Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func (t *fakeTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

// newRetry is the RetryTransport under test with the default attempt budget;
// the real backoff (1s, 2s) applies, so cases that retry are kept short.
func newRetry(next http.RoundTripper) *RetryTransport {
	return NewRetryTransport("test", next, DefaultMaxRetry)
}

// timed reports how long f took; taking the clock inside the helper keeps the
// start time out of the test body.
func timed(f func()) time.Duration {
	start := time.Now()
	f()
	return time.Since(start)
}

func TestRetryTransportSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		method       string
		markSafe     bool
		statuses     []int
		wantAttempts int
		wantErr      bool
		wantStatus   int
	}{
		// mutations must not be re-sent when their fate is unknown
		{"post 502 not retried", http.MethodPost, false, []int{502}, 1, false, 502},
		{"patch transport error not retried", http.MethodPatch, false, []int{0}, 1, true, 0},
		{"delete 500 not retried", http.MethodDelete, false, []int{500}, 1, false, 500},
		// 429 was rejected before it was acted on, so mutations may retry it
		{"post 429 retried", http.MethodPost, false, []int{429, 201}, 2, false, 201},
		// reads ride through everything
		{"get 502 retried", http.MethodGet, false, []int{502, 200}, 2, false, 200},
		{"head 503 retried", http.MethodHead, false, []int{503, 200}, 2, false, 200},
		{"get transport error retried", http.MethodGet, false, []int{0, 200}, 2, false, 200},
		{"marked post 502 retried", http.MethodPost, true, []int{502, 200}, 2, false, 200},
		{"marked post transport error retried", http.MethodPost, true, []int{0, 200}, 2, false, 200},
		// the budget is finite
		{"get exhausts attempts and returns the last response", http.MethodGet, false, []int{500, 500, 500}, 3, false, 500},
		{"get exhausts attempts and returns the last error", http.MethodGet, false, []int{0, 0, 0}, 3, true, 0},
		// success needs no retry
		{"get 200 once", http.MethodGet, false, []int{200}, 1, false, 200},
		{"get 404 is not transient", http.MethodGet, false, []int{404}, 1, false, 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ft := &fakeTransport{statuses: tc.statuses}
			rt := newRetry(ft)
			req, err := http.NewRequestWithContext(context.Background(), tc.method, "https://example.test/", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			if tc.markSafe {
				req = MarkRetrySafe(req)
			}

			resp, err := rt.RoundTrip(req)
			if resp != nil {
				defer func() { _ = resp.Body.Close() }()
			}

			if got := ft.count(); got != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", got, tc.wantAttempts)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

// verifies the retry transport rewinds request bodies between attempts, so a
// retried POST sends the same payload each time
func TestRetryTransportRewindsBody(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests) // retried for every method, no mark needed
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: newRetry(http.DefaultTransport)}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader("hello body"))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after retries, got %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(bodies))
	}
	for i, b := range bodies {
		if b != "hello body" {
			t.Errorf("attempt %d body = %q, want %q", i+1, b, "hello body")
		}
	}
}

// a body without GetBody cannot be replayed, so the first response is returned as-is
func TestRetryTransportBodyWithoutGetBodyIsNotRetried(t *testing.T) {
	t.Parallel()

	ft := &fakeTransport{statuses: []int{429, 200}}
	rt := newRetry(ft)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Body = io.NopCloser(strings.NewReader("stream"))
	req.GetBody = nil

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ft.count() != 1 || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("attempts = %d, status = %d; want 1 attempt returning 429", ft.count(), resp.StatusCode)
	}
}

func TestRetryTransportHonoursRetryAfterOn429(t *testing.T) {
	t.Parallel()

	// Retry-After: 0 makes the retry immediate, which is observable because the
	// default backoff for the first retry is a full second
	h := http.Header{}
	h.Set("Retry-After", "0")
	ft := &fakeTransport{statuses: []int{429, 200}, headers: h}
	rt := newRetry(ft)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	var resp *http.Response
	elapsed := timed(func() { resp, err = rt.RoundTrip(req) }) //nolint:bodyclose // closed by the defer below
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if elapsed > 500*time.Millisecond {
		t.Errorf("retry took %s, want Retry-After: 0 to make it immediate", elapsed)
	}
	if resp.StatusCode != http.StatusOK || ft.count() != 2 {
		t.Errorf("status = %d after %d attempts, want 200 after 2", resp.StatusCode, ft.count())
	}
}

func TestRetryTransportAbortsWaitWhenContextCancelled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		statuses []int
	}{
		{"during a 5xx backoff", []int{500, 200}},
		{"during a transport-error backoff", []int{0, 200}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ft := &fakeTransport{statuses: tc.statuses}
			rt := newRetry(ft)
			ctx, cancel := context.WithCancel(context.Background())
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}

			// cancel while the transport is sleeping out the 1s backoff
			go func() {
				time.Sleep(50 * time.Millisecond)
				cancel()
			}()

			var resp *http.Response
			elapsed := timed(func() { resp, err = rt.RoundTrip(req) }) //nolint:bodyclose // nil on the expected error path, closed just below otherwise
			if resp != nil {
				_ = resp.Body.Close()
				t.Fatalf("got a response (%d), want the wait aborted", resp.StatusCode)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if elapsed > 500*time.Millisecond {
				t.Errorf("RoundTrip took %s, want the backoff abandoned on cancel", elapsed)
			}
			if ft.count() != 1 {
				t.Errorf("attempts = %d, want 1", ft.count())
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"absent", "", 0, false},
		{"seconds", "5", 5 * time.Second, true},
		{"seconds with whitespace", " 7 ", 7 * time.Second, true},
		{"zero", "0", 0, true},
		{"negative clamps to zero", "-3", 0, true},
		{"capped", "3600", MaxRetryAfter, true},
		{"http date in the future", now.Add(10 * time.Second).Format(http.TimeFormat), 10 * time.Second, true},
		{"http date in the past clamps to zero", now.Add(-10 * time.Second).Format(http.TimeFormat), 0, true},
		{"http date far ahead is capped", now.Add(time.Hour).Format(http.TimeFormat), MaxRetryAfter, true},
		{"garbage", "soon", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := retryAfter(tc.value, now)
			if ok != tc.ok || got != tc.want {
				t.Errorf("retryAfter(%q) = (%s, %v), want (%s, %v)", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestBackoffDoubles(t *testing.T) {
	t.Parallel()

	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if got := backoff(attempt); got != want {
			t.Errorf("backoff(%d) = %s, want %s", attempt, got, want)
		}
	}
}

func TestRetrySafe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		marked bool
		want   bool
	}{
		{http.MethodGet, false, true},
		{http.MethodHead, false, true},
		{http.MethodOptions, false, true},
		{http.MethodPost, false, false},
		{http.MethodPut, false, false},
		{http.MethodPatch, false, false},
		{http.MethodDelete, false, false},
		{http.MethodPost, true, true},
		{http.MethodDelete, true, true},
	}
	for _, tc := range tests {
		req, err := http.NewRequestWithContext(context.Background(), tc.method, "https://example.test/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if tc.marked {
			req = MarkRetrySafe(req)
		}
		if got := retrySafe(req); got != tc.want {
			t.Errorf("retrySafe(%s, marked=%v) = %v, want %v", tc.method, tc.marked, got, tc.want)
		}
	}
}

func TestNewBaseTransportSetsTimeouts(t *testing.T) {
	t.Parallel()

	rt := NewBaseTransport()
	tr, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("NewBaseTransport() = %T, want *http.Transport", rt)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second || tr.ResponseHeaderTimeout != 30*time.Second || tr.DialContext == nil {
		t.Errorf("timeouts = (tls %s, header %s, dial set %v), want (10s, 30s, true)", tr.TLSHandshakeTimeout, tr.ResponseHeaderTimeout, tr.DialContext != nil)
	}
	if tr == http.DefaultTransport {
		t.Error("NewBaseTransport() returned http.DefaultTransport itself instead of a clone")
	}
}

func TestNewHTTPClientEndToEnd(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := NewHTTPClient("test")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Errorf("got %d %q, want 200 {\"ok\":true}", resp.StatusCode, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Errorf("server hits = %d, want 2 (one 429 then success)", hits)
	}
}

// a zero or negative attempt budget must still send the request once rather
// than returning a nil response and nil error
func TestRetryTransportSendsAtLeastOnce(t *testing.T) {
	t.Parallel()

	for _, budget := range []int{0, -1} {
		ft := &fakeTransport{statuses: []int{200}}
		rt := NewRetryTransport("test", ft, budget)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("maxRetry %d: unexpected error: %v", budget, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || ft.count() != 1 {
			t.Errorf("maxRetry %d: status %d after %d attempts, want 200 after 1", budget, resp.StatusCode, ft.count())
		}
	}
}

// a GetBody that fails means the body cannot be replayed, so no retry happens
func TestRetryTransportGetBodyErrorIsNotRetried(t *testing.T) {
	t.Parallel()

	ft := &fakeTransport{statuses: []int{429, 200}}
	rt := newRetry(ft)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test/", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("body gone") }

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ft.count() != 1 || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("attempts = %d, status = %d; want 1 attempt returning 429", ft.count(), resp.StatusCode)
	}
}

// errReader fails on the first read, which makes httputil's dump helpers fail
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReader) Close() error             { return nil }

// errorTransport always fails the round trip
type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed")
}

// bodyTransport returns a 200 whose body errors on read
type bodyTransport struct{}

func (bodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, ContentLength: 5, Body: errReader{}, Header: http.Header{}}, nil
}

func TestPrettyPrintJSON(t *testing.T) {
	t.Parallel()

	in := "HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\"a\":1,\"b\":[2,3]}"
	want := "HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\n \"a\": 1,\n \"b\": [\n  2,\n  3\n ]\n}"
	if got := prettyPrintJSON([]byte(in)); got != want {
		t.Errorf("prettyPrintJSON =\n%s\nwant\n%s", got, want)
	}
	// non-JSON lines pass through untouched
	if got := prettyPrintJSON([]byte("plain text\nnot json")); got != "plain text\nnot json" {
		t.Errorf("prettyPrintJSON(plain) = %q", got)
	}
}

// Transport writes to the shared clog.Log, so this test swaps it and runs serially.
//
//nolint:paralleltest // mutates package-level clog.Log
func TestTransportTraceLogging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	orig := clog.Log
	clog.Log = clog.NewWithOutput(&buf)
	t.Cleanup(func() { clog.Log = orig })

	do := func() {
		t.Helper()
		client := &http.Client{Transport: NewTransport("Widget", http.DefaultTransport)}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/things", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = resp.Body.Close()
	}

	// below TRACE nothing is dumped, so bodies are never read for logging
	clog.Log.SetLevel(logrus.DebugLevel)
	do()
	if buf.Len() != 0 {
		t.Fatalf("at DEBUG the transport logged %q, want nothing", buf.String())
	}

	clog.Log.SetLevel(logrus.TraceLevel)
	do()
	out := buf.String()
	// logrus quotes the message, so the pretty-printed body's own quotes arrive escaped
	for _, want := range []string{"Widget API Request Details", "GET /things", "Widget API Response Details", `{\n \"hello\": \"world\"\n}`} {
		if !strings.Contains(out, want) {
			t.Errorf("trace output missing %q:\n%s", want, out)
		}
	}
	// a transport error passes straight through, with the request still dumped
	buf.Reset()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewTransport("Widget", errorTransport{}).RoundTrip(req) //nolint:bodyclose // nil on the error path
	if err == nil || resp != nil {
		t.Fatalf("RoundTrip = (%v, %v), want (nil, error)", resp, err)
	}
	if !strings.Contains(buf.String(), "Widget API Request Details") {
		t.Errorf("request was not dumped before the transport error:\n%s", buf.String())
	}

	// dump failures are logged at DEBUG and never break the exchange: a request
	// body that cannot be read, then a response body that cannot be read
	buf.Reset()
	req, err = http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Body, req.ContentLength = errReader{}, 5
	resp, err = NewTransport("Widget", bodyTransport{}).RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
	for _, want := range []string{"Widget API Request error", "Widget API Response error"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("debug output missing %q:\n%s", want, buf.String())
		}
	}
}
