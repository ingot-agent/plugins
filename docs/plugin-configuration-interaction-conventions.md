# Plugin configuration through Operation and Interaction

Status: v1.1, 2026-09-22

This document defines how official Ingot plugins expose configuration behavior
through the existing `operation` and `interaction` contracts. It does not add
configuration-specific meaning to either contract.

Normative terms such as **MUST**, **SHOULD**, and **MAY** describe the required
contract for official plugins.

## 1. Boundaries

- A plugin owns the meaning, validation, persistence, migration, and activation
  policy of its configuration.
- A plugin MAY expose configuration behavior through one or more Operations.
  Plugins without configurable behavior do not need a configuration Operation.
- An official Operation described as reviewing or updating a plugin's complete
  configuration MUST cover every current persisted setting. Deprecated fields
  retained only for compatibility and fields explicitly documented as outside
  that Operation MAY be excluded. Any excluded current setting MUST be
  preserved and the Operation definition MUST describe the narrower scope.
- An Operation is an external invocation boundary. Components MUST obtain
  reusable cross-plugin behavior through typed capabilities, not by invoking
  another plugin's Operation by name.
- Interaction remains a presentation-neutral channel for requesting values,
  reporting events, and publishing current state. It has no knowledge of
  configuration pages, widgets, layouts, drafts, save buttons, or restarts.
- A Host MUST NOT decode plugin-private configuration or infer business meaning
  from plugin names, Operation names, or field names.

## 2. Operation conventions

- The Operation definition MUST describe one coherent user or automation task.
  A single invocation MAY issue zero, one, or multiple Interaction requests.
- Input and output schemas MUST describe the complete machine-facing contract.
  A successful Result MUST satisfy the output schema and report the actual
  outcome of that invocation.
- The invocation-scoped Interaction Channel MUST NOT be retained or used after
  `Invoke` returns.
- Cancellation or an unavailable Interaction facility MUST terminate the
  unfinished task. A plugin MUST NOT synthesize answers from defaults and save
  configuration after interaction fails or is canceled.

## 3. Interaction field semantics

- `Request.Name`, `Field.Name`, and `Option.Value` MUST be stable machine-facing
  identifiers. Labels and descriptions are human-facing explanations and MUST
  NOT be used for identity or business decisions.
- `FieldString` with Options expresses ordered suggestions and still permits a
  different string. `FieldChoice` and `FieldMultiChoice` express a closed set of
  allowed values. A plugin MUST use the closed form when it knows the complete
  valid set.
- `FieldObject` and `FieldList` MUST represent the real data structure. Plugins
  MUST NOT add artificial nesting for a particular UI or serialize structured
  configuration into a string to avoid recursive fields.
- Object member order and list order are significant declaration and value
  order. A list MAY be empty unless the plugin applies an additional business
  rule.
- `Required` means that an answer is required when no Default exists. It does
  not imply non-empty text, a positive number, a non-empty list, or manual user
  input. Those are plugin-owned business rules.
- `Default` is a value the Host may use when an answer is omitted. It does not
  mean "system default", "inherited value", "unchanged", or "not edited".
- `false`, zero, the empty string, and an empty list are explicit values. They
  MUST NOT be treated as omitted unless that meaning is explicitly part of the
  plugin's business contract.
- Current Interaction values have no `null` or generic unset variant. Plugins
  MUST NOT depend on a Host returning a string for an integer or number field
  to represent clearing it.
- `Sensitive` marks a field whose value is sensitive. It does not define
  encryption, persistence, or keep/replace/clear behavior.

## 4. Business choices and dependent requests

- A plugin MUST obtain valid candidates from its injected typed capabilities or
  its own domain data. It MUST NOT inspect another plugin's private state.
- When the valid set is not enumerable, the plugin SHOULD use an open string
  field and validate the answer itself. Suggestions MAY still be supplied.
- When later candidates depend on an earlier answer, the plugin SHOULD issue
  another Request after receiving and validating the prerequisite answer.
  Requests are immutable snapshots; the Host is not expected to mutate Options
  in place or recognize special field names.
- A static nested field descriptor MUST NOT publish a union of parent-dependent
  values as a closed Choice when some listed values are invalid for some list
  items. The plugin SHOULD use dependent Requests when practical; otherwise it
  MUST use an open String with suggestions and validate the selected value
  against that item's parent-owned domain.
- If keep, replace, clear, inherit, or automatic selection are distinct business
  actions, the plugin MUST request that intent explicitly when the value alone
  is ambiguous. Ordinary Choice fields and subsequent Requests are sufficient;
  Interaction does not need configuration-specific field kinds.
- A plugin MUST define whether an answer is a full replacement or a partial
  update. Configuration that was not requested MUST be preserved.
- For repeated objects, the plugin owns entry identity, ordering, rename, and
  deletion semantics. Renaming an entry MUST NOT accidentally discard hidden or
  sensitive properties that belong to the same logical entry.
- A repeated entry with a hidden sensitive value SHOULD carry a stable source
  identifier and an explicit keep, replace, or clear action. Removing the entry
  from a replacement list expresses deletion; renaming its editable name does
  not change its source identity.

## 5. Sensitive values

- Existing secrets MUST NOT be placed in Request descriptions, Options, Events,
  States, Results, errors, logs, or non-sensitive Defaults.
- Validation errors for compound sensitive values such as credential-bearing
  URLs or request headers MUST identify the field without echoing the submitted
  value.
- A sensitive field MAY indicate that a stored value exists, but its secret
  value MUST not be projected to the Host merely to support editing.
- Keep, replace, and clear behavior MUST be explicit plugin business behavior.
  An empty sensitive answer MUST NOT silently acquire one of those meanings by
  convention.
- A plugin MUST account for nested values: marking a child field Sensitive does
  not make a parent Object or List Default safe to expose.

## 6. Validation and persistence

- Host validation covers the declared Interaction shape and closed Options.
  The plugin MUST validate domain rules such as ranges, paths, URLs, names,
  references, uniqueness, and cross-field constraints.
- Configuration accepted by an Operation MUST pass the same applicable
  validation used when constructing the plugin. Saving a value that prevents
  the next runtime from starting is an Operation defect.
- The plugin MUST check cancellation again before committing a change.
- The plugin MUST prevent a stale read-interact-write cycle from overwriting a
  concurrent update. Atomic file replacement prevents partial writes but does
  not prevent lost updates.
- Persistence MUST have one clear commit point. After a successful commit,
  cancellation cannot imply rollback, and a later Event or State publication
  failure MUST NOT be reported as if persistence failed.
- Recoverable business errors MAY be reported and followed by another Request.
  Unrecoverable dependency, storage, or cancellation errors SHOULD end the
  invocation.

## 7. Saved and active configuration

- Persisted configuration and configuration captured by the running component
  are separate facts.
- An Operation Result MUST distinguish saved values from active values whenever
  they differ materially.
- A restart requirement MUST be calculated relative to the running component,
  not merely by comparing the new file with the file read at invocation start.
- A configuration Operation that applies changes immediately MUST publish the
  new runtime state after persistence succeeds and before returning success.
  Failed validation, stale-write detection, or persistence MUST leave the
  active state unchanged. Successful live model configuration returns
  `restart_required:false`; see
  [live model provider configuration](live-model-provider-configuration.md).
- All current configurable official plugins apply successful Operation changes
  immediately and return `restart_required:false`; see
  [live official plugin configuration](live-plugin-configuration.md).
- Plugins that can start usefully while unconfigured SHOULD remain constructible
  and expose the capability needed to complete initial configuration. This is
  not a requirement for plugins with no configuration or no meaningful
  unconfigured behavior.

## 8. Event and State usage

- Event reports a fact that occurred. State represents the current replaceable
  snapshot of a named fact. Neither is plugin configuration storage.
- Level conveys severity only and MUST NOT prescribe a widget or Host action.
- Plugins MUST NOT assume State is persisted across Host restarts.
- Until Hosts isolate State identity by producing component, plugins SHOULD use
  sufficiently specific State names to avoid collisions.

## 9. Host consumption

These requirements apply to a Host such as app-webui and are not Interaction
protocol semantics:

- The Host MUST consume fields recursively and preserve explicit scalar values,
  empty collections, object members, and list order.
- Presentation MAY vary by field shape and available space. Navigation and
  layout MUST NOT change the Request or response semantics.
- Submitting an Interaction answers that Request. A Host MUST NOT universally
  label the action as saving configuration.
- The Host MUST preserve unfinished input while navigating one pending Request,
  and MUST NOT automatically resend a response or Operation after reconnecting.
- Sensitive filtering MUST be recursive. Sensitive values MUST not appear in
  summaries, persisted drafts, diagnostics, or telemetry.
- When a supplied compound Default contains any sensitive descendant, the Host
  MUST suppress that compound Default unless it can safely project a value that
  preserves the plugin's replacement semantics without exposing the sensitive
  descendant. Suppressing the complete Default is the safe fallback.

## 10. Current contract limitations

- The current Web Host fills omitted answers from `Default`; it does not treat
  Default as browser-only prefill data.
- Explicit JSON `null` is rejected for every current field kind.
- Request Options cannot be updated in place. Dependent choices require another
  Request with the current contract.
- Business validation failures do not have a structured field-error protocol.
  A plugin can explain the error and issue another Request.
- `model.ProviderEntry` exposes invocation callbacks, not model enumeration. Missing
  model discovery is a model capability issue, not an Interaction issue.
- `model.ProviderSource` supplies current provider names and invocation
  capabilities. Configuration Operations read a fresh snapshot when building
  their choices; a displayed Request remains immutable, and submissions must
  be revalidated against the current directory. Provider discovery does not
  imply model enumeration or automatic refresh of an already-open form.
- The current Web Host recursively inspects compound Defaults and suppresses the
  complete Default when any supplied descendant is sensitive. Consequently,
  non-sensitive siblings in that same compound value are not prefilled. This is
  intentional until Interaction can represent a safe partial compound Default
  without changing replacement semantics.
- SDK Operation Group is an optional presentation hint and is not Operation
  identity. Hosts must provide their own stable internal identity and routing
  for ungrouped Operations and duplicate display names. A Host MAY provide a
  local fallback label for an empty Group, but that label is presentation only
  and MUST NOT be sent back as the Operation Group.
- A List has one static Element descriptor, so the current contract cannot
  declare different closed Options for each item based on another member of
  that item. Dependent Requests or open suggestions are required in that case.
