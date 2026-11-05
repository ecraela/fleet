# Local rollback authority and tenant work isolation

Status: accepted, 2026-11-03

## Decision

Raw rollback bytes require a separate private observation binding. The agent
creates it only after reading the complete configuration under one DPA version.
It binds the content artifact to the canonical document, actual certificate
paths, request-log profile and compiled traffic-rule dependencies. Rollback must
match that document and the current
node's TLS, telemetry and traffic-policy grants, both before validation and before
commit. A remotely supplied document, staged candidate or commit response cannot
create the binding. DPA remains the sole configuration writer; raw settings outside
the editor and normalized commit receipts retain their existing semantics.

Fleet status uses one unlabelled up series and one optional collection-time
series per agent. Ingestion validates their labels, cardinality, values and
collection-time coherence before either storage path. Historical queries group
by the full tenant/cluster/node identity and retain only unique status series.
Readers check every row's scope before publishing anything, then isolate malformed
values and duplicate status to their own node. Incident evaluation continues for
valid peers; a scope or storage failure remains a global unknown result.
The history-backed live feed uses the same singleton reduction and node isolation.
Its storage query retains one per-node overflow sentinel so excessive series
cannot consume the whole response; the affected node remains unavailable.

Durable maintenance fences and runnable work have separate budgets. Rollouts and
runnable maintenance share an atomic limit of 16 per installation and eight per
organization, including resumed withdrawal/rejoin. A held or attention reservation
continues fencing its node and entrypoint without occupying another tenant's
runnable slot. New withdrawal is also bounded by 16 nonterminal reservations per
organization. Every worker tick selects runnable jobs separately from a rotating
batch of at most 16 idle safety checks, preserving continuous drain/health
windows even with many held reservations. Idle checks advance their scheduling
checkpoint. Automatic rollback and
recovery handoff retain their existing safety authority.

## Consequences

The wire protocol, database schema, enrollment and persistent keys are unchanged.
Backups include the agent's binding directory alongside raw artifacts, operations,
receipts and identity. Older snapshots remain retained but cannot be restored
without an independent local observation. The current configuration gains a
binding on first successful observation after upgrade; subsequent edits capture
their rollback base normally. Missing bindings never trigger re-enrollment.

These boundaries apply equally to standalone and linked Converge managers.
HAProxy operations use permissions and local policy, without approvals. DNS
retains its separate review process. Private release qualification does not
authorize host upgrades or public source distribution.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
