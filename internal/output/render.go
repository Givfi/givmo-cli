package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// Printer renders command results either as JSON (machine-readable) or as a
// human table/text form. Every command takes a *Printer so `--json` behaves
// uniformly everywhere.
type Printer struct {
	JSON bool
	Out  io.Writer
	Err  io.Writer
}

// NewPrinter builds a Printer. jsonMode selects the machine-readable form.
func NewPrinter(out, errw io.Writer, jsonMode bool) *Printer {
	return &Printer{JSON: jsonMode, Out: out, Err: errw}
}

// Result renders a successful command result. In JSON mode it emits `v` as
// indented JSON. Otherwise it invokes human, which should render the same data
// as a table/text. `human` may be nil (JSON-only payloads).
func (p *Printer) Result(v any, human func(w io.Writer)) error {
	if p.JSON {
		return p.writeJSON(v)
	}
	if human != nil {
		human(p.Out)
		return nil
	}
	// Fall back to JSON if a command supplied no human renderer.
	return p.writeJSON(v)
}

func (p *Printer) writeJSON(v any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Errorf renders an *Error through the single envelope. In JSON mode it emits
// the envelope as JSON to stderr; otherwise a compact human block. It never
// prints secrets — the Error type has no secret-bearing fields.
func (p *Printer) Errorf(e *Error) {
	if e == nil {
		return
	}
	if p.JSON {
		enc := json.NewEncoder(p.Err)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(struct {
			Error *Error `json:"error"`
		}{e})
		return
	}
	fmt.Fprintf(p.Err, "error: %s\n", e.Message)
	if e.Remediation != "" {
		fmt.Fprintf(p.Err, "  remediation: %s\n", e.Remediation)
	}
	if e.RequestID != "" {
		fmt.Fprintf(p.Err, "  request_id:  %s\n", e.RequestID)
	}
	fmt.Fprintf(p.Err, "  exit_code:   %d (%s)\n", e.Code, CodeName(e.Code))
	if e.DocURL != "" {
		fmt.Fprintf(p.Err, "  docs:        %s\n", e.DocURL)
	}
}

// Table renders rows as an aligned text table with the given headers. Each row
// must have len(headers) columns. This is the shared human renderer for list
// commands.
func Table(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

// KeyValues renders an ordered set of key/value pairs as a two-column block.
// Keys are printed in the order given (not sorted) to preserve semantic order.
func KeyValues(w io.Writer, pairs [][2]string) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, kv := range pairs {
		fmt.Fprintf(tw, "%s:\t%s\n", kv[0], kv[1])
	}
	_ = tw.Flush()
}

// SortedKeys returns the keys of m in stable sorted order (helper for
// deterministic human output of maps).
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
