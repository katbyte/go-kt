package whitespace

import (
	"slices"
	"testing"
)

// Every problem on its own, as a name, a file name and an original title
// read it: what is wrong, the text made visible, and the text put right.
func TestProblemsVisibleFixed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		text     string
		kind     Text
		problems []string
		visible  string
		fixed    string
	}{
		{"Zzyzx  Film", Name, []string{DoubleSpace}, "Zzyzx␣␣Film", "Zzyzx Film"},
		{"Zzyzx   Film", Name, []string{DoubleSpace}, "Zzyzx␣␣␣Film", "Zzyzx Film"},
		{" Zzyzx Film", Name, []string{EdgeSpace}, "␣Zzyzx Film", "Zzyzx Film"},
		{"Zzyzx Film ", Name, []string{EdgeSpace}, "Zzyzx Film␣", "Zzyzx Film"},
		{"Zzyzx : Film", Name, []string{BeforeColon}, "Zzyzx␣: Film", "Zzyzx: Film"},
		// the look-alike colon a renamer writes where a file name cannot hold
		// one, and its own spelling is no problem
		{"Zzyzx ꞉ Film", Name, []string{BeforeColon}, "Zzyzx␣꞉ Film", "Zzyzx꞉ Film"},
		{"Zzyzx꞉ Film", Name, nil, "Zzyzx꞉ Film", "Zzyzx꞉ Film"},
		// the spaces that are not the ordinary one, each named
		{"Zzyzx Film", Name, []string{OddSpace}, "Zzyzx[U+00A0]Film", "Zzyzx Film"},
		{"Zzyzx\tFilm", Name, []string{OddSpace}, "Zzyzx[U+0009]Film", "Zzyzx Film"},
		{"Zzyzx\nFilm", Name, []string{OddSpace}, "Zzyzx[U+000A]Film", "Zzyzx Film"},
		{"Zzyzx Film", Name, []string{OddSpace}, "Zzyzx[U+2009]Film", "Zzyzx Film"},
		{"Zzyzx Film", Name, []string{OddSpace}, "Zzyzx[U+202F]Film", "Zzyzx Film"},
		{"Zzyzx Film", Name, []string{OddSpace}, "Zzyzx[U+205F]Film", "Zzyzx Film"},
		{"Zzyzx Film ", Name, []string{OddSpace, EdgeSpace}, "Zzyzx Film[U+00A0]", "Zzyzx Film"},
		// the ideographic space is how Japanese titles are written
		{"進撃　巨人", Name, nil, "進撃　巨人", "進撃　巨人"},
		{"　進撃の巨人　", Name, nil, "　進撃の巨人　", "　進撃の巨人　"},
		// a file name is a stem and an extension
		{"Zzyzx Film (2001) .mkv", File, []string{BeforeExtension}, "Zzyzx Film (2001)␣.mkv", "Zzyzx Film (2001).mkv"},
		{"Zzyzx  Film (2001).mkv", File, []string{DoubleSpace}, "Zzyzx␣␣Film (2001).mkv", "Zzyzx Film (2001).mkv"},
		{"Zzyzx Film .mkv", File, []string{OddSpace, BeforeExtension}, "Zzyzx Film[U+00A0].mkv", "Zzyzx Film.mkv"},
		{" Zzyzx Film.mkv", File, []string{EdgeSpace}, "␣Zzyzx Film.mkv", "Zzyzx Film.mkv"},
		{"Zzyzx Film (2001).mkv", File, nil, "Zzyzx Film (2001).mkv", "Zzyzx Film (2001).mkv"},
		// a name whose last dot starts no extension is all stem
		{"Zzyzx Vs. The World ", File, []string{EdgeSpace}, "Zzyzx Vs. The World␣", "Zzyzx Vs. The World"},
		// an original title keeps its language's typography: French sets a
		// space, often a narrow no-break one, before a colon
		{"Zzyzx : Le Film", Foreign, nil, "Zzyzx : Le Film", "Zzyzx : Le Film"},
		{"Zzyzx : Le Film", Foreign, nil, "Zzyzx : Le Film", "Zzyzx : Le Film"},
		{"Zzyzx : Le Film", Foreign, nil, "Zzyzx : Le Film", "Zzyzx : Le Film"},
		// and the rest is still out of place there
		{"Zzyzx  : Le Film", Foreign, []string{DoubleSpace}, "Zzyzx␣␣: Le Film", "Zzyzx : Le Film"},
		{"Zzyzx\t: Le Film", Foreign, []string{OddSpace}, "Zzyzx[U+0009]: Le Film", "Zzyzx : Le Film"},
		{"Zzyzx : Le Film ", Foreign, []string{EdgeSpace}, "Zzyzx : Le Film␣", "Zzyzx : Le Film"},
	} {
		if got := Problems(tc.text, tc.kind); !slices.Equal(got, tc.problems) {
			t.Errorf("Problems(%q, %d) = %v, want %v", tc.text, tc.kind, got, tc.problems)
		}
		if got := Visible(tc.text, tc.kind); got != tc.visible {
			t.Errorf("Visible(%q, %d) = %q, want %q", tc.text, tc.kind, got, tc.visible)
		}
		if got := Fixed(tc.text, tc.kind); got != tc.fixed {
			t.Errorf("Fixed(%q, %d) = %q, want %q", tc.text, tc.kind, got, tc.fixed)
		}
		// what is put right has nothing left to report
		if left := Problems(Fixed(tc.text, tc.kind), tc.kind); len(left) != 0 {
			t.Errorf("%q put right as %q still has %v", tc.text, Fixed(tc.text, tc.kind), left)
		}
		// and every problem is one of the named ones, in their order
		for i := 1; i < len(tc.problems); i++ {
			if slices.Index(ProblemOrder, tc.problems[i-1]) >= slices.Index(ProblemOrder, tc.problems[i]) {
				t.Errorf("%v are not in ProblemOrder", tc.problems)
			}
		}
	}
}

// What a title holds where a name has two spaces: the colon a renamer
// dropped, or a word spelled in asterisks. Nothing when the words either
// side are not in the title, or the title holds another name there.
func TestDroppedAt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, title, want string }{
		{"Dune  Part Two (2024)", "Dune: Part Two", ":"},
		{"Zzyzx's  Is Here", "Zzyzx's ****** Is Here", "******"},
		{"Zzyzx  Film", "Zzyzx  Film", ""},
		{"Zzyzx  Film", "Something Else", ""},
		{"Zzyzx  Film", "Zzyzx and a whole other long title before the Film", ""},
		{"Zzyzx Film", "Zzyzx: Film", ""},
		{"  Zzyzx", "Zzyzx", ""},
	} {
		if got := DroppedAt(tc.name, tc.title); got != tc.want {
			t.Errorf("DroppedAt(%q, %q) = %q, want %q", tc.name, tc.title, got, tc.want)
		}
	}
}

func TestWords(t *testing.T) {
	t.Parallel()

	if IndexWord("Episode IV", "IV") != 8 || IndexWord("DIVE", "IV") != -1 || IndexWord("IV and IV", "IV") != 0 || IndexWord("", "IV") != -1 {
		t.Error("IndexWord")
	}
	if !IsWordChar('a') || !IsWordChar('٣') || IsWordChar(' ') || IsWordChar('-') {
		t.Error("IsWordChar")
	}
	for name, want := range map[string][2]string{
		"Zzyzx Film (2001).mkv": {"Zzyzx Film (2001)", ".mkv"},
		"Zzyzx Vs. The World":   {"Zzyzx Vs. The World", ""},
		".hidden":               {".hidden", ""},
		"a.longextension":       {"a.longextension", ""},
		"a.m kv":                {"a.m kv", ""},
	} {
		if stem, ext := SplitExt(name); stem != want[0] || ext != want[1] {
			t.Errorf("SplitExt(%q) = %q, %q; want %q, %q", name, stem, ext, want[0], want[1])
		}
	}
	if !Odd(' ') || Odd(' ') || Odd('　') || !Typographic(' ') || Typographic('\t') || !Any('\t') || !Any(' ') || Any('x') {
		t.Error("the kinds of space")
	}
	if !StartsWithSpace("\tx") || StartsWithSpace("x ") || !EndsWithSpace("x ") || EndsWithSpace(" x") {
		t.Error("the ends")
	}
}
