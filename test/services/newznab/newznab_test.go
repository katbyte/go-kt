package newznab

import (
	"cmp"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testKey = "indexer-key"

// The releases the tests search: three of a series, one of them a season
// pack, and two films whose names start alike.
const (
	episode7   = "Zzyzx.Road.S01E07.1080p.WEB-DL-TEST"
	episode8   = "Zzyzx.Road.S01E08.2160p.WEB-DL-TEST"
	seasonPack = "Zzyzx.Road.S02.1080p.WEB-DL-TEST"
	film       = "Quux.2009.1080p.BluRay.x264-TEST"
	otherFilm  = "Quuxotic.2011.1080p.BluRay.x264-TEST"
)

func catalogue() []Release {
	return []Release{
		{Title: episode7, TVDBID: 111, TvMazeID: 11, Season: 1, Episode: 7, Category: CategoryTVHD, Size: 3 << 30},
		{Title: episode8, TVDBID: 111, Season: 1, Episode: 8, Category: CategoryTVUHD},
		{Title: seasonPack, TVDBID: 111, Season: 2, Category: CategoryTVHD},
		{Title: film, TMDBID: 222, IMDBID: "tt0000333", Category: CategoryMoviesHD},
		{Title: otherFilm, TMDBID: 444, IMDBID: "555", Category: CategoryMoviesHD},
	}
}

// The mirrors below read the answers the way an *arr's parsers do: an attr
// by its name whatever its namespace, and an error by its code.
type feedDoc struct {
	Channel struct {
		Response struct {
			Offset int `xml:"offset,attr"`
			Total  int `xml:"total,attr"`
		} `xml:"response"`
		Items []itemDoc `xml:"item"`
	} `xml:"channel"`
}

type itemDoc struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	Link      string `xml:"link"`
	Category  string `xml:"category"`
	Enclosure struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Attrs []struct {
		XMLName xml.Name
		Name    string `xml:"name,attr"`
		Value   string `xml:"value,attr"`
	} `xml:"attr"`
}

// attrs is every value an item carries under a name, in order.
func (i *itemDoc) attrs(name string) []string {
	var out []string
	for _, a := range i.Attrs {
		if a.Name == name {
			out = append(out, a.Value)
		}
	}

	return out
}

type errorDoc struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

type capsDoc struct {
	Server struct {
		Title string `xml:"title,attr"`
	} `xml:"server"`
	Limits struct {
		Max     int `xml:"max,attr"`
		Default int `xml:"default,attr"`
	} `xml:"limits"`
	Searching struct {
		Kinds []struct {
			XMLName   xml.Name
			Available string `xml:"available,attr"`
			Params    string `xml:"supportedParams,attr"`
		} `xml:",any"`
	} `xml:"searching"`
	Categories []struct {
		ID   int    `xml:"id,attr"`
		Name string `xml:"name,attr"`
		Subs []struct {
			ID   int    `xml:"id,attr"`
			Name string `xml:"name,attr"`
		} `xml:"subcat"`
	} `xml:"categories>category"`
}

// kind is what the capabilities say of one kind of search: whether it is
// offered, and with which parameters.
func (c *capsDoc) kind(name string) (available, params string) {
	for _, k := range c.Searching.Kinds {
		if k.XMLName.Local == name {
			return k.Available, k.Params
		}
	}

	return "", ""
}

// serve starts a server with one usenet indexer, "tv", that asks for testKey
// and holds the catalogue, and whatever other indexers are named.
func serve(t *testing.T, others ...Site) (*Server, *Indexer) {
	t.Helper()

	s, err := New(Options{Sites: append([]Site{{Name: "tv", APIKey: testKey, Releases: catalogue()}}, others...)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tv := s.Indexer("tv")

	return s, tv
}

// newestFirst is titles in the order a feed lists them: the newest first by
// the date each has in the catalogue, and by title where two were posted at
// once. A release that did not say when it was posted was given a date, so a
// test reads the order off what was given and not off what it hopes.
func newestFirst(ix *Indexer, titles ...string) []string {
	posted := map[string]time.Time{}
	for _, r := range ix.Releases() {
		posted[r.Title] = r.PubDate
	}
	out := slices.Clone(titles)
	slices.SortFunc(out, func(a, b string) int { return cmp.Or(posted[b].Compare(posted[a]), cmp.Compare(a, b)) })

	return out
}

// get fetches an address and returns the answer whole.
func get(t *testing.T, target string) (status int, header http.Header, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return resp.StatusCode, resp.Header, string(raw)
}

// api calls an indexer's API with its key and the parameters given, a
// function first: api(t, ix, "tvsearch", "tvdbid", "111").
func api(t *testing.T, ix *Indexer, function string, params ...string) (status int, body string) {
	t.Helper()

	q := url.Values{"t": {function}, "apikey": {testKey}}
	for i := 0; i+1 < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	status, _, body = get(t, ix.LocalURL()+"/api?"+q.Encode())

	return status, body
}

// feedOf is the feed a search answers, read.
func feedOf(t *testing.T, ix *Indexer, function string, params ...string) feedDoc {
	t.Helper()

	status, body := api(t, ix, function, params...)
	if status != http.StatusOK {
		t.Fatalf("%s %v = %d: %s", function, params, status, body)
	}
	var f feedDoc
	if err := xml.Unmarshal([]byte(body), &f); err != nil {
		t.Fatalf("%s %v is no feed: %v\n%s", function, params, err, body)
	}

	return f
}

// titles is the titles a feed lists, in its order.
func titles(f feedDoc) []string {
	out := make([]string, 0, len(f.Channel.Items))
	for _, i := range f.Channel.Items {
		out = append(out, i.Title)
	}

	return out
}

// refused is the Newznab error an answer is, read.
func refused(t *testing.T, body string) errorDoc {
	t.Helper()

	var e errorDoc
	if err := xml.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("no error document: %v\n%s", err, body)
	}

	return e
}

// An indexer says what it can do without being shown a key: the page it
// serves, the kinds of search it offers and their parameters, and its
// categories, each as its Site says or as the defaults do.
func TestCaps(t *testing.T) {
	t.Parallel()

	music := Site{Name: "music", Searches: []string{SearchText, SearchMusic}, TVSearchParams: []string{"q", "imdbid"}, PageSize: 25, Categories: []Category{{ID: CategoryAudio, Name: "Audio"}}}
	s, tv := serve(t, music)

	for _, key := range []string{"", "apikey=wrong", "apikey=" + testKey} {
		status, _, body := get(t, tv.LocalURL()+"/api?t=caps&"+key)
		var c capsDoc
		if err := xml.Unmarshal([]byte(body), &c); err != nil || status != http.StatusOK {
			t.Fatalf("caps with %q = %d, %v: %s", key, status, err, body)
		}
		if c.Server.Title != "tv" || c.Limits.Max != DefaultPageSize || c.Limits.Default != DefaultPageSize {
			t.Errorf("caps with %q: title %q, limits %+v", key, c.Server.Title, c.Limits)
		}
		for kind, want := range map[string][2]string{SearchText: {"yes", "q"}, SearchTV: {"yes", "q,season,ep,tvdbid"}, SearchMovie: {"yes", "q,imdbid,tmdbid"}, SearchMusic: {"no", "q,artist,album"}, searchAudio: {"no", "q,artist,album"}, SearchBook: {"no", "q,author,title"}} {
			if available, params := c.kind(kind); available != want[0] || params != want[1] {
				t.Errorf("%s is available=%q with %q, want %q with %q", kind, available, params, want[0], want[1])
			}
		}
		if len(c.Categories) != 2 || c.Categories[0].ID != CategoryMovies || len(c.Categories[0].Subs) != 3 || c.Categories[1].ID != CategoryTV || len(c.Categories[1].Subs) != 8 {
			t.Errorf("the default categories = %+v", c.Categories)
		}
	}

	_, _, body := get(t, s.Indexer("music").LocalURL()+"/api?t=caps")
	var c capsDoc
	if err := xml.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	if c.Limits.Max != 25 || len(c.Categories) != 1 || c.Categories[0].Name != "Audio" {
		t.Errorf("a site's own page size and categories: %+v, %+v", c.Limits, c.Categories)
	}
	// an indexer that offers music offers it under both of its names
	for kind, want := range map[string][2]string{SearchTV: {"no", "q,imdbid"}, SearchMovie: {"no", "q,imdbid,tmdbid"}, SearchMusic: {"yes", "q,artist,album"}, searchAudio: {"yes", "q,artist,album"}} {
		if available, params := c.kind(kind); available != want[0] || params != want[1] {
			t.Errorf("music's %s is available=%q with %q, want %q with %q", kind, available, params, want[0], want[1])
		}
	}
}

// A search answers the releases it matches, newest first and by title where
// two were posted at once: by category or its parent, by any of the ids asked
// for, by season and episode, and by every word of the text as a whole word.
// Ids belong to a TV or a movie search, and season and episode to a TV one.
func TestSearch(t *testing.T) {
	t.Parallel()

	_, ix := serve(t)
	all := []string{film, otherFilm, episode7, episode8, seasonPack}

	for _, tc := range []struct {
		function string
		params   []string
		want     []string
	}{
		{"tvsearch", nil, all}, // the RSS sync: everything
		{"tvsearch", []string{"cat", "5000"}, []string{episode7, episode8, seasonPack}},
		{"tvsearch", []string{"cat", "5045"}, []string{episode8}},
		{"tvsearch", []string{"cat", "5030,5040"}, []string{episode7, seasonPack}},
		{"tvsearch", []string{"tvdbid", "111", "season", "1", "ep", "7"}, []string{episode7}},
		{"tvsearch", []string{"tvdbid", "111", "season", "S01"}, []string{episode7, episode8}},
		{"tvsearch", []string{"tvdbid", "111", "season", "2"}, []string{seasonPack}},
		{"tvsearch", []string{"tvdbid", "999"}, []string{}},
		{"tvsearch", []string{"tvdbid", "999", "tvmazeid", "11"}, []string{episode7}}, // any id asked for
		{"movie", []string{"tmdbid", "222"}, []string{film}},
		{"movie", []string{"imdbid", "tt0000333"}, []string{film}},
		{"movie", []string{"imdbid", "333"}, []string{film}},
		{"movie", []string{"imdbid", "tt0000555"}, []string{otherFilm}}, // a release's id written without its tt
		{"movie", []string{"cat", "2000", "q", "1080p bluray"}, []string{film, otherFilm}},
		{"search", []string{"q", "quux"}, []string{film}}, // a whole word, not the start of one
		{"search", []string{"q", "ZZYZX 2160p"}, []string{episode8}},
		{"search", []string{"q", "zzyzx", "season", "2", "tvdbid", "999"}, []string{episode7, episode8, seasonPack}}, // a plain search is text alone
		{"movie", []string{"season", "9"}, all},                                                                      // season is a TV search's
	} {
		if got, want := titles(feedOf(t, ix, tc.function, tc.params...)), newestFirst(ix, tc.want...); !slices.Equal(got, want) {
			t.Errorf("%s %v = %v, want %v", tc.function, tc.params, got, want)
		}
	}

	// newest first
	if err := ix.Offer(Release{Title: "Zzyzx.Road.S03E01.1080p.WEB-DL-TEST", Category: CategoryTVHD, PubDate: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := titles(feedOf(t, ix, "tvsearch")); len(got) != 6 || got[0] != "Zzyzx.Road.S03E01.1080p.WEB-DL-TEST" {
		t.Errorf("with a newer release the feed = %v, want it first", got)
	}
}

// An item carries what an *arr reads a release from: links built from the
// address the container uses, the key in the link it fetches, the size, the
// categories, the ids, and for usenet the group and poster.
func TestFeedItem(t *testing.T) {
	t.Parallel()

	s, err := New(Options{PublicHost: "host.test", Sites: []Site{{Name: "tv", APIKey: testKey, Releases: []Release{{Title: episode7, Category: CategoryTVHD, TVDBID: 111, TvMazeID: 11, IMDBID: "tt0000333", Season: 1, Episode: 7, Size: 3 << 30, Files: 4, Languages: []string{"English", "German"}, Scene: true, Nuked: true}}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ix := s.Indexer("tv")

	base := "http://host.test:" + strconv.Itoa(s.Port()) + "/tv"
	if ix.URL() != base || ix.LocalURL() != "http://127.0.0.1:"+strconv.Itoa(s.Port())+"/tv" || ix.Name() != "tv" {
		t.Errorf("URL %q, LocalURL %q, Name %q", ix.URL(), ix.LocalURL(), ix.Name())
	}

	f := feedOf(t, ix, "tvsearch")
	if len(f.Channel.Items) != 1 || f.Channel.Response.Total != 1 {
		t.Fatalf("the feed = %+v", f)
	}
	item := f.Channel.Items[0]
	guid := ix.Releases()[0].GUID
	if item.GUID != base+"/details/"+guid || item.Link != base+"/download/"+guid+"?apikey="+testKey || item.Enclosure.URL != item.Link {
		t.Errorf("guid %q, link %q, enclosure %q", item.GUID, item.Link, item.Enclosure.URL)
	}
	if item.Enclosure.Length != 3<<30 || item.Enclosure.Type != "application/x-nzb" || item.Category != "TV > HD" {
		t.Errorf("enclosure %+v, category %q", item.Enclosure, item.Category)
	}
	for name, want := range map[string][]string{
		"category": {"5000", "5040"}, "size": {"3221225472"}, "guid": {guid}, "grabs": {"0"}, "files": {"4"}, "group": {"alt.binaries.test"}, "password": {"0"},
		"tvdbid": {"111"}, "tvmazeid": {"11"}, "imdb": {"0000333"}, "season": {"1"}, "episode": {"7"}, "language": {"English, German"}, "prematch": {"1"}, "nuked": {"1"},
		"tmdbid": nil, "seeders": nil, "infohash": nil,
	} {
		if got := item.attrs(name); !slices.Equal(got, want) {
			t.Errorf("attr %s = %v, want %v", name, got, want)
		}
	}
	if len(item.attrs("poster")) != 1 || len(item.attrs("usenetdate")) != 1 {
		t.Errorf("a usenet item has a poster and a usenet date: %+v", item.Attrs)
	}
	for _, a := range item.Attrs {
		if a.XMLName.Space != newznabNamespace {
			t.Errorf("attr %s is in the namespace %q, want newznab's", a.Name, a.XMLName.Space)
		}
	}
}

// A torrent indexer answers in Torznab: its attrs in that namespace, with
// what a torrent has in place of what a usenet post has, and a .torrent a
// client can parse where the NZB would be.
func TestTorrent(t *testing.T) {
	t.Parallel()

	s, _ := serve(t, Site{Name: "torrents", Protocol: Torrent, Releases: []Release{{Title: film, Category: CategoryMoviesHD, TMDBID: 222, Size: 9 << 20, Seeders: 5, Peers: 2, Freeleech: true}, {Title: otherFilm, Category: CategoryMoviesHD}}})
	ix := s.Indexer("torrents")

	f := feedOf(t, ix, "movie", "tmdbid", "222")
	if len(f.Channel.Items) != 1 {
		t.Fatalf("the feed = %+v", f)
	}
	item := f.Channel.Items[0]
	if item.Enclosure.Type != "application/x-bittorrent" || item.Category != "Movies > HD" {
		t.Errorf("enclosure %+v, category %q", item.Enclosure, item.Category)
	}
	for name, want := range map[string][]string{"seeders": {"5"}, "peers": {"7"}, "downloadvolumefactor": {"0"}, "uploadvolumefactor": {"1"}, "tmdbid": {"222"}, "files": nil, "group": nil, "usenetdate": nil} {
		if got := item.attrs(name); !slices.Equal(got, want) {
			t.Errorf("attr %s = %v, want %v", name, got, want)
		}
	}
	hash := item.attrs("infohash")
	if len(hash) != 1 || len(hash[0]) != 40 {
		t.Errorf("infohash = %v, want forty hex digits", hash)
	}
	for _, a := range item.Attrs {
		if a.XMLName.Space != torznabNamespace {
			t.Errorf("attr %s is in the namespace %q, want torznab's", a.Name, a.XMLName.Space)
		}
	}
	// a torrent that is not freeleech costs its size
	if got := feedOf(t, ix, "search", "q", "quuxotic").Channel.Items[0].attrs("downloadvolumefactor"); !slices.Equal(got, []string{"1"}) {
		t.Errorf("downloadvolumefactor of a torrent that is not freeleech = %v", got)
	}

	// three pieces of four megabytes hold nine, and each has a hash of twenty bytes
	status, header, body := get(t, item.Link)
	if status != http.StatusOK || header.Get("Content-Type") != "application/x-bittorrent" || !strings.HasSuffix(header.Get("Content-Disposition"), `.torrent"`) {
		t.Errorf("the torrent = %d, %v", status, header)
	}
	if !strings.HasPrefix(body, "d8:announce") || !strings.Contains(body, "6:lengthi9437184e") || !strings.Contains(body, "6:pieces60:") {
		t.Errorf("the torrent = %q", body)
	}
	if got := ix.Grabbed(); !slices.Equal(got, []string{film}) {
		t.Errorf("grabbed = %v", got)
	}

	// a torrent that never said its size was given one, which the feed and
	// the torrent both say: a torrent of nothing is one no client takes
	unsaid, given := feedOf(t, ix, "search", "q", "quuxotic").Channel.Items[0], ix.Releases()[1].Size
	if given < torrentSizes[0][0] || given > torrentSizes[len(torrentSizes)-1][1] {
		t.Errorf("a torrent that never said its size was given %d", given)
	}
	if got := unsaid.attrs("size"); !slices.Equal(got, []string{strconv.FormatInt(given, 10)}) || unsaid.Enclosure.Length != given {
		t.Errorf("the feed's size of it = %v and %d, want the %d it was given", got, unsaid.Enclosure.Length, given)
	}
	pieces := (given + torrentPieceLength - 1) / torrentPieceLength
	if _, _, body := get(t, unsaid.Link); !strings.Contains(body, fmt.Sprintf("6:lengthi%de", given)) || !strings.Contains(body, fmt.Sprintf("6:pieces%d:", 20*pieces)) {
		t.Errorf("its torrent = %.80q, want %d bytes in %d pieces", body, given, pieces)
	}
}

// A feed is served a page at a time: the site's page size unless a smaller
// one is asked for, from the offset asked for, with the total beside it.
func TestPaging(t *testing.T) {
	t.Parallel()

	s, _ := serve(t, Site{Name: "small", PageSize: 2, Releases: catalogue()})
	ix := s.Indexer("small")

	order := newestFirst(ix, film, otherFilm, episode7, episode8, seasonPack)

	for _, tc := range []struct {
		params []string
		offset int
		want   []string
	}{
		{nil, 0, order[:2]},
		{[]string{"offset", "2"}, 2, order[2:4]},
		{[]string{"offset", "4"}, 4, order[4:]},
		{[]string{"offset", "9"}, 9, nil},
		{[]string{"limit", "1"}, 0, order[:1]},
		{[]string{"limit", "50"}, 0, order[:2]}, // no more than the site's page
		{[]string{"offset", "-3", "limit", "0"}, 0, order[:2]},
	} {
		f := feedOf(t, ix, "search", tc.params...)
		if got := titles(f); !slices.Equal(got, tc.want) || f.Channel.Response.Total != 5 || f.Channel.Response.Offset != tc.offset {
			t.Errorf("search %v = %v at offset %d of %d, want %v at %d of 5", tc.params, got, f.Channel.Response.Offset, f.Channel.Response.Total, tc.want, tc.offset)
		}
	}
}

// An indexer with a key refuses a call without it and a call with another,
// each in the words an *arr reads the reason from; one with no key takes any.
func TestKeys(t *testing.T) {
	t.Parallel()

	s, tv := serve(t, Site{Name: "open", Releases: catalogue()})

	_, _, body := get(t, tv.LocalURL()+"/api?t=search")
	if e := refused(t, body); e.Code != ErrMissingParameter || !strings.Contains(e.Description, "apikey") {
		t.Errorf("a search with no key = %+v", e)
	}
	_, _, body = get(t, tv.LocalURL()+"/api?t=search&apikey=wrong")
	if e := refused(t, body); e.Code != ErrIncorrectCredentials {
		t.Errorf("a search with the wrong key = %+v", e)
	}
	guid := tv.Releases()[0].GUID
	_, _, body = get(t, tv.LocalURL()+"/download/"+guid)
	if e := refused(t, body); e.Code != ErrMissingParameter {
		t.Errorf("a fetch with no key = %+v", e)
	}
	if got := tv.Grabbed(); len(got) != 0 {
		t.Errorf("a fetch that was refused was counted: %v", got)
	}

	open := s.Indexer("open")
	for _, key := range []string{"", "&apikey=anything"} {
		status, _, body := get(t, open.LocalURL()+"/api?t=search"+key)
		var f feedDoc
		if err := xml.Unmarshal([]byte(body), &f); err != nil || status != http.StatusOK || len(f.Channel.Items) != 5 {
			t.Errorf("an indexer with no key, asked with %q = %d, %v: %s", key, status, err, body)
		}
	}
}

// A fetch of the link a feed gives answers the release's NZB, whose segments
// add up to its size, and counts the grab; a release that is not there is a
// 404 in Newznab's words.
func TestDownload(t *testing.T) {
	t.Parallel()

	_, ix := serve(t)
	if err := ix.Offer(Release{Title: "Zzyzx.Road.S04.1080p.WEB-DL-TEST", Category: CategoryTVHD, Size: 1000, Files: 3, Group: "alt.binaries.zzyzx"}); err != nil {
		t.Fatal(err)
	}

	item := feedOf(t, ix, "search", "q", "s04").Channel.Items[0]
	for range 2 {
		status, header, body := get(t, item.Link)
		if status != http.StatusOK || header.Get("Content-Type") != "application/x-nzb" || header.Get("Content-Disposition") != `attachment; filename="Zzyzx.Road.S04.1080p.WEB-DL-TEST.nzb"` {
			t.Fatalf("the NZB = %d, %v", status, header)
		}
		var nzb struct {
			Files []struct {
				Group    string `xml:"groups>group"`
				Segments []struct {
					Bytes int64 `xml:"bytes,attr"`
				} `xml:"segments>segment"`
			} `xml:"file"`
		}
		if err := xml.Unmarshal([]byte(body), &nzb); err != nil {
			t.Fatalf("the NZB does not parse: %v\n%s", err, body)
		}
		var size int64
		for _, f := range nzb.Files {
			size += f.Segments[0].Bytes
		}
		if len(nzb.Files) != 3 || size != 1000 || nzb.Files[0].Group != "alt.binaries.zzyzx" {
			t.Errorf("the NZB lists %d files of %d bytes in %q, want 3 of 1000 in alt.binaries.zzyzx", len(nzb.Files), size, nzb.Files[0].Group)
		}
	}

	if got := ix.Grabbed(); !slices.Equal(got, []string{"Zzyzx.Road.S04.1080p.WEB-DL-TEST", "Zzyzx.Road.S04.1080p.WEB-DL-TEST"}) {
		t.Errorf("grabbed = %v, want the release twice", got)
	}
	if got := feedOf(t, ix, "search", "q", "s04").Channel.Items[0].attrs("grabs"); !slices.Equal(got, []string{"2"}) {
		t.Errorf("the feed counts %v grabs, want 2", got)
	}

	// a name as a client may ask for it, with the file's extension
	if status, _, _ := get(t, strings.Replace(item.Link, "?", ".nzb?", 1)); status != http.StatusOK {
		t.Errorf("a fetch of the guid with .nzb = %d", status)
	}

	status, _, body := get(t, ix.LocalURL()+"/download/gone?apikey="+testKey)
	if e := refused(t, body); status != http.StatusNotFound || e.Code != ErrNoSuchItem {
		t.Errorf("a release that is not there = %d %+v, want a 404 saying no such item", status, e)
	}
}

// What an indexer cannot answer it says in Newznab's codes, naming the
// parameter at fault and never what it was sent as; what is no indexer or no
// API is a plain 404.
func TestErrors(t *testing.T) {
	t.Parallel()

	_, ix := serve(t)

	for _, tc := range []struct {
		function string
		params   []string
		code     int
		says     string
	}{
		{"", nil, ErrMissingParameter, "(t)"},
		{"teleport<b>", nil, ErrNoSuchFunction, "No such function"},
		{"search", []string{"cat", "films<b>"}, ErrIncorrectParameter, "(cat)"},
		{"tvsearch", []string{"season", "two<b>"}, ErrIncorrectParameter, "(season)"},
		{"movie", []string{"tmdbid", "x<b>"}, ErrIncorrectParameter, "(tmdbid)"},
		{"search", []string{"offset", "x<b>"}, ErrIncorrectParameter, "(offset)"},
	} {
		status, body := api(t, ix, tc.function, tc.params...)
		if e := refused(t, body); status != http.StatusOK || e.Code != tc.code || !strings.Contains(e.Description, tc.says) {
			t.Errorf("%q %v = %d %+v, want code %d saying %q", tc.function, tc.params, status, e, tc.code, tc.says)
		}
		if strings.Contains(body, "b>") || strings.Contains(body, "teleport") {
			t.Errorf("%q %v wrote what it was sent back: %s", tc.function, tc.params, body)
		}
	}

	// an episode that is no number is a daily show's date, which nothing here is filed by
	if got := titles(feedOf(t, ix, "tvsearch", "tvdbid", "111", "ep", "10/09")); len(got) != 3 {
		t.Errorf("a search by a date = %v, want it read as no episode asked for", got)
	}

	root := strings.TrimSuffix(ix.LocalURL(), "/tv")
	for _, path := range []string{"/", "/nobody/api?t=caps", "/tv", "/tv/elsewhere?t=caps", "/tv/apiary?t=caps"} {
		if status, _, _ := get(t, root+path); status != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, status)
		}
	}
	// an indexer set up with an API path of its own
	if status, _, _ := get(t, ix.LocalURL()+"/newznab/api?t=caps"); status != http.StatusOK {
		t.Errorf("the API under a path of its own = %d", status)
	}
}

// An indexer made to fail answers every call with the status or the Newznab
// error it was given, its capabilities included; one that fails now and then
// fails every Nth search and the ones straight after it, and still says what
// it can do and hands over a release.
func TestFailure(t *testing.T) {
	t.Parallel()

	_, ix := serve(t)
	guid := ix.Releases()[0].GUID

	ix.SetFailure(Failure{Status: http.StatusServiceUnavailable})
	for _, path := range []string{"/api?t=search&apikey=" + testKey, "/api?t=caps", "/download/" + guid + "?apikey=" + testKey} {
		if status, _, _ := get(t, ix.LocalURL()+path); status != http.StatusServiceUnavailable {
			t.Errorf("a down indexer answered %s with %d", path, status)
		}
	}

	ix.SetFailure(Failure{Code: ErrRequestLimitReached, Description: "Request limit reached"})
	status, body := api(t, ix, "search")
	if e := refused(t, body); status != http.StatusOK || e.Code != ErrRequestLimitReached || e.Description != "Request limit reached" {
		t.Errorf("a limited indexer = %d %+v", status, e)
	}
	ix.SetFailure(Failure{Code: ErrUnknown})
	if _, body := api(t, ix, "search"); refused(t, body).Description != "Unknown error" {
		t.Errorf("a failure with no words of its own = %s", body)
	}

	ix.SetFailure(Failure{Status: http.StatusBadGateway, Every: 3})
	got := make([]int, 0, 4)
	for range 4 {
		status, _ := api(t, ix, "search")
		got = append(got, status)
		if status, _, _ := get(t, ix.LocalURL()+"/api?t=caps"); status != http.StatusOK {
			t.Errorf("a flaky indexer answered its capabilities with %d", status)
		}
	}
	if !slices.Equal(got, []int{200, 200, 502, 502}) {
		t.Errorf("a search of an indexer that fails every third = %v, want two answered, the third failed and the next within its spell", got)
	}
	if status, _, _ := get(t, ix.LocalURL()+"/download/"+guid+"?apikey="+testKey); status != http.StatusOK {
		t.Errorf("a flaky indexer in its spell answered a fetch with %d", status)
	}

	ix.SetFailure(Failure{})
	if status, _ := api(t, ix, "search"); status != http.StatusOK {
		t.Errorf("a mended indexer = %d", status)
	}
}

// A slow indexer holds every answer back for as long as it was told to.
func TestDelay(t *testing.T) {
	t.Parallel()

	_, ix := serve(t)
	ix.SetDelay(200 * time.Millisecond)

	began := time.Now()
	if status, _ := api(t, ix, "search"); status != http.StatusOK || time.Since(began) < 200*time.Millisecond {
		t.Errorf("a slow indexer answered %d after %s", status, time.Since(began))
	}

	ix.SetDelay(0)
	began = time.Now()
	api(t, ix, "search")
	if time.Since(began) > 150*time.Millisecond {
		t.Errorf("an indexer no longer slow took %s", time.Since(began))
	}
}

// The catalogue changes while the indexer runs. A release offered again
// takes the place of the one with its GUID and keeps the date it was given,
// which is what an *arr knows a blocklisted release by; a release that does
// not say what is the test's to say is refused, and nothing offered with it
// is taken; and what is read back is a copy.
func TestCatalogue(t *testing.T) {
	t.Parallel()

	s, ix := serve(t)

	first := ix.Releases()
	if len(first) != 5 || first[0].Files != 1 || first[0].Group == "" || first[0].Poster == "" || len(first[0].GUID) != 40 {
		t.Fatalf("what a release left unsaid: %+v", first[0])
	}
	if first[1].Size != 0 || first[1].Category != CategoryTVUHD {
		t.Errorf("a usenet release's size not given is unknown, and its category is the one it said: %+v", first[1])
	}
	for _, r := range first {
		if age := time.Since(r.PubDate); age < 30*time.Minute || age > 8*24*time.Hour+time.Minute {
			t.Errorf("%s, which did not say when it was posted, was given %s ago, want between half an hour and eight days", r.Title, age)
		}
	}

	if err := ix.Offer(Release{Title: episode7, Category: CategoryTVHD, Size: 42}, Release{Title: "Zzyzx.Road.S05E01.720p.HDTV-TEST", Category: CategoryTVHD, Languages: []string{"German"}}); err != nil {
		t.Fatal(err)
	}
	after := ix.Releases()
	if len(after) != 6 || after[0].Size != 42 || !after[0].PubDate.Equal(first[0].PubDate) || after[0].GUID != first[0].GUID {
		t.Errorf("a release offered again: %+v, want it in the first's place with the first's date", after[0])
	}
	after[5].Languages[0] = "Klingon"
	if got := ix.Releases()[5].Languages; !slices.Equal(got, []string{"German"}) {
		t.Errorf("what was read back was the catalogue's own: %v", got)
	}

	// a category is the test's to say, and so is a title: neither is made up
	if err := ix.Offer(Release{Title: "Zzyzx.Road.S06E01.1080p.WEB-DL-TEST", Category: CategoryTVHD}, Release{Title: "Zzyzx.Road.S06E02.1080p.WEB-DL-TEST"}); err == nil || !strings.Contains(err.Error(), "S06E02") || !strings.Contains(err.Error(), "category") {
		t.Errorf("a release with no category = %v, want it refused by name", err)
	}
	if err := ix.SetReleases(Release{Category: CategoryTVHD}); err == nil || !strings.Contains(err.Error(), "title") {
		t.Errorf("a release with no title = %v, want it refused", err)
	}
	if got := ix.Releases(); len(got) != 6 {
		t.Errorf("after two refusals the catalogue holds %d, want the 6 it held: nothing offered with a refused release is taken", len(got))
	}

	ix.Withdraw(episode7, film, "never offered")
	if got, want := titles(feedOf(t, ix, "search")), newestFirst(ix, otherFilm, episode8, seasonPack, "Zzyzx.Road.S05E01.720p.HDTV-TEST"); !slices.Equal(got, want) {
		t.Errorf("after two were withdrawn the feed = %v, want %v", got, want)
	}

	if err := ix.SetReleases(Release{Title: film, Category: CategoryMoviesHD, GUID: "its-own"}); err != nil {
		t.Fatal(err)
	}
	if got := ix.Releases(); len(got) != 1 || got[0].GUID != "its-own" {
		t.Errorf("a catalogue replaced = %+v", got)
	}
	if err := ix.SetReleases(); err != nil {
		t.Fatal(err)
	}
	if got := titles(feedOf(t, ix, "search")); len(got) != 0 {
		t.Errorf("an empty catalogue's feed = %v", got)
	}

	// an indexer added again starts afresh, and the handle held before is no longer the one served
	again, err := s.Add(Site{Name: "tv", Releases: catalogue()[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if s.Indexer("tv") != again || len(again.Releases()) != 1 || len(titles(feedOf(t, again, "search"))) != 1 {
		t.Errorf("an indexer added again: %+v", again.Releases())
	}
}

// What a release does not say and has to have is picked from the seed: the
// same seed gives the same release the same date and the same size on another
// server, another seed gives others, and the picks are spread, over a week
// of dates and from an episode's size to a disc's.
func TestSeed(t *testing.T) {
	t.Parallel()

	releases := make([]Release, 0, 40)
	for i := range 40 {
		releases = append(releases, Release{Title: fmt.Sprintf("Zzyzx.Road.S01E%02d.1080p.WEB-DL-TEST", i+1), Category: CategoryTVHD})
	}
	start := func(seed uint64) *Indexer {
		s, err := New(Options{Seed: seed, Sites: []Site{{Name: "torrents", Protocol: Torrent, Releases: releases}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if seed != 0 && s.Seed() != seed {
			t.Errorf("a server given the seed %d says it is %d", seed, s.Seed())
		}

		return s.Indexer("torrents")
	}

	one, same, other := start(7).Releases(), start(7).Releases(), start(8).Releases()
	// the servers started a moment apart, so a date is held to the others' by how far each is from the first release's
	apart := func(of []Release, i int) time.Duration { return of[i].PubDate.Sub(of[0].PubDate) }
	differing := 0
	for i := range one {
		if one[i].Size != same[i].Size || apart(one, i) != apart(same, i) {
			t.Errorf("%s: %d bytes and %s with one seed, %d and %s with the same again", one[i].Title, one[i].Size, apart(one, i), same[i].Size, apart(same, i))
		}
		if one[i].Size != other[i].Size {
			differing++
		}
	}
	if differing < 30 {
		t.Errorf("another seed gave %d of 40 releases another size, want nearly all", differing)
	}

	smallest, largest := slices.MinFunc(one, func(a, b Release) int { return cmp.Compare(a.Size, b.Size) }).Size, slices.MaxFunc(one, func(a, b Release) int { return cmp.Compare(a.Size, b.Size) }).Size
	if smallest < 200<<20 || smallest > 1<<30 || largest < 20<<30 || largest > 60<<30 {
		t.Errorf("40 torrents were given sizes from %d to %d, want from an episode's to a disc's", smallest, largest)
	}
	newest, oldest := slices.MaxFunc(one, func(a, b Release) int { return a.PubDate.Compare(b.PubDate) }).PubDate, slices.MinFunc(one, func(a, b Release) int { return a.PubDate.Compare(b.PubDate) }).PubDate
	if spread := newest.Sub(oldest); spread < 5*24*time.Hour || spread > 8*24*time.Hour {
		t.Errorf("40 releases were given dates %s apart, want most of a week", spread)
	}

	if start(0).server.Seed() == 0 {
		t.Error("a server given no seed picked none")
	}
}

// An indexer needs a name it can be served under and a protocol there is; a
// server started with one that has neither does not start.
func TestAdd(t *testing.T) {
	t.Parallel()

	s, _ := serve(t)

	for _, site := range []Site{{}, {Name: "a/b"}, {Name: "a?b"}, {Name: "a#b"}, {Name: "ftp", Protocol: "ftp"}} {
		if ix, err := s.Add(site); err == nil || ix != nil {
			t.Errorf("Add(%+v) = %v, %v, want it refused", site, ix, err)
		}
	}
	if ix, err := s.Add(Site{Name: "films", Releases: []Release{{Title: film}}}); err == nil || ix != nil || s.Indexer("films") != nil {
		t.Errorf("an indexer with a release that does not say its category = %v, %v, want it refused", ix, err)
	}
	if s.Indexer("nobody") != nil {
		t.Error("an indexer that was never added is served")
	}
	if _, err := New(Options{Sites: []Site{{Name: "ok"}, {Name: ""}}}); err == nil {
		t.Error("a server with an indexer that has no name started")
	}
	if _, err := New(Options{Addr: s.Addr()}); err == nil {
		t.Error("a server started on an address in use")
	}
}

// Every call is kept for a test to hold the server under test to: which
// indexer was asked, for what, and with which parameters, the key among them.
func TestRequests(t *testing.T) {
	t.Parallel()

	s, tv := serve(t, Site{Name: "other"})
	other := s.Indexer("other")

	api(t, tv, "tvsearch", "tvdbid", "111")
	get(t, other.LocalURL()+"/api?t=caps")
	get(t, tv.LocalURL()+"/download/"+tv.Releases()[0].GUID+"?apikey="+testKey)
	get(t, strings.TrimSuffix(tv.LocalURL(), "/tv")+"/nobody/api?t=caps")

	asked := s.Requests()
	functions := make([]string, 0, len(asked))
	for _, r := range asked {
		functions = append(functions, r.Site+" "+r.Function)
	}
	if !slices.Equal(functions, []string{"tv tvsearch", "other caps", "tv download"}) {
		t.Errorf("the server's requests = %v", functions)
	}

	got := tv.Requests()
	if len(got) != 2 || got[0].Method != http.MethodGet || got[0].Path != "/tv/api" || got[0].Query.Get("tvdbid") != "111" || got[0].Query.Get("apikey") != testKey || got[0].Time.IsZero() {
		t.Errorf("tv's requests = %+v", got)
	}
	if got := other.Requests(); len(got) != 1 || got[0].Function != "caps" {
		t.Errorf("other's requests = %+v", got)
	}
}
