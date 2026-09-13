package version

import (
	"runtime/debug"
	"testing"
)

// A build must never report a blank version. The -X flag has been given an
// empty value before, which is not the same as not being given at all.
func TestVersionIsNeverBlank(t *testing.T) {
	t.Parallel()

	if Version == "" {
		t.Error("Version is blank; the build stamped an empty -X value")
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	withMain := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/katbyte/tool", Version: v}}
	}

	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{"stamped wins over build info", "v1.2.3", withMain("v9.9.9"), "v1.2.3"},
		{"stamped wins without build info", "v1.2.3", nil, "v1.2.3"},
		{"dev falls back to build info", "dev", withMain("v0.4.0"), "v0.4.0"},
		{"empty stamp falls back to build info", "", withMain("v0.4.0"), "v0.4.0"},
		{"dev with local build info stays dev", "dev", withMain("(devel)"), "dev"},
		{"dev with empty module version stays dev", "dev", withMain(""), "dev"},
		{"dev without build info stays dev", "dev", nil, "dev"},
		{"empty stamp without build info becomes dev", "", nil, "dev"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolve(tc.stamped, tc.info); got != tc.want {
				t.Errorf("resolve(%q) = %q, want %q", tc.stamped, got, tc.want)
			}
		})
	}
}
