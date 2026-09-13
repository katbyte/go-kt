package cout

import (
	"reflect"
	"testing"
)

func TestLevelFromFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		silent, quiet, verbose bool
		want                    Verbosity
	}{
		{"none set is normal", false, false, false, VerbosityNormal},
		{"verbose", false, false, true, VerbosityVerbose},
		{"quiet", false, true, false, VerbosityQuiet},
		{"silent", true, false, false, VerbositySilent},
		{"quiet beats verbose", false, true, true, VerbosityQuiet},
		{"silent beats quiet", true, true, false, VerbositySilent},
		{"silent beats everything", true, true, true, VerbositySilent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := LevelFromFlags(tc.silent, tc.quiet, tc.verbose); got != tc.want {
				t.Errorf("LevelFromFlags(%v, %v, %v) = %s, want %s", tc.silent, tc.quiet, tc.verbose, got, tc.want)
			}
			f := Flags{Silent: tc.silent, Quiet: tc.quiet, Verbose: tc.verbose}
			if got := f.Level(); got != tc.want {
				t.Errorf("Flags%+v.Level() = %s, want %s", f, got, tc.want)
			}
		})
	}
}

// the tags are what let viper fill an embedded Flags, so they are part of the contract
func TestFlagsMapstructureTags(t *testing.T) {
	t.Parallel()

	want := map[string]string{"Silent": "silent", "Quiet": "quiet", "Verbose": "verbose"}
	rt := reflect.TypeFor[Flags]()
	if rt.NumField() != len(want) {
		t.Fatalf("Flags has %d fields, want %d", rt.NumField(), len(want))
	}
	for name, tag := range want {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Fatalf("Flags has no field %s", name)
		}
		if got := f.Tag.Get("mapstructure"); got != tag {
			t.Errorf("Flags.%s mapstructure tag = %q, want %q", name, got, tag)
		}
	}
}

// Apply and SetLevelFromFlags write the package Level, so they run serially.
//
//nolint:paralleltest // mutates package-level Level
func TestApplyAndSetLevelFromFlags(t *testing.T) {
	orig := Level
	t.Cleanup(func() { Level = orig })

	Level = VerbosityNormal
	Flags{Quiet: true}.Apply()
	if Level != VerbosityQuiet {
		t.Errorf("after Flags{Quiet}.Apply(): Level = %s, want quiet", Level)
	}

	SetLevelFromFlags(false, false, true)
	if Level != VerbosityVerbose {
		t.Errorf("after SetLevelFromFlags(verbose): Level = %s, want verbose", Level)
	}

	SetLevelFromFlags(false, false, false)
	if Level != VerbosityNormal {
		t.Errorf("after SetLevelFromFlags(none): Level = %s, want normal", Level)
	}
}
