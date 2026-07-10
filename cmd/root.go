// Package cmd implements the givmo CLI's cobra command tree.
//
// EXECUTION MODEL: every command returns an error. The root Execute funnels all
// errors through the single output.Error envelope and exits with the stable
// exit code carried by that envelope (see internal/output/exit.go). This keeps
// exit codes and error rendering uniform across the whole surface — a hard exit
// criterion for the CLI's AI-agent-driven contract.
package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/givfi/givmo-cli/internal/output"
	"github.com/givfi/givmo-cli/internal/version"
	"github.com/spf13/cobra"
)

// Global flags (persistent across all subcommands).
var (
	flagJSON    bool
	flagProfile string
	flagAPIKey  string
)

// rootLongHelp documents the CLI including the exit-code contract, so
// `givmo --help` is self-describing for humans and agents.
const rootLongHelp = `givmo — the developer CLI for the Givmo agentic-giving platform.

Givmo exposes one remote MCP server (https://mcp.givmo.io/mcp) plus a REST API.
This CLI serves the PUBLIC (catalog) and CONSUMER (user-delegated OAuth) tiers.

MONEY SAFETY: a donation is created via a secretless, single-use hosted-checkout
URL (a "gco_" token). The human completes payment and accepts terms on the
Givmo-hosted page. This CLI never handles a card, a client secret, or a dn_ id,
and never accepts terms. It only DISPLAYS the checkout URL.

UNTRUSTED INPUT: donate.json manifests are treated as hostile. The manifest
commands surface every rejected/sanitized claim.

STABLE EXIT CODES (scripts/agents can branch on these):
  0  ok
  1  generic error
  2  usage error (bad flags/args)
  3  auth required/failed        -> run 'givmo login'
  4  not found
  5  rate limited
  6  network error / endpoint not enabled
  7  validation / rejected manifest

ERROR ENVELOPE: with --json, errors render as
  {"error":{"code","message","remediation","request_id","doc_url}}
The 'remediation' field is phrased so an AI agent can act on it directly.`

// newRootCmd builds the root command with all subcommands attached.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "givmo",
		Short:         "Developer CLI for the Givmo agentic-giving platform",
		Long:          rootLongHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
	}

	root.PersistentFlags().BoolVar(&flagJSON, "json", false, "emit machine-readable JSON instead of human tables/text")
	root.PersistentFlags().StringVar(&flagProfile, "profile", "", "override the active profile (sandbox|production)")
	root.PersistentFlags().StringVar(&flagAPIKey, "api-key", "", "non-interactive API key (overrides stored token; also GIVMO_API_KEY)")

	root.SetVersionTemplate(fmt.Sprintf("givmo %s (commit %s, built %s)\n",
		version.Version, version.Commit, version.Date))

	root.AddCommand(
		newLoginCmd(),
		newLogoutCmd(),
		newWhoamiCmd(),
		newConfigCmd(),
		newCharitiesCmd(),
		newCauseETFsCmd(),
		newDonationIntentsCmd(),
		newReceiptsCmd(),
		newListenCmd(),
		newTriggerCmd(),
		newFixturesCmd(),
		newSandboxCmd(),
		newManifestCmd(),
		newOpenAPICmd(),
		newLogsCmd(),
		newMCPCmd(),
		newAPICmd(),
	)
	return root
}

// Execute runs the root command and maps errors to stable exit codes. It is the
// single place the process exit code is decided.
func Execute() {
	root := newRootCmd()
	err := root.Execute()
	if err == nil {
		os.Exit(output.ExitOK)
	}

	// Cobra flag/usage errors map to ExitUsage.
	e := output.AsError(err)
	if isUsageError(err) {
		e = output.New(output.ExitUsage, err.Error(),
			"Check `givmo <command> --help` for correct usage.")
	}

	// Render via the single envelope. Build a printer here in case the failure
	// happened before the command built its own (e.g. flag parsing).
	p := output.NewPrinter(os.Stdout, os.Stderr, flagJSON)
	p.Errorf(e)
	os.Exit(e.ExitCode())
}

// isUsageError reports whether err is a cobra argument/flag validation error
// (which should map to ExitUsage, not the generic bucket).
func isUsageError(err error) bool {
	var oe *output.Error
	if errors.As(err, &oe) {
		return false // already classified
	}
	msg := err.Error()
	for _, s := range []string{
		"unknown flag", "unknown command", "unknown shorthand",
		"invalid argument", "required flag", "accepts", "requires at least",
		"requires exactly", "flag needs an argument", "unknown flag:",
	} {
		if containsFold(msg, s) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexFold(s, sub) >= 0)
}

func indexFold(s, sub string) int {
	// small case-insensitive substring search (avoids importing strings twice
	// for this single helper).
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 32
		}
		return b
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		ok := true
		for j := 0; j < len(sub); j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// apiKeyFromEnvOrFlag resolves the non-interactive API key.
func apiKeyFromEnvOrFlag() string {
	if flagAPIKey != "" {
		return flagAPIKey
	}
	return os.Getenv("GIVMO_API_KEY")
}
