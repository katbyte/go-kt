package chttp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

// explained is an error with what the operating system may be doing about
// it said after it.
type explained struct {
	err  error
	hint string
}

func (e *explained) Error() string { return e.err.Error() + e.hint }
func (e *explained) Unwrap() error { return e.err }

// Explain adds, to the error of a request that never got an answer, what the
// operating system may be doing about it where that is known: a dial a Mac
// refused with "no route to host" is usually its Local Network privacy,
// which no retry and no server fixes, and the bare error sends its reader
// to the network. The error keeps what it wraps. One there is nothing to
// say about, and one already explained, comes back as it was.
//
// Transport does this for every request it carries; Explain is for a client
// built on another transport.
func Explain(err error) error {
	if localNetworkHint == "" || err == nil || !errors.Is(err, syscall.EHOSTUNREACH) {
		return err
	}
	if _, done := errors.AsType[*explained](err); done {
		return err
	}

	return &explained{err: err, hint: localNetworkHint}
}

// credentialParams are the query parameters a credential commonly travels
// in, lowercased.
var credentialParams = []string{"api_key", "apikey", "api_token", "access_token", "token"}

// RedactURL replaces the value of every credential query parameter in a URL
// with REDACTED, and any password in it, leaving the rest as it was sent:
// the parameters around it, their order and a fragment. The parameters are
// the common ones (api_key, apikey, api_token, access_token, token) and
// whatever also names, compared without case.
func RedactURL(raw string, also ...string) string {
	base, query, found := strings.Cut(raw, "?")
	if u, err := url.Parse(base); err == nil && u.User != nil {
		base = u.Redacted()
	}
	if !found {
		return base
	}
	query, fragment, hasFragment := strings.Cut(query, "#")
	parts := strings.Split(query, "&")
	for i, p := range parts {
		key, _, _ := strings.Cut(p, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key
		}
		secret := func(param string) bool { return strings.EqualFold(param, name) }
		if slices.ContainsFunc(credentialParams, secret) || slices.ContainsFunc(also, secret) {
			parts[i] = key + "=REDACTED"
		}
	}
	out := base + "?" + strings.Join(parts, "&")
	if hasFragment {
		out += "#" + fragment
	}

	return out
}

// RedactError hides the credentials in the URL a failed request names. A
// request that gets no answer (a timeout, a refused connection) fails with a
// *url.Error that prints its whole URL, and a key sent in the query is in
// it, so it would reach a log or a tool's answer. The error keeps its type
// and what it wraps; only the URL it prints changes (see RedactURL).
//
// It is for the place the error is first seen: a message already built
// around the error (fmt.Errorf with %w) keeps the text it was built with.
func RedactError(err error, also ...string) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		urlErr.URL = RedactURL(urlErr.URL, also...)
	}

	return err
}

// KeepCredentialsOnHost is a redirect policy, for an http.Client's
// CheckRedirect: a redirect is followed (up to Go's usual ten), but one that
// leaves the first request's host, or goes from https down to http, reaches
// its target without the credentials.
//
// Go itself drops only Authorization on the way to another domain, and keeps
// it for a subdomain; a token an API takes in a header of its own it would
// hand to whatever host a redirect names. This drops Authorization and the
// headers named. Another port on the same host is the same machine, and is
// left alone.
func KeepCredentialsOnHost(headers ...string) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		first := via[0].URL
		if strings.EqualFold(req.URL.Hostname(), first.Hostname()) && (first.Scheme != "https" || req.URL.Scheme == "https") {
			return nil
		}
		req.Header.Del("Authorization")
		for _, h := range headers {
			req.Header.Del(h)
		}

		return nil
	}
}

// StatusError is an answer with a status the request did not ask for.
type StatusError struct {
	Method string
	Path   string
	// StatusCode is the status the server answered with
	StatusCode int
	// Expected are the statuses the request takes as success, when it
	// names them
	Expected []int
	// Body is the start of the response body (see Preview)
	Body string
	// Tries is how many times the request was sent, when more than once
	Tries int
	// Note is what the caller knows about this status on this API: which
	// key to check for a 401, what permission a 403 wants
	Note string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	if len(e.Expected) > 0 {
		want := make([]string, 0, len(e.Expected))
		for _, c := range e.Expected {
			want = append(want, strconv.Itoa(c))
		}
		msg += " (expected " + strings.Join(want, " or ") + ")"
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	if e.Tries > 1 {
		msg += fmt.Sprintf(" (tried %d times)", e.Tries)
	}
	if e.Note != "" {
		msg += " (" + e.Note + ")"
	}

	return msg
}

// StatusCode is the status of a *StatusError in err's chain, or 0.
func StatusCode(err error) int {
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.StatusCode
	}

	return 0
}

// IsNotFound reports whether err is a 404 from the server.
func IsNotFound(err error) bool { return StatusCode(err) == http.StatusNotFound }

// PreviewLen is how much of a response body a StatusError carries.
const PreviewLen = 300

// Preview is the start of a response body for an error to carry: the space
// around it gone, cut at PreviewLen bytes on a whole character, with "..."
// to say there was more.
func Preview(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) <= PreviewLen {
		return s
	}
	n := PreviewLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n] + "..."
}
