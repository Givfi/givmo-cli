# Vendored: donate.json reference parser

`donate.go` is vendored verbatim from the `donate.json` manifest spec repo
(spec module `github.com/givfi/donate-json/parser`, spec v1.0). It is
standard-library-only. The CLI's `manifest validate|sign` commands are thin
wrappers over this package so the CLI and the published spec cannot drift.

**Keep in sync with the spec repo.** Any change to the manifest semantics
happens in the spec repo first, then is re-vendored here. Do not fork the
parsing/sanitization/signing logic in the CLI.
