// Command givmo is the developer CLI for the Givmo agentic-giving platform.
//
// See `givmo --help` for the full command surface, stable exit-code contract,
// and money-safety posture. Implementation lives in ./cmd and ./internal.
package main

import "github.com/givfi/givmo-cli/cmd"

func main() {
	cmd.Execute()
}
