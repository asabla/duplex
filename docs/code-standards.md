# Code standards

This document defines the source readability rules and manual review requirements
for duplex. [Development](development.md) owns tool setup and check
commands. [Architecture](architecture.md) owns package and runtime boundaries.

The shared local and CI quality gate enforces the automated checks below. Manual
review still determines whether a group or function has one clear responsibility.
Passing automated checks does not establish release acceptance.

## Applicability and preservation

Apply the review rules to handwritten Go, Go templates, HTML, CSS, JavaScript,
shell, Python, and AWK, including tests and development scripts. Use the existing
layout conventions for Make, YAML, and other configuration files.

Exclude third-party assets, vendored dependencies, generated output, and retained
evidence from readability edits. In particular, preserve `htmx.min.js`, license
files, generated dependency notices, and qualification records. Handwritten
generator scripts remain in scope. Change generated output through its generator
only when a separate task requires it.

Readability and structure changes must preserve behavior, interfaces, errors,
evaluation order, security controls, accessibility, durable identities, and stored
formats. Check resource lifetime, cancellation, lock scope, and transaction scope
when moving statements or extracting functions. Keep mechanical layout changes
separate from responsibility changes so each diff can be reviewed.

## Automated checks

The repository configuration and scripts determine actual enforcement:

| Area | Current check |
| --- | --- |
| Go layout and imports | `gofumpt` and `goimports`, through golangci-lint. |
| Go statement groups | `wsl_v5`, default checks plus `after-block`, including tests. |
| Production Go function size | `funlen`, at most 40 statements; physical-line checks disabled (`lines: -1`). |
| Production control-flow paths | `cyclop`, with `max-complexity: 15`. |
| Production nesting and control flow | `gocognit`, with `min-complexity: 20`. This is the tool's reporting threshold; retain its exact boundary. |
| Errors and resource use | Checks include `errcheck`, `bodyclose`, `rowserrcheck`, `sqlclosecheck`, and context checks. |
| Static analysis and security | Checks include `govet`, `staticcheck`, `gosec`, secret scans, and vulnerability checks. |
| Suppression syntax | `nolintlint` requires a named check and an explanation. |
| Templates, CSS, and JavaScript | Prettier 3.9.10; Go templates use `prettier-plugin-go-template` 0.0.15 with strict whitespace handling. |
| Rendered HTML | `html-validate` 11.17.0 checks `input-missing-label` and `prefer-native-element` on controlled pages and HTMX fragments. |
| Shell and Python | shfmt 3.14.1 with POSIX syntax and two-space indentation; `sh -n`; Ruff 0.17.2 formatting. Script behavior has controlled tests. |
| Behavior and coverage | The quality gate runs race and controlled media tests, Linux-specific tests, coverage checks, and both Linux architecture builds. |

The full enabled Go rule list is in [`.golangci.yml`](../.golangci.yml).
[`scripts/check.sh`](../scripts/check.sh) defines the local gate; CI calls the
same gate, including [`scripts/check-readability.sh`](../scripts/check-readability.sh).
The `_test.go` exemption applies only to `cyclop`, `gocognit`, and `funlen`. Other
enabled checks, including statement spacing, still apply to tests.

[`.editorconfig`](../.editorconfig) supplies editor layout conventions. AWK and
configuration layout remain subject to manual review. The coverage harness checks
AWK merge and failure behavior. The rendered HTML rules check specific markup
properties; they do not prove complete accessibility. Browser and keyboard checks
remain required for affected interface behavior.

## Manual review rules

### Statement groups and responsibilities

- Keep statements for one operation or decision in one group. Keep an operation
  beside its error check. Separate unrelated groups with one blank line.
- Give each function one clear responsibility. Extract a function when its name,
  inputs, and result express a meaningful operation.
- Do not create helpers only to satisfy a metric. Blank lines and comments do not
  resolve separate responsibilities.
- Use conventional multiline control flow. Put independent statements on separate
  lines. A simple Go accessor can remain on one line when the formatter permits it.
- Keep variable scope narrow and related declarations together. Use names that
  describe the operation or value; make names more explicit as scope increases.
- Divide dense expressions and composite literals into named stages or readable
  fields when that makes evaluation and intent clear.
- Explain reasons, constraints, and non-obvious decisions in comments. Remove
  comments that only repeat the code.

### Go

Use the [Google Go Style Guide](https://google.github.io/styleguide/go/) as the
general language reference. Keep `gofumpt` and `goimports` as the layout tools.

Make inputs, results, cancellation, and errors explicit. Keep error propagation
and cleanup beside the operation that creates the obligation. Preserve error
identity, wrapping, deferred cleanup, and partial-success behavior during
extraction. Keep interfaces small and tied to a real consumer boundary; introduce
them when the boundary needs one. Avoid generic `utils`, `common`, or `helpers`
packages. Package responsibility and state ownership belong in Architecture.

### Go templates, HTML, CSS, and JavaScript

- Use readable templates, semantic HTML, native controls, compact CSS, embedded
  HTMX, and small amounts of plain JavaScript. There is no frontend build step.
- Group related controls with suitable elements. Preserve labels, accessible
  names, IDs, DOM relationships, keyboard operation, focus, and return paths.
- Preserve significant whitespace and rendered text when changing template
  layout. Review complete pages and HTMX fragments together.
- Protect unsaved forms and active controls before background swaps. Check form
  errors, empty results, stale revisions, and success feedback when affected.
- Group CSS by the component or behavior it controls. Use readable multiline
  declarations. Preserve selector order, specificity, responsive behavior, and
  focus styles. Do not use empty elements for spacing.
- Group JavaScript by the event or behavior it implements. Use explicit blocks
  for control flow and preserve event order, cancellation, and DOM update scope.
- Keep filters, sorting, and pagination in URLs and bounded database queries.

Use the [WAI table guidance](https://www.w3.org/WAI/tutorials/tables/) for table
names and header relationships. Use the GOV.UK [table](https://design-system.service.gov.uk/components/table/)
and [pagination](https://design-system.service.gov.uk/components/pagination/)
patterns as interaction references. No component library is required.

### Shell, Python, AWK, and configuration

- Keep related command stages together with explicit failure handling. Preserve
  exit status, quoting, environment scope, working directory, and cleanup.
- Preserve existing shell portability and interpreter requirements. Use named
  stages for dense pipelines and Python or AWK expressions when that helps review.
- Keep script inputs, outputs, and file writes clear. Separate collection,
  transformation, and publication when they are distinct responsibilities.
- Preserve Make recipe tabs, configuration keys, and schema meaning. Follow
  `.editorconfig` and existing conventions. Do not apply source formatters to
  generated profiles, dependency notices, or third-party assets.

## Check configuration and limits

The [golangci-lint settings reference](https://golangci-lint.run/docs/linters/configuration/)
describes supported options; the pinned version and repository configuration are
the authority. Keep existing complexity thresholds unchanged. The 40-statement
production function limit is a responsibility review boundary. It has no physical
line limit, so readable multiline expressions do not incur an extra size penalty.

Preserve complete scenarios and fixtures in `_test.go`; do not split tests only to
reduce a metric. Shared test helpers still need meaningful responsibilities.

Template checks select the Go-template parser explicitly. Use strict HTML
whitespace handling and keep `goTemplateBracketSpacing` disabled. The plugin can
need a second formatting pass after compact template code is expanded. Check the
result again; the accepted source must be stable. Preserve rendered text, native
option labels, and any text read by JavaScript. Check significant whitespace with
controlled fixtures and the Browser before changing formatter versions.

Tool versions are pinned in `.tool-versions` and the development-only lockfile in
`tools/readability`. There is no application frontend build or Node.js runtime
requirement. Use Development for setup and exact commands. The gate checks full
source and does not rewrite files or install dependencies.

Before adding or changing a tool, identify the gap, verify compatibility and
formatter stability, and pin the version. Run the same checks locally and in CI.
Do not add blanket exclusions, use change-only reporting, or increase limits to
hide findings.

## Exceptions and review evidence

Keep a necessary exception as narrow as the tool permits. Name the check and
explain the reason at the relevant code. Record broader policy exceptions here;
the test metric exemptions above are deliberate policy decisions. One
`prettier-ignore` applies to the playlist option in
[`videos.html`](../internal/admin/templates/videos.html): the enhanced picker reads
its exact `textContent`, so the label must remain on one line. A behavioral test
checks that exact text. The rest of the template remains checked. Existing
suppressions remain subject to review when their code changes.

For a readability change, report the automated checks that ran, the manual review
of statement groups and function responsibilities, and each remaining exception.
Inspect the final diff for artificial splitting and behavior changes. Add or
update behavioral tests when structural work creates a meaningful risk; do not
add tests only to check formatting. Rendered interface changes also require the
built-in Browser and a manual keyboard check. Record failures and skipped checks
without treating them as acceptance evidence.
