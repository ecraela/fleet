# Management response, installation and observation boundaries

Status: accepted, 2026-10-30

## Decision

Paired installations return inert API data at the relay. Ordinary responses use
JSON; successful GET event routes use SSE. Empty 204 responses remain supported.
The relay rejects missing or unexpected types and duplicate header frames, sets
its own MIME type and sandbox policy, and retains local signing of DNS evidence.

Downloaded host tools run with isolated Python (`python3 -I`). The authenticated
self-hosted tool pins its `agentctl.py` helper and executes a verified byte
snapshot without adding the release directory to Python's import path. Update
the embedded helper digest whenever that helper changes. Runtime release
selection remains independent: current host tools can install or roll back an
older compatible, authenticated runtime without executing its older installer.

Restore copies bounded regular ciphertext into private staging, verifies its
signed proof and decrypts that same snapshot. Changing the supplied path or its
original inode cannot replace the verified recovery input. Keys, archive
validation, fresh destinations, stopped restores and source fencing keep their
existing rules. Staging needs space for the ciphertext copy and decrypted data.

Login admission checks source and identity limits before charging the shared
hashing budget. Denied attempts still count against their narrower limit. Both
storage implementations use the same ordering.

Each manager admits at most eight control opens/reports per second for one
authenticated node, across its streams and reconnects. Idle limiter records
expire and the limiter has a fixed maximum size. Normal one-second agent reports
and result acknowledgements retain the existing protocol.

Operation results append audit events when the normalized status changes.
Repeated unresolved claims may update the existing reason, but cannot append
permanent events by alternating errors or replaying an unverifiable success.
An unknown command can still resolve to a verified success or a failure.

Automatic discovery retains at most 1,024 configuration revisions per node.
The check runs under the existing node transaction lock and survives manager
restarts. Latest discovery still updates after the limit, and the configuration
API and both consoles expose the limit explicitly. Permissioned edits and
rollouts can capture their actual current rollback base beyond this automatic
budget. Existing immutable history is never deleted or rewritten. Raw artifact
IDs keep their existing meaning; the reduced document does not determine them.

Incident workers finish a paged sweep before waiting. Four bounded workers and
per-cluster deadlines keep a blocked cluster from monopolizing evaluation.
Repeated valid failure observations can advance opening by at most five seconds
per evaluation, even when scheduling is late. Cached application failures and
unknown evidence do not advance it; a long gap still resets healthy recovery.
Large or unavailable installations can have delayed evaluation, but inventory
growth must not keep sustained failures permanently pending.

## Consequences

These changes add no agent protocol or database schema requirement. Independent
local login, optional Converge linking, tenant permissions and node-local grants
remain the authority boundaries. HAProxy changes have no approval workflow; DNS
keeps its normal review process. Publication and production qualification remain
separate decisions.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
