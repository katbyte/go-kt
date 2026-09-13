package clog

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
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
