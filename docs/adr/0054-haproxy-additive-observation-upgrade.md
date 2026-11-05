# ADR 0054 — Additive observation-store upgrade and software rollback

Status: accepted for the private release candidate.

Schema 20 adds the bounded latest-observation table described in ADR 0053.
Existing schema-19 tables, identities, audit records, configuration and historical
telemetry are unchanged. Older manager and ingestion binaries do not use the new
table and can continue running against the upgraded database.

The signed self-hosted profile now declares schema 20. Its installer accepts
profiles 19 and 20 and permits that specific transition when every other profile
field, including all storage image digests, is identical. Other schema or storage
changes remain rejected. Migration still uses the separate database owner;
runtime processes cannot run migrations or alter audit controls.

Upgrade authenticates the candidate, stops manager/ingestion writers, applies
the transactional migration, grants the existing runtime role access to the new
table and verifies the expected schema and process readiness. Deployment history
retains the selected signed profile and exact previous software release.

Software rollback keeps schema 20 and runs the previous schema-19 binaries. It
does not remove the observation table or rewind any state. Operators must keep
using the authenticated schema-20 installer for rollback and subsequent lifecycle
operations: the older installer correctly rejects a schema it predates. Failed
candidate activation and interrupted-operation recovery retain the same rule.

Qualification must install the candidate directly into fresh storage, then
separately install the published schema-19 baseline, upgrade, roll back and
upgrade again with real nodes and HTTP/HTTPS traffic. The database stays at 20
during software rollback. Encrypted backup and cold restore must preserve node
identities, keys, TLS and history. This qualification does not authorize arbitrary
future migrations or storage-version downgrades.
