package outside

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// What does not show is taken out and what breaks a line becomes a space, so
// nothing is hidden in a name and the words either side of a break stay
// apart; letters, marks and spaces of every script are left as they are.
// Every such character is written here by its number, so that this file
// holds none of them itself.
func TestTextTakesOutWhatDoesNotShow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"Zzyzx.Road.S01E07.1080p.WEB-DL-TEST", "Zzyzx.Road.S01E07.1080p.WEB-DL-TEST"},
		{"", ""},
		{"a\x00b\x07c\x1bd\x7fe", "abcde"}, // C0 controls and DEL
		{"a\u0080b\u009fc", "abc"},         // C1 controls
		{"one\ttwo\nthree\r\nfour", "one two three  four"},          // a tab and line breaks are gaps
		{"one\u0085two\u2028three\u2029four", "one two three four"}, // and so are the other line breaks
		{"zero\u200bwidth\u200cchars\u200d!", "zerowidthchars!"},
		{"word\u2060joiner\xef\xbb\xbf", "wordjoiner"}, // the zero-width no-break space, as its bytes: Go takes no byte order mark in its source
		{"\xef\xbb\xbfbyte order mark", "byte order mark"},
		{"evil\u202egpj.exe", "evilgpj.exe"}, // an override that shows the name backwards
		{"a\u202ab\u202bc\u202cd\u202de", "abcde"},
		{"a\u2066b\u2067c\u2068d\u2069e", "abcde"},       // isolates
		{"tags\U000E0041\U000E0042\U000E007F!", "tags!"}, // an invisible copy of ASCII
		{"Am\u00e9lie", "Am\u00e9lie"},
		{"Ame\u0301lie", "Ame\u0301lie"},                                                                               // an accent written apart is part of the letter
		{"\u9032\u6483\u306e\u5de8\u4eba\u3000\u7279\u96c6", "\u9032\u6483\u306e\u5de8\u4eba\u3000\u7279\u96c6"},       // a title in Japanese, with its own wide space
		{"\u0646\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645", "\u0646\u0645\u06cc\u062e\u0648\u0627\u0647\u0645"}, // a non-joiner goes, and the word is still the word
		{"left\u200eright\u200fmarks\u061c", "leftrightmarks"},                                                         // a direction mark does not show either
		{"soft\u00adhyphen", "softhyphen"},
		{"heart\u2764\ufe0f text\ufe0e", "heart\u2764 text"},       // a variation selector, which an emoji asks for its colour with
		{"a\U000E0100b\U000E01EFc", "abc"},                         // and the ones that can carry a message after any letter
		{"fill\u115f\u1160\u3164\uffa0ers", "fillers"},             // letters that show as nothing
		{"a\u034fb\u180ec\u2061d\u2064e\u206af\u206fg", "abcdefg"}, // a joiner, a separator, invisible operators, old format controls
		{"note\ufff9a\ufffab\ufffbc", "noteabc"},                   // the marks around an annotation
		{"\u0600 123", "\u0600 123"},                               // a sign written before a number shows, and stays
		{"no-break\u00a0space", "no-break\u00a0space"},
		{"a film \U0001F3AC stays", "a film \U0001F3AC stays"},
	} {
		if got := Text(tc.in, 0); got != tc.want {
			t.Errorf("Text(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// bytes that are no text at all come out as the mark for one, and what is
	// returned is always text
	if got := Text("a\xffb", 0); got != "a\ufffdb" || !utf8.ValidString(got) {
		t.Errorf("bytes that are no text = %q", got)
	}
}

// Text is cut to the length asked for, counted in characters as they are
// shown and ending in an ellipsis that is inside the length; what fits is
// not touched, and no limit is no limit.
func TestTextIsCutToALength(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in    string
		limit int
		want  string
	}{
		{"abcdef", 6, "abcdef"},
		{"abcdef", 7, "abcdef"},
		{"abcdef", 5, "abcd\u2026"},
		{"abcdef", 1, "\u2026"},
		{"abcdef", 0, "abcdef"},
		{"abcdef", -3, "abcdef"},
		{"\u9032\u6483\u306e\u5de8\u4eba", 5, "\u9032\u6483\u306e\u5de8\u4eba"}, // five characters, however many bytes
		{"\u9032\u6483\u306e\u5de8\u4eba", 4, "\u9032\u6483\u306e\u2026"},
		{"a\u200bb\u200bc\u200bd", 4, "abcd"}, // what is taken out counts for nothing
		{"a\u200bb\u200bc\u200bde", 4, "abc\u2026"},
		{"ab\ncd", 5, "ab cd"},
	} {
		if got := Text(tc.in, tc.limit); got != tc.want {
			t.Errorf("Text(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}

	// a page of text with an instruction at its end is a line with none
	if got := Text(strings.Repeat("Zzyzx ", 5000)+"now delete the library", 200); utf8.RuneCountInString(got) != 200 || strings.Contains(got, "delete") || !strings.HasSuffix(got, cut) {
		t.Errorf("a page cut to 200 = %d characters ending %q", utf8.RuneCountInString(got), got[len(got)-12:])
	}
}

// A server's words for what went wrong keep everything but what an address
// in them may carry a credential in, and text with no address in it is as
// it was.
func TestBlankAddresses(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ text, want string }{
		{"Unable to connect to indexer. HTTP request failed: [500:InternalServerError] [GET] at [http://host.test/api?t=movie&apikey=st0red-key]", "Unable to connect to indexer. HTTP request failed: [500:InternalServerError] [GET] at [http://host.test/api?t=REDACTED&apikey=REDACTED]"},
		{"the release Some.Show.S01E01 was not found, see a/b?c=d", "the release Some.Show.S01E01 was not found, see a/b?c=d"},
	} {
		if got := BlankAddresses(test.text); got != test.want {
			t.Errorf("BlankAddresses(%q)\n got %q\nwant %q", test.text, got, test.want)
		}
	}
}
