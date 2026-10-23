package cmd

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestReadme_InstallsTheCommandItRuns: the README's install line is the first
// thing a stranger runs, and every command after it runs `givmo`. go install
// names a command after the last element of its import path, so the line must
// name a main package of this module whose last element is the CLI's own name.
// The line once installed the module root, which go install names givmo-cli, and
// every command in the README then failed with "command not found".
func TestReadme_InstallsTheCommandItRuns(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := regexp.MustCompile("(?m)^go install (\\S+)@latest$").FindAllStringSubmatch(string(readme), -1)
	if len(lines) != 1 {
		t.Fatalf("README.md has %d `go install …@latest` lines, want one", len(lines))
	}
	pkg := lines[0][1]

	gomod, err := os.Open(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	defer gomod.Close()
	var module string
	for sc := bufio.NewScanner(gomod); sc.Scan(); {
		if m, ok := strings.CutPrefix(sc.Text(), "module "); ok {
			module = strings.TrimSpace(m)
			break
		}
	}
	rel, ok := strings.CutPrefix(pkg, module)
	if module == "" || !ok || (rel != "" && !strings.HasPrefix(rel, "/")) {
		t.Fatalf("README.md installs %s, which is not a package of this module (%s)", pkg, module)
	}

	dir := filepath.Join("..", filepath.FromSlash(rel))
	sources, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	packages := map[string]bool{}
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), src, nil, parser.PackageClauseOnly)
		if err != nil {
			t.Fatalf("README.md installs %s: %v", pkg, err)
		}
		packages[f.Name.Name] = true
	}
	if len(packages) != 1 || !packages["main"] {
		t.Fatalf("README.md installs %s, which is not a command (packages in %s: %v)", pkg, dir, packages)
	}
	if got, want := path.Base(pkg), newRootCmd().Name(); got != want {
		t.Errorf("README.md installs %s, which go install names %q; every command runs %q", pkg, got, want)
	}
}

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
