package metricstest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// page is a page as a tool writes it: a reading with labels, a total, and the line saying which build wrote it.
const page = `# HELP zzyzx_door_openings_total How many times each door has been opened.
# TYPE zzyzx_door_openings_total counter
zzyzx_door_openings_total{door="left",shelf="top"} 12
zzyzx_door_openings_total{door="right",shelf="top"} 3
# HELP zzyzx_doors Doors, by state.
# TYPE zzyzx_doors gauge
zzyzx_doors{shelf="top",state="open"} 2
zzyzx_doors{shelf="top",state="say \"ah\""} 1
# HELP zzyzx_metrics_build_info Always 1, with the version of the tool that wrote the page.
# TYPE zzyzx_metrics_build_info gauge
zzyzx_metrics_build_info{commit="",goversion="go1.27.2",version="dev"} 1
`

// saved is the page as Saved keeps it: without the build's reading.
var saved = strings.Join(slices.Collect(strings.Lines(page))[:8], "")

// A page that is what was saved passes, whichever build wrote it.
func TestAPageThatIsWhatWasSavedPasses(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "page.txt")
	if err := os.WriteFile(file, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := compare(page, file); err != nil {
		t.Errorf("compare = %v, want nil", err)
	}

	if err := compare(strings.ReplaceAll(page, "go1.27.2", "go1.28.0"), file); err != nil {
		t.Errorf("compare of a page from another build = %v, want nil", err)
	}

	if err := compare(strings.ReplaceAll(page, "zzyzx_metrics_build_info", "zzyzx_exporter_build_info"), file); err != nil {
		t.Errorf("compare of a page whose build reading is named for an exporter = %v, want nil", err)
	}

	Saved(t, page, file)
}

// A changed page fails with what changed: the lines saved that are gone, and the lines that are new.
func TestAChangedPageSaysWhatChanged(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "page.txt")
	if err := os.WriteFile(file, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}

	err := compare(strings.Replace(page, `door="right",shelf="top"} 3`, `door="back",shelf="top"} 3`, 1), file)
	if err == nil {
		t.Fatal("compare of a changed page = nil, want what changed")
	}

	for _, want := range []string{"not the one saved in " + file, "go test -update", `- zzyzx_door_openings_total{door="right",shelf="top"} 3`, `+ zzyzx_door_openings_total{door="back",shelf="top"} 3`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("compare = %v, want %q in it", err, want)
		}
	}

	if err := compare(strings.Replace(page, "zzyzx_doors{shelf=\"top\",state=\"open\"} 2\n", "", 1)+"zzyzx_doors{shelf=\"top\",state=\"open\"} 2\n", file); err == nil || !strings.Contains(err.Error(), "in another order") {
		t.Errorf("compare of the same lines in another order = %v, want it to say so", err)
	}
}

// With nothing saved, the test says how to save it; -update writes the page, and it then passes.
func TestUpdateWritesThePage(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "testdata", "page.txt")
	if err := compare(page, file); err == nil || !strings.Contains(err.Error(), "no page is saved in "+file) {
		t.Errorf("compare with nothing saved = %v, want it to say so", err)
	}

	if err := save(page, file); err != nil {
		t.Fatalf("save = %v", err)
	}

	if err := compare(page, file); err != nil {
		t.Errorf("compare once saved = %v, want nil", err)
	}

	got, err := os.ReadFile(file) //nolint:gosec // G304: a file of the test's own
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != saved {
		t.Errorf("the page written is\n%s\nwant it without the build's reading\n%s", got, saved)
	}
}

// The naming check finds what Prometheus's rules would, and lets pass only what is allowed by its reading and its words.
func TestLint(t *testing.T) {
	t.Parallel()

	bad := page + "# HELP zzyzx_link_rate_gigabits How fast the link is.\n# TYPE zzyzx_link_rate_gigabits gauge\nzzyzx_link_rate_gigabits 12\n"

	problems, err := complaints(page, nil)
	if err != nil || len(problems) != 0 {
		t.Errorf("lint of a good page = %q, %v, want nothing", problems, err)
	}

	problems, err = complaints(bad, nil)
	if err != nil || len(problems) != 1 || !strings.HasPrefix(problems[0], `zzyzx_link_rate_gigabits: use base unit "bytes" instead of "gigabits"`) {
		t.Errorf("lint of a page with gigabits = %q, %v, want the one complaint", problems, err)
	}

	for _, a := range []Allowed{{Reading: "zzyzx_link_rate_gigabits", Problem: `instead of "gigabits"`}, {Reading: "zzyzx_link_rate_gigabits"}, {Reading: "_gigabits", Problem: `instead of "gigabits"`}} {
		if problems, err := complaints(bad, []Allowed{a}); err != nil || len(problems) != 0 {
			t.Errorf("lint allowing %+v = %q, %v, want nothing", a, problems, err)
		}
	}

	for _, a := range []Allowed{{Reading: "zzyzx_link_rate", Problem: `instead of "gigabits"`}, {Reading: "_bits", Problem: `instead of "gigabits"`}, {Reading: "zzyzx_link_rate_gigabits", Problem: "counter"}} {
		if problems, err := complaints(bad, []Allowed{a}); err != nil || len(problems) != 1 {
			t.Errorf("lint allowing %+v = %q, %v, want the complaint still made", a, problems, err)
		}
	}

	if _, err := complaints("zzyzx_doors{state=\"open\" 2\n", nil); err == nil {
		t.Error("lint of a page that cannot be read = nil, want an error")
	}

	Lint(t, bad, Allowed{Reading: "zzyzx_link_rate_gigabits", Problem: `instead of "gigabits"`})
}

// A reading is found by its name and some of its labels, read as Prometheus reads them, quotes and all.
func TestReadingsAndOne(t *testing.T) {
	t.Parallel()

	got := Readings(t, page, "zzyzx_doors")
	if len(got) != 2 || got[1].Labels["state"] != `say "ah"` || got[1].Value != 1 || got[0].Labels["shelf"] != "top" {
		t.Errorf("Readings = %+v, want both doors readings with their labels", got)
	}

	if got := Readings(t, page, "zzyzx_nothing"); len(got) != 0 {
		t.Errorf("Readings of a name not on the page = %+v, want none", got)
	}

	for _, c := range []struct {
		labels map[string]string
		value  float64
		one    bool
	}{
		{map[string]string{"door": "left"}, 12, true},
		{map[string]string{"door": "right", "shelf": "top"}, 3, true},
		{map[string]string{"shelf": "top"}, 0, false},
		{map[string]string{"door": "back"}, 0, false},
	} {
		if value, one := One(t, page, "zzyzx_door_openings_total", c.labels); value != c.value || one != c.one {
			t.Errorf("One(%v) = %v, %v, want %v, %v", c.labels, value, one, c.value, c.one)
		}
	}

	if _, err := parse("zzyzx_doors{state=\"open\" 2\n", "zzyzx_doors"); err == nil {
		t.Error("parse of a page that cannot be read = nil, want an error")
	}
}

// Get reads what a serving tool answers, and an answer other than 200 OK is an error with the answer in it.
func TestGet(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			http.Error(w, "the readings could not be gathered", http.StatusInternalServerError)
			return
		}

		_, _ = fmt.Fprint(w, page)
	}))
	t.Cleanup(srv.Close)

	if got := Get(t, srv.URL+"/metrics"); got != page {
		t.Errorf("Get = %q, want the page", got)
	}

	if _, err := fetch(t.Context(), srv.URL+"/broken"); err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "could not be gathered") {
		t.Errorf("fetch of a broken page = %v, want the status and what it said", err)
	}
}

// Until reads the page until it is what the test waits for, and gives back the page as it last was if it never is.
func TestUntil(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if asked.Add(1) < 3 {
			_, _ = fmt.Fprint(w, "zzyzx_up 0\n")
			return
		}

		_, _ = fmt.Fprint(w, "zzyzx_up 1\n")
	}))
	t.Cleanup(srv.Close)

	if got, ok := Until(t, srv.URL, 10*time.Second, func(page string) bool { return strings.Contains(page, "zzyzx_up 1") }); got != "zzyzx_up 1\n" || !ok {
		t.Errorf("Until = %q, %v, want the page once it said up", got, ok)
	}

	if got, ok := Until(t, srv.URL, 300*time.Millisecond, func(page string) bool { return strings.Contains(page, "zzyzx_up 2") }); got != "zzyzx_up 1\n" || ok {
		t.Errorf("Until a page that never comes = %q, %v, want the page as it last was and that it never came", got, ok)
	}
}
