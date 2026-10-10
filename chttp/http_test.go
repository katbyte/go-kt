package chttp

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// connDropped and connRefused are what a fakeTransport fails with in place of a
// status: a connection that was there and went, which is worth another try,
// and a server that is not there at all, which is not.
const (
	connDropped = 0
	connRefused = -1
)

// fakeTransport returns each queued outcome in order, counting attempts and
// recording the Retry-After it was told to send with each response.
type fakeTransport struct {
	mu       sync.Mutex
	attempts int
	statuses []int // connDropped or connRefused return an error instead of a response
	headers  http.Header
}

func (t *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	i := min(t.attempts, len(t.statuses)-1)
	t.attempts++
	switch t.statuses[i] {
	case connDropped:
		return nil, fmt.Errorf("read tcp 10.0.0.2:51000->10.0.0.1:443: %w", syscall.ECONNRESET)
	case connRefused:
		return nil, fmt.Errorf("dial tcp 10.0.0.1:443: %w", syscall.ECONNREFUSED)
	}
	h := http.Header{}
	if t.headers != nil {
		h = t.headers.Clone()
	}

	return &http.Response{StatusCode: t.statuses[i], Header: h, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

func (t *fakeTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

// atOnce is a client's options with no wait between tries, for the tests
// that are not about the wait.
func atOnce() Options {
	return Options{Name: "test", Retry: Retry{Wait: func(int) time.Duration { return 0 }}}
}

// newRetry is the RetryTransport under test with the default tries and no
// wait between them.
func newRetry(next http.RoundTripper) *RetryTransport {
	return NewRetryTransport(atOnce(), next)
}

// newSlowRetry is the RetryTransport with the defaults a client gets, the
// real backoff (1s, 2s) among them: for the tests that are about the wait.
func newSlowRetry(next http.RoundTripper) *RetryTransport {
	return NewRetryTransport(Options{Name: "test"}, next)
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
		{"patch dropped connection not retried", http.MethodPatch, false, []int{connDropped}, 1, true, 0},
		{"delete 503 not retried", http.MethodDelete, false, []int{503}, 1, false, 503},
		// 429 was rejected before it was acted on, so mutations may retry it
		{"post 429 retried", http.MethodPost, false, []int{429, 201}, 2, false, 201},
		// reads ride through what a gateway answers for a server it could not reach, and a dropped connection
		{"get 502 retried", http.MethodGet, false, []int{502, 200}, 2, false, 200},
		{"head 503 retried", http.MethodHead, false, []int{503, 200}, 2, false, 200},
		{"get 504 retried", http.MethodGet, false, []int{504, 200}, 2, false, 200},
		{"get dropped connection retried", http.MethodGet, false, []int{connDropped, 200}, 2, false, 200},
		{"marked post 502 retried", http.MethodPost, true, []int{502, 200}, 2, false, 200},
		{"marked post dropped connection retried", http.MethodPost, true, []int{connDropped, 200}, 2, false, 200},
		// what will not get better is not asked for again: a server's own error, and a server that is not there
		{"get 500 not retried", http.MethodGet, false, []int{500, 200}, 1, false, 500},
		{"get 501 not retried", http.MethodGet, false, []int{501, 200}, 1, false, 501},
		{"get refused connection not retried", http.MethodGet, false, []int{connRefused, 200}, 1, true, 0},
		// the budget is finite
		{"get exhausts attempts and returns the last response", http.MethodGet, false, []int{503, 503, 503}, 3, false, 503},
		{"get exhausts attempts and returns the last error", http.MethodGet, false, []int{connDropped, connDropped, connDropped}, 3, true, 0},
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
	rt := newSlowRetry(ft)
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
		{"during a 5xx backoff", []int{503, 200}},
		{"during a transport-error backoff", []int{connDropped, 200}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ft := &fakeTransport{statuses: tc.statuses}
			rt := newSlowRetry(ft)
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

	rt := NewBaseTransport(Options{})
	tr, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("NewBaseTransport() = %T, want *http.Transport", rt)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second || tr.ResponseHeaderTimeout != DefaultHeaderWait || tr.DialContext == nil {
		t.Errorf("timeouts = (tls %s, header %s, dial set %v), want (10s, %s, true)", tr.TLSHandshakeTimeout, tr.ResponseHeaderTimeout, tr.DialContext != nil, DefaultHeaderWait)
	}
	if tr == http.DefaultTransport {
		t.Error("NewBaseTransport() returned http.DefaultTransport itself instead of a clone")
	}

	// an API that is slow to start answering is given longer by its client
	slow, ok := NewBaseTransport(Options{HeaderWait: 2 * time.Minute}).(*http.Transport)
	if !ok || slow.ResponseHeaderTimeout != 2*time.Minute {
		t.Errorf("with a header wait of two minutes the transport waits %v", slow.ResponseHeaderTimeout)
	}
}

func TestNewEndToEnd(t *testing.T) {
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

	client := New(Options{Name: "test"})
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
	if got := Tries(resp); got != 2 {
		t.Errorf("Tries = %d, want the two times it was sent", got)
	}
}

// a negative attempt budget must still send the request once rather than
// returning a nil response and nil error, and a budget of one sends it once
// whatever comes back
func TestRetryTransportSendsAtLeastOnce(t *testing.T) {
	t.Parallel()

	for _, budget := range []int{1, -1} {
		ft := &fakeTransport{statuses: []int{200}}
		rt := NewRetryTransport(Options{Retry: Retry{Tries: budget}}, ft)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("tries %d: unexpected error: %v", budget, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || ft.count() != 1 {
			t.Errorf("tries %d: status %d after %d attempts, want 200 after 1", budget, resp.StatusCode, ft.count())
		}

		ft = &fakeTransport{statuses: []int{503, 200}}
		resp, err = NewRetryTransport(Options{Retry: Retry{Tries: budget}}, ft).RoundTrip(req)
		if err != nil {
			t.Fatalf("tries %d: unexpected error: %v", budget, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || ft.count() != 1 {
			t.Errorf("tries %d: status %d after %d attempts, want the 503 after 1", budget, resp.StatusCode, ft.count())
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

// What is worth another try, how often and how long after are the caller's
// to set: an API that answers 500 for "busy" adds it, and one whose server
// restarts under it rides through a refused connection.
func TestRetryIsTheCallersToSet(t *testing.T) {
	t.Parallel()

	get := func(o Options, statuses ...int) (int, int, error) {
		t.Helper()

		ft := &fakeTransport{statuses: statuses}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := NewRetryTransport(o, ft).RoundTrip(req)
		if err != nil {
			return 0, ft.count(), err
		}
		defer func() { _ = resp.Body.Close() }()

		return resp.StatusCode, ft.count(), nil
	}
	instant := func(int) time.Duration { return 0 }

	if status, sent, err := get(Options{Retry: Retry{Wait: instant, Status: func(code int) bool { return code == 500 }}}, 500, 200); err != nil || status != 200 || sent != 2 {
		t.Errorf("with 500 named as worth another try: status %d after %d tries (%v), want 200 after 2", status, sent, err)
	}
	// naming the statuses replaces the default ones
	if status, sent, _ := get(Options{Retry: Retry{Wait: instant, Status: func(code int) bool { return code == 500 }}}, 503, 200); status != 503 || sent != 1 {
		t.Errorf("with only 500 named, a 503 came back as %d after %d tries, want it as it was after 1", status, sent)
	}
	if status, sent, err := get(Options{Retry: Retry{Wait: instant, Error: func(err error) bool { return Dropped(err) || Refused(err) }}}, connRefused, connDropped, 200); err != nil || status != 200 || sent != 3 {
		t.Errorf("with a refused connection named as worth another try beside a dropped one: status %d after %d tries (%v), want 200 after 3", status, sent, err)
	}
	if status, sent, err := get(Options{Retry: Retry{Wait: instant, Tries: 5}}, 503, 503, 503, 503, 200); err != nil || status != 200 || sent != 5 {
		t.Errorf("with five tries: status %d after %d tries (%v), want 200 after 5", status, sent, err)
	}

	var waits []int
	if _, sent, _ := get(Options{Retry: Retry{Tries: 4, Wait: func(attempt int) time.Duration { waits = append(waits, attempt); return 0 }}}, 503); sent != 4 || !slices.Equal(waits, []int{0, 1, 2}) {
		t.Errorf("four tries waited after attempts %v, want after 0, 1 and 2", waits)
	}
}

// A request that got no answer however often it was sent says how often,
// and is still the error it was.
func TestAFailureSaysHowOftenItWasTried(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	_, err = newRetry(&fakeTransport{statuses: []int{connDropped}}).RoundTrip(req) //nolint:bodyclose // no response on the error path
	if err == nil || !errors.Is(err, syscall.ECONNRESET) || !strings.HasSuffix(err.Error(), "connection reset by peer (tried 3 times)") {
		t.Errorf("err = %v, want the dropped connection, tried 3 times", err)
	}

	_, err = newRetry(&fakeTransport{statuses: []int{connRefused}}).RoundTrip(req) //nolint:bodyclose // no response on the error path
	if err == nil || strings.Contains(err.Error(), "tried") {
		t.Errorf("err = %v, want a refused connection, sent once, to say nothing of tries", err)
	}
}

// Tries is how often the request behind an answer was sent.
func TestTries(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	for want, statuses := range map[int][]int{1: {200}, 2: {503, 404}, 3: {connDropped, 502, 503}} {
		resp, err := newRetry(&fakeTransport{statuses: statuses}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := Tries(resp); got != want {
			t.Errorf("after %v Tries = %d, want %d", statuses, got, want)
		}
	}

	if Tries(nil) != 0 || Tries(&http.Response{}) != 0 || Tries(&http.Response{Request: req}) != 0 {
		t.Error("an answer no client here sent was tried some number of times, want 0")
	}
}

// whole is what the cutShort server answers when it answers whole.
const whole = `{"items":["a","b","c"],"total":3}`

// scripted is a server that answers each request in turn as it is told to,
// and the last way for every request after: "ok" is the whole answer, "cut"
// says the answer is longer than what it sends before it drops the
// connection, and a number is that status with nothing after it.
func scripted(t *testing.T, outcomes ...string) (srv *httptest.Server, requests func() int) {
	t.Helper()

	var mu sync.Mutex
	hits := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		outcome := outcomes[min(hits, len(outcomes)-1)]
		hits++
		mu.Unlock()

		switch outcome {
		case "ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, whole)
		case "cut":
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("the test server cannot drop a connection")

				return
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				t.Error(err)

				return
			}
			_, _ = fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(whole), whole[:len(whole)/2])
			_ = buf.Flush()
			_ = conn.Close()
		default:
			status, err := strconv.Atoi(outcome)
			if err != nil {
				t.Errorf("the test server was told to answer %q", outcome)
			}
			w.WriteHeader(status)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()

		return hits
	}
}

// cutShort is a server whose answer stops part way for the first few
// requests, and then comes whole.
func cutShort(t *testing.T, times int) (srv *httptest.Server, requests func() int) {
	t.Helper()

	outcomes := make([]string, 0, times+1)
	for range times {
		outcomes = append(outcomes, "cut")
	}

	return scripted(t, append(outcomes, "ok")...)
}

// Fetch reads an answer whole, and asks again for one that stopped part way,
// which a retry inside a transport cannot do.
func TestFetch(t *testing.T) {
	t.Parallel()

	fetch := func(method string, srv *httptest.Server, limit int64) (*http.Response, []byte, error) {
		t.Helper()

		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}

		return New(atOnce()).Fetch(req, limit)
	}

	t.Run("an answer that arrives whole", func(t *testing.T) {
		t.Parallel()

		srv, hits := cutShort(t, 0)
		resp, body, err := fetch(http.MethodGet, srv, 1<<20) //nolint:bodyclose // Fetch hands the body back read and closed
		if err != nil || string(body) != whole || resp.StatusCode != http.StatusOK || hits() != 1 || Tries(resp) != 1 {
			t.Errorf("Fetch = %q (%v) after %d requests, want the answer after 1", body, err, hits())
		}
		// the body is read and closed: there is nothing left for the caller to do with it
		if _, err := resp.Body.Read(make([]byte, 1)); err == nil {
			t.Error("the body Fetch handed back can still be read")
		}
	})

	t.Run("an answer that stops part way is asked for again", func(t *testing.T) {
		t.Parallel()

		srv, hits := cutShort(t, 2)
		resp, body, err := fetch(http.MethodGet, srv, 1<<20) //nolint:bodyclose // Fetch hands the body back read and closed
		if err != nil || string(body) != whole || hits() != 3 {
			t.Fatalf("Fetch = %q (%v) after %d requests, want the whole answer after 3", body, err, hits())
		}
		if got := Tries(resp); got != 3 {
			t.Errorf("Tries = %d, want the 3 times it was sent", got)
		}
	})

	t.Run("one that stops every time is an error that says how often", func(t *testing.T) {
		t.Parallel()

		srv, hits := cutShort(t, 99)
		_, body, err := fetch(http.MethodGet, srv, 1<<20) //nolint:bodyclose // Fetch hands the body back read and closed
		if err == nil || body != nil || hits() != 3 || !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "reading the answer") || !strings.HasSuffix(err.Error(), "(tried 3 times)") {
			t.Errorf("Fetch = %q (%v) after %d requests, want the cut-short read, tried 3 times", body, err, hits())
		}
	})

	t.Run("a write that stops part way is not sent again", func(t *testing.T) {
		t.Parallel()

		srv, hits := cutShort(t, 99)
		_, _, err := fetch(http.MethodPost, srv, 1<<20) //nolint:bodyclose // Fetch hands the body back read and closed
		if err == nil || hits() != 1 || strings.Contains(err.Error(), "tried") {
			t.Errorf("Fetch = %v after %d requests, want an error after 1: the write may have been made", err, hits())
		}
	})

	t.Run("an answer over the limit is an error, not an answer cut to fit", func(t *testing.T) {
		t.Parallel()

		srv, hits := cutShort(t, 0)
		resp, body, err := fetch(http.MethodGet, srv, int64(len(whole))-1) //nolint:bodyclose // Fetch hands the body back read and closed
		if !errors.Is(err, ErrTooLarge) || body != nil || hits() != 1 || !strings.Contains(err.Error(), "over 32 bytes") {
			t.Errorf("Fetch = %q (%v), want it refused as over 32 bytes", body, err)
		}
		if resp == nil || resp.StatusCode != http.StatusOK {
			t.Error("the response did not come back with the refusal, so its status cannot be read")
		}

		if _, body, err := fetch(http.MethodGet, srv, int64(len(whole))); err != nil || string(body) != whole { //nolint:bodyclose // Fetch hands the body back read and closed
			t.Errorf("at exactly the limit Fetch = %q (%v), want the answer", body, err)
		}
	})

	t.Run("however it fails, a request is sent no more than the tries it has", func(t *testing.T) {
		t.Parallel()

		// refused by a gateway twice and then cut short, over and over: three sends, not three times three
		srv, hits := scripted(t, "503", "503", "cut", "503", "503", "cut", "503", "503", "cut", "ok")
		_, _, err := fetch(http.MethodGet, srv, 1<<20) //nolint:bodyclose // Fetch hands the body back read and closed
		if err == nil || hits() != 3 || !strings.HasSuffix(err.Error(), "(tried 3 times)") {
			t.Errorf("Fetch = %v after %d requests, want an error after 3 that says so", err, hits())
		}

		// and the third try, when it comes whole, is still an answer
		srv, hits = scripted(t, "503", "cut", "ok")
		resp, body, err := fetch(http.MethodGet, srv, 1<<20) //nolint:bodyclose // Fetch hands the body back read and closed
		if err != nil || string(body) != whole || hits() != 3 || Tries(resp) != 3 {
			t.Errorf("Fetch = %q (%v) after %d requests, want the answer on the third", body, err, hits())
		}
	})

	t.Run("the largest limit there is, is no limit", func(t *testing.T) {
		t.Parallel()

		srv, _ := cutShort(t, 0)
		_, body, err := fetch(http.MethodGet, srv, math.MaxInt64) //nolint:bodyclose // Fetch hands the body back read and closed
		if err != nil || string(body) != whole {
			t.Errorf("with no limit Fetch = %q (%v), want the answer", body, err)
		}

		// and no room at all is an answer that has nothing in it, or a refusal
		if _, _, err := fetch(http.MethodGet, srv, 0); !errors.Is(err, ErrTooLarge) { //nolint:bodyclose // Fetch hands the body back read and closed
			t.Errorf("with a limit of nothing Fetch = %v, want an answer of 33 bytes refused", err)
		}
	})

	t.Run("no answer at all is the transport's error", func(t *testing.T) {
		t.Parallel()

		srv, _ := cutShort(t, 0)
		srv.Close()
		_, _, err := fetch(http.MethodGet, srv, 1<<20) //nolint:bodyclose // Fetch hands the body back read and closed
		if _, ok := errors.AsType[*net.OpError](err); err == nil || !ok {
			t.Errorf("Fetch = %v, want the failed dial", err)
		}
	})
}

// A limit reads the way a person says it.
func TestSize(t *testing.T) {
	t.Parallel()

	for n, want := range map[int64]string{64 << 20: "64 MiB", 8 << 10: "8 KiB", 1500: "1500 bytes", 1: "1 bytes", 3<<20 + 1: "3145729 bytes"} {
		if got := size(n); got != want {
			t.Errorf("size(%d) = %q, want %q", n, got, want)
		}
	}
}

// The package imports nothing outside the standard library, so an SDK built
// on it hands whoever uses that SDK no logger and no other dependency. Its
// tests may: they are not built into anything.
func TestOnlyTheStandardLibraryIsImported(t *testing.T) {
	t.Parallel()

	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(f fs.FileInfo) bool { return !strings.HasSuffix(f.Name(), "_test.go") }, parser.ImportsOnly) //nolint:staticcheck // one directory's imports are all that is wanted, which this still gives
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			files++
			for _, imp := range file.Imports {
				if first, _, _ := strings.Cut(strings.Trim(imp.Path.Value, `"`), "/"); strings.Contains(first, ".") {
					t.Errorf("%s imports %s, which is not in the standard library", name, imp.Path.Value)
				}
			}
		}
	}
	if files < 3 {
		t.Errorf("read the imports of %d files, want every file of the package", files)
	}
}

// A redirect followed on the way is another request, not another try: it
// uses up none of the tries the request has.
func TestARedirectIsNotATry(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		n := len(asked)
		mu.Unlock()

		switch {
		case r.URL.Path == "/old":
			http.Redirect(w, r, "/new", http.StatusFound)
		case n == 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = io.WriteString(w, whole)
		}
	}))
	defer srv.Close()

	o := atOnce()
	o.Retry.Tries = 2
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/old", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, body, err := New(o).Fetch(req, 1<<20) //nolint:bodyclose // Fetch hands the body back read and closed
	if err != nil || string(body) != whole || !slices.Equal(asked, []string{"/old", "/new", "/new"}) {
		t.Fatalf("Fetch = %q (%v) having asked for %v, want the answer after one redirect and one retry", body, err, asked)
	}
	if got := Tries(resp); got != 2 {
		t.Errorf("Tries = %d, want 2: the redirect was not a try", got)
	}
}

// A client is built on the transport it is handed, where it is handed one.
func TestNewOnABaseOfTheCallersOwn(t *testing.T) {
	t.Parallel()

	base := &fakeTransport{statuses: []int{503, 200}}
	o := atOnce()
	o.Base = base
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := New(o).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || base.count() != 2 {
		t.Errorf("status %d after %d requests through the caller's transport, want 200 after 2: the retries sit on top of it", resp.StatusCode, base.count())
	}
}
