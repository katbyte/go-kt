package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"

	"github.com/katbyte/go-kt/prometheus/metrics"
)

// shelf is a tool's collector that reports on several shelves, each reading starting with which shelf it is of, and writes what collect says.
type shelf struct {
	list    *metrics.List
	collect func(list map[string]*prometheus.Desc, ch chan<- prometheus.Metric)
	descs   map[string]*prometheus.Desc
}

func newShelf(collect func(list map[string]*prometheus.Desc, ch chan<- prometheus.Metric)) shelf {
	list := metrics.NewList(testNamespace, "shelf")

	return shelf{list: list, collect: collect, descs: map[string]*prometheus.Desc{
		"up":       list.Reading("up", "Whether the shelf answered (1) or not (0)."),
		"doors":    list.Reading("doors", "Doors on the shelf, by state.", "state"),
		"door":     list.Reading("door_state", "Whether each door is in each state (1) or not (0).", "door", "state"),
		"opened":   list.Reading("door_opened_timestamp_seconds", "When each door was last opened, in seconds since 1970.", "door"),
		"openings": list.Reading("door_openings_total", "How many times each door has been opened.", "door"),
		"weight":   list.Reading("weight_grams", "What the shelf holds."),
	}}
}

func (s shelf) Describe(ch chan<- *prometheus.Desc) { s.list.Describe(ch) }

func (s shelf) Collect(ch chan<- prometheus.Metric) { s.collect(s.descs, ch) }

// pageOf is the page of one shelf collector, written as text, with an error for a page that cannot be written.
func pageOf(t *testing.T, s shelf) (string, error) {
	t.Helper()

	p, err := metrics.New(metrics.Options{Name: testName, Namespace: testNamespace}, s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var b strings.Builder
	err = p.Write(&b)

	return b.String(), err
}

// Every kind of reading a tool writes, each starting with the list's own label, and the page still what Prometheus checks names for.
func TestTheWriterWritesEachKindOfReading(t *testing.T) {
	t.Parallel()

	opened := time.Date(2000, 1, 2, 3, 4, 5, 250000000, time.UTC)
	s := newShelf(func(d map[string]*prometheus.Desc, ch chan<- prometheus.Metric) {
		w := metrics.NewWriter(ch, "top")
		w.Flag(d["up"], true)
		w.Counts(d["doors"], []string{"open", "shut"}, map[string]int{"open": 2, "jammed": 1})
		w.States(d["door"], []string{"open", "shut"}, "shut", "left")
		w.At(d["opened"], opened, "left")
		w.At(d["opened"], time.Time{}, "right")
		w.Total(d["openings"], uint64(12), "left")
		w.Gauge(d["weight"], 2500)

		w = metrics.NewWriter(ch, "bottom")
		w.Flag(d["up"], false)
		w.Gauge(d["doors"], 0, "open")
	})

	text, err := pageOf(t, s)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := []string{
		`zzyzx_door_opened_timestamp_seconds{door="left",shelf="top"} 9.4678224525e+08`,
		`zzyzx_door_openings_total{door="left",shelf="top"} 12`,
		`zzyzx_door_state{door="left",shelf="top",state="open"} 0`,
		`zzyzx_door_state{door="left",shelf="top",state="shut"} 1`,
		`zzyzx_doors{shelf="bottom",state="open"} 0`,
		`zzyzx_doors{shelf="top",state="jammed"} 1`,
		`zzyzx_doors{shelf="top",state="open"} 2`,
		`zzyzx_doors{shelf="top",state="shut"} 0`,
		`zzyzx_up{shelf="bottom"} 0`,
		`zzyzx_up{shelf="top"} 1`,
		`zzyzx_weight_grams{shelf="top"} 2500`,
	}

	got := slices.DeleteFunc(samples(text), func(line string) bool { return strings.HasPrefix(line, "zzyzx_metrics_build_info") })
	if !slices.Equal(got, want) {
		t.Errorf("the page holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if !strings.Contains(text, "# TYPE zzyzx_door_openings_total counter") || !strings.Contains(text, "# TYPE zzyzx_doors gauge") {
		t.Errorf("a total is not a counter or a count is not a gauge:\n%s", text)
	}

	problems, err := promlint.New(strings.NewReader(text)).Lint()
	if err != nil {
		t.Fatalf("checking the page: %v", err)
	}

	for _, problem := range problems {
		t.Errorf("%s: %s", problem.Metric, problem.Text)
	}
}

// A state that is not one of those known is still written, at 1, and nothing at all is in a state when there is no saying which.
func TestStatesCopesWithWhatItHasNotHeardOf(t *testing.T) {
	t.Parallel()

	s := newShelf(func(d map[string]*prometheus.Desc, ch chan<- prometheus.Metric) {
		w := metrics.NewWriter(ch, "top")
		w.States(d["door"], []string{"open", "shut"}, "ajar", "left")
		w.States(d["door"], []string{"open", "shut"}, "", "right")
	})

	text, err := pageOf(t, s)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	for _, want := range []string{`{door="left",shelf="top",state="ajar"} 1`, `{door="left",shelf="top",state="open"} 0`, `{door="right",shelf="top",state="open"} 0`, `{door="right",shelf="top",state="shut"} 0`} {
		if !strings.Contains(text, want) {
			t.Errorf("the page lacks %s:\n%s", want, text)
		}
	}

	if strings.Contains(text, `state=""`) {
		t.Errorf("a door in no known state was written as being in one with no name:\n%s", text)
	}
}

// Two things that report under the same name do not spoil the page: the first is kept and the second left off.
func TestASecondReadingOfOneNameIsLeftOff(t *testing.T) {
	t.Parallel()

	s := newShelf(func(d map[string]*prometheus.Desc, ch chan<- prometheus.Metric) {
		w := metrics.NewWriter(ch, "top")
		w.Total(d["openings"], 12, "left")
		w.Total(d["openings"], 99, "left")
		w.Total(d["openings"], 3, "right")
	})

	text, err := pageOf(t, s)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !strings.Contains(text, `zzyzx_door_openings_total{door="left",shelf="top"} 12`) || strings.Contains(text, " 99") || !strings.Contains(text, `door="right"`) {
		t.Errorf("the page with one door reported twice is\n%s\nwant the first left door and the right one", text)
	}
}

// A reading written with the wrong number of labels is a mistake in the tool, said by name when the page is written. Written or served, the rest of
// the page is still there.
func TestAReadingWithTheWrongLabelsIsSaidAndLeftOff(t *testing.T) {
	t.Parallel()

	s := newShelf(func(d map[string]*prometheus.Desc, ch chan<- prometheus.Metric) {
		w := metrics.NewWriter(ch, "top")
		w.Flag(d["up"], true)
		w.Gauge(d["doors"], 1)
	})

	text, err := pageOf(t, s)
	if err == nil || !strings.Contains(err.Error(), "zzyzx_doors") {
		t.Errorf("Write with a reading short of a label = %v, want an error naming it", err)
	}

	if !strings.Contains(text, `zzyzx_up{shelf="top"} 1`) || strings.Contains(text, "zzyzx_doors{") {
		t.Errorf("the written page is\n%s\nwant the reading that could be written and not the one that could not", text)
	}

	p, err := metrics.New(metrics.Options{Name: testName, Namespace: testNamespace}, s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, metrics.Path, http.NoBody))

	if body := rec.Body.String(); !strings.Contains(body, `zzyzx_up{shelf="top"} 1`) || strings.Contains(body, "zzyzx_doors{") {
		t.Errorf("the served page is\n%s\nwant the reading that could be written and not the one that could not", body)
	}
}

// A reading whose name or labels a metrics store would not take is refused when the page is made, not when it is first read. Prometheus's own
// library takes a dash; not every store does.
func TestAReadingAStoreWouldNotTakeIsRefused(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		namespace string
		fixed     []string
		name      string
		labels    []string
		want      string
	}{
		{"zz-yzx", nil, "up", nil, `"zz-yzx_up" cannot be a reading's name`},
		{testNamespace, nil, "door-state", nil, `"zzyzx_door-state" cannot be a reading's name`},
		{testNamespace, []string{"shelf-name"}, "up", nil, `"shelf-name" cannot be the name of a label of zzyzx_up`},
		{testNamespace, nil, "doors", []string{"__door"}, `"__door" cannot be the name of a label of zzyzx_doors`},
	} {
		list := metrics.NewList(c.namespace, c.fixed...)
		list.Reading(c.name, "What it is.", c.labels...)

		s := shelf{list: list, collect: func(map[string]*prometheus.Desc, chan<- prometheus.Metric) {}}
		if _, err := metrics.New(metrics.Options{Name: testName, Namespace: testNamespace}, s); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("New = %v, want an error saying %s", err, c.want)
		}
	}
}

// A thing listed twice is written once whole: its status, carried in a label, would otherwise put it on the page in two states at once.
func TestFirstKeepsAThingListedTwiceOffThePage(t *testing.T) {
	t.Parallel()

	type door struct{ name, state string }

	s := newShelf(func(d map[string]*prometheus.Desc, ch chan<- prometheus.Metric) {
		for _, shelf := range []string{"top", "bottom"} {
			w := metrics.NewWriter(ch, shelf)
			for _, door := range []door{{"left", "open"}, {"left", "shut"}, {"right", "shut"}} {
				if w.First("door", door.name) {
					w.States(d["door"], nil, door.state, door.name)
				}
			}
		}
	})

	text, err := pageOf(t, s)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	for _, shelf := range []string{"top", "bottom"} {
		if !strings.Contains(text, `zzyzx_door_state{door="left",shelf="`+shelf+`",state="open"} 1`) || strings.Contains(text, `zzyzx_door_state{door="left",shelf="`+shelf+`",state="shut"}`) || !strings.Contains(text, `zzyzx_door_state{door="right",shelf="`+shelf+`",state="shut"} 1`) {
			t.Errorf("the page with the left door listed twice on the %s shelf is\n%s\nwant it once, as first listed, and the right door", shelf, text)
		}
	}
}

// A tool's own kinds, a named string type, are counted as they are, and any number is a reading.
func TestKindsAndNumbersOfATypeOfTheToolsOwn(t *testing.T) {
	t.Parallel()

	type state string

	s := newShelf(func(d map[string]*prometheus.Desc, ch chan<- prometheus.Metric) {
		w := metrics.NewWriter(ch, "top")
		w.Counts(d["doors"], []state{"open", "shut"}, map[state]int{"open": 1})
		w.States(d["door"], []state{"open", "shut"}, state("open"), "left")
		w.Gauge(d["weight"], int64(1)<<40)
		w.Total(d["openings"], uint32(7), "left")
		w.Gauge(d["up"], float32(1))
	})

	text, err := pageOf(t, s)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	for _, want := range []string{`zzyzx_doors{shelf="top",state="open"} 1`, `zzyzx_doors{shelf="top",state="shut"} 0`, `zzyzx_door_state{door="left",shelf="top",state="open"} 1`, `zzyzx_weight_grams{shelf="top"} 1.099511627776e+12`, `zzyzx_door_openings_total{door="left",shelf="top"} 7`, `zzyzx_up{shelf="top"} 1`} {
		if !strings.Contains(text, want) {
			t.Errorf("the page lacks %s:\n%s", want, text)
		}
	}
}
