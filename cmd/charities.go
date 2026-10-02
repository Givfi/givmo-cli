package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// charity is the CLI's display view of a charity, mapped from the search_charities
// / get_charity_profile MCP tool result. It deliberately carries no
// deductibility/eligibility authority beyond the platform-supplied IRS Pub 78
// snapshot — those are resolved by Givmo, not asserted client-side.
type charity struct {
	CharityID  string   `json:"charity_id"`
	Name       string   `json:"name"`
	EIN        string   `json:"ein,omitempty"`
	Categories []string `json:"categories,omitempty"`
	State      string   `json:"state,omitempty"`
	Mission    string   `json:"mission,omitempty"`
	Website    string   `json:"website,omitempty"`
	LogoURL    string   `json:"logo_url,omitempty"`
	// Pub78 is present only on a get (get_charity_profile) result.
	Pub78 *charityPub78 `json:"irs_pub78_status,omitempty"`
}

// charityPub78 is the platform-supplied IRS Pub 78 deductibility snapshot.
type charityPub78 struct {
	Pub78Listed        bool   `json:"pub78_listed"`
	DeductibilityCodes string `json:"deductibility_codes,omitempty"`
	AutoRevoked        bool   `json:"auto_revoked"`
	AsOf               string `json:"as_of,omitempty"`
}

// charitySearchResult is the search_charities tool payload.
type charitySearchResult struct {
	Provenance string    `json:"provenance"`
	Query      string    `json:"query"`
	EIN        string    `json:"ein"`
	Count      int       `json:"count"`
	Charities  []charity `json:"charities"`
}

// charityProfileResult is the get_charity_profile tool payload.
type charityProfileResult struct {
	Provenance string  `json:"provenance"`
	Charity    charity `json:"charity"`
}

var charitiesEIN string

func newCharitiesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "charities",
		Short: "Search and inspect charities in the public catalog",
		Long: `Search the Givmo public charity catalog and fetch a single charity profile.

This is public-tier catalog data served by the search_charities / get_charity_profile
MCP tools; no login is required. Search matches by name/keyword and/or exact EIN and
returns the top matches Givmo actively lists (the server caps the result set).`,
	}
	cmd.AddCommand(newCharitiesSearchCmd(), newCharitiesGetCmd())
	return cmd
}

func newCharitiesSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search [query] [--ein <EIN>]",
		Short: "Search charities by name/keyword and/or exact EIN",
		Long: `Search the public charity catalog. Provide a name/keyword query, an exact
--ein, or both (at least one is required). Returns the charities Givmo actively
lists, each with its opaque charity_id (pass it to 'charities get').`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			query := ""
			if len(args) == 1 {
				query = strings.TrimSpace(args[0])
			}
			ein := strings.TrimSpace(charitiesEIN)
			if query == "" && ein == "" {
				return output.New(output.ExitUsage,
					"a search term is required",
					"Pass a name/keyword query and/or --ein <EIN>, e.g. `givmo charities search \"clean water\"`.")
			}
			toolArgs := map[string]any{}
			if query != "" {
				toolArgs["query"] = query
			}
			if ein != "" {
				toolArgs["ein"] = ein
			}
			ctx, cancel := toolContext()
			defer cancel()

			payload, err := app.callTool(ctx, "search_charities", toolArgs, false, "")
			if err != nil {
				return err
			}
			var res charitySearchResult
			if uerr := decodeToolPayload(payload, &res); uerr != nil {
				return uerr
			}
			return app.Printer.Result(res, func(w io.Writer) {
				if len(res.Charities) == 0 {
					fmt.Fprintln(w, "no charities matched")
					return
				}
				rows := make([][]string, 0, len(res.Charities))
				for _, ch := range res.Charities {
					rows = append(rows, []string{ch.CharityID, ch.Name, ch.EIN, ch.State, strings.Join(ch.Categories, ", ")})
				}
				output.Table(w, []string{"CHARITY_ID", "NAME", "EIN", "STATE", "CATEGORIES"}, rows)
				if res.Provenance != "" {
					fmt.Fprintf(w, "\nsource: %s\n", res.Provenance)
				}
			})
		},
	}
	cmd.Flags().StringVar(&charitiesEIN, "ein", "", "exact EIN (tax id) to match")
	return cmd
}

func newCharitiesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <charity_id|EIN>",
		Short: "Fetch a single charity profile by opaque charity_id (or EIN)",
		Long: `Fetch one charity's full catalog profile via get_charity_profile.

Pass the opaque charity_id (ch_…) from 'charities search'. For convenience an EIN
is also accepted: the CLI resolves it to a charity_id via search first, then loads
the profile.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			ctx, cancel := toolContext()
			defer cancel()

			charityID, err := app.resolveCharityID(ctx, strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}
			payload, err := app.callTool(ctx, "get_charity_profile", map[string]any{"charity_id": charityID}, false, "")
			if err != nil {
				return err
			}
			var res charityProfileResult
			if uerr := decodeToolPayload(payload, &res); uerr != nil {
				return uerr
			}
			ch := res.Charity
			return app.Printer.Result(res, func(w io.Writer) {
				pairs := [][2]string{
					{"charity_id", ch.CharityID},
					{"name", ch.Name},
					{"ein", ch.EIN},
					{"state", ch.State},
					{"categories", strings.Join(ch.Categories, ", ")},
					{"website", ch.Website},
					{"mission", ch.Mission},
				}
				if ch.Pub78 != nil {
					pairs = append(pairs,
						[2]string{"pub78_listed", fmt.Sprintf("%t", ch.Pub78.Pub78Listed)},
						[2]string{"deductibility_codes", ch.Pub78.DeductibilityCodes},
						[2]string{"auto_revoked", fmt.Sprintf("%t", ch.Pub78.AutoRevoked)},
						[2]string{"pub78_as_of", ch.Pub78.AsOf},
					)
				}
				output.KeyValues(w, pairs)
				if res.Provenance != "" {
					fmt.Fprintf(w, "\nsource: %s\n", res.Provenance)
				}
			})
		},
	}
}

// resolveCharityID returns an opaque charity_id for get_charity_profile. An
// argument already shaped like an opaque id (ch_…) is used verbatim; an EIN is
// resolved to a charity_id via a search_charities lookup; anything else is passed
// through (get_charity_profile returns a clean not_found for a bad id).
func (a *appCtx) resolveCharityID(ctx context.Context, arg string) (string, error) {
	if strings.HasPrefix(arg, "ch_") || !looksLikeEIN(arg) {
		return arg, nil
	}
	payload, err := a.callTool(ctx, "search_charities", map[string]any{"ein": arg}, false, "")
	if err != nil {
		return "", err
	}
	var res charitySearchResult
	if uerr := decodeToolPayload(payload, &res); uerr != nil {
		return "", uerr
	}
	if len(res.Charities) == 0 {
		return "", output.New(output.ExitNotFound,
			fmt.Sprintf("no charity Givmo lists matches EIN %q", arg),
			"Verify the EIN, or search by name with `givmo charities search <query>`.")
	}
	return res.Charities[0].CharityID, nil
}

// looksLikeEIN reports whether s is a US EIN (nine digits, optionally with a
// single dash, e.g. "12-3456789" or "123456789"). Pure → unit-tested.
func looksLikeEIN(s string) bool {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(digits) != 9 {
		return false
	}
	// Reject if the original carried characters other than digits and a single dash.
	stripped := strings.ReplaceAll(s, "-", "")
	return stripped == digits
}
