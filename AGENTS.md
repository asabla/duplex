# Repository instructions

## Communication

- Use direct, formal language. Do not use emojis unless requested.
- Do not give time estimates unless requested.
- Use ASD-STE100 Simplified Technical English principles for technical explanations.
- For browser interaction and verification, use the built-in Browser tool
  (`@Browser`, `plugin://browser@openai-bundled`). Do not use Playwright tools,
  scripts, or browser sessions unless the user explicitly requests Playwright.

## Read before changes

1. Read [Code standards](docs/code-standards.md) for readability, manual review,
   and the distinction between existing and planned checks.
2. Read [Development](docs/development.md) for tools, commands, and verification.
3. Read [Architecture](docs/architecture.md) for package responsibilities,
   state ownership, dependencies, and runtime invariants.
4. For structural work, read the [refactor work list](docs/structure-refactor.md).
   Recheck its source evidence and dependencies before selecting an item.
5. Read the documentation for the behavior that the task affects. Use the
   [README documentation index](README.md#documentation) to find it.
6. Read the relevant source and tests. Confirm current behavior before changing it.

Keep detailed readability rules in Code standards, runtime boundaries in
Architecture, check procedures in Development, and stage status in the refactor
work list. Update the relevant authority when a later implementation changes it.

## Concurrent work

- Keep each change within the authorized task. Other work can use this checkout.
- Inspect Git status and the target file before each edit. Do not overwrite,
  reset, stage, commit, or reformat unrelated changes.
- For documentation preparation, edit one file, review its complete diff, then
  start the next file. If another task is editing that file, use another
  unmodified documentation file and report the overlap.
- Recheck source claims before writing them. Source can change during this work.
- Documentation-only tasks must not change source code, lint configuration,
  dependencies, generated output, or retained qualification evidence.

## Commands and evidence

- `make`: show help; no setup or build.
- `make build`: build the application.
- `make test`: run race tests and controlled media tests.
- `make check`: run the complete local quality gate, also used in CI.

Use the pinned tools and the verification procedure in Development. For prose
changes, check links, commands, consistency, and source claims; do not run the
application quality gate only to verify prose. For code changes, run the relevant
behavioral checks and the required quality gate. Report missing tools, failures,
and skipped checks. Do not treat historical evidence as a passing current result.

## Change boundaries

- Preserve behavior, interfaces, errors, security controls, accessibility, durable
  identities, and stored formats during readability and structure work.
- Improve responsibilities within existing packages first. Add a package only
  for a concrete responsibility or dependency boundary.
- Preserve complete test scenarios. Do not split code only to reduce a metric.
- Proposed readability checks are not yet enforced. Documentation preparation
  does not authorize enabling checks, applying automatic fixes, or refactoring.
- Keep existing quality gates and release requirements. Read the
  [validation register](docs/validation.md) before making acceptance claims.
- Never commit credentials, signed source URLs, raw profiles, or local
  installation state. Exclude third-party assets and generated output from
  readability edits.
