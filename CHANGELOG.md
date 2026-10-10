## Unreleased

- mcp/registry (fix): the line written for a write blanked only an argument named as a credential, so a key, a cookie or a passkey under a name of someone else's choosing, in a settings object, was written in clear to a log at info. Blanked now, with its names kept, is everything under an argument a tool takes as a free-form object; in an address anywhere in what is left, who it signs in as, the value of every parameter and what follows its `#`; and whatever `Config.SecretArguments` names, whole, for a file's text, a command or an address whose path is the secret. `Config.ShownArguments` names, by tool, a free-form argument that holds no credential and is written as sent. What was blanked is blanked in the failure beside it too, which is apt to repeat what it was sent. The arguments are written with their names in order, where they were written as the caller had them
- mcp/registry: a failure's words reach the caller with what an address in them may carry a credential in taken out, around every tool: who it signs in as, the value of every parameter and what follows its `#`. A server quotes an address back in an error with a credential of its own on the end, an indexer's stored key, and the tool handed that to the model as it was. An answer's addresses are left as they are
- outside: add `BlankAddresses`, text with what each address in it may carry a credential in blanked, who it signs in as, the value of every parameter and what follows its `#`: for a server's own words about what went wrong, a failed test's reason or a line of a log, which quote the address it called with its own key on the end. It is the rule a failure's words and the line written for a write are held to. An address written inside brackets, as `at [http://host/api?apikey=...]` is, keeps the bracket that closes them
- mcp/registry: `Config.BlankAddresses` names the values in an answer that are a server's own words for what went wrong, by the JSON names they are answered under, and each address under such a name is answered as one in a failure is, however deep and whether or not answers are cleaned; every other address in an answer is the caller's to use and is left. `BlankAddressesUnused` is the names on that list no tool answers under, for a server's test to hold to none
- mcp/registry (fix): an empty map in an answer is sent as `{}`, as an empty list is sent as `[]`; it was sent as null, which the SDK refused the whole call for, the tool's own schema saying an object
- mcp/registry: a tool whose input is `any` takes no arguments, and refuses one as every other tool does and as the shared instructions say; the SDK's schema for such a tool took whatever it was sent. This changes what a server lists for such a tool, whose input schema now says it takes no properties; a call with no arguments, or with `{}`, is answered as before
- chttp (fix): a `StatusError` and a `Preview` carry the start of a server's answer with its credentials blanked; a server that refuses a save echoes the record back, key and all, and the error reaches a log and whoever called with it in clear
- chttp: add `SecretName`, whether a name is a credential's by the rule a trace hides one by; a name that ends `passphrase`, `passkey`, `cookie`, `private_key` or `privatekey` is one now, in a trace as well
- mcp/acctest: `Suite.Secret` has everything a tool answers or refuses with read, all run long, for a credential the suite gives the server or the server's own, and `Suite.LeakReport` says where one was shown, for a suite's `TestMain` to fail on: a server hands a key back where nobody thinks to test, and it takes no test to think of the tool that shows it; sonarr-mcp's, which radarr-mcp and prowlarr-mcp had each copied
- mcp/registry: `Config.CleanAnswers` has every piece of text in every tool's answer, and in its refusal, cleaned as text from outside is and cut to a length, once around every tool where a tool cleaning its own fields can miss one; `Config.LeaveAsIs` names the values a later call hands back, a path or a release's id, which are answered as they are; and `LeaveAsIsUnused` is the names on that list no tool answers under, for a server's test to hold to none
- test/replayproxy: a proxy with no `CassetteDir` keeps no recordings and replays alone, for a suite that keeps its server from the internet and answers what it asks itself; one that records or verifies still needs somewhere to keep them
- test/env: a miss says what to do about it in the suite's own words when it gave a `RecordHint`, in the report at the end of a run as well as in the log, and a suite with no recordings is told that a request had no answer, and to answer it or find what asked, where both were told it had no recording and to record one

## v0.6.1 (2026-10-10)

- add `outside`: text that came from outside, a release's name or a file's or a description anyone can write, made safe to hand a model: `Text` takes out every character that does not show (control and format characters, among them the zero-width ones and those that override the direction text runs in, and the tag characters and variation selectors a message can be hidden in) and cuts what is left to a length
- mcp/server: add `Instructions`, what a server tells a client that connects: `DefaultInstructions`, the words every server shares, and then the server's own
- mcp/registry: one line is written for every call of a write or a delete tool (`Config.LogWrite`, by default the log): the kind, the tool, what the call sent with its credentials blanked, and whether it was refused, failed or answered
- mcp/registry: a call with an argument the tool does not take is told the arguments it does take, after the SDK's own refusal
- chttp: add `RedactJSON`, JSON text with its credentials blanked by the rule a trace hides them by
- mcp/registry: add `Hints.Installs`, for a tool that has the server bring something in from outside and run it, an app or a plug-in; a client is told it is open world, the one hint MCP has for that and for `SendsOut`

## v0.6.0 (2026-10-10)

- add `test/services/arrserver`: the servers a Sonarr, Radarr or Prowlarr calls home to, as a handler for a live suite to put behind its proxy: the update server, which is asked once a build is two weeks old, and the clock, whose recorded answer is wrong the next day; prowlarr-mcp's, brought over
- add `test/services/newznab`: Newznab and Torznab indexers for a live suite to run beside the server under test, any number on one listener, each usenet or torrent, with a catalogue the test changes as it goes and failures it sets; a release says its title and category, and the date and the torrent size it does not say are picked from a spread by a seed that can be given again, so no test leans on one made-up value; the three indexers of sonarr-mcp, radarr-mcp and prowlarr-mcp made one
- add `test/services/sabnzbd`: a SABnzbd for a live suite to run beside the server under test, a download client that downloads nothing and whose jobs the test progresses, pauses, completes with the files it names, or fails; sonarr-mcp's, brought over
- mcp/registry (breaking): `Hints.OpenWorld` is `Hints.SendsOut`, and it is for a tool that itself sends something to a person or a service beyond the server, an email or a notification; a tool that makes the server fetch from its own providers or hand work to its own download client is not one
- mcp/acctest: `DecimalOr0` reads a fraction that may be absent, 0 when it is, beside `NumOr0`, which reads 1.5 as 1
- test/replayproxy: `Serve` takes a host and a path ("clock.example.org/v1/time") to answer that one path from the test while the host's other paths replay: a recorded time of day is wrong from the next day on
- test/env: `ListenProxy` brings the proxy up without asking the container anything, for a server that calls out as it starts and so is started only once the proxy listens; `StartProxy` is `ListenProxy` and then the check
- test/env (breaking): `CheckReachable(ctx, what, port)` replaces `CheckProxyReachable(ctx, port)` and names what could not be reached, so a suite can check the port of each service it runs as well as the proxy's
- test/env: the check probes the name the server was given for the host, `APP_TEST_HOST` (`Host`), where it probed `host.docker.internal` whatever the test script had told the container

## v0.5.1 (2026-10-09)

- whitespace: a space that is not the ordinary one is out of place where an ordinary one would be: two no-break spaces are a double space, and one before a colon is a space before a colon
- whitespace: a space after a file's extension no longer hides one before it; `SplitExt` reads a name without the spaces after it
- whitespace: `Fixed` is empty when nothing is left of a name but spaces or its extension, where " .mkv" was put right as ".mkv"
- whitespace: `Visible` marks the ordinary space beside an odd one

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
