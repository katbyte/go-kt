package text_test

import (
	"testing"

	"github.com/katbyte/go-kt/text"
)

// A description is cut at the end of its first sentence on its first line, not at the full stop of an abbreviation, and always ends as a sentence.
func TestFirstSentence(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"Lists the series. Paged, 50 to a page.":                                    "Lists the series.",
		"Sets the cover from a url (e.g. from item_cover_search). Writes the item.": "Sets the cover from a url (e.g. from item_cover_search).",
		"Finds books, films, etc. by title. Paged.":                                 "Finds books, films, etc. by title.",
		"  Gets a book\n\nThe whole of it, chapters and all.":                       "Gets a book.",
		"Is the server up? It is asked every minute.":                               "Is the server up?",
		"Version 1.5 is out. It is current.":                                        "Version 1.5 is out.",
		"Stop!":                                                                     "Stop!",
		"Is the server up?":                                                         "Is the server up?",
		"One sentence and no more":                                                  "One sentence and no more.",
		"   ":                                                                       "",
	} {
		if got := text.FirstSentence(in); got != want {
			t.Errorf("FirstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"one line":                "one line",
		"  first \n second":       "first",
		"\n\n  after blanks \nno": "after blanks",
		"":                        "",
	} {
		if got := text.FirstLine(in); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArticle(t *testing.T) {
	t.Parallel()

	for word, want := range map[string]string{"indexer": "an", "Indexer": "an", "notification": "a", "": "a"} {
		if got := text.Article(word); got != want {
			t.Errorf("Article(%q) = %q, want %q", word, got, want)
		}
	}
}
