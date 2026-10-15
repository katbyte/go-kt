package httpd_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/katbyte/go-kt/httpd"
)

// The health check answers ok to a GET, and to nothing else.
func TestHealth(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	httpd.Health(mux)

	for method, want := range map[string]int{http.MethodGet: http.StatusOK, http.MethodPost: http.StatusMethodNotAllowed} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, httpd.HealthPath, http.NoBody))

		if rec.Code != want || (want == http.StatusOK && rec.Body.String() != "ok\n") {
			t.Errorf("%s %s = %d %q, want %d", method, httpd.HealthPath, rec.Code, rec.Body.String(), want)
		}
	}
}

// An address that cannot be listened on is said by name.
func TestListenNamesAnAddressItCannotTake(t *testing.T) {
	t.Parallel()

	if _, err := httpd.Listen(t.Context(), "256.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "listening on 256.0.0.1:1") {
		t.Errorf("Listen on an address that does not exist = %v, want an error naming it", err)
	}
}

// A request under way when the stop comes is answered in full; what runs at the stop runs; and serving ends with no error.
func TestServeLetsARequestUnderWayFinish(t *testing.T) {
	t.Parallel()

	ln, err := httpd.Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	started, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("done\n"))
	})

	var stopped atomic.Bool
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- httpd.Serve(ctx, ln, mux, func() { stopped.Store(true) }) }()

	answered := make(chan string, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/slow", http.NoBody)
		if err != nil {
			answered <- err.Error()
			return
		}

		res, err := http.DefaultClient.Do(req)
		if err != nil {
			answered <- err.Error()
			return
		}

		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		answered <- string(body)
	}()

	<-started
	cancel()
	close(release)

	if got := <-answered; got != "done\n" {
		t.Errorf("the request under way at the stop got %q, want its whole answer", got)
	}

	if err := <-served; err != nil {
		t.Errorf("Serve = %v, want nil once stopped", err)
	}

	if !stopped.Load() {
		t.Error("what was to run at the stop did not run")
	}
}

// Serving that stops by itself, its listener gone, is an error that says where.
func TestServeSaysWhenServingStopsByItself(t *testing.T) {
	t.Parallel()

	ln, err := httpd.Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	if err := httpd.Serve(t.Context(), ln, http.NewServeMux()); err == nil || !strings.Contains(err.Error(), "serving on "+addr) {
		t.Errorf("Serve on a closed listener = %v, want an error naming where", err)
	}
}
