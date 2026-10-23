// Command givmo is the developer CLI for the Givmo agentic-giving platform.
//
//	go install github.com/givfi/givmo-cli/cmd/givmo@latest
//
// installs it under the name its help and docs use, givmo. The module root's
// main package is the same program, kept so an earlier
// `go install github.com/givfi/givmo-cli@latest` still installs and updates;
// it names the binary givmo-cli.
//
// See `givmo --help` for the full command surface, stable exit-code contract,
// and money-safety posture. Implementation lives in ./cmd and ./internal.
package main

import "github.com/givfi/givmo-cli/cmd"

func main() {
	cmd.Execute()
}
