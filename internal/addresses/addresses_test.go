package addresses

import (
	"slices"
	"testing"
)

// An address in a piece of text is blanked where it may carry a credential
// and left where it does not, the words around it as they were. One written
// inside brackets ends at the bracket that closes them; any other mark
// against its end goes with what it carries.
func TestBlank(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ text, want string }{
		{"", ""},
		{"plain text, a/b and c:d?e=f", "plain text, a/b and c:d?e=f"},
		{"see https://docs.test/a/b ok", "see https://docs.test/a/b ok"},
		{"HTTP request failed: [500:InternalServerError] [GET] at [http://host.test/api?t=movie&cat=2000,2010&apikey=st0red-key]", "HTTP request failed: [500:InternalServerError] [GET] at [http://host.test/api?t=REDACTED&cat=REDACTED&apikey=REDACTED]"},
		{"failed at [http://host.test/api?apikey=st0red-key]: try again, or see (https://docs.test/help#indexers), then {ftp://a.test/?x=1}.", "failed at [http://host.test/api?apikey=REDACTED]: try again, or see (https://docs.test/help#REDACTED), then {ftp://a.test/?x=REDACTED}."},
		{"[http://[::1]:8080/x?k=v] and http://[::1]:8080/x?k=v", "[http://[::1]:8080/x?k=REDACTED] and http://[::1]:8080/x?k=REDACTED"},
		{"opened [http://host.test/a?f[0]=v and never closed", "opened [http://host.test/a?f[0]=REDACTED and never closed"},
		{"(see https://host.test/a?b=c). Next", "(see https://host.test/a?b=REDACTED Next"},
		{"https://feeds.test/rss?auth=PRIVATE-T0KEN&format=rss&flag&", "https://feeds.test/rss?auth=REDACTED&format=REDACTED&REDACTED&"},
		{"https://kt:s1gnin@host.test and https://t0ken@git.test/org/repo.git", "https://REDACTED@host.test and https://REDACTED@git.test/org/repo.git"},
		{"[http://]", "[http://]"},
	} {
		if got := Blank(test.text, nil); got != test.want {
			t.Errorf("Blank(%q)\n got %q\nwant %q", test.text, got, test.want)
		}
	}
}

// Each piece that was blanked is handed over, in the order it stood.
func TestBlankHandsOverWhatItBlanked(t *testing.T) {
	t.Parallel()

	var hidden []string
	Blank("at [https://kt:s1gnin@host.test/a?key=st0red-key&flag#fr4gment]", func(piece string) { hidden = append(hidden, piece) })
	if want := []string{"kt", "s1gnin", "st0red-key", "flag", "fr4gment"}; !slices.Equal(hidden, want) {
		t.Errorf("the pieces blanked were %q, want %q", hidden, want)
	}
}
