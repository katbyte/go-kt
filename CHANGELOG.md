## v0.2.0 (unreleased)

- add `pointer`: `From` (dereference or zero value) and `To` (pointer to a value), extracted from ghp-sync
- cout: add `Flags` (embed with `mapstructure:",squash"`, then `Apply()`) and `SetLevelFromFlags`/`LevelFromFlags`, replacing the silent/quiet/verbose switch every tool carried

## v0.1.0 (2026-09-13)

- initial release: `clog`, `cout`, `chttp`, and `version`, extracted from the copies in tctest, koi, ghp-sync, tf-provider-profile, and friends
