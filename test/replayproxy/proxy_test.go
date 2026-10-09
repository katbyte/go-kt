package replayproxy

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// loopback is where a proxy under test listens: any free port, this
	// machine only
	loopback    = "127.0.0.1:0"
	contentJSON = "application/json"
	apiHost     = "api.example.org"
	// recordedAnswer is what a cassette holds before a re-record replaces it
	recordedAnswer = `{"runtime":117}`
)

// quiet is a logger for a proxy whose log no test reads.
func quiet() *log.Logger { return log.New(io.Discard, "", 0) }

// logBuffer is a log a test reads while the proxy may still be writing to it.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.String()
}

// clientThrough returns an http.Client that reaches the given proxy and
// accepts its minted certificates, the way a container told to trust them
// does.
func clientThrough(t *testing.T, p *Proxy) *http.Client {
	t.Helper()

	proxyURL, err := url.Parse("http://" + p.Addr())
	if err != nil {
		t.Fatal(err)
	}

	return &http.Client{
		// an answer whose body never ends fails a test rather than stalling it
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the proxy mints its own certs on purpose
		},
	}
}

// fetch sends one request through the proxy, asking for every encoding the
// way a server's own http client does.
func fetch(t *testing.T, client *http.Client, method, target, body string) (status int, answer string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip, compress, deflate, br")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return resp.StatusCode, string(raw)
}

// get fetches a url through the proxy.
func get(t *testing.T, p *Proxy, target string) (status int, body string) {
	t.Helper()

	return fetch(t, clientThrough(t, p), http.MethodGet, target, "")
}

func writeCassette(t *testing.T, dir string, c cassette) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hostFile(c.Host)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// oneRecording is a cassette dir holding one answer, for GET host+path.
func oneRecording(t *testing.T, host, path, body string) string {
	t.Helper()

	dir := t.TempDir()
	writeCassette(t, dir, cassette{Host: host, Interactions: []*interaction{{
		Key: "GET " + host + path, Method: http.MethodGet, Host: host, Path: path,
		Status: http.StatusOK, Headers: map[string]string{"Content-Type": contentJSON}, Body: body,
	}}})

	return dir
}

// recorded reads a cassette back, by path.
func recorded(t *testing.T, dir, host string) map[string][]*interaction {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, hostFile(host))) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	var c cassette
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	out := map[string][]*interaction{}
	for _, i := range c.Interactions {
		out[i.Path] = append(out[i.Path], i)
	}

	return out
}

// A recorded request must come back through the CONNECT tunnel byte for byte.
func TestReplayServesRecording(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCassette(t, dir, cassette{
		Host: apiHost,
		Interactions: []*interaction{{
			Key:     "GET " + apiHost + "/authors?name=Zzyzx",
			Method:  http.MethodGet,
			Host:    apiHost,
			Path:    "/authors",
			Query:   "name=Zzyzx",
			Status:  http.StatusOK,
			Headers: map[string]string{"Content-Type": contentJSON},
			Body:    `[{"id":"A1","name":"Zzyzx Author"}]`,
		}},
	})

	p, err := New(Options{CassetteDir: dir, Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+apiHost+"/authors?name=Zzyzx", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != contentJSON {
		t.Errorf("content-type = %q", ct)
	}
	if string(body) != `[{"id":"A1","name":"Zzyzx Author"}]` {
		t.Errorf("body = %q", body)
	}
	if misses := p.Misses(); len(misses) != 0 {
		t.Errorf("misses = %v, want none", misses)
	}
}

// A client in the same process reaches the recordings through Transport,
// trusting the proxy's own certificate rather than skipping the check.
func TestTransportTrustsTheProxy(t *testing.T) {
	t.Parallel()

	p, err := New(Options{CassetteDir: oneRecording(t, apiHost, "/v1/things/550", `{"id":550}`), Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	if status, body := fetch(t, &http.Client{Transport: p.Transport()}, http.MethodGet, "https://"+apiHost+"/v1/things/550", ""); status != http.StatusOK || body != `{"id":550}` {
		t.Errorf("%d %s", status, body)
	}
}

// A request with no recording must fail loudly rather than look like an empty
// but successful response, which would let a test pass for the wrong reason;
// the log says how the suite records one when it was told. The refusal is
// read to its end, through a tunnel: it once went out with no length on a
// tunnel held open, and whoever read it waited a minute for the last byte.
func TestReplayMissIsLoud(t *testing.T) {
	t.Parallel()

	for _, hint := range []string{"", "record it with APP_TEST_RECORD=1"} {
		var logged logBuffer
		p, err := New(Options{CassetteDir: t.TempDir(), Addr: loopback, Logger: log.New(&logged, "", 0), RecordHint: hint})
		if err != nil {
			t.Fatal(err)
		}

		status, body := get(t, p, "https://"+apiHost+"/1.0/catalog/products?keywords=nothing")
		if status != http.StatusBadGateway || !strings.Contains(body, "no recording for GET "+apiHost+"/1.0/catalog/products") {
			t.Errorf("a miss = %d %q, want a 502 naming the request", status, body)
		}
		misses := p.Misses()
		if len(misses) != 1 || misses[0] != "GET "+apiHost+"/1.0/catalog/products?keywords=nothing" {
			t.Errorf("misses = %v, want the one request", misses)
		}
		want := "REPLAY MISS GET " + apiHost + "/1.0/catalog/products?keywords=nothing\n"
		if hint != "" {
			want = "REPLAY MISS GET " + apiHost + "/1.0/catalog/products?keywords=nothing (" + hint + ")\n"
		}
		if got := logged.String(); !strings.Contains(got, want) {
			t.Errorf("with the hint %q the log = %q, want %q in it", hint, got, want)
		}
		_ = p.Close()
	}
}

// A host a test serves itself answers from its handler, over plain http and
// through a tunnel alike, and is never a miss; once stopped it is a host like
// any other.
func TestServeAnswersALocalHost(t *testing.T) {
	t.Parallel()

	p, err := New(Options{CassetteDir: t.TempDir(), Addr: loopback, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	stop := p.Serve("Feed.Test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/show.xml" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "served /show.xml")
	}))

	for _, u := range []string{"http://feed.test/show.xml", "http://feed.test:8080/show.xml", "https://feed.test/show.xml"} {
		if status, body := get(t, p, u); status != http.StatusOK || body != "served /show.xml" {
			t.Errorf("%s = %d %q", u, status, body)
		}
	}
	if status, _ := get(t, p, "https://feed.test/other.xml"); status != http.StatusNotFound {
		t.Errorf("what the handler does not have = %d, want its own 404", status)
	}
	if misses := p.Misses(); len(misses) != 0 {
		t.Errorf("misses = %v, want none", misses)
	}

	stop()
	if status, _ := get(t, p, "http://feed.test/show.xml"); status != http.StatusBadGateway {
		t.Errorf("after stop: status = %d, want 502", status)
	}
}

// A host the proxy is told to ignore - the server reaching itself - is
// answered with nothing, by name or by address, with a port or without: it is
// no miss, it is never fetched, and a recording run writes nothing for it.
func TestIgnoredHostsAreAnsweredAndNeverRecorded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p, err := New(Options{Mode: Record, CassetteDir: dir, Addr: loopback, Logger: quiet(), IgnoreHosts: []string{"192.0.2.7", "Self.Test"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://192.0.2.7:8096/System/Ping", "http://192.0.2.7/", "https://self.test/emby/System/Info/Public"} {
		if status, body := get(t, p, u); status != http.StatusNoContent || body != "" {
			t.Errorf("%s = %d %q, want an empty 204", u, status, body)
		}
	}
	if misses := p.Misses(); len(misses) != 0 {
		t.Errorf("misses = %v, want none", misses)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Errorf("an ignored host was recorded: %v", files)
	}
}

// Query parameter order must not matter, or a cassette would miss on a request
// that is the same in every way that affects the response.
func TestKeyIsOrderIndependent(t *testing.T) {
	t.Parallel()

	a := key("get", "API.Example.org", "/1.0/catalog", url.Values{"b": {"2"}, "a": {"1"}})
	b := key(http.MethodGet, apiHost, "/1.0/catalog", url.Values{"a": {"1"}, "b": {"2"}})
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}

// A body over the cap is elided rather than committed, and a binary one is
// stored base64 so the cassette stays valid JSON.
func TestBodyStorage(t *testing.T) {
	t.Parallel()

	var big interaction
	big.setBody(make([]byte, maxBodyBytes+1), "text/xml")
	if !big.Elided || big.ElidedSize != maxBodyBytes+1 {
		t.Errorf("oversized body not elided: %+v", big)
	}

	// audio is elided however small, so no episode audio reaches the repository
	var audio interaction
	audio.setBody([]byte("ID3short"), "audio/mpeg")
	if !audio.Elided || string(audio.bytes()) != elidedBody {
		t.Errorf("audio body not elided: %+v", audio)
	}

	// an elided image still has to replay as something decodable, or the
	// server will not accept it as a cover
	var jpeg interaction
	jpeg.setBody(make([]byte, 500<<10), "image/jpeg")
	if !jpeg.Elided {
		t.Fatalf("image not elided: %+v", jpeg)
	}
	if got := jpeg.bytes(); len(got) < 4 || got[0] != 0xff || got[1] != 0xd8 {
		t.Errorf("elided image did not replay as a JPEG: %v", got[:min(4, len(got))])
	}

	var png interaction
	png.setBody(make([]byte, 10), "image/png")
	if got := png.bytes(); len(got) < 4 || got[1] != 'P' {
		t.Errorf("elided png did not replay as a PNG: %v", got[:min(4, len(got))])
	}

	// but a large RSS feed must survive intact or it will not parse on replay
	var rss interaction
	rss.setBody(bytes.Repeat([]byte("x"), 900<<10), "application/rss+xml")
	if rss.Elided || len(rss.Body) != 900<<10 {
		t.Errorf("large feed was not kept intact: elided=%v len=%d", rss.Elided, len(rss.Body))
	}

	// bytes that are not text are kept as base64 (a server needs them back
	// intact): a Latin-1 list under octet-stream too. A blob under that type
	// holding NUL bytes is a plugin or an installer, elided like media
	var binary interaction
	binary.setBody([]byte("Studio Ol\xe9\n"), "application/octet-stream")
	if binary.BodyBase64 == "" || binary.Body != "" || binary.Elided || string(binary.bytes()) != "Studio Ol\xe9\n" {
		t.Errorf("binary body not base64: %+v", binary)
	}
	var blob interaction
	blob.setBody([]byte{0x4d, 0x5a, 0x90, 0x00}, "application/octet-stream")
	if !blob.Elided || blob.BodyBase64 != "" {
		t.Errorf("an octet-stream blob was kept: %+v", blob)
	}
	var installer interaction
	installer.setBody([]byte("MZ"), "application/x-msdownload")
	if !installer.Elided {
		t.Errorf("an installer was kept: %+v", installer)
	}

	var text interaction
	text.setBody([]byte(`{"ok":true}`), contentJSON)
	if text.Body != `{"ok":true}` || text.BodyBase64 != "" {
		t.Errorf("text body not stored as text: %+v", text)
	}
}

// The headers that change on every answer, and the ones that say which
// machine recorded, stay out of a cassette; the rest are kept.
func TestKeepHeadersDropsWhatChangesEveryTime(t *testing.T) {
	t.Parallel()

	got := keepHeaders(http.Header{
		"Content-Type":          {contentJSON},
		"Date":                  {"Mon, 01 Jan 2024 00:00:00 GMT"},
		"Etag":                  {`"abc"`},
		"Via":                   {"1.1 edge"},
		"X-Ratelimit-Remaining": {"39"},
		"X-Memc-Key":            {"k"},
		"Cf-Ray":                {"1-YVR"},
		"Server":                {"openresty"},
		"Empty":                 {},
	})
	if want := map[string]string{"Content-Type": contentJSON, "Server": "openresty"}; !maps.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
}

// A redacted parameter is neither keyed on nor stored, so an operator's own
// key replays against a cassette recorded with someone else's.
func TestRedactQueryKeysWithoutTheSecret(t *testing.T) {
	t.Parallel()

	p, err := New(Options{CassetteDir: oneRecording(t, apiHost, "/v1/things/348", recordedAnswer), Addr: loopback, RedactQuery: []string{"api_key"}, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	if status, body := get(t, p, "https://"+apiHost+"/v1/things/348?api_key=someone-elses"); status != http.StatusOK || body != recordedAnswer {
		t.Errorf("redacted request missed the cassette: %d %s", status, body)
	}
	if m := p.Misses(); len(m) != 0 {
		t.Errorf("misses = %v", m)
	}
	// and a miss names the request without the key
	if status, _ := get(t, p, "https://"+apiHost+"/v1/things/1?api_key=someone-elses&page=2"); status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", status)
	}
	if m := p.Misses(); !slices.Equal(m, []string{"GET " + apiHost + "/v1/things/1?page=2"}) {
		t.Errorf("misses = %v, want the request without its key", m)
	}
}

// A login answer must not reach the cassette with its token in it, and
// everything around the token must survive byte for byte.
func TestRedactJSONFields(t *testing.T) {
	t.Parallel()

	const login = `{"status":"success","data":{"token":"eyJhbGciOiJSUzI1NiJ9.payload.sig"}}`
	got, secrets := redactJSONFields(login, []string{"token"})
	if strings.Contains(got, "eyJ") {
		t.Errorf("the token survived: %s", got)
	}
	if want := `{"status":"success","data":{"token":"` + redactedValue + `"}}`; got != want {
		t.Errorf("redacted = %s, want %s", got, want)
	}
	if !slices.Equal(secrets, []string{"eyJhbGciOiJSUzI1NiJ9.payload.sig"}) {
		t.Errorf("secrets = %v, want the token, so a header carrying it can be scrubbed too", secrets)
	}

	// the same value in a header goes too: a new request token can come back
	// in the body and again in a link
	i := &interaction{Body: `{"request_token":"abc123"}`, Headers: map[string]string{"Authentication-Callback": "https://www.example.org/authenticate/abc123", "Server": "openresty"}}
	i.redact([]string{"request_token"})
	if strings.Contains(i.Body, "abc123") || strings.Contains(i.Headers["Authentication-Callback"], "abc123") || i.Headers["Server"] != "openresty" {
		t.Errorf("redact = %+v", i)
	}
	if !i.redacted() || (&interaction{Body: `{"request_token":"abc123"}`}).redacted() {
		t.Error("a recording is told to be redacted by the value it holds, and only by that")
	}
	// a cassette recorded before this package was shared holds this wording,
	// and must still be told to be redacted
	if redactedValue != "redacted by the provider proxy" {
		t.Errorf("the redacted value is now %q: recordings already made would no longer be recognised", redactedValue)
	}

	// a value carrying an escaped quote, a field that is not named, a body
	// that is not JSON, and no fields at all
	for _, tc := range []struct {
		name, body, want string
		fields           []string
	}{
		{"escaped quote", `{"token":"a\"b","keep":"x"}`, `{"token":"` + redactedValue + `","keep":"x"}`, []string{"token"}},
		{"spacing kept", `{"token" : "abc"}`, `{"token" : "` + redactedValue + `"}`, []string{"token"}},
		{"other fields left alone", `{"apikey":"abc"}`, `{"apikey":"abc"}`, []string{"token"}},
		{"not json", `plain text with token: abc`, `plain text with token: abc`, []string{"token"}},
		{"no fields named", `{"token":"abc"}`, `{"token":"abc"}`, nil},
	} {
		if got, _ := redactJSONFields(tc.body, tc.fields); got != tc.want {
			t.Errorf("%s: redacted = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// A newline in a host or a url cannot start a log line of its own.
func TestLogSafe(t *testing.T) {
	t.Parallel()

	if got := logSafe("GET host/a\r\nREPLAY MISS forged"); got != "GET host/aREPLAY MISS forged" {
		t.Errorf("logSafe = %q", got)
	}
}

// An authority kept on disk is minted and written the first time and loaded
// after that, so a container started before the proxy, with the certificate
// mounted into it, trusts every proxy a later run starts.
func TestAuthorityOnDiskIsMintedOnceAndReused(t *testing.T) {
	t.Parallel()

	ca := filepath.Join(t.TempDir(), "ca")
	certFile, keyFile := filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	cassettes := oneRecording(t, apiHost, "/v1/things/550", `{"id":550}`)
	opts := Options{CassetteDir: cassettes, Addr: loopback, Logger: quiet(), CACert: certFile, CAKey: keyFile}

	first, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	minted := first.ca
	_ = first.Close()
	if info, err := os.Stat(keyFile); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Errorf("the key file: %v, mode %v, want it written for its owner alone", err, info.Mode().Perm())
	}

	second, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if !second.ca.Equal(minted) {
		t.Error("a second proxy minted an authority of its own, want the one on disk")
	}

	// what the container does: trust the certificate file, nothing else
	pemBytes, err := os.ReadFile(certFile) //nolint:gosec // a path this test named
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("the certificate file holds no certificate")
	}
	proxyURL, err := url.Parse("http://" + second.Addr())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	if status, body := fetch(t, client, http.MethodGet, "https://"+apiHost+"/v1/things/550", ""); status != http.StatusOK || body != `{"id":550}` {
		t.Errorf("through a client trusting the file: %d %s", status, body)
	}

	// files that are not an authority are refused, saying which
	for name, tc := range map[string]struct{ cert, key, want string }{
		"no certificate": {"not a certificate", "", "holds no CERTIFICATE block"},
		"no key":         {string(pemBytes), "not a key", "holds no PEM block"},
		"another kind":   {string(pemBytes), "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n", "want EC PRIVATE KEY"},
	} {
		dir := t.TempDir()
		bad := Options{CassetteDir: cassettes, Addr: loopback, Logger: quiet(), CACert: filepath.Join(dir, "ca.pem"), CAKey: filepath.Join(dir, "ca.key")}
		if err := os.WriteFile(bad.CACert, []byte(tc.cert), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bad.CAKey, []byte(tc.key), 0o600); err != nil {
			t.Fatal(err)
		}
		if p, err := New(bad); err == nil || !strings.Contains(err.Error(), tc.want) {
			if p != nil {
				_ = p.Close()
			}
			t.Errorf("%s: New = %v, want an error saying %q", name, err, tc.want)
		}
	}
}

// A proxy needs somewhere to keep its recordings.
func TestNewNeedsACassetteDir(t *testing.T) {
	t.Parallel()

	if p, err := New(Options{}); err == nil {
		_ = p.Close()
		t.Error("New with no cassette dir started a proxy")
	}
}
