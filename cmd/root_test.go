package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestCommandTree_AllGroupsWiredWithHelp asserts the full command surface is
// present and every command/subcommand has a real Short help description (a
// stated exit criterion).
func TestCommandTree_AllGroupsWiredWithHelp(t *testing.T) {
	root := newRootCmd()

	// Global flags exist.
	for _, f := range []string{"json", "profile", "api-key"} {
		if root.PersistentFlags().Lookup(f) == nil {
			t.Errorf("global flag --%s missing", f)
		}
	}

	wantTop := []string{
		"login", "logout", "whoami",
		"config", "charities", "cause-etfs",
		"donation-intents", "receipts",
		"listen", "trigger", "fixtures", "sandbox",
		"manifest", "openapi", "logs", "mcp", "api",
	}
	have := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		have[c.Name()] = c
	}
	for _, name := range wantTop {
		c, ok := have[name]
		if !ok {
			t.Errorf("top-level command %q is missing", name)
			continue
		}
		if c.Short == "" {
			t.Errorf("command %q has no Short help", name)
		}
	}

	// Every command in the whole tree must have a Short description and --json
	// must be inheritable everywhere (persistent on root).
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			if sub.Short == "" {
				t.Errorf("subcommand %q has no Short help", sub.CommandPath())
			}
			walk(sub)
		}
	}
	walk(root)
}

func TestExpectedSubcommands(t *testing.T) {
	root := newRootCmd()
	find := func(path ...string) *cobra.Command {
		cur := root
		for _, name := range path {
			var next *cobra.Command
			for _, c := range cur.Commands() {
				if c.Name() == name {
					next = c
					break
				}
			}
			if next == nil {
				t.Fatalf("command path %v not found (missing %q)", path, name)
			}
			cur = next
		}
		return cur
	}
	// Spot-check the key nested commands exist.
	find("config", "use-profile")
	find("config", "view")
	find("charities", "search")
	find("charities", "get")
	find("cause-etfs", "list")
	find("cause-etfs", "get")
	find("donation-intents", "create")
	find("donation-intents", "list")
	find("receipts", "summary")
	find("fixtures", "run")
	find("sandbox", "seed")
	find("sandbox", "reset")
	find("manifest", "validate")
	find("manifest", "sign")
	find("manifest", "submit")
	find("openapi", "pull")
	find("logs", "tail")
	find("mcp", "serve")
	find("mcp", "install")
}
