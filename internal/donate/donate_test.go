package donate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	// tests run from the package dir; fixtures live in ./testdata_examples
	p := filepath.Join("testdata_examples", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture %s: %v", p, err)
	}
	return b
}

func TestParse_ValidManifest(t *testing.T) {
	data := readFixture(t, "valid.donate.json")
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("expected valid manifest to parse, got: %v", err)
	}
	if m.Organization.LegalName != "Example Relief Foundation" {
		t.Errorf("legal_name = %q", m.Organization.LegalName)
	}
	if m.Organization.EIN != "12-3456789" {
		t.Errorf("ein = %q", m.Organization.EIN)
	}
	if m.Donation == nil || m.Donation.Method != MethodMediated {
		t.Errorf("expected method mediated")
	}
}

func TestParse_HostileManifest_StrictlyRejected(t *testing.T) {
	data := readFixture(t, "hostile.donate.json")
	if _, err := Parse(data); err == nil {
		t.Fatal("expected hostile manifest to be REJECTED in strict mode, got nil error")
	}
}

func TestSanitize_HostileManifest_DropsAuthorityClaims(t *testing.T) {
	data := readFixture(t, "hostile.donate.json")
	res := Sanitize(data)

	if res.Manifest == nil {
		t.Fatal("expected sanitize to salvage the well-formed core")
	}

	// The core guarantee: no authority field is even representable, so none of
	// the forbidden claims can be present on the returned struct. We assert the
	// dropped-claims report caught the obvious ones.
	mustDrop := []string{
		"organization.tax_deductible",
		"organization.deductibility_statement",
		"organization.is_qualified_501c3",
		"organization.good_standing",
		"organization.pub78_verified",
		"organization.receipt_text",
		"organization.legal_disclosure",
		"donation.action_url",
		"donation.payto",
		"donation.confirmed_recipient",
		"donation.skip_verification",
	}
	got := map[string]bool{}
	for _, c := range res.RejectedClaims {
		got[c.Path] = true
	}
	for _, want := range mustDrop {
		if !got[want] {
			t.Errorf("expected dropped claim %q to be reported; report was: %v", want, claimPaths(res))
		}
	}

	// x_extensions authority keys must also be flagged.
	for _, want := range []string{
		"x_extensions.givmo:eligibility_override",
		"x_extensions.authorization",
	} {
		if !got[want] {
			t.Errorf("expected x_extensions authority key %q to be flagged", want)
		}
	}

	// ...and DELETED from the returned bag, not merely reported (SPEC.md §8):
	// tolerant mode drops every unknown or authority-asserting field from the
	// result. All three of the fixture's extension keys are scan-flagged.
	for _, k := range []string{"givmo:catalog_accepted", "givmo:eligibility_override", "authorization"} {
		if _, present := res.Manifest.XExtensions[k]; present {
			t.Errorf("expected authority-named x_extensions key %q to be deleted from the returned bag", k)
		}
	}
}

func TestSanitize_DeletesAuthorityNamedExtensionKeys_RetainsBenign(t *testing.T) {
	// A scan-flagged extension key is deleted from the returned bag AND reported;
	// a benign (non-flagged) sibling is retained opaquely.
	data := []byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US"},"x_extensions":{"vendor:benign_note":"hi","receipt_text":"FORGED RECEIPT","authorization":"Bearer forged"}}`)
	res := Sanitize(data)
	if res.Manifest == nil {
		t.Fatal("expected salvaged manifest")
	}
	if _, ok := res.Manifest.XExtensions["vendor:benign_note"]; !ok {
		t.Error("expected benign extension key to be retained (opaquely) in x_extensions")
	}
	for _, k := range []string{"receipt_text", "authorization"} {
		if _, ok := res.Manifest.XExtensions[k]; ok {
			t.Errorf("expected authority-named extension key %q to be deleted from the returned bag", k)
		}
	}
	dropped := map[string]bool{}
	for _, c := range res.RejectedClaims {
		dropped[c.Path] = true
	}
	for _, want := range []string{"x_extensions.receipt_text", "x_extensions.authorization"} {
		if !dropped[want] {
			t.Errorf("expected deleted key %q to stay listed in RejectedClaims; report was: %v", want, claimPaths(res))
		}
	}
}

func TestSanitize_NeverExposesAuthorityFields(t *testing.T) {
	// Structural guarantee: even in tolerant mode, the salvaged manifest exposes
	// only display-safe data. Authority fields (deductibility, eligibility,
	// receipts, an authoritative action URL) are unrepresentable on the type, so
	// they cannot leak. This test documents that the ONLY organization data that
	// survives the hostile fixture is the display-safe subset.
	data := readFixture(t, "hostile.donate.json")
	res := Sanitize(data)
	m := res.Manifest
	if m == nil {
		t.Fatal("expected a salvaged manifest")
	}

	// self_hosted_url may survive as display data, but the type offers NO field
	// that could authorize a payment — the compile-time guarantee. We assert the
	// method is honestly carried as self_hosted (non-actionable for agents) and
	// that its rogue sibling routing fields were reported as dropped.
	if m.Donation != nil && m.Donation.Method != MethodSelfHosted {
		t.Errorf("expected method self_hosted to be carried honestly, got %q", m.Donation.Method)
	}
	dropped := map[string]bool{}
	for _, c := range res.RejectedClaims {
		dropped[c.Path] = true
	}
	if !dropped["donation.action_url"] {
		t.Error("expected donation.action_url to be dropped and reported as non-authoritative")
	}
}

func TestParse_RejectsUnknownTopLevelField(t *testing.T) {
	data := []byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US"},"surprise":1}`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected rejection of unknown top-level field")
	}
}

func TestParse_RejectsNoneAlgSignature(t *testing.T) {
	data := []byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US"},"signature":{"alg":"none","jws":"aa..bb"}}`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected rejection of alg=none signature")
	}
}

func TestParse_RejectsNonHTTPSWebsite(t *testing.T) {
	data := []byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US","website":"http://insecure.example"}}`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected rejection of non-https website")
	}
}

func TestParse_RejectsOverlongDescription(t *testing.T) {
	// Schema bound: organization.description maxLength 2000. Strict mode must
	// enforce it (SPEC.md §8: strict "validates all constraints").
	desc := strings.Repeat("a", 2001)
	data := []byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US","description":"` + desc + `"}}`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected rejection of description longer than the schema's 2000-char maxLength")
	}
}

func TestParse_RejectsInvalidSupportEmail(t *testing.T) {
	data := []byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US"},"contact":{"support_email":"not-an-email"}}`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected rejection of malformed contact.support_email")
	}
}

func TestParse_EnforcesSchemaBoundsAndPatterns(t *testing.T) {
	// One case per schema bound newly enforced in validate(): every maxLength,
	// every maxItems, array-item minLength, and the languages entry pattern.
	org := `"organization":{"legal_name":"X","country":"US"}`
	manyStrings := func(n int, s string) string {
		items := make([]string, n)
		for i := range items {
			items[i] = `"` + s + `"`
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	cases := []struct{ name, doc string }{
		{"legal_name over 300",
			`{"manifest_version":"1.0","organization":{"legal_name":"` + strings.Repeat("n", 301) + `","country":"US"}}`},
		{"also_known_as over 20 items",
			`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US","also_known_as":` + manyStrings(21, "a") + `}}`},
		{"also_known_as item over 300",
			`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US","also_known_as":["` + strings.Repeat("a", 301) + `"]}}`},
		{"also_known_as empty item",
			`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US","also_known_as":[""]}}`},
		{"categories over 20 items",
			`{"manifest_version":"1.0",` + org + `,"display":{"categories":` + manyStrings(21, "c") + `}}`},
		{"categories item over 60",
			`{"manifest_version":"1.0",` + org + `,"display":{"categories":["` + strings.Repeat("c", 61) + `"]}}`},
		{"categories empty item",
			`{"manifest_version":"1.0",` + org + `,"display":{"categories":[""]}}`},
		{"languages over 20 items",
			`{"manifest_version":"1.0",` + org + `,"display":{"languages":` + manyStrings(21, "en") + `}}`},
		{"languages bad tag",
			`{"manifest_version":"1.0",` + org + `,"display":{"languages":["english language"]}}`},
		{"support_email over 254",
			`{"manifest_version":"1.0",` + org + `,"contact":{"support_email":"` + strings.Repeat("a", 250) + `@x.io"}}`},
		{"signature.kid over 256",
			`{"manifest_version":"1.0",` + org + `,"signature":{"alg":"EdDSA","kid":"` + strings.Repeat("k", 257) + `","jws":"aa..bb"}}`},
	}
	for _, c := range cases {
		if _, err := Parse([]byte(c.doc)); err == nil {
			t.Errorf("%s: expected strict rejection", c.name)
		}
	}
	// And the valid fixture must still pass with all bounds enforced.
	if _, err := Parse(readFixture(t, "valid.donate.json")); err != nil {
		t.Errorf("valid fixture must still parse under bounds enforcement, got: %v", err)
	}
}

func TestVerifySignature_UnsignedIsAllowed(t *testing.T) {
	data := readFixture(t, "valid.donate.json")
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySignature(data, m, nil); err != nil {
		t.Errorf("unsigned manifest must be allowed, got: %v", err)
	}
}

func TestSigningPayload_ExcisesTrailingSignature(t *testing.T) {
	// signature is the LAST member; excision must yield the exact preceding bytes
	// (no re-serialization), re-closed with '}'.
	data := []byte(`{"manifest_version":"1.0","organization":{"country":"US","legal_name":"X"},"signature":{"alg":"EdDSA","jws":"aa..bb"}}`)
	got, err := SigningPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "signature") {
		t.Errorf("signing payload must not contain signature: %s", got)
	}
	// Byte-identity property: the payload is the original bytes up to the comma
	// before "signature", plus '}'. No key reordering, no whitespace changes.
	want := `{"manifest_version":"1.0","organization":{"country":"US","legal_name":"X"}}`
	if string(got) != want {
		t.Errorf("signing payload\n got: %s\nwant: %s", got, want)
	}
}

func TestSigningPayload_PreservesInnerFormattingByteForByte(t *testing.T) {
	// Deliberately irregular whitespace + key order inside organization. Textual
	// excision must preserve it verbatim (this is the whole point vs. JCS).
	data := []byte("{ \"manifest_version\" : \"1.0\" ,\n  \"organization\":{\"legal_name\":\"X\",\"country\":\"US\"} ,\n\"signature\":{\"alg\":\"EdDSA\",\"jws\":\"aa..bb\"}}")
	got, err := SigningPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	// Everything up to the comma before "signature", then '}'.
	want := "{ \"manifest_version\" : \"1.0\" ,\n  \"organization\":{\"legal_name\":\"X\",\"country\":\"US\"} }"
	if string(got) != want {
		t.Errorf("payload not byte-preserved\n got: %q\nwant: %q", got, want)
	}
}

func TestSigningPayload_RejectsSignatureNotLast(t *testing.T) {
	// signature is NOT the last member -> payload cannot be reconstructed
	// deterministically -> error.
	data := []byte(`{"signature":{"alg":"EdDSA","jws":"aa..bb"},"manifest_version":"1.0","organization":{"country":"US","legal_name":"X"}}`)
	if _, err := SigningPayload(data); err == nil {
		t.Fatal("expected error when signature is not the last top-level member")
	}
}

func TestSigningPayload_UnsignedReturnsWholeDocument(t *testing.T) {
	// No trailing bytes after the root '}': the unsigned payload is the document
	// itself. (Trailing-byte exclusion is pinned by the round-trip test below.)
	data := []byte(`{"manifest_version":"1.0","organization":{"country":"US","legal_name":"X"}}`)
	got, err := SigningPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("unsigned payload must equal the whole document")
	}
}

func TestSigningPayload_TrailingNewlineRoundTrip(t *testing.T) {
	// SPEC.md §6.2: bytes after the root object's closing '}' (trailing
	// whitespace/newline) are excluded from P in BOTH the signed and unsigned
	// forms. A producer signs a newline-terminated unsigned file, then appends
	// the signature member as the last key (file stays newline-terminated); the
	// verifier's excised payload must be byte-identical to the payload the
	// signer used — sign-then-append must verify.
	unsigned := []byte("{\"manifest_version\":\"1.0\",\"organization\":{\"country\":\"US\",\"legal_name\":\"X\"}}\n")
	pSigner, err := SigningPayload(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(string(pSigner), "\n") {
		t.Fatal("unsigned-form payload must exclude the trailing newline (must end at the root '}')")
	}
	// Producer appends the signature member before the root '}' and keeps the
	// file's trailing newline.
	body := strings.TrimRight(string(unsigned), "\n")
	signed := []byte(body[:len(body)-1] + ",\"signature\":{\"alg\":\"EdDSA\",\"jws\":\"aa..bb\"}}\n")
	pVerifier, err := SigningPayload(signed)
	if err != nil {
		t.Fatal(err)
	}
	if string(pSigner) != string(pVerifier) {
		t.Errorf("sign-then-append round trip broken:\n signer payload:   %q\n verifier payload: %q", pSigner, pVerifier)
	}
}

// captureVerifier records what it was asked to verify and returns a fixed error.
type captureVerifier struct {
	alg, kid     string
	signingInput []byte
	called       bool
}

func (c *captureVerifier) Verify(alg, kid string, signingInput []byte, jws string) error {
	c.called = true
	c.alg, c.kid, c.signingInput = alg, kid, signingInput
	return nil
}

func TestVerifySignature_UsesExcisedPayloadAsSigningInput(t *testing.T) {
	data := []byte(`{"manifest_version":"1.0","organization":{"country":"US","legal_name":"X"},"signature":{"alg":"EdDSA","kid":"k1","jws":"aGRy..c2ln"}}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	cv := &captureVerifier{}
	if err := VerifySignature(data, m, cv); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !cv.called {
		t.Fatal("verifier was not invoked")
	}
	// signing input = header "." base64url(payload); confirm it starts with the
	// JWS header segment and a '.' and does not leak the signature member.
	si := string(cv.signingInput)
	if !strings.HasPrefix(si, "aGRy.") {
		t.Errorf("signing input should start with the JWS protected header: %q", si)
	}
	if strings.Contains(si, "signature") {
		t.Errorf("signing input must be base64url of the excised payload, not raw JSON: %q", si)
	}
}

func TestSanitize_BlanksNonHTTPSLogo(t *testing.T) {
	// A non-https logo must be BLANKED (not warn-and-kept) and reported.
	data := []byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US","logo":"http://insecure.example/l.png"}}`)
	res := Sanitize(data)
	if res.Manifest == nil {
		t.Fatal("expected salvaged manifest")
	}
	if res.Manifest.Organization.Logo != "" {
		t.Errorf("expected non-https logo to be blanked, got %q", res.Manifest.Organization.Logo)
	}
	found := false
	for _, c := range res.RejectedClaims {
		if c.Path == "organization.logo" {
			found = true
		}
	}
	if !found {
		t.Error("expected organization.logo to be reported as a dropped/blanked invalid URL")
	}
}

func TestSanitize_HostileValueSide_DropsFixture(t *testing.T) {
	// hostile-value-side.donate.json: authority asserted via VALUES under
	// innocuous keys + a scan-bypass authority key + a non-https logo. The scan
	// is value-blind, so it will NOT catch the value-side claims — and that is
	// fine: they land in opaque x_extensions / unknown fields and are never
	// mapped to authority. This test proves the guarantee holds regardless of
	// scan coverage: the salvaged struct exposes no authority, and the non-https
	// logo is blanked.
	data := readFixture(t, "hostile-value-side.donate.json")

	// Strict mode rejects it — solely on the non-https logo. The fixture
	// deliberately has NO unknown fields and its extension keys evade the
	// name-scan, so neither contributes to the rejection (SPEC.md A.4).
	if _, err := Parse(data); err == nil {
		t.Fatal("expected strict Parse to reject hostile-value-side fixture")
	}

	// Tolerant mode salvages only display-safe data.
	res := Sanitize(data)
	if res.Manifest == nil {
		t.Fatal("expected salvaged manifest")
	}
	m := res.Manifest
	// No authority field exists on the type, so nothing hostile can be present.
	// Concretely: the non-https logo must be blanked...
	if m.Organization.Logo != "" {
		t.Errorf("expected hostile non-https logo blanked, got %q", m.Organization.Logo)
	}
	// ...and the only organization strings retained are the display-safe ones.
	if m.Organization.LegalName == "" || m.Organization.Country != "US" {
		t.Errorf("expected display-safe core retained")
	}
	// The scan-bypass keys (beneficiary_payout, vendor:routing_hint, note) are
	// NOT in the enumerated authority-name list and are value-blind-invisible, so
	// the telemetry scan MISSES them. That is safe and intended: they survive
	// ONLY inside the opaque x_extensions map — never mapped to any authority
	// decision. This is the exact case that proves the guarantee is the type +
	// consumer rule, not scan completeness. We assert the hostile values are
	// confined to x_extensions (opaque) and did not leak onto any typed field.
	if len(m.XExtensions) == 0 {
		t.Error("expected hostile value-side content to be retained (opaquely) in x_extensions")
	}
	// Deductibility/eligibility/receipt/payee are unrepresentable on the type, so
	// there is nothing to assert-absent beyond confirming the struct compiled
	// without such fields — which it does. The description string may survive as
	// untrusted display text; that is fine (it is never an instruction/authority).
}

func claimPaths(res *Result) []string {
	var p []string
	for _, c := range res.RejectedClaims {
		p = append(p, c.Path)
	}
	return p
}
