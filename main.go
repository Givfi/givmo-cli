// Command givmo-cli is the givmo developer CLI built from the module root. It is
// kept so an earlier `go install github.com/givfi/givmo-cli@latest` still
// installs and updates; go install names it after the module, givmo-cli. The
// README installs ./cmd/givmo, the same program under the name its help and
// docs use, givmo.
//
// See `givmo --help` for the full command surface, stable exit-code contract,
// and money-safety posture. Implementation lives in ./cmd and ./internal.
package main

import "github.com/givfi/givmo-cli/cmd"

func main() {
	cmd.Execute()
}
