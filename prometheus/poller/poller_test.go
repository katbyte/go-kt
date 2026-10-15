package poller_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/prometheus/metricstest"
	"github.com/katbyte/go-kt/prometheus/poller"
)

const testNamespace = "zzyzx"

// errPhy is a phy that would not say how its link is.
var errPhy = errors.New("phy1 would not say")

// card is a target as a listing gives it: the kernel's name for it, and its model.
type card struct {
	name, model string
}

// called is what each card is called on the page: its model and the number in the kernel's name, given every card ever seen.
func called(all map[string]poller.Target[card]) map[string]string {
	names := map[string]string{}
	for key, t := range all {
		names[key] = t.Value.model + "-" + strings.TrimPrefix(key, "host")
	}

	return names
}

// page is the page of these collectors' readings, written as text.
func page(t *testing.T, collectors ...prometheus.Collector) string {
	t.Helper()

	p, err := metrics.New(metrics.Options{Name: "zzyzx-metrics", Namespace: testNamespace}, collectors...)
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}

	var b strings.Builder
	if err := p.Write(&b); err != nil {
		t.Fatalf("Write: %v", err)
	}

	return b.String()
}

// A tool that asks about one thing keeps each question's last good answer, records how each schedule's round went, and stops serving an answer once
// it is older than three of its schedule's intervals.
func TestOneThingIsAskedAndItsAnswersKept(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p, err := poller.New(poller.Options[struct{}]{Namespace: testNamespace, Kind: "box"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		doors := poller.AskOne(p.Schedule("fast", time.Minute), "doors", func(context.Context) (int, error) { return 3, nil })
		weight := poller.AskOne(p.Schedule("slow", time.Hour), "weight", func(context.Context) (float64, error) { return 0, errors.New("the scale is off") })

		err = p.PollAll(t.Context())
		if err == nil || err.Error() != "the box: the slow round failed; the log says why" {
			t.Errorf("PollAll = %v, want the slow round named as failed, and nothing of the fast one", err)
		}

		if err := p.Poll(t.Context(), "slow"); err == nil || err.Error() != "the box: the slow round: weight: the scale is off" {
			t.Errorf("Poll of the slow schedule = %v, want why it failed", err)
		}

		if a := doors.Answer(); !a.Known() || a.Value != 3 {
			t.Errorf("doors = %+v, want 3", a)
		}

		if a := weight.Answer(); a.Known() {
			t.Errorf("weight = %+v, want nothing for a question never answered", a)
		}

		if r := p.Round("", "fast"); !r.Ran || !r.OK || r.Failures != 0 || r.LastSuccess.IsZero() {
			t.Errorf("the fast round = %+v, want one that ran and was answered", r)
		}

		if r := p.Round("", "slow"); !r.Ran || r.OK || r.Failures != 2 || !r.LastSuccess.IsZero() {
			t.Errorf("the slow round = %+v, want one that ran and failed", r)
		}

		time.Sleep(3 * time.Minute)
		if !doors.Answer().Known() {
			t.Error("an answer three intervals old is not served, want it served until it is older")
		}

		time.Sleep(time.Second)
		if a := doors.Answer(); a.Known() {
			t.Errorf("an answer older than three intervals = %+v, want nothing", a)
		}
	})
}

// Run asks each schedule at once on start and then on its own interval, until the context ends.
func TestRunAsksEachScheduleOnItsOwnInterval(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p, err := poller.New(poller.Options[struct{}]{Namespace: testNamespace})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		var fast, slow atomic.Int32
		poller.AskOne(p.Schedule("fast", time.Minute), "fast", func(context.Context) (int32, error) { return fast.Add(1), nil })
		poller.AskOne(p.Schedule("slow", 10*time.Minute), "slow", func(context.Context) (int32, error) { return slow.Add(1), nil })

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { p.Run(ctx); close(done) }()

		time.Sleep(10*time.Minute + time.Second)
		cancel()
		<-done

		if fast.Load() != 11 || slow.Load() != 2 {
			t.Errorf("in ten minutes the fast schedule was asked %d times and the slow %d, want 11 and 2", fast.Load(), slow.Load())
		}
	})
}

// Targets come from a listing each round. One the listing stops naming stays known with what it last said of it, and fails its rounds without being
// asked; a listing that fails costs every target known its round.
func TestTargetsComeAndGoWithTheListing(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		listed := []card{{"host1", "9500-16i"}, {"host8", "9500-16i"}}
		var listing error
		asked := map[string]int{}

		p, err := poller.New(poller.Options[card]{
			Namespace: testNamespace,
			Label:     "card",
			Kind:      "SAS card",
			Targets: func(context.Context) ([]card, error) {
				mu.Lock()
				defer mu.Unlock()

				return slices.Clone(listed), listing
			},
			Key:   func(c card) string { return c.name },
			Names: called,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		links := poller.Ask(p.Schedule("links", time.Minute), "links", func(_ context.Context, c card) (string, error) {
			mu.Lock()
			defer mu.Unlock()

			asked[c.name]++

			return "up on " + c.name, nil
		})

		if err := p.PollAll(t.Context()); err != nil {
			t.Fatalf("PollAll: %v", err)
		}

		if got := p.Targets(); !slices.Equal(got, []string{"host1", "host8"}) {
			t.Errorf("Targets = %q, want both cards", got)
		}

		if a := links.For("host8"); a.Value != "up on host8" {
			t.Errorf("host8's links = %+v, want its own answer", a)
		}

		mu.Lock()
		listed = listed[:1]
		mu.Unlock()

		time.Sleep(time.Minute)
		err = p.Poll(t.Context(), "links")
		if err == nil || !strings.Contains(err.Error(), "card 9500-16i-8 (host8): the links round: no longer listed") || strings.Contains(err.Error(), "host1") {
			t.Errorf("PollAll with host8 gone = %v, want host8 failed by its page name and host1 not", err)
		}

		if g := p.Target("host8"); g.Listed || g.Value.model != "9500-16i" || !g.At.Before(time.Now()) {
			t.Errorf("host8 once gone = %+v, want it unlisted, with what was last said of it and when", g)
		}

		if asked["host8"] != 1 || asked["host1"] != 2 {
			t.Errorf("asked = %v, want host8 asked only while it was listed", asked)
		}

		if r := p.Round("host8", "links"); r.OK || r.Failures != 1 {
			t.Errorf("host8's round = %+v, want one failure", r)
		}

		mu.Lock()
		listing = errors.New("the kernel would not say")
		mu.Unlock()

		err = p.Poll(t.Context(), "links")
		for _, want := range []string{"card 9500-16i-1 (host1): the links round: listing: the kernel would not say", "card 9500-16i-8 (host8): the links round: listing:"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("PollAll with the listing failing = %v, want %q in it", err, want)
			}
		}

		if r := p.Round("host1", "links"); r.OK || r.Failures != 1 {
			t.Errorf("host1's round once the listing failed = %+v, want it failed", r)
		}
	})
}

// A non-string target needs a key to be told apart by; a string is its own.
func TestNewNeedsAKeyForTargetsThatAreNotStrings(t *testing.T) {
	t.Parallel()

	if _, err := poller.New(poller.Options[card]{Namespace: testNamespace, Targets: func(context.Context) ([]card, error) { return nil, nil }}); err == nil || !strings.Contains(err.Error(), "Key") {
		t.Errorf("New with cards and no Key = %v, want an error saying a Key is needed", err)
	}

	if _, err := poller.New(poller.Options[string]{Namespace: testNamespace, Targets: poller.Fixed("emby")}); err != nil {
		t.Errorf("New with names and no Key = %v, want nil", err)
	}
}

// A server that hangs is given its schedule's interval and no more, and holds up no other server, which is asked and recorded as answered.
func TestEachTargetHasItsOwnTimeLimit(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p, err := poller.New(poller.Options[string]{Namespace: testNamespace, Label: "server", Targets: poller.Fixed("emby", "jellyfin")})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		poller.Ask(p.Schedule("sessions", 15*time.Second), "sessions", func(ctx context.Context, server string) (int, error) {
			if server == "emby" {
				<-ctx.Done()
				return 0, ctx.Err()
			}

			return 2, nil
		})

		err = p.Poll(t.Context(), "sessions")
		if err == nil || !strings.Contains(err.Error(), "server emby: the sessions round: sessions: context deadline exceeded") || strings.Contains(err.Error(), "jellyfin") {
			t.Errorf("PollAll with emby hung = %v, want emby failed and jellyfin not", err)
		}

		if r := p.Round("emby", "sessions"); r.OK || r.Took != 15*time.Second {
			t.Errorf("emby's round = %+v, want it failed after the 15s it was given", r)
		}

		if r := p.Round("jellyfin", "sessions"); !r.OK || r.Took != 0 {
			t.Errorf("jellyfin's round = %+v, want it answered straight away", r)
		}
	})
}

// What could be read is kept though the round fails, when the question says it is worth keeping; a schedule with nothing it could ask has no
// round; and a round cut short by the program stopping is not counted against what was asked.
func TestRoundsThatAreNotPlainSuccessOrFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p, err := poller.New(poller.Options[struct{}]{Namespace: testNamespace})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		var rounds atomic.Int32
		links := poller.AskOne(p.Schedule("links", time.Minute), "links", func(context.Context) ([]string, error) {
			if rounds.Add(1) == 1 {
				return []string{"phy0"}, poller.Partial(errPhy)
			}

			return []string{"a half-read phy"}, errors.New("the card is gone")
		})
		sensors := poller.AskOne(p.Schedule("sensors", time.Minute), "sensors", func(context.Context) (string, error) { return "only root may read them", poller.ErrNothing })

		err = p.PollAll(t.Context())
		if err == nil || err.Error() != "the links round failed; the log says why" || !errors.Is(err, errPhy) {
			t.Errorf("PollAll = %v, want the links round named as failed, why behind it, and nothing of sensors", err)
		}

		if a := links.Answer(); !slices.Equal(a.Value, []string{"phy0"}) {
			t.Errorf("links = %+v, want what could be read kept", a)
		}

		if r := p.Round("", "sensors"); r.Ran {
			t.Errorf("the sensors round = %+v, want none for a schedule with nothing it could ask", r)
		}

		if a := sensors.Answer(); a.Known() {
			t.Errorf("sensors = %+v, want nothing kept from a question that had nothing it could ask", a)
		}

		if err := p.Poll(t.Context(), "links"); err == nil {
			t.Error("Poll of links the second time = nil, want it failed")
		}

		if a := links.Answer(); !slices.Equal(a.Value, []string{"phy0"}) {
			t.Errorf("links after a round that failed = %+v, want the last good answer kept, not what came with the failure", a)
		}

		poller.AskOne(p.Schedule("slow", time.Hour), "slow", func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		})

		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()

		if err := p.Poll(ctx, "slow"); err != nil {
			t.Errorf("Poll cut short by stopping = %v, want nil", err)
		}

		if r := p.Round("", "slow"); r.Ran {
			t.Errorf("the slow round cut short = %+v, want none recorded", r)
		}

		if err := p.Poll(t.Context(), "nothing"); err == nil || !strings.Contains(err.Error(), "no nothing schedule") {
			t.Errorf("Poll of a schedule there is not = %v, want an error naming it", err)
		}
	})
}

// The poller's own readings say how the asking goes for each target and schedule that has finished a round, by what the target is called on the
// page; none for a schedule that has not, and no last success before there is one.
func TestThePollersReadings(t *testing.T) {
	t.Parallel()

	p, err := poller.New(poller.Options[card]{Namespace: testNamespace, Label: "card", Targets: func(context.Context) ([]card, error) { return nil, nil }, Key: func(c card) string { return c.name }, Names: called})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, name := range []string{"links", "sensors", "enclosures"} {
		p.Schedule(name, time.Minute)
	}

	p.SetTarget(poller.Target[card]{Key: "host1", Value: card{"host1", "9500-16i"}, Listed: true, At: time.Now()})
	p.SetRound("host1", "links", poller.Round{Ran: true, Took: 3 * time.Second, Failures: 2, LastSuccess: time.Unix(946684800, 0)})
	p.SetRound("host1", "sensors", poller.Round{Ran: true, Took: time.Second, Failures: 1})

	text := page(t, p)
	metricstest.Lint(t, text)

	for _, c := range []struct {
		name, poll string
		value      float64
	}{
		{"zzyzx_poll_interval_seconds", "links", 60},
		{"zzyzx_poll_duration_seconds", "links", 3},
		{"zzyzx_poll_failures_total", "links", 2},
		{"zzyzx_poll_last_success_timestamp_seconds", "links", 946684800},
		{"zzyzx_poll_duration_seconds", "sensors", 1},
	} {
		if got, one := metricstest.One(t, text, c.name, map[string]string{"card": "9500-16i-1", "poll": c.poll}); !one || got != c.value {
			t.Errorf("%s for %s = %v, %v, want %v", c.name, c.poll, got, one, c.value)
		}
	}

	if got := metricstest.Readings(t, text, "zzyzx_poll_last_success_timestamp_seconds"); len(got) != 1 {
		t.Errorf("last successes = %+v, want only the schedule that has had one", got)
	}

	if got := metricstest.Readings(t, text, "zzyzx_poll_interval_seconds"); len(got) != 2 {
		t.Errorf("intervals = %+v, want none for the schedule that has not run", got)
	}

	if got := p.Names(); !maps.Equal(got, map[string]string{"host1": "9500-16i-1"}) {
		t.Errorf("Names = %v, want each target by its page name", got)
	}

	one, err := poller.New(poller.Options[struct{}]{Namespace: testNamespace})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	one.Schedule("pools", time.Minute)
	one.SetRound("", "pools", poller.Round{Ran: true, OK: true, Took: time.Second, LastSuccess: time.Unix(946684800, 0)})

	if got := metricstest.Readings(t, page(t, one), "zzyzx_poll_duration_seconds"); len(got) != 1 || !maps.Equal(got[0].Labels, map[string]string{"poll": "pools"}) {
		t.Errorf("a tool with one target's readings = %+v, want them labelled by schedule alone", got)
	}
}

// A schedule with no interval, or a second of one name, is a mistake in the tool and said at once.
func TestAScheduleMadeWrongPanics(t *testing.T) {
	t.Parallel()

	p, err := poller.New(poller.Options[struct{}]{Namespace: testNamespace})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p.Schedule("pools", time.Minute)

	for name, every := range map[string]time.Duration{"pools": time.Minute, "disks": 0} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Schedule(%s, %s) did not panic", name, every)
				}
			}()

			p.Schedule(name, every)
		}()
	}
}

func TestParseIntervals(t *testing.T) {
	t.Parallel()

	defaults := map[string]time.Duration{"sessions": 15 * time.Second, "library": 5 * time.Minute, "history": 30 * time.Second}

	for setting, want := range map[string]map[string]time.Duration{
		"":                            defaults,
		" , ":                         defaults,
		"library=1h":                  {"sessions": 15 * time.Second, "library": time.Hour, "history": 30 * time.Second},
		" Sessions = 5s , history=1m": {"sessions": 5 * time.Second, "library": 5 * time.Minute, "history": time.Minute},
	} {
		got, err := poller.ParseIntervals(setting, defaults)
		if err != nil || !maps.Equal(got, want) {
			t.Errorf("ParseIntervals(%q) = %v, %v, want %v", setting, got, err, want)
		}
	}

	for setting, want := range map[string]string{
		"plays=1m":              `history, library, sessions, and not "plays=1m"`,
		"library":               `and not "library"`,
		"library=soon":          `how often library is asked is written as 30s or 10m, and not "soon"`,
		"library=0s":            `and not "0s"`,
		"library=-1m":           `and not "-1m"`,
		"library=1m,x=":         `and not "x="`,
		"library=1m,library=2m": `how often library is asked is given twice`,
	} {
		if got, err := poller.ParseIntervals(setting, defaults); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseIntervals(%q) = %v, %v, want an error saying %s", setting, got, err, want)
		}
	}

	if defaults["library"] != 5*time.Minute {
		t.Errorf("ParseIntervals changed the defaults it was given: %v", defaults)
	}
}

// What PollAll says is which rounds failed, a few words for each target; why is in the log. Many rounds are counted rather than named.
func TestPollAllSaysWhichRoundsFailed(t *testing.T) {
	t.Parallel()

	p, err := poller.New(poller.Options[string]{Namespace: testNamespace, Label: "server", Targets: poller.Fixed("emby", "jellyfin")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, name := range []string{"sessions", "library", "history", "plugins", "logins"} {
		poller.Ask(p.Schedule(name, time.Minute), name, func(_ context.Context, server string) (int, error) {
			if server == "emby" || name == "library" {
				return 0, errors.New("no answer")
			}

			return 1, nil
		})
	}

	want := "server emby: the sessions, library, history and 2 other rounds failed; server jellyfin: the library round failed; the log says why"
	if err := p.PollAll(t.Context()); err == nil || err.Error() != want {
		t.Errorf("PollAll = %v, want %s", err, want)
	}
}

// A schedule a tool left out has no Kept, and asking it for an answer gives none rather than stopping the tool.
func TestALeftOutScheduleHasNoAnswer(t *testing.T) {
	t.Parallel()

	var apps *poller.Kept[int]
	if a := apps.Answer(); a.Known() {
		t.Errorf("a nil Kept's answer = %+v, want none", a)
	}
}
