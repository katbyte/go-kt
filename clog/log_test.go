package clog

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/katbyte/go-kt/chttp"
)

func TestNewDefaults(t *testing.T) {
	t.Parallel()

	l := New()
	if l.GetLevel() != DefaultLevel {
		t.Errorf("level = %s, want %s", l.GetLevel(), DefaultLevel)
	}
	f, ok := l.Formatter.(*logrus.TextFormatter)
	if !ok {
		t.Fatalf("formatter = %T, want *logrus.TextFormatter", l.Formatter)
	}
	if !f.FullTimestamp || f.TimestampFormat != TimestampFormat {
		t.Errorf("formatter = %+v, want FullTimestamp with %q", f, TimestampFormat)
	}
}

func TestNewWithOutputWritesTimestampedLines(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := NewWithOutput(&buf)
	l.Warn("something happened")

	out := buf.String()
	if !strings.Contains(out, "something happened") {
		t.Fatalf("output %q does not contain the message", out)
	}
	// logrus prints time="2006-01-02 15:04:05" when the output is not a terminal
	if !strings.Contains(out, `time="`) {
		t.Errorf("output %q has no timestamp", out)
	}
	if !strings.Contains(out, "level=warning") {
		t.Errorf("output %q has no level", out)
	}
}

func TestSetLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		level     string
		source    string
		want      logrus.Level
		wantError string // substring expected in the logged error, empty for none
	}{
		{"empty keeps the default", "", "", DefaultLevel, ""},
		{"lower case", "debug", "", logrus.DebugLevel, ""},
		{"upper case", "TRACE", "", logrus.TraceLevel, ""},
		{"mixed case", "Info", "", logrus.InfoLevel, ""},
		{"typo falls back and names the value", "chatty", "", FallbackLevel, "chatty"},
		{"typo from env names the variable", "warnn", "TCTEST_LOG", FallbackLevel, "`TCTEST_LOG`"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			l := NewWithOutput(&buf)
			applyLevel(l, tc.level, tc.source)

			if l.GetLevel() != tc.want {
				t.Errorf("level = %s, want %s", l.GetLevel(), tc.want)
			}
			if tc.wantError == "" {
				if buf.Len() != 0 {
					t.Errorf("unexpected log output: %q", buf.String())
				}
				return
			}
			if !strings.Contains(buf.String(), tc.wantError) {
				t.Errorf("log output %q does not mention %q", buf.String(), tc.wantError)
			}
			if !strings.Contains(buf.String(), "defaulting to trace") {
				t.Errorf("log output %q does not announce the fallback", buf.String())
			}
		})
	}
}

// The package-level helpers mutate the shared Log (and t.Setenv forbids
// t.Parallel anyway), so these cases run serially.
func TestPackageLevelSetters(t *testing.T) {
	var buf bytes.Buffer
	orig := Log
	Log = NewWithOutput(&buf)
	t.Cleanup(func() { Log = orig })

	t.Setenv("GO_KT_TEST_LOG", "debug")
	SetLevelFromEnv("GO_KT_TEST_LOG")
	if Log.GetLevel() != logrus.DebugLevel {
		t.Errorf("SetLevelFromEnv: level = %s, want debug", Log.GetLevel())
	}

	t.Setenv("GO_KT_TEST_LOG", "")
	SetLevelFromEnv("GO_KT_TEST_LOG")
	if Log.GetLevel() != DefaultLevel {
		t.Errorf("SetLevelFromEnv (empty): level = %s, want %s", Log.GetLevel(), DefaultLevel)
	}

	SetLevel("error")
	if Log.GetLevel() != logrus.ErrorLevel {
		t.Errorf("SetLevel: level = %s, want error", Log.GetLevel())
	}

	SetLevel("nonsense")
	if Log.GetLevel() != FallbackLevel {
		t.Errorf("SetLevel (bad): level = %s, want %s", Log.GetLevel(), FallbackLevel)
	}
	if !strings.Contains(buf.String(), "nonsense") {
		t.Errorf("SetLevel (bad) logged %q, want the bad value named", buf.String())
	}
}

// read is a body that says whether anything read it.
type read struct {
	io.Reader

	touched bool
}

func (r *read) Read(p []byte) (int, error) {
	r.touched = true

	return r.Reader.Read(p)
}

func (*read) Close() error { return nil }

// answer is a transport that answers every request with one body.
type answer struct{ body *read }

func (a answer) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: http.Header{"Content-Type": {"application/json"}}, Body: a.body, Request: req}, nil
}

// A logger made here is what chttp asks for as it stands, and what chttp
// relies on holds of it: below trace a request's trace is never put
// together, so nothing is read from the answer to show it.
func TestALoggerIsWhatAnHTTPClientTraces(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := NewWithOutput(&buf)
	var _ chttp.Logger = l
	var _ chttp.Logger = Log

	send := func() *read {
		t.Helper()

		body := &read{Reader: strings.NewReader(`{"hello":"world"}`)}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://nas.invalid/things", http.NoBody)
		req.Header.Set("Authorization", "Bearer s3cret")
		resp, err := chttp.NewTransport(chttp.Options{Name: "Widget", Log: l}, answer{body}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if got, err := io.ReadAll(resp.Body); err != nil || string(got) != `{"hello":"world"}` {
			t.Fatalf("the caller read %q (%v), want the answer", got, err)
		}

		return body
	}

	l.SetLevel(logrus.DebugLevel)
	body := send()
	if buf.Len() != 0 {
		t.Fatalf("at debug the client logged %q, want nothing", buf.String())
	}

	// the same request with the body watched from before it is sent: nothing reads it until the caller does
	watched := &read{Reader: strings.NewReader(`{"hello":"world"}`)}
	resp, err := chttp.NewTransport(chttp.Options{Name: "Widget", Log: l}, answer{watched}).RoundTrip(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://nas.invalid/things", http.NoBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if watched.touched || !body.touched {
		t.Errorf("at debug the answer was read before its caller read it: %v", watched.touched)
	}

	l.SetLevel(logrus.TraceLevel)
	send()
	out := buf.String()
	// logrus quotes the message, so the pretty-printed body's own quotes arrive escaped
	for _, want := range []string{"level=trace", "Widget API Request Details", "GET /things HTTP/1.1", "Authorization: REDACTED", "Widget API Response Details", `{\n \"hello\": \"world\"\n}`} {
		if !strings.Contains(out, want) {
			t.Errorf("the trace is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Errorf("the trace shows the key:\n%s", out)
	}
}
