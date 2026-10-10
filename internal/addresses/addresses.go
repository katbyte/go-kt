// Package addresses blanks what an address in a piece of text may carry a
// credential in. It is the one rule behind outside.BlankAddresses and behind
// what mcp/registry writes down of a call and hands back of a failure, kept
// here so that the registry can be told what was blanked as well.
package addresses

import (
	"regexp"
	"strings"
)

// Blanked stands for a value that is not shown, as it does in a trace of a
// request.
const Blanked = "REDACTED"

// found finds each address in a piece of text: a scheme, "://" and what
// follows it up to a space, a quote or an angle bracket.
var found = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)

// closers are the brackets an address may be written inside, each with the
// one that closes it.
var closers = map[byte]byte{'[': ']', '(': ')', '{': '}'}

// Blank is text with what each address in it may carry a credential in
// blanked, and the rest as it was: who the address signs in as, the value
// of every parameter after its "?", whose names are someone else's and say
// nothing of what they hold, and what follows its "#". The host, the path
// and the parameters' names stay. Each piece blanked is handed to hidden,
// when there is one.
//
// An address written inside brackets, as "at [http://host/api?apikey=...]"
// is, ends at the bracket that closes them. Any other mark against the end
// of an address cannot be told from the end of what it carries, and goes
// with it. A secret in the path itself, as a webhook's is, has no rule to
// find it by.
func Blank(text string, hidden func(string)) string {
	places := found.FindAllStringIndex(text, -1)
	if places == nil {
		return text
	}
	if hidden == nil {
		hidden = func(string) {}
	}

	var out strings.Builder
	done := 0
	for _, place := range places {
		start, end := place[0], place[1]
		if closer, inside := closers[before(text, start)]; inside {
			if within := strings.TrimRight(text[start:end], ".,;:!?"); strings.HasSuffix(within, string(closer)) {
				end = start + len(within) - 1
			}
		}

		out.WriteString(text[done:start])
		out.WriteString(one(text[start:end], hidden))
		done = end
	}
	out.WriteString(text[done:])

	return out.String()
}

// before is the byte before a place in text, and 0 at its start.
func before(text string, at int) byte {
	if at == 0 {
		return 0
	}

	return text[at-1]
}

// one is a single address with what it may carry a credential in blanked.
func one(raw string, hidden func(string)) string {
	rest, fragment, _ := strings.Cut(raw, "#")
	rest, query, hasQuery := strings.Cut(rest, "?")
	scheme, rest, _ := strings.Cut(rest, "://")
	host, path, hasPath := strings.Cut(rest, "/")

	if at := strings.LastIndex(host, "@"); at >= 0 {
		user, password, _ := strings.Cut(host[:at], ":")
		hidden(user)
		hidden(password)
		host = Blanked + host[at:]
	}
	out := scheme + "://" + host
	if hasPath {
		out += "/" + path
	}

	if hasQuery {
		parts := strings.Split(query, "&")
		for i, part := range parts {
			name, value, named := strings.Cut(part, "=")
			switch {
			case named && value != "":
				hidden(value)
				parts[i] = name + "=" + Blanked
			case !named && part != "":
				hidden(part)
				parts[i] = Blanked
			}
		}
		out += "?" + strings.Join(parts, "&")
	}
	if fragment != "" {
		hidden(fragment)
		out += "#" + Blanked
	}

	return out
}
