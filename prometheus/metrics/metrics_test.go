package metrics_test

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"

	"github.com/katbyte/go-kt/prometheus/metrics"
)

const (
	testName = "zzyzx-metrics"
	// testNamespace starts the name of each of a list's readings
	testNamespace = "zzyzx"
)

// readings is a tool's own collector: a reading with no labels of its own and one with a label.
type readings struct {
	up, doors *prometheus.Desc
}

func newReadings() readings {
	return readings{
		up:    prometheus.NewDesc("zzyzx_up", "Whether the thing answered (1) or not (0).", nil, nil),
		doors: prometheus.NewDesc("zzyzx_doors", "Doors, by whether they are open or shut.", []string{"state"}, nil),
	}
}

func (r readings) Describe(ch chan<- *prometheus.Desc) {
	ch <- r.up
	ch <- r.doors
}

func (r readings) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(r.up, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(r.doors, prometheus.GaugeValue, 3, "open")
}

// written is the page as text.
func written(t *testing.T, p *metrics.Page) string {
	t.Helper()

	var b strings.Builder
	if err := p.Write(&b); err != nil {
		t.Fatalf("Write: %v", err)
	}

	return b.String()
}

// samples is the lines of a page that are readings, not the words about them.
func samples(text string) []string {
	var out []string
	for line := range strings.Lines(text) {
		if !strings.HasPrefix(line, "#") {
			out = append(out, strings.TrimSpace(line))
		}
	}

	return out
}

// The page holds the tool's readings and the one that says what wrote it, and nothing of the Go runtime's or the process's.
func TestThePageHoldsOnlyTheToolsReadings(t *testing.T) {
	t.Parallel()

	p, err := metrics.New(metrics.Options{Name: testName}, newReadings())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := samples(written(t, p))
	if len(got) != 3 || got[0] != `zzyzx_doors{state="open"} 3` || !strings.HasPrefix(got[1], "zzyzx_metrics_build_info{") || got[2] != "zzyzx_up 1" {
		t.Errorf("the page holds %q, want the two readings and the build's and no more", got)
	}

	for _, label := range []string{`version="`, `commit="`, `goversion="go`} {
		if !strings.Contains(got[1], label) {
			t.Errorf("the build's reading is %q, want %s in it", got[1], label)
		}
	}

	problems, err := promlint.New(strings.NewReader(written(t, p))).Lint()
	if err != nil {
		t.Fatalf("checking the page: %v", err)
	}

	for _, problem := range problems {
		t.Errorf("%s: %s", problem.Metric, problem.Text)
	}
}

// Whoever runs the tool says what is on every reading, the build's included: a store tells one source from another by it.
func TestLabelsAreOnEveryReading(t *testing.T) {
	t.Parallel()

	p, err := metrics.New(metrics.Options{Name: testName, Labels: map[string]string{"hostname": "ntp", "site": "home"}}, newReadings())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := samples(written(t, p))
	if len(got) != 3 {
		t.Fatalf("the page holds %q, want three readings", got)
	}

	for _, line := range got {
		if !strings.Contains(line, `hostname="ntp"`) || !strings.Contains(line, `site="home"`) {
			t.Errorf("%s has not both labels on it", line)
		}
	}

	if want := `zzyzx_doors{hostname="ntp",site="home",state="open"} 3`; got[0] != want {
		t.Errorf("a reading with a label of its own = %s, want %s", got[0], want)
	}
}

// A page that cannot be made is said when the tool starts, with what is wrong.
func TestNewRefusesWhatAStoreWouldNotTake(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		options metrics.Options
		want    string
	}{
		"no name":                                    {metrics.Options{}, "the page needs the tool's name"},
		"a name that makes no reading's name":        {metrics.Options{Name: "zzyzx.exporter"}, `"zzyzx.exporter_build_info" cannot name the reading`},
		"a build reading not named as such readings": {metrics.Options{Name: testName, BuildInfo: "zzyzx_build"}, `"zzyzx_build" cannot name the reading`},
		"a build reading a store would not take":     {metrics.Options{Name: testName, BuildInfo: "zz-yzx_build_info"}, `"zz-yzx_build_info" cannot name the reading`},
		"a label named as a reading's own is":        {metrics.Options{Name: testName, Labels: map[string]string{"hostname": "ntp", "state": "x"}}, `the label "state" is one a reading on the page already has`},
		"a label named as the build's own is":        {metrics.Options{Name: testName, Labels: map[string]string{"version": "x"}}, `the label "version" is one a reading on the page already has`},
		"a label with a dash in its name":            {metrics.Options{Name: testName, Labels: map[string]string{"host-name": "ntp"}}, "host-name"},
		"a label a store keeps for itself":           {metrics.Options{Name: testName, Labels: map[string]string{"__name__": "x"}}, "__name__"},
		"a label with nothing to say under it":       {metrics.Options{Name: testName, Labels: map[string]string{"hostname": ""}}, "has no value"},
	} {
		if _, err := metrics.New(c.options, newReadings()); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: New = %v, want an error saying %q", name, err, c.want)
		}
	}
}

// A collector that cannot be put on the page with no labels at all is not blamed on a label.
func TestNewSaysWhenTheReadingsThemselvesAreWrong(t *testing.T) {
	t.Parallel()

	_, err := metrics.New(metrics.Options{Name: testName, Labels: map[string]string{"hostname": "ntp"}}, newReadings(), newReadings())
	if err == nil || !strings.Contains(err.Error(), "putting the readings on the page") || strings.Contains(err.Error(), "hostname") {
		t.Errorf("New with the same readings twice = %v, want an error about the readings and not about a label", err)
	}
}

func TestParseLabels(t *testing.T) {
	t.Parallel()

	for setting, want := range map[string]map[string]string{
		"":                             nil,
		"   ":                          nil,
		"hostname=ntp":                 {"hostname": "ntp"},
		"hostname=ntp,job=leontp":      {"hostname": "ntp", "job": "leontp"},
		" hostname = ntp , site=home ": {"hostname": "ntp", "site": "home"},
		"where=rack 2, shelf=a=b":      {"where": "rack 2", "shelf": "a=b"},
	} {
		got, err := metrics.ParseLabels(setting)
		if err != nil {
			t.Errorf("ParseLabels(%q): %v", setting, err)
			continue
		}

		if !maps.Equal(got, want) {
			t.Errorf("ParseLabels(%q) = %v, want %v", setting, got, want)
		}
	}

	for setting, want := range map[string]string{
		"hostname":              "not written name=value",
		"hostname=ntp,":         "not written name=value",
		"hostname=ntp,site":     `"site"`,
		"hostname=a,hostname=b": "given twice",
		"hostname=":             "has no value",
		"=ntp":                  "cannot be a label's name",
		"host name=ntp":         "host name",
		"1st=ntp":               "1st",
		"__address__=ntp":       "two underscores",
	} {
		if got, err := metrics.ParseLabels(setting); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseLabels(%q) = %v, %v, want an error saying %q", setting, got, err, want)
		}
	}
}

// The page, the health probe and a root that says where they are, and nothing else.
func TestRoutes(t *testing.T) {
	t.Parallel()

	p, err := metrics.New(metrics.Options{Name: testName, Labels: map[string]string{"hostname": "ntp"}}, newReadings())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := p.Handler()

	for _, c := range []struct {
		method, path string
		status       int
		want         string
	}{
		{http.MethodGet, metrics.Path, http.StatusOK, `zzyzx_up{hostname="ntp"} 1`},
		{http.MethodGet, metrics.HealthPath, http.StatusOK, "ok\n"},
		{http.MethodGet, "/", http.StatusOK, testName + " "},
		{http.MethodGet, "/", http.StatusOK, metrics.Path + "\n" + metrics.HealthPath + "\n"},
		{http.MethodGet, "/nothing-here", http.StatusNotFound, ""},
		{http.MethodPost, metrics.Path, http.StatusMethodNotAllowed, ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), c.method, c.path, http.NoBody))

		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s %s = %d %q, want %d with %q in it", c.method, c.path, rec.Code, rec.Body.String(), c.status, c.want)
		}
	}
}

func TestServeUntilStopped(t *testing.T) {
	t.Parallel()

	// a port nothing else has: taken, noted and given back
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	p, err := metrics.New(metrics.Options{Name: testName}, newReadings())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- metrics.Serve(ctx, addr, p.Handler()) }()

	var body string
	for range 50 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+metrics.HealthPath, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		b := make([]byte, 8)
		n, _ := res.Body.Read(b)
		_ = res.Body.Close()
		body = string(b[:n])

		break
	}
	if strings.TrimSpace(body) != "ok" {
		t.Errorf("%s over a real port = %q, want ok", metrics.HealthPath, body)
	}

	cancel()
	if err := <-served; err != nil {
		t.Errorf("Serve stopped with %v, want nil", err)
	}
}

func TestServeSaysWhenItCannotListen(t *testing.T) {
	t.Parallel()

	p, err := metrics.New(metrics.Options{Name: testName})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = metrics.Serve(t.Context(), "256.0.0.1:1", p.Handler())
	if err == nil || !strings.Contains(err.Error(), "256.0.0.1:1") {
		t.Errorf("Serve on an address that does not exist = %v, want an error naming it", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("Serve failed for being stopped, not for the address: %v", err)
	}
}

// The reading that says which build wrote the page is named for the tool, as Prometheus's own tools name theirs; a tool can name it outright to keep a
// name a dashboard already uses.
func TestTheBuildReadingIsNamedForTheTool(t *testing.T) {
	t.Parallel()

	for want, o := range map[string]metrics.Options{
		"zzyzx_exporter_build_info": {Name: "zzyzx-exporter"},
		"zzyzx_metrics_build_info":  {Name: "zzyzx-exporter", BuildInfo: "zzyzx_metrics_build_info"},
	} {
		p, err := metrics.New(o, newReadings())
		if err != nil {
			t.Fatalf("New(%+v): %v", o, err)
		}

		if text := written(t, p); !strings.Contains(text, "\n"+want+"{") {
			t.Errorf("the page of %+v is\n%s\nwant the build's reading named %s", o, text, want)
		}
	}
}
