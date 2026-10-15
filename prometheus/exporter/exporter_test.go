package exporter_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/katbyte/go-kt/prometheus/exporter"
	"github.com/katbyte/go-kt/prometheus/metrics"
)

// asker is a poller whose PollAll fails as the test says, and whose Run counts how often it ran and when it was told to stop.
type asker struct {
	failed  error
	ran     atomic.Int32
	stopped atomic.Bool
}

func (a *asker) Run(ctx context.Context) {
	a.ran.Add(1)
	<-ctx.Done()
	a.stopped.Store(true)
}

func (a *asker) PollAll(context.Context) error { return a.failed }

// up is a page with one reading of its own.
func up(t *testing.T) *metrics.Page {
	t.Helper()

	d := prometheus.NewDesc("zzyzx_up", "Whether the thing answered (1) or not (0).", nil, nil)
	page, err := metrics.New(metrics.Options{Name: "zzyzx-metrics", Namespace: "zzyzx"}, collector{d})
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}

	return page
}

type collector struct{ d *prometheus.Desc }

func (c collector) Describe(ch chan<- *prometheus.Desc) { ch <- c.d }

func (c collector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.d, prometheus.GaugeValue, 0)
}

// Print writes the page whatever happened, and then says what failed.
func TestPrintWritesThePageAndThenSaysWhatFailed(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	err := exporter.Print(t.Context(), &out, &asker{failed: errors.New("the NAS did not answer")}, up(t))
	if err == nil || !strings.Contains(err.Error(), "the NAS did not answer") {
		t.Errorf("Print = %v, want the poll's failure", err)
	}

	if !strings.Contains(out.String(), "zzyzx_up 0") {
		t.Errorf("Print wrote %q, want the page all the same", out.String())
	}

	out.Reset()
	if err := exporter.Print(t.Context(), &out, &asker{}, up(t)); err != nil || !strings.Contains(out.String(), "zzyzx_up 0") {
		t.Errorf("Print with everything answered = %v, %q, want nil and the page", err, out.String())
	}
}

// Serve asks while it serves, and when serving stops by itself the asking stops with it and the reason is given back.
func TestServeStopsAskingWhenServingStops(t *testing.T) {
	t.Parallel()

	a := &asker{}
	err := exporter.Serve(t.Context(), "256.0.0.1:1", a, up(t))
	if err == nil || !strings.Contains(err.Error(), "256.0.0.1:1") {
		t.Errorf("Serve on an address that does not exist = %v, want an error naming it", err)
	}

	if a.ran.Load() != 1 || !a.stopped.Load() {
		t.Errorf("the poller ran %d times and stopped %v, want once and stopped", a.ran.Load(), a.stopped.Load())
	}
}

// Serve asks and serves until the context ends, and then both stop.
func TestServeUntilTheContextEnds(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	a := &asker{}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- exporter.Serve(ctx, addr, a, up(t)) }()

	for a.ran.Load() == 0 {
		select {
		case err := <-served:
			t.Fatalf("Serve stopped before it was told to: %v", err)
		default:
		}
	}

	cancel()
	if err := <-served; err != nil {
		t.Errorf("Serve stopped with %v, want nil", err)
	}

	if !a.stopped.Load() {
		t.Error("the poller was not stopped with the serving")
	}
}
