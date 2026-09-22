# session-sqlite

`session.sqlite` implements persistent sessions, session discovery, immutable
workspace bindings, and child-session records in one transactional SQLite store.
See the [plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory/module suffix is `session-sqlite`; [the manifest](ingot.plugin.toml)
names `session.sqlite`, with component `default` in `.` and Ingot compatibility
`>=0.3.0 <0.4.0`. It declares state `schema_version = 3` and
`min_reader_version = 1`.

`New(ctx, deps)` requires `State state.Scope` with a nonempty absolute directory.
All exports share the same database-backed implementation:

| Export | Capability |
| --- | --- |
| `Store` | `session.Store` (`Append`, `Load`) |
| `Manager` | `session.Manager` (create/get/rename/archive/restore/delete/fork) |
| `Query` | `session.Query` (`List`) |
| `ChildSessions` | `agent.ChildSessionRepository` |
| `WorkspaceResolver` | `workspace.Resolver` |
| `WorkspaceManager` | `workspace.Manager` |

`Config` is currently an empty reserved struct. There is no `config.toml` reader,
configuration operation, path setting, or user-selectable database policy in this
plugin. It is fully functional without configuration. The build recipe
`plugins.toml` selects the plugin; it does not set its storage path.

## Database and schema

The database is `<state.Scope.Dir()>/sessions.sqlite3`. The implementation uses
the pure-Go `modernc.org/sqlite` driver, enables foreign keys and a 5-second busy
timeout, and limits each store instance to one open connection. It does not
explicitly enable WAL mode. The directory is created with mode `0700` and the
database chmod is `0600` where the platform implements Unix permissions.

The current SQLite `PRAGMA user_version` is 3. Startup runs schema initialization
and migrations in a transaction: it creates missing tables, adds `sessions.meta`
to older databases where needed, creates child-metadata indexes, and records
version 3. Versions above 3 fail with `ErrUnsupportedSchema`. The manifest's state
version and SQLite's `user_version` are distinct checks; neither is permission to
downgrade a database with an older binary.

| Table | Stored data |
| --- | --- |
| `sessions` | ID, title, UTC creation/update/archive times, namespaced JSON metadata |
| `entries` | Ordered opaque payloads with kind/version, keyed by session ID and sequence |
| `session_workspaces` | One durable root path per session |

Entry payload interpretation belongs to the producing plugin. For example,
`agent.default` owns `agent.message`, and `context.compact` owns its checkpoints.
The session store preserves payload bytes and does not decode those formats.
Metadata namespace names must be nonempty UTF-8, and each namespace value must be
one JSON object. The child repository uses the `agent` namespace and JSON indexes.

## Session lifecycle

`Append` assigns the next sequence and advances the activity timestamp in the
same transaction. Appending to an archived session returns `session.ErrArchived`.
`Load` returns all entries in ascending sequence and works for archived sessions.
Missing IDs return `session.ErrNotFound`.

Rename, archive, and restore do not advance the conversation activity timestamp.
Archive/restore are idempotent for existing sessions. `List` includes active and
archived ordinary sessions, ordered by updated time descending, creation time
descending, then ID ascending. It excludes child sessions, which have a separate
discovery API.

`Fork` transactionally copies the full entry log and existing workspace binding.
The result is a fresh, unarchived root session, with new timestamps and empty
metadata. An empty requested title inherits the source title. Child relationships
and metadata are not copied, even when the source itself is a child session.

Deletion cascades to entries and workspace bindings, but never deletes workspace
files or content in `asset.local`. Sessions with children return
`ErrSessionHasChildren`. Active children or children whose execution is not
confirmed stopped cannot be physically deleted. Deleting a stopped leaf child
also updates its parent's recorded child list within the transaction.

## Workspace bindings

`Assign` requires an existing session and an absolute root that currently exists
as a directory. Any second assignment, including the same root, returns
`workspace.ErrAlreadyAssigned`. `Resolve` uses the execution scope's
session ID; an unassigned session returns `workspace.ErrNotAssigned`. There is
no fallback to the host's working directory and no workspace creation, cleanup,
or filesystem sandboxing in this plugin.

## Child-session persistence

The repository transactionally creates children and records the parent link,
supports conditional updates and branch transitions, and lists direct children
with cursor pagination (default 50, maximum 100). The agent's session-tree
component coordinates workspace assignment and dispatch after creation.

`RecoverChildSessions` is invoked by `agent.default` when enabled child-agent
support starts. Previously queued/working children become `interrupted` with
reason `runtime_restart` and diagnostic code `runtime_restarted`. Previously
queued work is known stopped; work that had started retains an unknown stopped
state. Recovery does not resume tasks or assert that external writers have exited.
See [agent-default](../agent-default/README.md) and
[tool-subagent](../tool-subagent/README.md) for lifecycle and tool semantics.

Cleanup closes the database. For a straightforward filesystem backup, stop the
runtime first and preserve the plugin state scope together with related asset
state; raw session entries can reference assets stored by another plugin.

## Source and checks

See [session_sqlite.go](session_sqlite.go), [store.go](store.go), [meta.go](meta.go),
and [child_sessions.go](child_sessions.go). Run `go test ./...` in this module.
[session_sqlite_test.go](session_sqlite_test.go), [store_test.go](store_test.go),
and [child_sessions_test.go](child_sessions_test.go) cover persistence, migrations,
transactional operations, workspace bindings, and restart recovery.
See [CONTRIBUTING](../CONTRIBUTING.md) for workspace instructions.
