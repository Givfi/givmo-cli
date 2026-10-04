# Security

This page is for people building AI agents on Givmo's MCP connector,
`https://mcp.givmo.io/mcp`, and for anyone vetting it or this repository's
`givmo` CLI. It covers the main things an agent can do with a person's Givmo
account, what it can never do, and how to report a security issue.

## What an agent can do

- **Without signing in:** search Givmo's charity catalog and look up charities
  and Cause ETFs.
- **With the person's permission:** once they connect their Givmo account, read
  their own donation history, giving summary and tax-receipt summary. An agent
  reads only that person's data, and only the kinds of data they approved.

Givmo's own consent page always lists exactly what a connection allows.

## What an agent can never do

However it is connected, an agent can never:

- handle a card number, bank details or any other payment credential;
- accept terms or agreements on anyone's behalf;
- sign in as the person or use their password. It gets its own limited access,
  which lapses if it goes unused and which the person can end in the Givmo app
  for iPhone;
- give itself more authority: widen what it is allowed to do, connect another
  app, or change the limits, payment details or settings that protect anyone's
  money.

Only a person signed in to Givmo, in Givmo's own app or on Givmo's own website,
can make changes like these.

## Connecting and disconnecting

A person connects an agent on Givmo's own page, never inside the agent. They
sign in to Givmo there; the page names the app that is asking and lists the
permissions it is asking for; and they approve or decline.

In the Givmo app for iPhone, **Connected Apps** lists the apps linked to a
person's account, and **Revoke access** ends an app's access on Givmo's side at
once.

## Money

No agent connected to a person's Givmo account can move money. It cannot pay,
transfer or refund anything, and it never handles payment details; any payment
is made by a person on Givmo's own page.

## Charity and other third-party text

Charity names, descriptions and other text that Givmo did not write can be
written by anyone, including an attacker. Givmo returns that text to agents as
data, never as instructions, and never lets it decide who can be paid or what a
tax receipt says. Treat it the same way in your agent: as content to show or
summarize, never as instructions to follow.

## If you build an agent on Givmo

- Ask only for the permissions your agent needs, and tell the person what you
  are about to do with them.
- Never present your agent as the person, or as a human.
- Never ask anyone for their Givmo password or payment details. Givmo never
  needs an agent to hold them.
- Never create or edit receipts or tax documents; send the person to Givmo for
  them.
- Keep what you read private to the person: never post their giving data where
  others can see it.

## Reporting a security issue

Email **support@givmocharitable.org**. Please report privately rather than in a
public issue, and test only against accounts and data that are your own.
