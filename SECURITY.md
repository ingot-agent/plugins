# Security policy: Ingot plugins

## Reporting a suspected vulnerability

Do not post exploit details, credentials, private prompts, state files or
sensitive reproduction data in a public issue or pull request.

**Reporting setup checked on 2026-09-22:** GitHub's API reported private
vulnerability reporting disabled for this repository. No dedicated security
email or guaranteed response schedule is published in this checkout.

Open the repository's [Security page](https://github.com/ingot-agent/plugins/security).
If maintainers have since enabled **Report a vulnerability**, use that private
form. Otherwise, open a [contact request](https://github.com/ingot-agent/plugins/issues/new?title=Private%20security%20reporting%20contact%20request)
containing only: “Please provide a private channel for a security report.”
Do not include affected code paths, reproduction steps or attachments in that
public coordination request. Wait for maintainers to establish a private
channel before sending technical details.

Once a private channel is available, include:

- exact plugin module paths/tags or source commits, Core/SDK/ABI versions, image identity if known, OS/architecture and a redacted recipe;
- the impact and the access/conditions needed to reproduce it;
- the smallest reproduction with synthetic data and credentials;
- expected versus observed behavior, and any suggested mitigation.

If the report spans repositories, name all affected modules in one private
report so maintainers can coordinate it. Ordinary non-security defects belong
in the public bug-report form.

## Scope and trust boundaries

This repository owns the official browser host, model adapters, tools, policy
interceptors and plugin state. Include the exact affected module and version;
each plugin directory containing `go.mod` is an independently versioned module.

The current [browser host](app-webui/README.md) is intended for a trusted,
local, single-user environment. It has no built-in login or tenant isolation;
its HTTP API can start executions, run Operations and answer approvals. Keep
its listener on loopback and do not expose it directly to a LAN or the Internet.
A Session workspace binding is not an OS/filesystem/network sandbox. Shell
and script hooks execute with the runtime account's permissions.

Approval interceptors control calls routed through the tool runtime; they do
not constrain arbitrary code in another plugin. The browser host omits
sensitive field defaults from its frontend projection, including compound
defaults containing sensitive descendants. Sensitive metadata alone does not
redact every host's output or encrypt plugin state files. Model providers can
receive the prompt, history, selected
attachments and tool output required by a request. See each module README for
its specific data and configuration behavior.

## Version information and disclosure

Please report the exact affected versions, including older releases and source
checkouts. This repository does not currently publish an LTS or guaranteed
security-backport schedule. Fix availability and migration requirements must
be stated in the corresponding release notes; an unreleased branch fix should
not be described as available in an existing tag.

Use the established private channel to coordinate investigation and disclosure.
Do not assume this document guarantees a response deadline or authorizes
testing systems, accounts or data that you do not control.

## Maintainer release requirement

Before public release, enable **Private vulnerability reporting** in the GitHub
repository's security settings, verify the reporter-facing form with an
appropriate account, and update the dated setup statement above. Monitor the
chosen channel and document any support/response policy only after it has been
agreed. Adding this file or an issue-template link does not enable reporting
in GitHub settings.
