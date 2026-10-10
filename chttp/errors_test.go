package chttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// refused is the error of a dial the system refused with "no route to host",
// as an http.Client hands it back.
func refused() error {
	return &url.Error{Op: "Get", URL: "https://nas.invalid/api", Err: fmt.Errorf("dial tcp 10.0.0.4:443: connect: %w", syscall.EHOSTUNREACH)}
}

// On a Mac a dial refused with "no route to host" says what may be refusing
// it, once, and stays the error it was; anywhere else, and for any other
// error, nothing is added.
func TestExplainSaysWhatAMacMayBeDoing(t *testing.T) {
	t.Parallel()

	err := refused()
	got := Explain(err)
	if !errors.Is(got, syscall.EHOSTUNREACH) {
		t.Errorf("explained, the error is no longer the one it wraps: %v", got)
	}
	if _, ok := errors.AsType[*url.Error](got); !ok {
		t.Errorf("explained, the error is %T, want the *url.Error still in it", got)
	}
	if localNetworkHint == "" {
		if !errors.Is(got, err) || got.Error() != err.Error() {
			t.Errorf("off a Mac the error reads %q, want it as it was", got)
		}
	} else {
		if !strings.HasPrefix(got.Error(), err.Error()) || !strings.HasSuffix(got.Error(), localNetworkHint) {
			t.Errorf("on a Mac the error reads %q, want the hint after the error", got)
		}
		// said once, however often it is asked for
		if again := Explain(Explain(got)); strings.Count(again.Error(), "Local Network") != 2 {
			t.Errorf("explained three times the error reads %q", again)
		}
	}

	if Explain(nil) != nil {
		t.Error("no error was explained into one")
	}
	other := fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED)
	if got := Explain(other); !errors.Is(got, other) || got.Error() != other.Error() {
		t.Errorf("a refused connection reads %q, want it as it was", got)
	}
}

// failing is a transport whose every request fails the way it is told to.
type failing struct{ err error }

func (f failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// Transport explains the errors of the requests it carries, so a tool built
// on it says so without asking.
func TestTransportExplainsARefusedDial(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://nas.invalid/api", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewTransport(Options{Name: "test"}, failing{fmt.Errorf("connect: %w", syscall.EHOSTUNREACH)}).RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !errors.Is(err, syscall.EHOSTUNREACH) || !strings.HasSuffix(err.Error(), "no route to host"+localNetworkHint) {
		t.Errorf("err = %v, want the refused dial with the system's hint after it", err)
	}
}

// timeoutError is an error that says it is a wait that ran out.
type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout awaiting response headers" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// A dropped connection is one that was there and went. What the next try
// would meet again is not one.
func TestDropped(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"reset by the other end":             &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)},
		"a write to a closed connection":     &url.Error{Op: "Post", URL: "http://nas/api", Err: fmt.Errorf("write tcp: %w", syscall.EPIPE)},
		"aborted":                            fmt.Errorf("read: %w", syscall.ECONNABORTED),
		"an answer that stopped part way":    fmt.Errorf("reading the answer: %w", io.ErrUnexpectedEOF),
		"an answer that never started":       &url.Error{Op: "Get", URL: "http://nas/api", Err: io.EOF},
		"a connection closed under the read": fmt.Errorf("read tcp: %w", net.ErrClosed),
		"net/http's words for an idle close": errors.New("http: server closed idle connection"),
		"http2's words for a server leaving": errors.New("http2: server sent GOAWAY and closed the connection"),
	} {
		if !Dropped(err) {
			t.Errorf("%s (%v) is not taken for a dropped connection", name, err)
		}
	}

	for name, err := range map[string]error{
		"no error":                        nil,
		"a server that is not there":      &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
		"no route to it":                  fmt.Errorf("connect: %w", syscall.EHOSTUNREACH),
		"a name that did not resolve":     &net.DNSError{Err: "no such host", Name: "nas.invalid", IsNotFound: true},
		"a wait that ran out":             &url.Error{Op: "Get", URL: "http://nas/api", Err: timeoutError{}},
		"a wait that ran out on a reset":  fmt.Errorf("%w: %w", timeoutError{}, syscall.ECONNRESET),
		"a request that was cancelled":    fmt.Errorf("Get: %w", context.Canceled),
		"a deadline that passed":          fmt.Errorf("Get: %w", context.DeadlineExceeded),
		"a certificate that did not hold": errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"),
		"an answer that is not HTTP":      errors.New("net/http: HTTP/1.x transport connection broken: malformed HTTP response"),
	} {
		if Dropped(err) {
			t.Errorf("%s (%v) is taken for a dropped connection", name, err)
		}
	}
}

// Only a credential's value goes: the parameters around it, their order, a
// fragment, and a URL with nothing to hide stay as they were sent.
func TestRedactURL(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{ //nolint:gosec // invented credentials, the point of the test
		"https://api.example.invalid/3/movie/348?api_key=s3cret&language=en": "https://api.example.invalid/3/movie/348?api_key=REDACTED&language=en",
		"https://api.example.invalid/x?apikey=s3cret&API_TOKEN=s3cret":       "https://api.example.invalid/x?apikey=REDACTED&API_TOKEN=REDACTED",
		"https://api.example.invalid/x?access_token=s3cret&token=s3cret#top": "https://api.example.invalid/x?access_token=REDACTED&token=REDACTED#top",
		"http://kt:s3cret@nas:8096/Items?api%5Fkey=s3cret":                   "http://kt:xxxxx@nas:8096/Items?api%5Fkey=REDACTED",
		"http://nas:8096/Items?Fields=Path&keyword=token":                    "http://nas:8096/Items?Fields=Path&keyword=token",
		"http://nas:8096/System/Info":                                        "http://nas:8096/System/Info",
		// a parameter of the API's own is redacted only when it is named
		"http://nas:8096/Items?Ids=1&X-Zzyzx-Token=s3cret": "http://nas:8096/Items?Ids=1&X-Zzyzx-Token=s3cret",
	} {
		if got := RedactURL(raw); got != want {
			t.Errorf("RedactURL(%s) = %s, want %s", raw, got, want)
		}
	}
	if got := RedactURL("http://nas:8096/Items?Ids=1&X-Zzyzx-Token=s3cret#top", "x-zzyzx-token"); got != "http://nas:8096/Items?Ids=1&X-Zzyzx-Token=REDACTED#top" {
		t.Errorf("with the API's own parameter named: %s", got)
	}
}

// A request that got no answer names its whole URL in its error, a key in
// the query with it: the key goes, and the error is the one it was.
func TestRedactErrorHidesTheKeyInAFailedRequestsURL(t *testing.T) {
	t.Parallel()

	const secret = "0123456789abcdef0123456789abcdef"
	timeout := errors.New("i/o timeout")
	err := &url.Error{Op: "Get", URL: "https://api.example.invalid/3/search?query=alien&api_key=" + secret + "&X-Zzyzx-Token=" + secret, Err: timeout}

	// redacted where it is first seen, and wrapped after: a message built
	// around the error before that keeps the URL it was built with
	got := fmt.Errorf("searching: %w", RedactError(err, "X-Zzyzx-Token"))
	if strings.Contains(got.Error(), secret) {
		t.Errorf("the error carries the key: %v", got)
	}
	// what is left says where the request went and why it failed
	for _, want := range []string{"api.example.invalid/3/search", "query=alien", "api_key=REDACTED", "X-Zzyzx-Token=REDACTED", "i/o timeout"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("error = %v, want it to say %q", got, want)
		}
	}
	if _, ok := errors.AsType[*url.Error](got); !ok || !errors.Is(got, timeout) {
		t.Errorf("error = %T %v, want the *url.Error and what it wraps still in it", got, got)
	}
	// an error with no URL in it is left alone
	if plain := RedactError(timeout); !errors.Is(plain, timeout) || plain.Error() != "i/o timeout" {
		t.Errorf("an error with no URL = %v", plain)
	}
}

// A redirect that leaves the first host, or goes from https down to http,
// arrives without the credentials; one that stays arrives with them.
func TestKeepCredentialsOnHost(t *testing.T) {
	t.Parallel()

	policy := KeepCredentialsOnHost("X-Zzyzx-Token")
	for _, tc := range []struct {
		name, from, to string
		kept           bool
	}{
		{"the same host", "http://nas.invalid:8096/a", "http://nas.invalid:8096/web/index.html", true},
		{"another port on the same host", "http://nas.invalid:8096/a", "http://nas.invalid:9000/b", true},
		{"the same host written in another case", "https://NAS.invalid/a", "https://nas.invalid/b", true},
		{"http up to https on the same host", "http://nas.invalid/a", "https://nas.invalid/b", true},
		{"another host", "https://nas.invalid/a", "https://elsewhere.invalid/b", false},
		{"a subdomain", "https://nas.invalid/a", "https://cdn.nas.invalid/b", false},
		{"https down to http", "https://nas.invalid/a", "http://nas.invalid/b", false},
	} {
		first, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.from, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		next, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.to, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		next.Header.Set("Authorization", "Bearer s3cret")
		next.Header.Set("X-Zzyzx-Token", "s3cret")
		next.Header.Set("Accept", "application/json")
		if err := policy(next, []*http.Request{first}); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if got := next.Header.Get("Authorization") != "" && next.Header.Get("X-Zzyzx-Token") != ""; got != tc.kept {
			t.Errorf("%s: credentials kept %v, want %v (%v)", tc.name, got, tc.kept, next.Header)
		}
		if next.Header.Get("Accept") == "" {
			t.Errorf("%s: a header that is no credential was dropped", tc.name)
		}
	}

	// and it stops where Go does
	first, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://nas.invalid/a", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = first
	}
	if err := policy(first, via); err == nil || err.Error() != "stopped after 10 redirects" {
		t.Errorf("after ten redirects: %v", err)
	}
	if err := policy(first, via[:9]); err != nil {
		t.Errorf("after nine redirects: %v", err)
	}
}

// A status error says what was asked, what came back, what was wanted, the
// start of the body, how often it was tried and what the caller knows.
func TestStatusError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err  StatusError
		want string
	}{
		{
			StatusError{Method: "GET", Path: "/System/Info", StatusCode: 401, Expected: []int{200}, Body: "bad token", Note: "API key rejected; check the token"},
			"GET /System/Info: HTTP 401 (expected 200): bad token (API key rejected; check the token)",
		},
		{StatusError{Method: "POST", Path: "/Items/1", StatusCode: 500, Expected: []int{200, 204}, Tries: 3}, "POST /Items/1: HTTP 500 (expected 200 or 204) (tried 3 times)"},
		{StatusError{Method: "GET", Path: "/x", StatusCode: 404, Body: "nope", Tries: 1}, "GET /x: HTTP 404: nope"},
		{StatusError{Method: "DELETE", Path: "/x", StatusCode: 403}, "DELETE /x: HTTP 403"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("error = %q, want %q", got, tc.want)
		}
	}

	wrapped := fmt.Errorf("reading the item: %w", &StatusError{Method: "GET", Path: "/x", StatusCode: http.StatusNotFound})
	if StatusCode(wrapped) != http.StatusNotFound || !IsNotFound(wrapped) {
		t.Errorf("a wrapped 404: status %d, not found %v", StatusCode(wrapped), IsNotFound(wrapped))
	}
	if other := errors.New("i/o timeout"); StatusCode(other) != 0 || IsNotFound(other) || StatusCode(nil) != 0 {
		t.Error("an error that is no status has one")
	}
	if IsNotFound(&StatusError{StatusCode: http.StatusGone}) {
		t.Error("a 410 reads as not found")
	}
}

// A preview is the start of a body, trimmed, cut on a whole character.
func TestPreview(t *testing.T) {
	t.Parallel()

	if got := Preview([]byte("  nope\n")); got != "nope" {
		t.Errorf("a short body = %q", got)
	}
	if got := Preview([]byte(strings.Repeat("x", PreviewLen))); got != strings.Repeat("x", PreviewLen) {
		t.Errorf("a body of exactly the length is cut: %d bytes", len(got))
	}
	if got := Preview([]byte(strings.Repeat("x", 1000))); got != strings.Repeat("x", PreviewLen)+"..." {
		t.Errorf("a long body = %d bytes ending %q", len(got), got[len(got)-5:])
	}
	// a character that straddles the cut is left out whole
	long := strings.Repeat("x", PreviewLen-1) + "éé" + strings.Repeat("y", 50)
	if got := Preview([]byte(long)); got != strings.Repeat("x", PreviewLen-1)+"..." {
		t.Errorf("a body cut inside a character ends %q", got[len(got)-6:])
	}
	if Preview(nil) != "" {
		t.Error("no body has a preview")
	}
}

// A redirect is refused, not followed: the request fails saying where it was
// sent and what to do, and nothing is asked of the address it was sent to.
func TestRefuseRedirects(t *testing.T) {
	t.Parallel()

	var followed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/zones/create" {
			http.Redirect(w, r, "/login?next=zones&token=s3cret", http.StatusFound)

			return
		}
		followed = append(followed, r.Method+" "+r.URL.Path)
	}))
	defer srv.Close()

	client := &http.Client{CheckRedirect: RefuseRedirects("set the server url to the address the console itself answers on")}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/zones/create", strings.NewReader("zone=example.test"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	want := "POST /api/zones/create was redirected to " + srv.URL + "/login?next=zones&token=REDACTED: set the server url to the address the console itself answers on"
	if err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("a redirected write = %v, want it refused with %q", err, want)
	}
	if refusal, ok := errors.AsType[*RedirectError](err); !ok || refusal.Method != http.MethodPost || refusal.Path != "/api/zones/create" {
		t.Errorf("the refusal is %#v, want a RedirectError of the request that was redirected", refusal)
	}
	if len(followed) != 0 {
		t.Errorf("the redirect was followed: %v", followed)
	}

	// with no advice to give it says where, and stops there
	bare := RefuseRedirects("")(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://kt:s3cret@elsewhere.invalid/page", http.NoBody), []*http.Request{httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://nas.invalid/api/me", http.NoBody)})
	if bare.Error() != "GET /api/me was redirected to https://kt:xxxxx@elsewhere.invalid/page" { //nolint:gosec // an invented password, hidden: the point of the test
		t.Errorf("with no advice the refusal reads %q", bare)
	}
}

// A page is told from an API's answer by how it starts, whatever it is
// labelled; a label of HTML alone is not enough, since a server's own
// one-word reply can carry it.
func TestIsWebPage(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		contentType, body string
		want              bool
	}{
		"a page":                               {"text/html; charset=utf-8", "<!DOCTYPE html><html><body>Sign in</body></html>", true},
		"a page with no doctype":               {"text/html", "\n  <HTML><head>", true},
		"a page a proxy labelled as JSON":      {"application/json", "<!doctype html>\n<title>502</title>", true},
		"a page with no label at all":          {"", "<html lang=\"en\">", true},
		"a fragment labelled as a page":        {"TEXT/HTML", "<div class=\"login\">", true},
		"a server's one word, labelled HTML":   {"text/html; charset=utf-8", "OK", false},
		"JSON":                                 {"application/json", `{"ok":true}`, false},
		"JSON labelled HTML":                   {"text/html", `{"ok":true}`, false},
		"a feed":                               {"text/xml", "<?xml version=\"1.0\"?><rss>", false},
		"nothing":                              {"text/html", "", false},
		"a page that starts far into the body": {"application/json", strings.Repeat(" ", 600) + "<html>", false},
	} {
		if got := IsWebPage(c.contentType, []byte(c.body)); got != c.want {
			t.Errorf("%s: IsWebPage(%q, %q) = %v, want %v", name, c.contentType, c.body[:min(len(c.body), 40)], got, c.want)
		}
	}
}
