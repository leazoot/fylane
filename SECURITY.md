# Security Policy

Fylane gives a remote model access to a folder on someone's computer. A
security regression here is a release blocker, not a bug.

## Reporting a vulnerability

Use GitHub's private reporting: **Security → Advisories → Report a
vulnerability**. It stays private until a fix ships.

Please include the version (`fylane-companion version`), your platform, and the
smallest reproduction you can manage. Do not open a public issue for a
vulnerability, and do not include real tokens, private file contents, or a
working exploit chain in the report.

You will get a first response within 5 working days. If a fix is needed, we
will agree a disclosure date with you before publishing.

## Supported versions

Fylane is pre-1.0. Fixes land on `main` and go out in the next release; there
are no maintained back-branches yet.

## Trust model

Everything below is a claim you are invited to test.

**The local user is the only authority.** Platform-side confirmations and MCP
annotations are hints. A write applies because someone at the keyboard said
yes, never because the model asserted it was allowed.

**The relay is untrusted with content.** It forwards frames in memory. It never
persists or logs file bodies, diffs, directory listings, or sensitive file
names, and it never sees an absolute path. Running your own changes who
operates it, not what it is allowed to hold.

**The workspace boundary is resolved, not matched.** Every path level is
resolved to its real path before use, which is what stops symlink and junction
escapes. Absolute paths, `..`, Windows reserved names, and device files are
rejected.

**Programs Fylane starts are bounded by the kernel.** They may read the
workspace and the toolchain caches; everything else on the machine is denied by
`sandbox-exec` on macOS and Landlock on Linux. Where the platform cannot do
this, Fylane says so at startup rather than pretending.

**Approval frequency is a setting; the checks are not.** Sandbox resolution,
the dangerous-command rule table, and the audit log run at every setting. There
is no flag that turns them off.

## In scope

- Escaping the workspace boundary, in any of the ways the resolver is meant to
  stop.
- Getting a write, a delete, or a consequential command applied without a local
  approval.
- Reading a file the sensitive-file rules are supposed to protect.
- Making the relay hold, log, or leak file content, paths, or file names.
- Forging or replaying device credentials, pairing codes, or OAuth grants.
- Reaching the local control API without its token.
- Command output that carries a credential past the redaction at the MCP
  boundary.

## Out of scope

- An attacker who already has your user account on the machine. Fylane runs as
  you and cannot defend against you.
- Anything a delegated coding agent does after you approve it — see below.
- Vulnerabilities in a tunnel provider, a language server, or an MCP server you
  configured yourself.
- Social engineering of the person clicking approve.

## Known limits

These are design decisions, documented so you do not have to discover them.

**The kernel boundary bounds reads, not writes.** It answers "what may this
process see", which is the question nothing else in the product can answer.
Writes are bounded instead by the transaction: diff, approval, backup, atomic
replace, rollback window.

**A delegated agent is not reviewed.** `code_task` hands a whole job to a
coding agent installed on your machine. Fylane approves the start and nothing
after it — the files that agent edits and the commands it runs do not pass the
rule table or the audit log. The approval prompt says so in those words.

**The rule table judges most commands by program name.** The workspace boundary
itself is judged by resolved path, but the dangerous-command classification is
name-based and a renamed binary is a different name.

**Windows has no read boundary.** There is no per-process read confinement this
design can use there. The path sandbox, rule table, approval gate, and audit
log all still run; the kernel layer does not exist.

**On Windows, file permissions are not enforced.** The control file that
carries the local API token, and the settings file, are written owner-only
(`0600`) on macOS and Linux. Windows has no such mode bits and Fylane sets no
ACL there, so those files are exactly as private as the data directory that
holds them — no more.

**No independent audit has been done.** Everything above is the implementer's
account of the implementer's code. Treat it accordingly until that changes.

## Handling secrets

Fylane never writes tokens, keys, or file contents to its logs. Credentials
live in the OS keychain, referenced by ID from the local database. The
diagnostics bundle (`fylane-companion diagnostics`) carries crash logs and
build info only, and says so before you share it.

If you believe a credential leaked through Fylane, rotate it first and report
second.
