package spelling

import "testing"

func TestKey(t *testing.T) {
	t.Parallel()

	t.Run("folds what means the same", func(t *testing.T) {
		t.Parallel()
		for in, want := range map[string]string{
			"Sci-Fi":             "sci fi",
			"  Sci-Fi / Drama ":  "sci fi drama",
			"science_fiction":    "science fiction",
			"Amélie":             "amelie",
			"Amélie":            "amelie", // the accent written as a separate mark
			"Straße":             "strasse",
			"Þór":                "thor",
			"Action & Adventure": "action adventure",
			"Yahoo!":             "yahoo",
			"ТИХИЙ ДОМ":          "тихий дом",
			"Zzyzx Studio Α":     "zzyzx studio α",
			"星の森　特集":             "星の森 特集", // an ideographic space is a space
			"नमस्ते":             "नमस्ते", // vowel signs are part of the word
		} {
			if got := Key(in); got != want {
				t.Errorf("Key(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("keeps every script apart", func(t *testing.T) {
		t.Parallel()
		for _, pair := range [][2]string{
			{"進撃の巨人", "鬼滅の刃"},
			{"Zzyzx Studio α", "Zzyzx Studio β"},
			{"Тихий дом", "Тихий сад"},
		} {
			a, b := Key(pair[0]), Key(pair[1])
			if a == "" || b == "" || a == b {
				t.Errorf("%q and %q are different values: %q against %q", pair[0], pair[1], a, b)
			}
		}
	})
}

func TestWordRune(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		r          rune
		afterLatin bool
		spelling   string
		word       bool
	}{
		{'é', false, "e", true},
		{'ß', false, "ss", true},
		{'の', false, "の", true},
		{'٣', false, "٣", true},    // a digit of another script
		{0x0301, true, "", true},   // a combining accent on a Latin letter: folded away
		{0x094D, false, "्", true}, // a mark in a script that writes vowels as marks: kept
		{'!', false, "", false},
		{'　', false, "", false},
	} {
		if spelling, word := WordRune(tc.r, tc.afterLatin); spelling != tc.spelling || word != tc.word {
			t.Errorf("WordRune(%U, %v) = %q, %v; want %q, %v", tc.r, tc.afterLatin, spelling, word, tc.spelling, tc.word)
		}
	}
	if FoldLetter('x') != "" || FoldLetter('ø') != "o" {
		t.Error("FoldLetter folds a plain letter, or not ø")
	}
}

func TestMarks(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"Discovery":          "",
		"discovery+":         "+",
		"Idea(L)":            "()",
		"Yahoo!":             "!",
		"Action & Adventure": "",
		"O'Neil":             "",
		"Amélie":             "",
	} {
		if got := Marks(in); got != want {
			t.Errorf("Marks(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTypoApart(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"teenage life", "teenage love", false},
		{"teenage life", "teenage lift", false},
		{"coming of age", "coming of rage", false},
		{"martial arts film", "martial arts flim", true},
		{"science fiction", "science fictoin", true},
		{"superhero", "superheros", true},
		{"sciencefiction", "science fiction", true},
		{"time travel", "time travell", true},
		{"cyberpunk noir", "cyberpunk nr", true}, // letters dropped from a word's middle
		{"film nior", "film noir", true},         // two letters swapped in a short word
		{"kids flim", "kids film", true},
		{"road tirp", "road trip", true},
		{"teenage love", "teenage lve", true},
		{"coming of age", "coming of ace", false},                // a short word changed: another word
		{"romance", "romance", false},                            // the same key is no typo
		{"drama", "dramas", false},                               // too short to call a slip
		{"science fiction films", "science fictoin flims", true}, // two slips in twelve letters, the first word the same
		{"adventure films", "adventrue flims", false},            // two slips, but the first word is not the same
	} {
		if got := TypoApart(Key(tc.a), Key(tc.b)); got != tc.want {
			t.Errorf("TypoApart(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	// a length is counted in letters: two ideographs are too short to call
	// a typo apart, however many bytes they take
	if TypoApart(Key("星光"), Key("月光")) || TruncationOf(Key("東星"), Key("東星 映画")) {
		t.Error("two-letter values were judged as long ones")
	}
	if Letters("星光") != 2 || Letters("noir") != 4 {
		t.Error("Letters counts bytes")
	}
}

func TestTruncationOf(t *testing.T) {
	t.Parallel()

	if !TruncationOf("warner bros", "warner bros pictures") {
		t.Error("warner bros is warner bros pictures cut off")
	}
	for _, pair := range [][2]string{
		{"a24", "a24 films"},           // too short
		{"warner", "warnerbros"},       // not whole words
		{"warner bros", "warner bros"}, // the same
		{"warner bros pictures", "warner bros"},
	} {
		if TruncationOf(pair[0], pair[1]) {
			t.Errorf("TruncationOf(%q, %q) is true", pair[0], pair[1])
		}
	}
}

func TestInitialsOf(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		short, long string
		want        bool
	}{
		{"c z dunn", "christian dunn", true},
		{"j k rowling", "joanne rowling", true},
		{"j smith", "john smith", true},
		{"john smith", "j smith", true},         // either may carry the initial
		{"é dumas", "émile dumas", true},        // one letter, however many bytes
		{"jim dale", "jim dole", false},         // another last name
		{"j smith", "john smyth", false},        // an initial of the first name, and another last name
		{"chris dunn", "christian dunn", false}, // a shortened first name is not an initial
		{"joe hill", "joey w hill", false},      // two people
		{"dunn", "christian dunn", false},       // one word is not a name with initials
		{"a lee", "b lee", false},               // two different initials
		{"j li", "john li", false},              // a last name too short to tell people apart by
		{"j smith", "j smith", false},           // the same
		{"j smith", "j r smith", false},         // both initials: nothing was reduced
	} {
		if got := InitialsOf(tc.short, tc.long); got != tc.want {
			t.Errorf("InitialsOf(%q, %q) = %v, want %v", tc.short, tc.long, got, tc.want)
		}
	}
}

func TestWordSlips(t *testing.T) {
	t.Parallel()

	if !Swapped("nior", "noir") || Swapped("noir", "noir") || Swapped("nr", "rn") || Swapped("niro", "noir") {
		t.Error("Swapped")
	}
	if !DroppedFromMiddle("nr", "noir") || DroppedFromMiddle("noi", "noir") || DroppedFromMiddle("oir", "noir") || DroppedFromMiddle("noir", "noir") || DroppedFromMiddle("n", "noir") {
		t.Error("DroppedFromMiddle")
	}
	if !SlipsInWords("film noir", "film nior") || SlipsInWords("teenage life", "teenage love") || !SlipsInWords("sciencefiction", "science fiction") {
		t.Error("SlipsInWords")
	}
}

func TestDistance(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b  string
		limit int
		want  int
	}{
		{"noir", "noir", 2, 0},
		{"noir", "nior", 2, 1}, // a transposition is one edit
		{"film", "flim", 2, 1},
		{"fiction", "fictoin", 2, 1},
		{"abc", "xyz", 2, 3}, // past the limit comes back as limit+1
		{"a", "abcd", 2, 3},  // lengths too far apart are not walked
		{"héllo", "hello", 1, 1},
		{"", "ab", 2, 2},
	} {
		if got := Distance(tc.a, tc.b, tc.limit); got != tc.want {
			t.Errorf("Distance(%q, %q, %d) = %d, want %d", tc.a, tc.b, tc.limit, got, tc.want)
		}
	}
}
