## v0.5.0 (2026-10-09)

- lock (breaking): one call, `lock.By`, locks any mix of things: a record (a value whose type has a `LockID` method), `lock.ID[T](id)` for one known only by its id, `lock.Field(thing, "name")` for a named piece of one, and `lock.String(s)`; `lock.ByString(s)` is short for the last. `ByID`, `ByName`, `MultipleByID`, `MultipleByName` and `NameID` are gone
- lock: add `lock.NewSet`, a set of locks of its own with the same calls and `Idle`, for an application that holds more than one server and for a test
- mcp/registry (breaking): an allow list beside toolsets adds to them, "these sets and these tools as well", where it used to narrow them and refuse a name they did not hold; a deny list is how a toolset is narrowed. On its own an allow list is still only the tools it names
- spelling: add `InitialsOf`, two names that are one person with a first name reduced to its initial; extracted from abs-mcp
- chttp (breaking): imports only the standard library and logs nothing unless handed a logger: `chttp.New(chttp.Options{Log: clog.Log})` replaces `NewHTTPClient(name)`, and `NewTransport`, `NewRetryTransport` and `NewBaseTransport` take `Options`
- chttp (breaking): by default a request is sent again for a 429, a 502, 503 or 504 and a dropped connection, and no longer for a plain 500, a server that cannot be reached or a timeout; what is worth another try, how often and how long after are set in `Options.Retry`. `DefaultMaxRetry` is `DefaultTries`
- chttp: a trace no longer shows credentials (the Authorization and cookie headers, any name that ends in password, secret, token or api_key, and whatever the application adds) and never reads more of a body than it prints, so a download stays a stream
- chttp: add `Fetch`, which reads an answer whole up to a limit and asks again for one that stops part way; `Tries`, how often a request was sent; `Dropped` and `Refused`, to say which failures are worth another try; `Options.HeaderWait`, for an API that is slow to start answering; and `Options.Base`, a transport of the caller's own underneath
- chttp: add `RefuseRedirects`, a redirect policy that follows none and says why, and `IsWebPage`, a page answering where an API's own answer was expected; extracted from abs-mcp and technitium-mcp

## v0.4.0 (2026-10-09)

- add `parallel`: runs a batch of jobs a few at a time and stops at the first error; extracted from abs-mcp and embyfin-mcp
- add `lock`: makes two edits of one record in one process take turns; extracted from abs-mcp and embyfin-mcp
- add `mcp/server`: serves an MCP server over stdio, or over HTTP behind a bearer token; extracted from the five MCP servers, with abs-mcp's fixes for sessions left open and a slow stop
- add `mcp/registry`: decides which tools an MCP session gets and what each tells a client; extracted from the five MCP servers
- add `test/replayproxy`: records what a server under test fetches from the internet once, and replays it in CI; one proxy made of the four copies in embyfin-mcp, abs-mcp, sonarr-mcp and radarr-mcp, and its refusal of a request with no recording now ends at once, where reading it to the end took a minute
- add `test/env`: a live suite's environment, its replay proxy and the files it lays out for the server; extracted from embyfin-mcp
- add `mcp/acctest`: a test suite that drives an MCP server as a client does and fails when a tool was never seen to work; extracted from embyfin-mcp
- chttp: a connection a Mac refuses with "no route to host" says what the system may be doing about it
- chttp: add what an API client needs around a request: credentials kept out of error messages and off other hosts on a redirect, and one error for an unexpected status

## v0.3.0 (2026-10-09)

- add `spelling`: says when two names are one thing spelled two ways, a typing slip apart, or one cut short; extracted from embyfin-mcp and abs-mcp
- add `whitespace`: finds the spaces out of place in a name or a file name, shows them, and gives the name put right; extracted from embyfin-mcp

## v0.2.0 (2026-09-13)

- add `pointer`: an optional value or its zero, and a pointer to a value; extracted from ghp-sync
- cout: add `Flags`, the silent, quiet and verbose flags as one set a tool embeds, replacing the switch every tool carried
- cout: add a JSON level between silent and quiet, for tools that print a JSON document

## v0.1.0 (2026-09-13)

- initial release: `clog`, `cout`, `chttp`, and `version`, extracted from the copies in tctest, koi, ghp-sync, tf-provider-profile, and friends
