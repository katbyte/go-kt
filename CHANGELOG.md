## Unreleased

- add `spelling`: `Key` folds a name's case, accents, separators and punctuation so two spellings of one thing meet; `TypoApart`, `SlipsInWords`, `Swapped`, `DroppedFromMiddle` and `Distance` say when two keys that still differ are a slip of the keyboard, `TruncationOf` when one is the other cut short, and `Marks` which marks make another name; extracted from embyfin-mcp's and abs-mcp's spelling audits
- add `whitespace`: `Problems` names the spaces out of place in a name, a file name or a title in its own language, `Visible` shows them and `Fixed` puts them right, `DroppedAt` reads what a title holds where a renamer left two spaces; extracted from embyfin-mcp's whitespace audit

## v0.2.0 (2026-09-13)

- add `pointer`: `From` (dereference or zero value) and `To` (pointer to a value), extracted from ghp-sync
- cout: add `Flags` (embed with `mapstructure:",squash"`, then `Apply()`) and `SetLevelFromFlags`/`LevelFromFlags`, replacing the silent/quiet/verbose switch every tool carried
- cout: add `VerbosityJSON` between silent and quiet, for tools that emit a JSON document on stdout; `Flags` carries a matching `JSON` field

## v0.1.0 (2026-09-13)

- initial release: `clog`, `cout`, `chttp`, and `version`, extracted from the copies in tctest, koi, ghp-sync, tf-provider-profile, and friends
