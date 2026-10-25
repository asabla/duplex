# Code standards

This document adapts the supplied general readability instructions and their Go
and proposed HTML policies to duplex. It is the authority for source readability
and manual review rules. [Development](development.md) owns tool setup and check
commands. [Architecture](architecture.md) owns package and runtime boundaries.

The existing checks remain active. The planned checks below are documentation
only; they have not been added to the quality gate. These rules do not establish
that the current source complies with the proposed policy.

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

## Existing automated checks

The repository configuration and scripts determine actual enforcement:

| Area | Current check |
| --- | --- |
| Go layout and imports | `gofumpt` and `goimports`, through golangci-lint. |
| Production control-flow paths | `cyclop`, with `max-complexity: 15`. |
| Production nesting and control flow | `gocognit`, with `min-complexity: 20`. This is the tool's reporting threshold; retain its exact boundary. |
| Errors and resource use | Checks include `errcheck`, `bodyclose`, `rowserrcheck`, `sqlclosecheck`, and context checks. |
| Static analysis and security | Checks include `govet`, `staticcheck`, `gosec`, secret scans, and vulnerability checks. |
| Suppression syntax | `nolintlint` requires a named check and an explanation. |
| Behavior and coverage | The quality gate runs race and controlled media tests, Linux-specific tests, coverage checks, and both Linux architecture builds. |

The full enabled Go rule list is in [`.golangci.yml`](../.golangci.yml).
[`scripts/check.sh`](../scripts/check.sh) defines the local gate; CI calls the
same gate. The complexity exemption for `_test.go` applies only to `cyclop` and
`gocognit`. Other enabled checks still apply to tests.

[`.editorconfig`](../.editorconfig) supplies editor layout conventions. The gate
has no configured formatter or validator for templates, HTML, CSS, JavaScript,
shell, Python, or AWK. Manual review is required for those files. Go handler and
template tests check selected behavior; they do not prove rendered accessibility.

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
  `.editorconfig` and existing conventions; no new language tools are selected.

## Planned automated checks

These settings require a separate implementation task and a stable source
baseline before activation. Keep the pinned tools and verify each setting against
their supported configuration. The [golangci-lint settings reference](https://golangci-lint.run/docs/linters/configuration/)
describes the available options; the pinned version is the authority.

| Planned check | Policy and reason |
| --- | --- |
| Go statement spacing | `wsl_v5`, default checks plus `after-block`. This checks supported layout patterns; reviewers still check the purpose of each group. |
| Production Go function size | `funlen`, 40 statements, physical-line check disabled (`lines: -1`). The supplied limit is a review boundary for coherent operations, without penalizing readable multiline layout. |
| Complexity | Keep existing `cyclop` and `gocognit` settings and their current reporting boundaries. They already constrain paths and nesting; do not raise them to hide findings. |
| Template formatting and HTML validation | First verify Go-template support and significant whitespace. Assess accessible-name and native-element rules against rendered pages and fragments before selecting tools. |

The planned function-size gate applies to production Go files. Preserve complete
scenarios and fixtures in `_test.go`; keep the current test complexity exemption.
Readability and planned spacing rules apply to tests. Shared test helpers still
need meaningful responsibilities. Do not split tests only to reduce a metric.

Before adding a tool, identify the gap, check compatibility with the existing
formatter, and pin it through the repository's dependency mechanism. Run the same
checks locally and in CI. Do not add blanket exclusions, hide existing findings
with change-only reporting, or increase limits to make a check pass.

## Exceptions and review evidence

Keep a necessary exception as narrow as the tool permits. Name the check and
explain the reason at the relevant code. Record broader policy exceptions here;
the test metric exemptions above are deliberate policy decisions. Existing
suppressions remain subject to review when their code changes.

For a readability change, report the automated checks that ran, the manual review
of statement groups and function responsibilities, and each remaining exception.
Inspect the final diff for artificial splitting and behavior changes. Add or
update behavioral tests when structural work creates a meaningful risk; do not
add tests only to check formatting. Rendered interface changes also require the
built-in Browser and a manual keyboard check. Record failures and skipped checks
without treating them as acceptance evidence.
