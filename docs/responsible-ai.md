# Responsible AI — Agent-Mediated Charitable Giving

> **Status:** Public release artifact for the Givmo agent surface, published with
> the same SDK-grade release discipline as the `givmo` CLI and the
> [`donate.json` manifest spec](https://givmo.io/schemas/donate/v1/donate.schema.json).
> It is the companion to the [Threat Model](./threat-model.md): that document is
> the adversarial view; this one is the design philosophy and the honesty posture.
>
> **Not legal or tax advice.** See the Threat Model §12 for the full disclaimers
> block, which applies equally here.

---

## 1. Why this program exists

AI agents are becoming a place where people manage their lives — including their
giving. Givmo operates one remote MCP server through which external agents can
search a charity catalog, read a linked donor's own giving history, and hand a
human off to a Givmo-controlled checkout.

The posture behind this program — call it **"build the rails *and* the
lighthouse"** — is deliberate: **if a safe, well-governed path for agent-mediated
giving does not exist, someone will build the unsafe version.** Giving is an
obvious thing to ask an agent to do, the failure modes are consequential (money,
tax documents, sanctioned recipients, deceptive claims), and the incentives to
cut corners are real. Publishing the rails *and* the threat model *and* the open
`donate.json` standard *and* the CLI — as release artifacts, unilaterally, with no
dependence on any partner — is the responsible response to that reality. The
adversarial analysis is in the [Threat Model](./threat-model.md); this document is
the constructive half.

---

## 2. The core discipline: discretion vs. determinism

The one principle that governs every design decision:

> **Money movement is deterministic, ceilinged, and human-terminated. Agent
> discretion is bounded to search, recommend, and hand off.**

An AI agent is *good* at discovery, ranking, natural-language matching, and
explanation, and Givmo lets it exercise judgment there. An AI agent must *never*
be the authority of record for who gets paid, whether a gift is deductible, what a
receipt says, whether sanctions screening passed, or whether terms were accepted
and money moved. Those are deterministic floors — legal and financial predicates,
not UX preferences. The rule of thumb is **"pre-screen the class, never pre-bind
the grant":** the agent narrows the field to eligible recipients; it never commits
a specific grant, and every disbursement is re-confirmed against authoritative
data at settlement.

This is not a constraint bolted on after the fact. It is the shape of the system,
and it is what lets Givmo give an agent genuine usefulness (real research, real
recommendations, a real hand-off) without ever giving it the authority to do harm
at the point money moves. The [Threat Model](./threat-model.md) §2–§3 states the
deterministic floors and the three-rung money ladder in full.

---

## 3. Provenance-labeled honesty over impressive answers

The central Responsible-AI commitment of this program:

> **A truthful "no detailed research yet" beats a confabulated profile.**

An agent that fills gaps with a plausible-sounding but invented charity profile is
*worse* than one that admits it does not know — because a donor may move money on
the strength of that fabrication. So the surface is built to degrade honestly:

- **Every charity fact is provenance-labeled.** Fields carry a machine-readable
  source (e.g., `irs_990`, `irs_990n`, `wikidata`, `website_llm`, `human`,
  `computed`) so an agent — and, downstream, a donor — can tell an IRS-filed fact
  from a website summary from a computed value. A mission summarized by an LLM from
  an organization's own (attacker-writable) website is labeled as such and is never
  presented with the authority of an IRS filing.
- **Out-of-corpus organizations degrade honestly.** An organization with no
  researched content renders with honest "no detailed research yet" / "not on
  Givmo yet" copy — never an invented profile, and never a donate action it cannot
  actually support.
- **Freshness is surfaced, not hidden.** Charity facts carry freshness provenance
  (fiscal year, data dates); IRS 990 data lags one to two years, and that lag is
  stated rather than papered over.
- **Good standing is enforced at emission.** Research-only or non-good-standing
  organizations get no donate chips, re-enforced at the moment cards are rendered —
  so an honest research answer never turns into an actionable donate prompt for an
  org that should not receive money.

This is the same instinct as the untrusted-text posture in the Threat Model
(§4.4, §7): the system's default toward charity-derived content is *label it, weight
it honestly, never let it masquerade as authority.*

---

## 4. Honesty about the program itself

Responsible-AI honesty extends to how Givmo describes *its own* controls and
claims — the deception standard reaches a program's representations about itself,
not only its giving copy:

- **Live vs. roadmap is labeled.** Rung 1 (hosted-checkout hand-off) is live.
  Rung 2 (capped in-band giving) and Rung 3 (autonomous recurring giving), and
  deep-research mode, are **gated future phases** and are described as such — never
  as deployed.
- **The DAF structure is disclosed, not blurred.** Donations are irrevocable gifts
  to Givmo Charitable Fund (a 501(c)(3) DAF sponsor with exclusive legal control),
  which grants onward on the donor's nonbinding recommendation; a recommended
  charity may not receive the funds. Givfi, Inc. (the for-profit operator) and GCF
  (the charity/donee/merchant of record) are kept distinct in every statement
  about who receives money, who is regulated, and who issues receipts.
- **Regulated, not exempt.** The program is described as **subject to, and
  complying with, California AB 488** (and Hawaii Act 205 from 2026-07-01) as a
  charitable fundraising platform / platform charity — never as unregulated or
  exempt.
- **No overclaiming on tax, "100%," or AML.** See Threat Model §12 for the full
  claims-discipline block — no unqualified "tax-deductible," no bare "100% to
  charity," no AML/KYC "exemption" framing, no "conduit"/"pass-through" language.

---

## 5. Consent honesty and human control

The human is always in control of what an agent may do on their behalf:

- **The consent screen tells the truth about scopes** and **names the requesting
  agent truthfully** — anti-lookalike identification is the consent screen's
  explicit job (Threat Model §4.8).
- **Narrowest scope that works.** The four consumer scopes are the exact ceiling
  of a consumer grant; grants are scope-subset; widening what an agent can do is
  never a silent change.
- **One-tap, server-side revocation.** A donor can see every linked agent and
  revoke any grant, honored server-side immediately.
- **The agent is always identified as an agent, never presented as a human**, and
  when a user interacts with Givmo's AI assistant they are told they are
  interacting with AI, not a person. The assistant is kept task-scoped.

---

## 6. The AI is never the donor — and why

The most load-bearing Responsible-AI invariant is also a legal one: **an AI agent
is never the donor.** Only a human — who accepts Givmo's terms and completes
payment on Givmo's own hosted checkout — can make a contribution or hold advisory
privileges over a fund.

This is a **design invariant with a tax-law rationale**, not a claim about settled
law on AI legal personhood or contract capacity. The DAF/tax structure requires a
human donor (a charitable deduction is allowed only on the donor's acknowledgment
that GCF has exclusive legal control, and a DAF is defined by advisory privileges
held by a *donor* — a person). Those personhood/capacity questions are unsettled,
and Givmo's invariant does not depend on resolving them: the system simply requires
a real, terms-accepted human at the point money moves, on every rung. The full
statement, including the three-rung ladder this holds across, is in the
[Threat Model](./threat-model.md) §3 and §4.8.

---

## 7. What we ask of integrating agent developers

The controls in this program are strongest when the consuming agent respects the
same posture. If you build on the Givmo MCP surface or consume a `donate.json`
manifest:

- **Treat charity-published text as untrusted.** Manifests, filings, missions, and
  website summaries are attacker-writable. Do not map any manifest field to a
  decision about who is paid, whether a gift is deductible, or any legal
  disclosure — resolve those only against authoritative out-of-band sources. This
  is the manifest spec's normative consumer obligation (`SPEC.md` §7.3), and no URL
  in a manifest is an authoritative action target (`SPEC.md` §7.6).
- **Preserve provenance to the user.** When you surface a charity fact, carry its
  source label through — do not launder a `website_llm` summary into an apparent
  IRS fact.
- **Never present the agent as the donor or as a human.** Identify the agent as an
  agent; route money through the human-terminated hosted checkout.
- **Do not fabricate receipts or tax documents.** Surface the link to the
  GCF-issued receipt; never generate, sign, or draft one.

The `givmo` CLI (`manifest validate` / `manifest sign`, the local
`mcp serve` bridge, `listen`/`trigger` against the sandbox) exists to make the
safe path the easy path.

---

## 8. Version & provenance

- **Document version:** v1.0 — 2026-07-06.
- **Applies to:** the Givmo MCP surface v1 (Rung 1 live; Rungs 2–3 and deep
  research gated). Companion artifacts: the `donate.json` manifest spec v1.0 and
  the `givmo` CLI, both in this bundle.
- **Companion:** [Threat Model](./threat-model.md).
- This is a general program description, not legal or tax advice (Threat Model
  §12).
