// Package donate is the reference parser for the donate.json manifest
// specification v1.0 (see ../SPEC.md).
//
// SECURITY MODEL — READ THIS FIRST.
//
// A donate.json manifest is UNTRUSTED, attacker-writable input. The website
// operator controls every byte.
//
// The specification's security guarantee lives in CONSUMER BEHAVIOR (SPEC.md
// §7.3), not in this parser's data types. Every conforming consumer — including
// one written from the JSON Schema alone — MUST NOT map any manifest field
// (defined, unknown, x_extensions, or free-text value) to a decision about
//
//	(a) who gets paid / recipient eligibility,
//	(b) tax-deductibility, or
//	(c) any legal disclosure, acknowledgment, or receipt copy.
//
// Those decisions are made by the CONSUMING PLATFORM against out-of-band
// authoritative sources (for Givmo: IRS Pub 78 / TEOS / BMF for eligibility,
// and a server-authored versioned registry for all legal/receipt copy) and its
// own controlled donation flow.
//
// This parser IMPLEMENTS that consumer rule with a robust technique (SPEC.md
// §7.4): a data model in which the three authorities are UNREPRESENTABLE — the
// Manifest type has no member for deductibility, eligibility/good-standing,
// receipt text, or legal copy, so hostile data has nowhere to land. This is a
// recommended pattern, but it is an implementation technique, not the spec's
// guarantee; the guarantee is the behavioral rule above. (The technique covers
// the THREE AUTHORITIES specifically; URLs ARE representable fields and are
// non-authoritative by CONSUMER RULE — see SPEC.md §7.6 — not by absence.)
//
// Two entry points:
//
//	Parse    — strict: rejects any unknown field (schema additionalProperties:false
//	           equivalent). Fails closed on a malformed or hostile manifest.
//	Sanitize — tolerant: accepts the well-formed subset, DROPS every unknown
//	           field (it never lands on the typed result) and DELETES every
//	           authority-named x_extensions key from the returned bag, BLANKS any
//	           invalid URL it would otherwise keep (a non-https URL is blanked,
//	           not passed through), and returns the dropped fields as
//	           RejectedClaims plus non-fatal Warnings so the caller can
//	           log/telemeter the attack.
//
// The authority-name scan (RejectedClaims) is a BEST-EFFORT, key-name-only
// TELEMETRY signal (SPEC.md §7.5). It is deliberately incomplete — it misses
// keys it does not enumerate and never inspects values — and NO security
// property depends on its completeness, precisely because such keys land in
// opaque x_extensions / unknown fields and are never mapped to authority. A
// clean scan is not evidence a manifest is benign.
//
// This package depends only on the Go standard library so it compiles cleanly
// into givmo-cli (`givmo manifest validate|sign`) with no external modules.
package donate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// SpecVersion is the manifest spec major.minor this parser implements.
const SpecVersion = "1.0"

// WellKnownPath is the fixed location a manifest is published at.
const WellKnownPath = "/.well-known/donate.json"

// ----------------------------------------------------------------------------
// The manifest type. Note what is ABSENT: there is deliberately no field for
// deductibility, qualification/good-standing, receipt text, legal disclosures,
// or an authoritative action/pay URL. Those are unrepresentable by design.
// ----------------------------------------------------------------------------

// Manifest is the parsed, trusted-for-DISPLAY-ONLY view of a donate.json.
type Manifest struct {
	ManifestVersion string       `json:"manifest_version"`
	GeneratedAt     string       `json:"generated_at,omitempty"`
	ExpiresAt       string       `json:"expires_at,omitempty"`
	Organization    Organization `json:"organization"`
	Donation        *Donation    `json:"donation,omitempty"`
	Display         *Display     `json:"display,omitempty"`
	Contact         *Contact     `json:"contact,omitempty"`
	Signature       *Signature   `json:"signature,omitempty"`
	// XExtensions is carried opaquely and is ALWAYS untrusted display data.
	XExtensions map[string]json.RawMessage `json:"x_extensions,omitempty"`
}

// Organization is the self-asserted identity of the publisher. Every field is a
// CLAIM. In particular EIN is a lookup KEY into IRS data, never proof of
// eligibility or deductibility.
type Organization struct {
	LegalName   string   `json:"legal_name"`
	AlsoKnownAs []string `json:"also_known_as,omitempty"`
	Country     string   `json:"country"`
	EIN         string   `json:"ein,omitempty"`
	Website     string   `json:"website,omitempty"`
	Logo        string   `json:"logo,omitempty"`
	Description string   `json:"description,omitempty"`
}

// Donation declares the discovery mode only. It cannot supply an authoritative
// action URL. See Method.
type Donation struct {
	Method        string `json:"method"`
	SelfHostedURL string `json:"self_hosted_url,omitempty"`
}

// Display holds cosmetic, non-authoritative hints.
type Display struct {
	PrimaryColor string   `json:"primary_color,omitempty"`
	Categories   []string `json:"categories,omitempty"`
	Languages    []string `json:"languages,omitempty"`
}

// Contact holds non-authoritative contact info for display / operator outreach.
type Contact struct {
	SupportEmail string `json:"support_email,omitempty"`
	SupportURL   string `json:"support_url,omitempty"`
}

// Signature is the optional detached-JWS integrity envelope. Verifying it proves
// authorship by an out-of-band-trusted key; it confers NO authority on any
// field. See VerifySignature.
type Signature struct {
	Alg string `json:"alg"`
	Kid string `json:"kid,omitempty"`
	JWS string `json:"jws"`
}

// Method values.
const (
	MethodMediated   = "mediated"    // org opts into mediated (platform-controlled) discovery
	MethodSelfHosted = "self_hosted" // informational; agentic consumers MUST NOT auto-transact
	MethodNone       = "none"        // identity only; no programmatic donation discovery
)

// ----------------------------------------------------------------------------
// Errors & findings
// ----------------------------------------------------------------------------

// ErrRejected is returned by Parse when the manifest is not schema-valid or
// carries disallowed fields.
var ErrRejected = errors.New("donate.json manifest rejected")

// RejectedClaim records one field that attempted to overreach and was dropped.
type RejectedClaim struct {
	// Path is a dotted JSON path, e.g. "organization.deductibility_statement".
	Path string
	// Reason is a human-readable explanation for logs/telemetry.
	Reason string
}

func (r RejectedClaim) String() string { return r.Path + ": " + r.Reason }

// Result is the outcome of Sanitize.
type Result struct {
	// Manifest is the clean, display-only record (nil if the well-formed core
	// could not be parsed at all).
	Manifest *Manifest
	// RejectedClaims lists every unknown / authority-asserting field that was
	// dropped. A non-empty slice on an otherwise-parseable manifest is a strong
	// signal of a hostile or non-conforming publisher — telemeter it.
	RejectedClaims []RejectedClaim
	// Warnings are non-fatal conformance issues (bad enum, non-https URL, etc.).
	Warnings []string
}

// ----------------------------------------------------------------------------
// Authority-name blocklist — BEST-EFFORT TELEMETRY ONLY (SPEC.md §7.5).
//
// This list drives a key-NAME-only, value-blind, deliberately-INCOMPLETE scan.
// NO security property depends on its completeness: the guarantee is the
// consumer-behavior rule (authority is never mapped in from the manifest) plus,
// in this implementation, the unrepresentable-authority technique. This scan
// exists to SURFACE hostile intent (log/block/telemeter), not to be the defense.
// It will miss keys it does not enumerate (payee, beneficiary, iban, homoglyphs,
// novel coinages) and never inspects values — and that is acceptable, because
// such keys land in opaque x_extensions / unknown fields and are never mapped to
// an authority decision regardless. A clean scan is NOT evidence of benignity.
// (In tolerant Sanitize, a scan-flagged x_extensions key is additionally DELETED
// from the returned bag — hygiene on top of the consumer rule, still not the
// load-bearing guarantee.)
// ----------------------------------------------------------------------------

var authoritySubstrings = []string{
	// deductibility / tax claims
	"deduct", "tax_", "_tax", "501c", "501(c)", "irs_", "pub78", "pub_78",
	// eligibility / standing self-attestation
	"qualified", "good_standing", "goodstanding", "eligib", "verified", "approved",
	"catalog_accepted", "override", "skip_verification", "confirmed_recipient",
	// receipts / legal copy
	"receipt", "acknowledg", "disclosure", "disclaimer", "legal_disclosure",
	"legal_copy", "legal_text", "legal_statement",
	// payee-authority URLs / routing / credentials
	"action_url", "actionurl", "payto", "pay_to", "authoriz", "bearer", "token",
	"credential", "secret", "execute", "grant_url", "route",
}

// specFieldNames is the set of legitimate, spec-defined key names (across all
// objects). The authority-name heuristic is only meant for UNKNOWN keys — the
// known fields are already safe because the Manifest type has no authority
// members. Exempting them prevents false positives such as "legal_name"
// tripping the "legal_" family. (Verified: an earlier broad "legal_" substring
// misflagged organization.legal_name.)
var specFieldNames = map[string]bool{
	"manifest_version": true, "generated_at": true, "expires_at": true,
	"organization": true, "donation": true, "display": true, "contact": true,
	"signature": true, "x_extensions": true,
	"legal_name": true, "also_known_as": true, "country": true, "ein": true,
	"website": true, "logo": true, "description": true,
	"method": true, "self_hosted_url": true,
	"primary_color": true, "categories": true, "languages": true,
	"support_email": true, "support_url": true,
	"alg": true, "kid": true, "jws": true,
}

// looksLikeAuthorityKey reports whether a JSON key name is attempting to assert
// payee-authority, deductibility, or legal copy. Spec-defined field names are
// exempt (they are safe by type and must not be misflagged).
func looksLikeAuthorityKey(key string) bool {
	if specFieldNames[key] {
		return false
	}
	k := strings.ToLower(key)
	for _, s := range authoritySubstrings {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// Parse — strict mode. Rejects unknown fields.
// ----------------------------------------------------------------------------

// Parse strictly decodes and validates a manifest. Any unknown field (i.e. any
// field not in the spec) causes rejection, matching the schema's
// additionalProperties:false. Returns ErrRejected (wrapped) on any failure.
func Parse(data []byte) (*Manifest, error) {
	// First pass: reject unknown fields anywhere at the top level and in known
	// nested objects via DisallowUnknownFields.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	// Reject trailing garbage after the JSON value.
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data after JSON document", ErrRejected)
	}
	// Second pass: even with a clean struct decode, scan x_extensions (which is
	// an open map by design) for authority-asserting keys and reject.
	if claims := scanExtensionsForAuthority(m.XExtensions); len(claims) > 0 {
		return nil, fmt.Errorf("%w: authority-asserting field in x_extensions: %s",
			ErrRejected, claims[0].Path)
	}
	if errs := validate(&m); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrRejected, strings.Join(errs, "; "))
	}
	return &m, nil
}

// ----------------------------------------------------------------------------
// Sanitize — tolerant mode. Accepts the well-formed subset, drops overreach.
// ----------------------------------------------------------------------------

// Sanitize tolerantly parses a manifest: it accepts the well-formed core, DROPS
// every unknown or authority-asserting field, and reports what it dropped —
// unknown fields never land on the typed result, and an authority-named
// x_extensions key is DELETED from the returned bag (while staying listed in
// RejectedClaims for telemetry). An authority-named key nested deeper inside a
// retained extension VALUE is reported by the scan but the value itself is
// retained as opaque, display-only data — it is never mapped to authority
// (SPEC.md §7.3). Sanitize never fails on overreach — that is the point — but
// it does return a nil Manifest if the mandatory core (manifest_version +
// organization) is unparseable.
func Sanitize(data []byte) *Result {
	res := &Result{}

	// Decode into a generic tree so we can see everything the attacker sent.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		res.Warnings = append(res.Warnings, "manifest is not a JSON object: "+err.Error())
		return res
	}

	// Walk the whole tree and record any authority-asserting key at any depth.
	// These are DROPPED regardless of where they appear.
	res.RejectedClaims = scanTreeForAuthority("", data)

	// Now re-decode only the KNOWN fields into the typed Manifest, tolerantly
	// (unknown fields are simply ignored by encoding/json without
	// DisallowUnknownFields). Because the type has no authority fields, nothing
	// dangerous can land here even if the scan missed a novel name.
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		res.Warnings = append(res.Warnings, "could not parse known fields: "+err.Error())
		return res
	}

	// Record every top-level and nested unknown key as a dropped claim too (not
	// just authority-named ones), so the caller has a full picture.
	res.RejectedClaims = append(res.RejectedClaims, unknownFieldClaims(data)...)

	// DELETE any scan-flagged authority-named key from the returned x_extensions
	// bag. The tree scan above already recorded each such key as a RejectedClaim;
	// deleting it here makes the tolerant contract exact (SPEC.md §8): every
	// unknown or authority-asserting field is dropped from the result, not merely
	// reported. Non-flagged extension keys are retained opaquely as untrusted,
	// display-only data.
	for k := range m.XExtensions {
		if looksLikeAuthorityKey(k) {
			delete(m.XExtensions, k)
		}
	}

	// DROP (do not merely warn about) any schema-invalid URL we would otherwise
	// keep: a non-https or malformed website/logo/self_hosted_url/support_url is
	// blanked and reported, so a downstream caller never receives an invalid URL
	// as if it were valid (SPEC.md §8, finding: tolerant mode must not pass
	// invalid URLs through). Callers still MUST re-validate URLs at use time.
	res.RejectedClaims = append(res.RejectedClaims, blankInvalidURLs(&m)...)
	res.RejectedClaims = dedupeClaims(res.RejectedClaims)

	// Remaining validation issues become warnings (tolerant mode does not reject).
	for _, e := range validate(&m) {
		res.Warnings = append(res.Warnings, e)
	}

	// What remains of x_extensions (authority-named keys were deleted above)
	// stays untrusted, opaque, display-only data.
	res.Manifest = &m
	return res
}

// blankInvalidURLs zeroes any URL field on m that is not a valid https URL and
// returns a RejectedClaim for each one blanked. This is the tolerant-mode
// guarantee that invalid URLs never survive.
func blankInvalidURLs(m *Manifest) []RejectedClaim {
	var claims []RejectedClaim
	blank := func(field string, val *string) {
		if *val == "" {
			return
		}
		if !isValidHTTPS(*val) {
			claims = append(claims, RejectedClaim{
				Path:   field,
				Reason: "not a valid https URL; blanked (tolerant mode drops invalid URLs)",
			})
			*val = ""
		}
	}
	blank("organization.website", &m.Organization.Website)
	blank("organization.logo", &m.Organization.Logo)
	if m.Donation != nil {
		blank("donation.self_hosted_url", &m.Donation.SelfHostedURL)
	}
	if m.Contact != nil {
		blank("contact.support_url", &m.Contact.SupportURL)
	}
	return claims
}

// isValidHTTPS reports whether raw is an absolute https URL with a host.
func isValidHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// ----------------------------------------------------------------------------
// Validation (shared). These check well-formedness, NOT authority.
// ----------------------------------------------------------------------------

var (
	reMajorMinor = regexp.MustCompile(`^1\.[0-9]+(\.[0-9]+)?$`)
	reEIN        = regexp.MustCompile(`^[0-9]{2}-[0-9]{7}$`)
	reCountry    = regexp.MustCompile(`^[A-Z]{2}$`)
	reHexColor   = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	reDetachdJWS = regexp.MustCompile(`^[A-Za-z0-9_-]+\.\.[A-Za-z0-9_-]+$`)
	// reEmail is the schema's conservative support_email shape backstop
	// (format:email is annotation-only in many JSON Schema validators).
	reEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	// reLangTag is the schema's languages[] entry pattern (BCP 47-shaped).
	reLangTag = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// maxLen enforces a schema maxLength bound, counted in Unicode code points to
// match JSON Schema string-length semantics (not bytes). Empty strings are
// skipped: presence/required checks are handled separately.
func maxLen(field, val string, max int) []string {
	if val != "" && utf8.RuneCountInString(val) > max {
		return []string{fmt.Sprintf("%s must be at most %d characters (schema maxLength)", field, max)}
	}
	return nil
}

var allowedSigAlgs = map[string]bool{
	"EdDSA": true, "ES256": true, "ES384": true, "RS256": true,
}

func validate(m *Manifest) []string {
	var errs []string

	if m.ManifestVersion == "" {
		errs = append(errs, "manifest_version is required")
	} else if !reMajorMinor.MatchString(m.ManifestVersion) {
		errs = append(errs, "manifest_version must be a 1.x version this parser implements")
	}

	if m.Organization.LegalName == "" {
		errs = append(errs, "organization.legal_name is required")
	}
	errs = append(errs, maxLen("organization.legal_name", m.Organization.LegalName, 300)...)
	if len(m.Organization.AlsoKnownAs) > 20 {
		errs = append(errs, "organization.also_known_as must have at most 20 entries (schema maxItems)")
	}
	for i, aka := range m.Organization.AlsoKnownAs {
		if aka == "" {
			errs = append(errs, fmt.Sprintf("organization.also_known_as[%d] must be a non-empty string", i))
		}
		errs = append(errs, maxLen(fmt.Sprintf("organization.also_known_as[%d]", i), aka, 300)...)
	}
	if m.Organization.Country == "" {
		errs = append(errs, "organization.country is required")
	} else if !reCountry.MatchString(m.Organization.Country) {
		errs = append(errs, "organization.country must be an ISO 3166-1 alpha-2 code")
	}
	if m.Organization.EIN != "" && !reEIN.MatchString(m.Organization.EIN) {
		errs = append(errs, "organization.ein must match NN-NNNNNNN (it is a lookup key, not proof of eligibility)")
	}
	errs = append(errs, requireHTTPS("organization.website", m.Organization.Website, false)...)
	errs = append(errs, requireHTTPS("organization.logo", m.Organization.Logo, false)...)
	errs = append(errs, maxLen("organization.description", m.Organization.Description, 2000)...)

	if m.GeneratedAt != "" && !isRFC3339(m.GeneratedAt) {
		errs = append(errs, "generated_at must be an RFC 3339 timestamp")
	}
	if m.ExpiresAt != "" && !isRFC3339(m.ExpiresAt) {
		errs = append(errs, "expires_at must be an RFC 3339 timestamp")
	}

	if m.Donation != nil {
		switch m.Donation.Method {
		case MethodMediated, MethodNone:
			if m.Donation.SelfHostedURL != "" {
				errs = append(errs, "donation.self_hosted_url is only permitted when method is self_hosted")
			}
		case MethodSelfHosted:
			errs = append(errs, requireHTTPS("donation.self_hosted_url", m.Donation.SelfHostedURL, false)...)
		case "":
			errs = append(errs, "donation.method is required when donation is present")
		default:
			errs = append(errs, "donation.method must be one of: mediated, self_hosted, none")
		}
	}

	if m.Display != nil {
		if m.Display.PrimaryColor != "" && !reHexColor.MatchString(m.Display.PrimaryColor) {
			errs = append(errs, "display.primary_color must be a #RRGGBB hex color")
		}
		if len(m.Display.Categories) > 20 {
			errs = append(errs, "display.categories must have at most 20 entries (schema maxItems)")
		}
		for i, c := range m.Display.Categories {
			if c == "" {
				errs = append(errs, fmt.Sprintf("display.categories[%d] must be a non-empty string", i))
			}
			errs = append(errs, maxLen(fmt.Sprintf("display.categories[%d]", i), c, 60)...)
		}
		if len(m.Display.Languages) > 20 {
			errs = append(errs, "display.languages must have at most 20 entries (schema maxItems)")
		}
		for i, l := range m.Display.Languages {
			if !reLangTag.MatchString(l) {
				errs = append(errs, fmt.Sprintf("display.languages[%d] must be a BCP 47-shaped language tag", i))
			}
		}
	}

	if m.Contact != nil {
		if m.Contact.SupportEmail != "" && !reEmail.MatchString(m.Contact.SupportEmail) {
			errs = append(errs, "contact.support_email must be an email-shaped address (display/outreach only; never an authority input)")
		}
		errs = append(errs, maxLen("contact.support_email", m.Contact.SupportEmail, 254)...)
		errs = append(errs, requireHTTPS("contact.support_url", m.Contact.SupportURL, false)...)
	}

	if m.Signature != nil {
		if !allowedSigAlgs[m.Signature.Alg] {
			errs = append(errs, "signature.alg must be an allowlisted asymmetric algorithm (EdDSA, ES256, ES384, RS256); 'none' and HMAC are forbidden")
		}
		errs = append(errs, maxLen("signature.kid", m.Signature.Kid, 256)...)
		if !reDetachdJWS.MatchString(m.Signature.JWS) {
			errs = append(errs, "signature.jws must be a detached JWS compact serialization (header..signature)")
		}
	}

	return errs
}

func requireHTTPS(field, raw string, required bool) []string {
	if raw == "" {
		if required {
			return []string{field + " is required"}
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return []string{field + " is not a valid URL"}
	}
	if u.Scheme != "https" {
		return []string{field + " must be an absolute https URL"}
	}
	if u.Host == "" {
		return []string{field + " must have a host"}
	}
	return nil
}

func isRFC3339(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// ----------------------------------------------------------------------------
// Tree scanning for authority-named keys — best-effort telemetry (SPEC.md §7.5).
// Incomplete by design; not a security boundary. See the blocklist note above.
// ----------------------------------------------------------------------------

// scanTreeForAuthority walks the entire JSON document and returns a
// RejectedClaim for every key (at any depth) whose NAME asserts authority.
func scanTreeForAuthority(prefix string, data []byte) []RejectedClaim {
	var claims []RejectedClaim
	var node interface{}
	if err := json.Unmarshal(data, &node); err != nil {
		return claims
	}
	var walk func(path string, v interface{})
	walk = func(path string, v interface{}) {
		switch t := v.(type) {
		case map[string]interface{}:
			for k, child := range t {
				childPath := k
				if path != "" {
					childPath = path + "." + k
				}
				if looksLikeAuthorityKey(k) {
					claims = append(claims, RejectedClaim{
						Path:   childPath,
						Reason: "field name asserts payee-authority, deductibility, or legal copy (manifest is untrusted; never mapped to authority)",
					})
				}
				walk(childPath, child)
			}
		case []interface{}:
			for i, child := range t {
				walk(fmt.Sprintf("%s[%d]", path, i), child)
			}
		}
	}
	walk(prefix, node)
	return claims
}

// scanExtensionsForAuthority scans only the x_extensions bag.
func scanExtensionsForAuthority(ext map[string]json.RawMessage) []RejectedClaim {
	var claims []RejectedClaim
	for k, v := range ext {
		if looksLikeAuthorityKey(k) {
			claims = append(claims, RejectedClaim{
				Path:   "x_extensions." + k,
				Reason: "authority-asserting key in x_extensions; forbidden",
			})
		}
		claims = append(claims, scanTreeForAuthority("x_extensions."+k, v)...)
	}
	return claims
}

// unknownFieldClaims reports top-level and known-nested-object keys that are not
// part of the spec (so the tolerant path can report everything it dropped).
func unknownFieldClaims(data []byte) []RejectedClaim {
	var claims []RejectedClaim
	known := map[string]map[string]bool{
		"": {
			"manifest_version": true, "generated_at": true, "expires_at": true,
			"organization": true, "donation": true, "display": true,
			"contact": true, "signature": true, "x_extensions": true,
		},
		"organization": {
			"legal_name": true, "also_known_as": true, "country": true,
			"ein": true, "website": true, "logo": true, "description": true,
		},
		"donation":  {"method": true, "self_hosted_url": true},
		"display":   {"primary_color": true, "categories": true, "languages": true},
		"contact":   {"support_email": true, "support_url": true},
		"signature": {"alg": true, "kid": true, "jws": true},
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return claims
	}
	for k, v := range top {
		if !known[""][k] {
			claims = append(claims, RejectedClaim{Path: k, Reason: "unknown top-level field; dropped"})
			continue
		}
		// descend one level for the known objects we enumerate
		if sub, ok := known[k]; ok {
			var obj map[string]json.RawMessage
			if json.Unmarshal(v, &obj) == nil {
				for sk := range obj {
					if !sub[sk] {
						claims = append(claims, RejectedClaim{
							Path:   k + "." + sk,
							Reason: "unknown field; dropped",
						})
					}
				}
			}
		}
	}
	return claims
}

func dedupeClaims(in []RejectedClaim) []RejectedClaim {
	seen := map[string]bool{}
	var out []RejectedClaim
	for _, c := range in {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ----------------------------------------------------------------------------
// Signing / verification — textual excision, NO canonicalization (SPEC.md §6.2).
//
// The signing payload P is the manifest bytes M with the "signature" member
// removed by a precise TEXTUAL excision — never by parse-and-re-serialize. This
// eliminates the JSON-canonicalization interoperability hazard entirely: there
// is no canonical form to agree on, so number-formatting and key-ordering
// divergences cannot make signatures fail to cross-verify. The one formatting
// constraint signing imposes is that "signature" MUST be the LAST top-level
// member; the payload is then M with everything from the comma before
// "signature" up to (but not including) the root closing '}' deleted.
//
// In BOTH the signed and the unsigned form, the payload ends at the root
// object's closing '}': bytes after it (trailing whitespace such as a final
// newline — the only thing JSON permits there) are never part of the payload
// (SPEC.md §6.2). That symmetry is what makes sign-then-append verify.
//
// The reference parser INTENTIONALLY does not bundle a JOSE/crypto dependency:
// the security model does not rely on the signature for authority. VerifySignature
// performs the structural checks (detached form, allowlisted asymmetric alg,
// 'none' rejected) and delegates cryptographic verification to a caller-supplied
// Verifier. givmo-cli wires in a concrete Verifier (crypto/ed25519, crypto/ecdsa)
// over keys it already trusts out-of-band, keeping the reference core dependency-free.
// ----------------------------------------------------------------------------

// Verifier cryptographically verifies a detached JWS over the given signing
// input. signingInput is ASCII(BASE64URL(protectedHeader)) || "." ||
// BASE64URL(payload); the implementation splits the JWS to recover the protected
// header and signature.
type Verifier interface {
	Verify(alg, kid string, signingInput []byte, jws string) error
}

// ErrSigningPayload indicates the signing payload could not be derived from the
// manifest bytes (e.g. "signature" is not the last top-level member).
var ErrSigningPayload = errors.New("cannot derive signing payload")

// SigningPayload returns P: the exact manifest bytes M with the entire
// "signature" member textually excised. It requires that "signature" be the
// LAST member of the top-level object, and returns the byte range from the start
// of M up to (and including) the character before the comma that precedes
// "signature", followed by the closing '}' — i.e. M without the trailing
// ",\"signature\":{…}" segment. The result is itself a well-formed JSON object.
//
// If the manifest has no "signature" member, P is the whole document up to and
// including the root object's closing '}' (the "sign first, then append
// signature" producer path). Per SPEC.md §6.2, bytes after the root '}' —
// trailing whitespace such as a final newline — are excluded from P in BOTH
// forms; the signed form's excision above already ends at the root '}'. This
// symmetry is what lets a producer sign a newline-terminated unsigned file,
// append the "signature" member, and still verify: the verifier's excised
// payload is byte-identical to the payload the signer used.
// SigningPayload never parses-and-re-serializes; it operates on raw bytes so the
// verifier reconstructs byte-for-byte what the signer signed.
func SigningPayload(data []byte) ([]byte, error) {
	// Confirm the top level is an object and find whether/where "signature" is.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("%w: not a JSON object: %v", ErrSigningPayload, err)
	}
	if _, ok := top["signature"]; !ok {
		// No signature member: the payload is the document up to and including
		// the root object's closing '}'. json.Unmarshal has already guaranteed
		// that any bytes after the top-level value are whitespace; trim them so
		// the unsigned form derives the identical payload the signed form's
		// excision (which ends at the closing '}') will later reconstruct.
		return bytes.TrimRight(data, " \t\n\r"), nil
	}
	// Require "signature" to be the last member: scan the raw bytes for the LAST
	// top-level key and confirm it is "signature". We do a shallow structural
	// scan rather than trusting map order (Go maps are unordered).
	lastKey, keyStart, err := lastTopLevelKey(data)
	if err != nil {
		return nil, err
	}
	if lastKey != "signature" {
		return nil, fmt.Errorf("%w: \"signature\" must be the last top-level member (found %q last)", ErrSigningPayload, lastKey)
	}
	// keyStart points at the opening quote of "signature". Walk back over any
	// insignificant whitespace to the separating comma, then drop from the comma
	// onward and re-close the object.
	i := keyStart - 1
	for i >= 0 && isJSONWS(data[i]) {
		i--
	}
	if i < 0 || data[i] != ',' {
		return nil, fmt.Errorf("%w: malformed separator before \"signature\"", ErrSigningPayload)
	}
	// data[:i] is everything up to (not including) the comma. Append the closing
	// brace to re-form a valid object.
	payload := make([]byte, 0, i+1)
	payload = append(payload, data[:i]...)
	payload = append(payload, '}')
	// Sanity: the result must parse as an object.
	var check map[string]json.RawMessage
	if err := json.Unmarshal(payload, &check); err != nil {
		return nil, fmt.Errorf("%w: excised payload does not parse: %v", ErrSigningPayload, err)
	}
	return payload, nil
}

// lastTopLevelKey returns the name and byte offset (of its opening quote) of the
// last key in the top-level JSON object, using a depth-aware string-aware scan.
func lastTopLevelKey(data []byte) (name string, quoteOffset int, err error) {
	depth := 0
	inStr := false
	esc := false
	// Track, at depth 1, the most recent key string encountered.
	var curKeyStart = -1
	var lastKeyStart = -1
	var lastKeyEnd = -1
	awaitingKey := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
				if depth == 1 && awaitingKey && curKeyStart >= 0 {
					lastKeyStart = curKeyStart
					lastKeyEnd = i
					awaitingKey = false
				}
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			curKeyStart = i
		case '{', '[':
			depth++
			if c == '{' && depth == 1 {
				awaitingKey = true
			}
		case '}', ']':
			depth--
		case ':':
			// value follows; not a key
		case ',':
			if depth == 1 {
				awaitingKey = true
			}
		}
	}
	if lastKeyStart < 0 || lastKeyEnd < 0 {
		return "", -1, fmt.Errorf("%w: no top-level key found", ErrSigningPayload)
	}
	// Unquote the key.
	var k string
	if err := json.Unmarshal(data[lastKeyStart:lastKeyEnd+1], &k); err != nil {
		return "", -1, fmt.Errorf("%w: bad key encoding: %v", ErrSigningPayload, err)
	}
	return k, lastKeyStart, nil
}

func isJSONWS(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// VerifySignature performs structural signature checks and, if a Verifier is
// supplied, cryptographic verification. A nil Verifier means "structure only" —
// callers that need cryptographic assurance MUST pass a real Verifier. Returns
// nil when the manifest is unsigned (unsigned is allowed; it simply carries no
// integrity assurance). Signature validity NEVER confers field authority.
func VerifySignature(data []byte, m *Manifest, v Verifier) error {
	if m.Signature == nil {
		return nil // unsigned: allowed, no assurance
	}
	if m.Signature.Alg == "none" || !allowedSigAlgs[m.Signature.Alg] {
		return fmt.Errorf("%w: signature.alg %q not allowed", ErrRejected, m.Signature.Alg)
	}
	if !reDetachdJWS.MatchString(m.Signature.JWS) {
		return fmt.Errorf("%w: signature.jws is not a detached JWS", ErrRejected)
	}
	if v == nil {
		return nil // structure-only check requested
	}
	payload, err := SigningPayload(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRejected, err)
	}
	header := strings.SplitN(m.Signature.JWS, "..", 2)[0]
	signingInput := append([]byte(header+"."), b64url(payload)...)
	if err := v.Verify(m.Signature.Alg, m.Signature.Kid, signingInput, m.Signature.JWS); err != nil {
		return fmt.Errorf("%w: signature verification failed: %v", ErrRejected, err)
	}
	return nil
}

func b64url(b []byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(b))
}
