# Keep the guides useful and testable

Documentation should get a reader to a checkable outcome, not just advertise a
feature. Use the [worked examples](../use-cases/index.md) as the format to follow.

## Before changing a guide

Read the actual UI labels, YAML schema, authorization path, and failure behavior.
Do not infer a feature from a button or a model's claim. Keep gateway source
availability, deployed server version, and installed iOS build distinct.

Every walkthrough should state:

1. Who it helps and the concrete outcome.
2. Required software, credentials/scopes, data, and any paid services.
3. Exact steps with placeholders clearly marked.
4. What success looks like in both output and records.
5. Normal, missing, conflicting, denied, and unavailable-dependency tests.
6. How to stop/recover and what cannot be undone.

Use fictional fixtures. Do not publish production keys, account/host identifiers,
private release receipts, workspace paths from an operator's machine, or raw
database/log dumps. Generated operational reports belong outside the public site.

## Run the checks

From the Personal repository root:

```bash
python3 -m pip install mkdocs-material
make docs-build
go test ./internal/agentvalidate -run TestDocumentationExamples
```

The strict build checks page references and snippet inclusion. An additional
offline check verifies every built page's local targets/anchors and rejects
private operational artifacts in the published output. The Go test
parses downloadable example definitions through the real validator and checks
their deliberately narrow tool policy. It does **not** call a paid model or
prove output quality. Replace model/provider/KB placeholders before trying an
example on an actual gateway.

After building, serve `site/` on loopback and inspect the home page, a table-heavy
use case, and a long code example in light/dark mode and at a phone-sized width.
Check navigation, search, copy buttons, downloadable YAML, and logo rendering.
Do not publish a developer server or serve the repository root with private files.

## Validate behavior proportionately

Use the repository's CI gates for Go/race tests, frontend tests/build, dependency
checks, and docs. The reusable isolated harness is documented in
[deploy/validation](https://github.com/vmodekurti/soulacy-personal/tree/main/deploy/validation).
Its conditional-record/WebDAV services and phone claims are **fixtures**, not
vendor integrations or physical-device testing.

For a real deployment, record version/artifact identity privately, back up data,
let active work drain, retain a rollback target, and run non-destructive smoke
checks. Ask before interrupting active work. Never equate local tests, a green
CI run, a TestFlight upload, or a simulator run with “every situation tested.”

## One brand, multiple surfaces

The approved mark is Blue Living Core. Web/marketing/docs use the same opaque
blue tile; the lowercase wordmark remains real text. Functional icons and agent
avatars are not product logos. Keep favicon, PWA, notification, and in-app
references consistent, and change asset/cache versions when artwork changes.
Brand source and asset tests live in the repository; copying a file into Git
does not by itself publish it to a deployed website.
