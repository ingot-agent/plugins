---
name: skill-create
description: Create a reusable Skill when the user wants repeatable instructions, domain guidance, or a workflow saved for future turns.
---
# Create a Skill

Use `add_skill` when the user explicitly asks to create a Skill, or when they
want stable and reusable guidance captured for future tasks. Do not create a
Skill merely to solve a one-off request.

## Design the Skill

1. Identify the repeatable task, its trigger conditions, and the expected outcome.
2. Choose a concise lowercase name containing only letters, digits, and single hyphens.
3. Write a description that tells the model when the Skill is relevant, not just what its file contains.
4. Put the operational procedure, constraints, edge cases, and verification steps in `instructions`.
5. Add UTF-8 text references only when detailed material would make the main instructions unnecessarily long.

Keep the Skill focused. Prefer concrete actions and decision rules over general
advice. Do not include secrets, credentials, scripts, executable payloads, or
claims that `allowed-tools` grants authorization.

## Create It

Call `add_skill` once with:

```json
{
  "name": "example-skill",
  "description": "Use when ...",
  "instructions": "# Procedure\n\n1. ...",
  "references": [
    {"path": "details.md", "content": "Supporting UTF-8 text"}
  ]
}
```

`add_skill` is create-only. If the name already exists, do not attempt to
overwrite it; choose a different name only when that represents a genuinely
different Skill. After creation, report the created name and what the Skill is
intended to handle.
