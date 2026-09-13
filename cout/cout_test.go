package cout

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	c "github.com/gookit/color"
)

// Colour rendering depends on the terminal; disabling it makes tags strip to
// plain text so every expectation below is deterministic. Tag rendering itself
// is covered by comparing against gookit's own output.
func TestMain(m *testing.M) {
	c.Disable()
	m.Run()
}

// capture runs f against a printer at level with both writers captured.
func capture(level Verbosity, f func(p printer)) (out, err string) {
	var o, e bytes.Buffer
	f(printer{level: level, out: &o, err: &e})
	return o.String(), e.String()
}

func TestVerbosityOrderingAndNames(t *testing.T) {
	t.Parallel()

	if VerbositySilent >= VerbosityJSON || VerbosityJSON >= VerbosityQuiet || VerbosityQuiet >= VerbosityNormal || VerbosityNormal >= VerbosityVerbose {
		t.Fatal("verbosity levels are not ordered silent < json < quiet < normal < verbose")
	}
	names := map[Verbosity]string{
		VerbositySilent:  "silent",
		VerbosityJSON:    "json",
		VerbosityQuiet:   "quiet",
		VerbosityNormal:  "normal",
		VerbosityVerbose: "verbose",
		Verbosity(42):    "unknown",
	}
	for v, want := range names {
		if got := v.String(); got != want {
			t.Errorf("Verbosity(%d).String() = %q, want %q", int(v), got, want)
		}
	}
}

func TestPrintfGating(t *testing.T) {
	t.Parallel()

	// which levels each call prints at, keyed by the call's minimum level
	tests := []struct {
		name    string
		minimum Verbosity
		prints  map[Verbosity]bool
	}{
		{"Printf (Normal)", VerbosityNormal, map[Verbosity]bool{
			VerbositySilent: false, VerbosityJSON: false, VerbosityQuiet: false, VerbosityNormal: true, VerbosityVerbose: true,
		}},
		{"Verbosef (Verbose)", VerbosityVerbose, map[Verbosity]bool{
			VerbositySilent: false, VerbosityJSON: false, VerbosityQuiet: false, VerbosityNormal: false, VerbosityVerbose: true,
		}},
		{"Quietf (Quiet and above)", VerbosityQuiet, map[Verbosity]bool{
			VerbositySilent: false, VerbosityJSON: false, VerbosityQuiet: true, VerbosityNormal: true, VerbosityVerbose: true,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for level, want := range tc.prints {
				out, err := capture(level, func(p printer) { p.printf(tc.minimum, "n=%d\n", 7) })
				if got := out == "n=7\n"; got != want {
					t.Errorf("at %s: printed = %v, want %v (out=%q)", level, got, want, out)
				}
				if err != "" {
					t.Errorf("at %s: wrote to err: %q", level, err)
				}
			}
		})
	}
}

func TestWriter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	for _, level := range []Verbosity{VerbositySilent, VerbosityJSON, VerbosityQuiet} {
		p := printer{level: level, out: &buf, err: io.Discard}
		if p.writer() != io.Discard {
			t.Errorf("at %s: writer is not io.Discard", level)
		}
	}
	for _, level := range []Verbosity{VerbosityNormal, VerbosityVerbose} {
		p := printer{level: level, out: &buf, err: io.Discard}
		if p.writer() != &buf {
			t.Errorf("at %s: writer is not Out", level)
		}
	}
}

func TestSprintfRendersTagsLikeGookit(t *testing.T) {
	t.Parallel()

	// with colour disabled the tags strip; with it enabled gookit renders them.
	// either way our Sprintf must agree with gookit's, including tags that
	// arrive through an argument rather than the format string
	format, arg := "<green>ok</> %s", "<yellow>arg</>"
	if got, want := Sprintf(format, arg), c.Sprintf(format, arg); got != want {
		t.Errorf("Sprintf = %q, want %q", got, want)
	}
	if got := Sprintf(format, arg); got != "ok arg" {
		t.Errorf("Sprintf with colour disabled = %q, want tags stripped", got)
	}
}

func TestTagsInArgumentsAreRendered(t *testing.T) {
	t.Parallel()

	out, _ := capture(VerbosityNormal, func(p printer) { p.printf(VerbosityNormal, "state %s\n", "<green>open</>") })
	if out != "state open\n" {
		t.Errorf("printf = %q, want the argument's tags rendered (stripped here)", out)
	}
}

// The package-level functions read the globals, so these run serially.
//
//nolint:paralleltest // mutates package-level Level, Out and Err
func TestPackageLevelFunctions(t *testing.T) {
	var out, errBuf bytes.Buffer
	origLevel, origOut, origErr := Level, Out, Err
	Out, Err = &out, &errBuf
	t.Cleanup(func() { Level, Out, Err = origLevel, origOut, origErr })

	reset := func() { out.Reset(); errBuf.Reset() }

	tests := []struct {
		name    string
		level   Verbosity
		wantOut string
		wantErr string
	}{
		{"silent", VerbositySilent, "", ""},
		{"json", VerbosityJSON, "", "error\n"},
		{"quiet", VerbosityQuiet, "quiet\nquietonly\n", "error\n"},
		{"normal", VerbosityNormal, "printf\nprintln\nquiet\n", "error\n"},
		{"verbose", VerbosityVerbose, "printf\nprintln\nverbose\nquiet\n", "error\n"},
	}
	for _, tc := range tests {
		reset()
		Level = tc.level

		Printf("<cyan>printf</>\n")
		Println("<cyan>println</>")
		Verbosef("verbose\n")
		Quietf("quiet\n")
		QuietOnlyf("quietonly\n")
		Errorf("<red>error</>\n")

		if got := out.String(); got != tc.wantOut {
			t.Errorf("%s: out = %q, want %q", tc.name, got, tc.wantOut)
		}
		if got := errBuf.String(); got != tc.wantErr {
			t.Errorf("%s: err = %q, want %q", tc.name, got, tc.wantErr)
		}
	}

	// Writer follows Level too
	Level = VerbosityQuiet
	if Writer() != io.Discard {
		t.Error("Writer() at quiet is not io.Discard")
	}
	Level = VerbosityNormal
	if Writer() != &out {
		t.Error("Writer() at normal is not Out")
	}
}

//nolint:paralleltest // mutates package-level Level and Out
func TestDefaultsAreStdoutAndStderr(t *testing.T) {
	origLevel, origOut, origErr := Level, Out, Err
	t.Cleanup(func() { Level, Out, Err = origLevel, origOut, origErr })

	if Out != os.Stdout || Err != os.Stderr || Level != VerbosityNormal {
		t.Errorf("defaults = (Level %s, Out %v, Err %v), want (normal, os.Stdout, os.Stderr)", Level, Out, Err)
	}

	// a data-channel tool redirects Out and error text still lands on Err
	var buf bytes.Buffer
	Out, Err = &buf, &buf
	Printf("progress\n")
	Errorf("failed\n")
	if !strings.Contains(buf.String(), "progress") || !strings.Contains(buf.String(), "failed") {
		t.Errorf("redirected output = %q", buf.String())
	}
}
