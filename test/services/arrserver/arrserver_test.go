package arrserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

const (
	installed = "2.6.5.5623"
	newer     = "2.7.0.5700"
	older     = "2.6.4.5600"
)

// get asks the server for a path and decodes its JSON answer into out, when
// the answer is a 200.
func get(t *testing.T, c *Server, target string, out any) int {
	t.Helper()

	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody))
	if rec.Code == http.StatusOK && out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s answered %q, which is not JSON: %v", target, rec.Body.String(), err)
		}
	}

	return rec.Code
}

type available struct {
	Available     bool
	UpdatePackage *struct {
		Version string
		Branch  string
		Changes struct{ New, Fixed []string }
	}
}

// An update is the newest release above the version that asks, and only that:
// the same release, or an older one, is no update.
func TestTheUpdateOnOffer(t *testing.T) {
	t.Parallel()

	c := New()
	var got available
	if code := get(t, c, "/v1/update/master?version="+installed, &got); code != http.StatusOK || got.Available {
		t.Errorf("a server with no releases answered %d, available %v", code, got.Available)
	}

	c.Offer(Update{Version: older}, Update{Version: installed}, Update{Version: newer, New: []string{"a thing"}})
	get(t, c, "/v1/update/master?version="+installed, &got)
	if !got.Available || got.UpdatePackage == nil || got.UpdatePackage.Version != newer || got.UpdatePackage.Branch != "master" {
		t.Errorf("with a newer release the update on offer is %+v", got)
	} else if !slices.Equal(got.UpdatePackage.Changes.New, []string{"a thing"}) || got.UpdatePackage.Changes.Fixed == nil {
		t.Errorf("its changes are %+v", got.UpdatePackage.Changes)
	}

	got = available{}
	get(t, c, "/v1/update/master?version="+newer, &got)
	if got.Available {
		t.Error("the newest release was offered itself as an update")
	}
	// 2.10 is above 2.9: the parts are numbers
	c.Offer(Update{Version: "2.10.0.1"})
	get(t, c, "/v1/update/master?version=2.9.0.1", &got)
	if !got.Available {
		t.Error("2.10 was not offered to 2.9")
	}
}

// The recent releases are every one it knows, newest first, each with a date.
func TestTheRecentReleases(t *testing.T) {
	t.Parallel()

	c := New()
	when := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	c.Offer(Update{Version: older, Released: when}, Update{Version: newer}, Update{Version: installed})

	var got []struct {
		Version     string
		ReleaseDate time.Time
	}
	if code := get(t, c, "/v1/update/develop/changes?version="+installed, &got); code != http.StatusOK || len(got) != 3 {
		t.Fatalf("changes answered %d with %d releases", code, len(got))
	}
	if got[0].Version != newer || got[1].Version != installed || got[2].Version != older {
		t.Errorf("the releases are not newest first: %+v", got)
	}
	if !got[2].ReleaseDate.Equal(when) || got[0].ReleaseDate.IsZero() {
		t.Errorf("the release dates are %v and %v", got[2].ReleaseDate, got[0].ReleaseDate)
	}

	var none []any
	get(t, New(), "/v1/update/master/changes", &none)
	if none == nil {
		t.Error("no releases was answered as null, not an empty list")
	}
}

// The time is the time it is, so the application's clock check passes on any
// day, and there are no notices.
func TestTheClockAndTheNotices(t *testing.T) {
	t.Parallel()

	c := New()
	var clock struct{ DateTimeUtc time.Time }
	if code := get(t, c, "/v1/time", &clock); code != http.StatusOK || time.Since(clock.DateTimeUtc).Abs() > time.Minute {
		t.Errorf("the time answered %d, %v", code, clock.DateTimeUtc)
	}
	var notices []any
	if code := get(t, c, "/v1/notification?version="+installed, &notices); code != http.StatusOK || notices == nil || len(notices) != 0 {
		t.Errorf("the notices answered %d, %v", code, notices)
	}
}

// What it has no answer for is a 404 it remembers, so a suite can say what an
// application started asking; down, everything is a 503.
func TestWhatItDoesNotKnowAndBeingDown(t *testing.T) {
	t.Parallel()

	c := New()
	if got := c.Report(); got != "" {
		t.Errorf("a server asked nothing reported %q", got)
	}
	if code := get(t, c, "/v1/somethingnew", nil); code != http.StatusNotFound {
		t.Errorf("an unknown path answered %d", code)
	}
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/time", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a POST answered %d", rec.Code)
	}
	if got := c.Unknown(); !slices.Equal(got, []string{"GET /v1/somethingnew", "POST /v1/time"}) {
		t.Errorf("unknown = %v", got)
	}

	if got := c.Report(); got != "\narrserver: 2 request(s) had no answer:\n  GET /v1/somethingnew\n  POST /v1/time\n" {
		t.Errorf("the report = %q", got)
	}

	c.SetDown(true)
	if code := get(t, c, "/v1/time", nil); code != http.StatusServiceUnavailable {
		t.Errorf("down, the time answered %d", code)
	}
	c.SetDown(false)
	if code := get(t, c, "/v1/time", nil); code != http.StatusOK {
		t.Errorf("back up, the time answered %d", code)
	}
	if got := c.Requests(); len(got) != 4 || got[0] != "GET /v1/somethingnew" {
		t.Errorf("requests = %v", got)
	}
	if len(c.Unknown()) != 2 {
		t.Errorf("a refusal while down was counted as unknown: %v", c.Unknown())
	}
}
