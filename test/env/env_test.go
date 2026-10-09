package env

import (
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/go-kt/test/replayproxy"
)

// lockedLog is a log a test reads while the proxy may still be writing to it.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.String()
}

// fake is an environment holding just these variables.
func fake(vars map[string]string) Env {
	return Env{prefix: "APP", getenv: func(name string) string { return vars[name] }}
}

// The environment is read as the test script writes it, under the app's
// prefix: a data dir that is not set is "", not a relative path, so a test
// that lays files out skips rather than writing into the checkout; the proxy
// port is 18080 unless set, and a port that is no number is refused.
func TestEnvironment(t *testing.T) {
	t.Parallel()

	if got := New("APP_").Var("TEST_DATA"); got != "APP_TEST_DATA" {
		t.Errorf("Var = %q", got)
	}
	// New reads the process's own environment
	if got := New("GOKT_ENV_TEST_NOT_SET").Get("SERVER"); got != "" {
		t.Errorf("a variable nobody set = %q", got)
	}

	e := fake(nil)
	if got := e.DataDir("media"); got != "" {
		t.Errorf("DataDir with nothing set = %q, want \"\"", got)
	}
	if e.Configured() || e.Recording() || e.Verifying() || e.Container() != "" {
		t.Error("an empty environment is configured for nothing")
	}
	if port, err := e.ProxyPort(); err != nil || port != DefaultProxyPort {
		t.Errorf("ProxyPort() unset = %d, %v", port, err)
	}

	e = fake(map[string]string{
		"APP_SERVER": "http://localhost:8096", "APP_TOKEN": "t", "APP_TEST_DATA": "/x/testenv/app",
		"APP_TEST_PROXY_PORT": "18280", "APP_TEST_CONTAINER": "app-test", "OTHER_BACKEND": "emby",
	})
	if e.Server() != "http://localhost:8096" || e.Token() != "t" || e.Container() != "app-test" {
		t.Errorf("server %q, token %q, container %q", e.Server(), e.Token(), e.Container())
	}
	if got := e.DataDir(); got != "/x/testenv/app" {
		t.Errorf("DataDir() = %q", got)
	}
	if got := e.DataDir("media", "films"); got != filepath.FromSlash("/x/testenv/app/media/films") {
		t.Errorf("DataDir(media, films) = %q", got)
	}
	if port, err := e.ProxyPort(); err != nil || port != 18280 {
		t.Errorf("ProxyPort() = %d, %v", port, err)
	}
	// a server and a token are enough, unless the suite needs more: another
	// app's variable does not count
	if !e.Configured() || e.Configured("BACKEND") {
		t.Errorf("Configured() = %v, Configured(BACKEND) = %v", e.Configured(), e.Configured("BACKEND"))
	}
	if fake(map[string]string{"APP_SERVER": "http://x"}).Configured() {
		t.Error("Configured() with no token")
	}

	if _, err := fake(map[string]string{"APP_TEST_PROXY_PORT": "many"}).ProxyPort(); err == nil || !strings.Contains(err.Error(), `APP_TEST_PROXY_PORT="many"`) {
		t.Errorf("a port that is no number = %v", err)
	}
}

// The proxy replays unless the environment says to record or to verify:
// APP_TEST_RECORD set to anything fills in what is missing, set to all it
// refreshes every recording, and it wins over APP_TEST_VERIFY.
func TestMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		record, verify string
		want           replayproxy.Mode
	}{
		{"", "", replayproxy.Replay},
		{"1", "", replayproxy.Record},
		{"yes", "", replayproxy.Record},
		{"all", "", replayproxy.Rerecord},
		{"ALL", "", replayproxy.Rerecord},
		{"", "1", replayproxy.Verify},
		{"1", "1", replayproxy.Record},
		{"all", "1", replayproxy.Rerecord},
	} {
		e := fake(map[string]string{"APP_TEST_RECORD": tc.record, "APP_TEST_VERIFY": tc.verify})
		if got := e.Mode(); got != tc.want {
			t.Errorf("record=%q verify=%q runs in mode %d, want %d", tc.record, tc.verify, got, tc.want)
		}
		if e.Recording() != (tc.record != "") || e.Verifying() != (tc.verify != "") {
			t.Errorf("record=%q verify=%q: Recording() = %v, Verifying() = %v", tc.record, tc.verify, e.Recording(), e.Verifying())
		}
	}
}

// What a stopped proxy saw is a failure when replay missed a recording or an
// answer changed shape, and nothing to say otherwise; a proxy never started
// has nothing to say either.
func TestProxyReport(t *testing.T) {
	t.Parallel()

	if got := (*Proxy)(nil).Report(); got != "" {
		t.Errorf("a nil proxy reported %q", got)
	}
	if got := (&Proxy{}).Report(); got != "" {
		t.Errorf("a clean proxy reported %q", got)
	}
	p := &Proxy{recordVar: "APP_TEST_RECORD", Misses: []string{"GET api.example.org/v1/things/1"}}
	if got := p.Report(); !strings.Contains(got, "1 request(s) had no recording:\n  GET api.example.org/v1/things/1\n") || !strings.Contains(got, "APP_TEST_RECORD=1") {
		t.Errorf("a miss reported %q", got)
	}
	p = &Proxy{recordVar: "APP_TEST_RECORD", Drifts: []replayproxy.Drift{{Key: "GET api.example.org/v1/things/1", StatusWas: 200, StatusIs: 404}}}
	if got := p.Report(); !strings.Contains(got, "1 response(s) changed shape") || !strings.Contains(got, "status 200 -> 404") || !strings.Contains(got, "APP_TEST_RECORD=all") {
		t.Errorf("a drift reported %q", got)
	}
	// stopping what was never started is harmless
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
	if err := (*Proxy)(nil).Stop(); err != nil {
		t.Error(err)
	}
	// and outside a container there is nothing to check or to ask
	e := fake(nil)
	if network, err := e.CheckProxyReachable(t.Context(), DefaultProxyPort); network != "" || err != nil {
		t.Errorf("CheckProxyReachable outside a container = %q, %v", network, err)
	}
	if got := e.ContainerAddresses(t.Context()); got != nil {
		t.Errorf("ContainerAddresses outside a container = %v", got)
	}
}

// What a suite leaves unsaid about its proxy the environment decides: how it
// runs, where it listens, the authority it signs with and how a miss says to
// record; what the suite did say is kept.
func TestProxyOptions(t *testing.T) {
	t.Parallel()

	got, err := fake(nil).proxyOptions(t.Context(), replayproxy.Options{CassetteDir: "c", Mode: replayproxy.Record, IgnoreHosts: []string{"self.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != replayproxy.Replay || got.Addr != ":18080" || got.CACert != "" || got.CAKey != "" || got.CassetteDir != "c" {
		t.Errorf("with nothing set the options = %+v, want a replay on every interface at 18080 with an authority of its own", got)
	}
	if got.RecordHint != "record it with APP_TEST_RECORD=1, which records only what is missing" || !slices.Equal(got.IgnoreHosts, []string{"self.test"}) {
		t.Errorf("hint %q, ignored hosts %v", got.RecordHint, got.IgnoreHosts)
	}

	e := fake(map[string]string{"APP_TEST_RECORD": "all", "APP_TEST_PROXY_PORT": "18280", "APP_TEST_PROXY_CA": "/x/ca"})
	got, err = e.proxyOptions(t.Context(), replayproxy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != replayproxy.Rerecord || got.Addr != ":18280" || got.CACert != filepath.FromSlash("/x/ca/ca.pem") || got.CAKey != filepath.FromSlash("/x/ca/ca.key") {
		t.Errorf("the options = %+v, want a re-record on 18280 signing with the authority under /x/ca", got)
	}
	// an address, an authority and a hint the suite names are its own
	got, err = e.proxyOptions(t.Context(), replayproxy.Options{Addr: "127.0.0.1:1", CACert: "mine.pem", CAKey: "mine.key", RecordHint: "run make record"})
	if err != nil || got.Addr != "127.0.0.1:1" || got.CACert != "mine.pem" || got.CAKey != "mine.key" || got.RecordHint != "run make record" {
		t.Errorf("the suite's own options were not kept: %+v, %v", got, err)
	}
}

// The proxy a suite starts signs with the authority kept where the
// environment says, answers a client in the test's own process, and says
// what it missed once stopped.
func TestStartProxy(t *testing.T) {
	t.Parallel()

	cassettes, ca := t.TempDir(), filepath.Join(t.TempDir(), "ca")
	recording := `{"host":"api.example.org","interactions":[{"key":"GET api.example.org/v1/things/550","method":"GET","host":"api.example.org","path":"/v1/things/550","status":200,"body":"{\"id\":550}"}]}`
	if err := os.WriteFile(filepath.Join(cassettes, "api.example.org.json"), []byte(recording), 0o600); err != nil {
		t.Fatal(err)
	}
	e := fake(map[string]string{"APP_TEST_PROXY_CA": ca})

	// this machine only, on any free port, so the test can run beside a real
	// suite; what a suite leaves to the environment is TestProxyOptions
	var logged lockedLog
	p, err := e.StartProxy(t.Context(), replayproxy.Options{CassetteDir: cassettes, Addr: "127.0.0.1:0", Logger: log.New(&logged, "", 0), IgnoreHosts: []string{"self.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Addr(), "127.0.0.1:") {
		t.Errorf("the proxy listens on %s, want the address the suite named", p.Addr())
	}
	for _, name := range []string{"ca.pem", "ca.key"} {
		if _, err := os.Stat(filepath.Join(ca, name)); err != nil {
			t.Errorf("the authority was not kept where the environment says: %v", err)
		}
	}

	client := &http.Client{Transport: p.Transport()}
	fetch := func(target string) (int, string) {
		t.Helper()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(body)
	}
	if status, body := fetch("https://api.example.org/v1/things/550"); status != http.StatusOK || body != `{"id":550}` {
		t.Errorf("a recorded request = %d %s", status, body)
	}
	if status, _ := fetch("https://self.test/ping"); status != http.StatusNoContent {
		t.Errorf("a host the suite ignores = %d, want 204", status)
	}
	stop := p.Serve("feed.test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "a feed") }))
	if status, body := fetch("https://feed.test/show.xml"); status != http.StatusOK || body != "a feed" {
		t.Errorf("a host the test serves = %d %s", status, body)
	}
	stop()
	if status, _ := fetch("https://api.example.org/v1/things/1"); status != http.StatusBadGateway {
		t.Errorf("a request with no recording = %d, want 502", status)
	}

	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Misses, []string{"GET api.example.org/v1/things/1"}) {
		t.Errorf("misses = %v", p.Misses)
	}
	if got := p.Report(); !strings.Contains(got, "GET api.example.org/v1/things/1") || !strings.Contains(got, "APP_TEST_RECORD=1") {
		t.Errorf("the report = %q", got)
	}
	if !strings.Contains(logged.String(), "record it with APP_TEST_RECORD=1") {
		t.Errorf("the proxy's log does not say how to record: %q", logged.String())
	}
	if err := p.Stop(); err != nil {
		t.Errorf("stopping twice: %v", err)
	}

	// a port that is no number stops it before it starts
	if _, err := fake(map[string]string{"APP_TEST_PROXY_PORT": "many"}).StartProxy(t.Context(), replayproxy.Options{CassetteDir: cassettes}); err == nil {
		t.Error("StartProxy with a port that is no number started")
	}
	// and so does a proxy with nowhere to keep recordings
	if _, err := e.StartProxy(t.Context(), replayproxy.Options{}); err == nil {
		t.Error("StartProxy with no cassette dir started")
	}
}

// Files laid out for the server are world-writable, copied whole, and read
// back as they are; a tree or a file that changed is named without its root.
func TestFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	Mkdir(t, root, dir)
	WriteFile(t, filepath.Join(dir, "f.txt"), []byte("one"))
	for _, p := range []string{filepath.Join(root, "a"), dir} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o777 {
			t.Errorf("%s: %v %v, want a world-writable folder", p, info.Mode(), err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "f.txt")); err != nil || info.Mode().Perm() != 0o666 {
		t.Errorf("the file: %v %v, want world-writable", info.Mode(), err)
	}

	CopyTree(t, root, filepath.Join(root, "a"), filepath.Join(root, "c"))
	if raw, err := os.ReadFile(filepath.Join(root, "c", "b", "f.txt")); err != nil || string(raw) != "one" { //nolint:gosec // under the test's own directory
		t.Errorf("the copy = %q, %v", raw, err)
	}

	before := TreeOf(t, root, filepath.Join(root, "c"))
	if _, ok := before[filepath.Join(root, "a")+"/"]; !ok || len(before) != 4 {
		t.Errorf("tree = %v, want the folders ending in / and the one file, c skipped", before)
	}
	files := FilesUnder(t, filepath.Join(root, "a"))
	if len(files) != 1 {
		t.Errorf("files under a = %v", files)
	}
	StillOnDisk(t, root, files, "nothing")
	SameTree(t, root, before, TreeOf(t, root, filepath.Join(root, "c")))

	WriteFile(t, filepath.Join(dir, "f.txt"), []byte("two"))
	WriteFile(t, filepath.Join(dir, "g.txt"), []byte("new"))
	after := TreeOf(t, root, filepath.Join(root, "c"))
	if len(after) != len(before)+1 || string(after[filepath.Join(dir, "f.txt")]) != "two" {
		t.Errorf("after the writes the tree = %v", after)
	}
	if got, want := treeChanges(root, before, after), []string{"/a/b/f.txt changed or went", "/a/b/g.txt appeared"}; !slices.Equal(got, want) {
		t.Errorf("the changes = %v, want %v", got, want)
	}
	if got, want := filesChanged(root, files, "a rename"), []string{"/a/b/f.txt went or changed with a rename"}; !slices.Equal(got, want) {
		t.Errorf("the files changed = %v, want %v", got, want)
	}
	if err := os.Remove(filepath.Join(dir, "g.txt")); err != nil {
		t.Fatal(err)
	}
	if got, want := treeChanges(root, after, TreeOf(t, root, filepath.Join(root, "c"))), []string{"/a/b/g.txt changed or went"}; !slices.Equal(got, want) {
		t.Errorf("after a removal the changes = %v, want %v", got, want)
	}
}
