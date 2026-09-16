# Official plugin configuration audit

Date: 2026-09-16

Status: Remediated and verified

This audit applies
[`plugin-configuration-interaction-conventions.md`](plugin-configuration-interaction-conventions.md)
to every first-level official plugin in this repository. It reviews source
behavior, tests, generated WebUI assets, and the boundary between plugin-owned
configuration semantics and presentation-neutral Interaction.

## Scope and final result

- All 16 official plugin modules were reviewed.
- 15 expose configuration through an Operation. `session-sqlite` has no current
  configuration surface and needs no configuration Operation.
- The review found four repository-wide defects and plugin-specific defects in
  12 modules. All findings in this report have been remediated.
- The remaining contract limitations are documented in the conventions rather
  than hidden in plugin- or Host-specific behavior.

## Severity definitions

- **P0**: can lose configuration, block the configuration entry point, or make
  the next runtime unable to start through a normal accepted workflow.
- **P1**: violates a public contract, can overwrite concurrent work, exposes a
  sensitive value, or reports a materially false result.
- **P2**: incomplete semantic contract or a usability problem that predictably
  causes invalid or confusing configuration.

## Cross-cutting remediation

All 15 configuration Operations now implement the same transaction boundary:

- check cancellation again immediately before commit;
- serialize the commit through a plugin-local mutex;
- reload and compare the source configuration under that mutex, rejecting a
  stale invocation instead of overwriting a newer update;
- calculate `restart_required` against the configuration captured by the
  running component rather than the file read at invocation start; and
- publish a precise output schema for the actual result members.

This resolves the original P1 findings for stale read-interact-write updates,
commit-boundary cancellation, inaccurate activation status, and undocumented
result objects.

## Plugin matrix

| Plugin | Original finding | Resolution |
| --- | --- | --- |
| `agent-default` | P0 numeric clear was impossible; P2 provider was unconstrained | Uses explicit `inherit`/`override` choices and dependent numeric Requests; provider is a closed Choice sourced from injected named providers |
| `app-webui` | P1 nested Sensitive Defaults could leak; empty Operation Group was rejected; duplicate display commands could invoke the wrong Operation | Recursively suppresses unsafe compound Defaults; routes by internal ID; keeps duplicate commands disambiguated; gives ungrouped commands a presentation-only fallback label |
| `asset-local` | Cross-cutting findings only | Common transaction, active-state, cancellation, and schema fixes applied |
| `context-compact` | P0 setup skipped construction normalization; P2 provider input omitted the known range | Setup shares normalization and uses a closed Provider Choice with an explicit request-provider option |
| `http-default` | P0 setup skipped construction validation; P1 credential-bearing proxy URLs were projected and echoed in errors; P2 proxy mode was open text | Setup shares normalization; proxy mode is closed; stored URLs use explicit keep/replace intent and a sensitive follow-up Request; validation errors redact the URL |
| `interceptor-approval` | P2 rule reordering falsely required restart | Common transaction fixes applied; rules are normalized by tool identity before active-state comparison |
| `interceptor-script` | P0 submitting hooks erased Environment maps; renames lost hidden values; a parent-dependent source union was incorrectly closed | Hook and environment entries carry stable source identity; environment values use explicit keep/replace/clear actions; per-Hook source names are open suggestions and are validated by the plugin |
| `model-openai-compatible` | P0 partial validation; rename lost secrets and hidden fields; secret intent was ambiguous; advanced headers and limits were not configurable | Shares full validation, carries source identity across rename, exposes every current provider setting, and manages API keys and header values through explicit secret actions without projecting values |
| `model-runtime` | P0 unconfigured multi-provider runtime could not expose setup; unknown providers could be saved; candidates were not shown | Allows the unconfigured runtime to construct, defers missing selection to request resolution, and uses a closed provider Choice from injected providers |
| `prompt-default` | P0 setup skipped construction validation | Setup shares construction validation and effective defaults |
| `session-sqlite` | No configuration surface | No change required |
| `tool-ask` | P0 non-positive limits could be saved | Setup shares construction validation and effective defaults |
| `tool-edit` | Cross-cutting findings only | Common transaction, active-state, cancellation, and schema fixes applied |
| `tool-runtime` | P0 non-positive limits could be saved | Setup shares construction validation and effective defaults |
| `tool-shell` | P0 nil and empty inheritance collapsed; hidden environment entries could be erased; environment ordering falsely required restart | Adds explicit `all`/`none`/`selected` inheritance, preserves nil versus empty TOML state, manages sensitive entries with stable source/actions, and canonicalizes runtime environment order |
| `usage-default` | P2 routes omitted current Provider suggestions | Keeps the intentionally open future-provider field while supplying current Provider names as String suggestions |

## Detailed resolutions

### `agent-default`

The setup flow no longer overloads integer and number fields with an impossible
empty-string clearing convention. Each optional override first asks for the
business action, then requests a typed value only when overriding. The runtime
injects `[]ingotabi.Named[model.Provider]`, allowing setup to publish the
complete provider range as closed Options. Model names remain open strings
because `model.Provider` does not expose model enumeration.

### `app-webui`

Host projection walks the field and Default value trees together. If any
supplied descendant is Sensitive, it withholds the entire compound Default so
no secret is embedded in the transport representation. Operation routing no
longer treats optional Group or display-name uniqueness as identity. Duplicate
slash-command text remains a selection instead of silently invoking the first
match, and an empty Group receives only a local display label.

The recursive frontend still consumes the same Interaction field tree. Simple
objects and scalar lists now edit inline, while complex repeated structures use
drilldown navigation. This is solely a presentation change and does not add UI
semantics to Interaction.

### Validation parity

`context-compact`, `http-default`, `prompt-default`, `tool-ask`, and
`tool-runtime` now run the same applicable normalization and validation used by
construction before saving. `model-openai-compatible` likewise validates the
complete provider contract, including URLs, models, headers, limits, and
concurrency. A successful setup invocation can no longer persist a value that
the same plugin rejects at its next construction.

### Provider candidates

`agent-default`, `model-runtime`, and `context-compact` receive the current
typed Provider collection and expose complete valid ranges as closed Choices.
Their explicit empty choices retain the relevant plugin-owned fallback
behavior. `usage-default` routes intentionally allow a future Provider name, so
the same collection is exposed as open String suggestions rather than a false
closed range. Model names remain open because `model.Provider` does not expose
model enumeration.

### Sensitive repeated entries

`interceptor-script` and `tool-shell` no longer project secret environment
values. Existing entries carry stable source names and explicit keep, replace,
or clear actions; removing a list item deletes it. Hook and variable renames no
longer discard values. Because an interceptor environment source range depends
on the selected Hook inside the same repeated object, the static nested field
uses open suggestions and plugin validation rather than a misleading Choice.

`model-openai-compatible` separates editable provider names from source entry
identity, so renaming retains hidden fields and stored credentials. API-key
handling uses an explicit keep, replace, or clear choice rather than assigning
meaning to an empty answer. Default request headers use the same source/action
model, treat values as Sensitive, and expose no stored value. The Operation now
also manages all four response, error, asset, and concurrency limits.

`http-default` treats the complete proxy URL as sensitive because it may carry
userinfo. Retaining an existing URL and replacing it are separate business
actions, the replacement is requested only through a Sensitive field, and URL
validation never includes the submitted credential-bearing value in errors.

### `model-runtime`

A runtime with multiple providers and no configured default can now construct
and export its setup Operation. Request resolution still fails clearly until a
selection exists. Setup receives the typed provider collection, exposes it as
a closed Choice, validates the answer again at commit, and cannot save a
provider that is absent from the current dependency set.

### `tool-shell`

Environment inheritance is now an explicit three-way business choice: inherit
all, inherit none, or inherit selected names. Persistence preserves the semantic
difference between nil and an explicit empty list. Explicit environment values
use name-only projection and explicit keep, replace, and clear actions. Removing
an entry from the replacement list deletes it.

## Verification

The remediated repository passed:

- `python3 scripts/validate_repo.py`;
- all Go tests for all 16 plugin modules, including full `http-default` and
  `app-webui` tests that require local loopback sockets;
- 65 app-webui frontend unit tests;
- app-webui TypeScript type checking and ESLint with no errors;
- app-webui production build, followed by a byte-for-byte identical rebuild of
  the generated distribution;
- 17 Playwright end-to-end tests using the system Chromium binary; and
- `git diff --check`.

Regression coverage was added for provider selection, proxy and header secret
preservation, rename behavior, validation parity, environment inheritance,
environment entry actions, recursive sensitive Default suppression, duplicate
and ungrouped Operations, and four-level nested field presentation on desktop
and mobile viewports.

## Residual boundaries

- Interaction remains a general presentation-neutral request/response channel;
  configuration is one use of it, not protocol-level meaning.
- Dependent Options require a subsequent Request because Options are immutable
  snapshots.
- `model.Provider` has no model-enumeration capability, so model discovery must
  be introduced as a typed model-domain capability rather than a UI convention.
- Compound Defaults containing a sensitive descendant are suppressed wholesale
  by app-webui. A future partial-default representation would require an
  explicit Interaction contract that preserves replacement semantics.
