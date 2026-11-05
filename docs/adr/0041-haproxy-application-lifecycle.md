# ADR 0041: Rolling application lifecycle with recoverable deletion

Status: Accepted

Date: 2026-10-25

The user authorized application disable, re-enable and deletion on customer-owned
HAProxy nodes. Extend the existing Go application revision and rolling update
contracts. Mutations require `haproxy.manage`, existing MFA, local agent control,
fresh artifact/version preconditions and tenant ownership. There is no HAProxy
review or approval workflow.

## Disabled and deleted applications

Safe HTTP application intent gains an optional `disabled` flag. Its absence
retains the previous active behavior and canonical digest. Disabled apps retain
their exact hostname/path routing and typed endpoint settings, but the compiler
writes their owned backend with no servers. HAProxy returns 503 for that selected
backend. Longest-prefix routing still applies: a disabled nested route cannot
fall through to a broader neighbor, while a more specific active neighbor keeps
serving. No upstream endpoint is contacted for the disabled app. Unmanaged
backends retain their existing non-empty, healthy-server requirements.

Deletion removes only the selected app's compiler-owned routing and backend in
the same DPA transaction. Shared frontend binds, listener/TLS references, other
applications, local certificates and unmanaged sections remain intact. Removing
a route releases its ownership; a request may then match an existing broader
route. Deleting the last app does not remove its shared listener or TLS material.

Deletion is recoverable: an immutable application revision carries a tombstone,
and the console exposes deleted apps and an explicit Restore application action.
History and audit remain append-only. Restoration creates a new revision and
recomposes current neighboring settings; route conflicts fail. Ordinary stale
editors cannot resurrect a deleted app or toggle disabled state. Historical
deletion revisions are visible but cannot be replayed through the generic version
restore action. Deleted apps release active application slots; existing bounded
revision/history limits still apply.

## Authority and execution

`POST /v1/clusters/{cluster}/applications/{application}/lifecycle` accepts only
`disable`, `enable`, `delete` or `restore`, a request ID, expected app revision and
current node/entrypoint preconditions. Endpoint settings are resolved from the
stored effective application revision, not supplied by the caller. Each request
uses existing cluster/entrypoint serialization, durable checkpoints, per-node
withdraw/drain/update/health/rejoin and compensation. A failed change retains
the effective last successful lifecycle. Repeated requests return the same
revision; changed payloads under the same request ID conflict.

Agents advertise application control version 2 and carry the disabled field in
the generated protobuf contract. Lifecycle operations require version 2 on every
target. Ordinary version 1 application operations remain compatible. Discovery
checks that disabled backends contain no servers and deleted backends are absent
from the loaded runtime. Disabled/deleted status is separate from unhealthy or
missing telemetry; stale observations become unknown. Disabled request checks
stop until the application is enabled again.

Scheduled TLS renewal defers while an application rollout is active, allowing
its temporary mixed-node projection to converge without cancelling certificate
authority. A real hostname/authority change retains ADR 0040's explicit setup
requirement. Disabling an app retains its hostname and certificate needs.

## DNS remains independent

Lifecycle actions never write DNS or automatically remove certificate validation
delegation. After deletion the console offers Review DNS cleanup, showing existing
accessible DNS links and opening the normal zone editor/review. A shared hostname
is identified so its traffic record is kept for neighboring apps. Cleanup is an
explicit ordinary DNS edit, followed by review, approval, apply and verification;
this slice does not add an automatic cleanup-draft endpoint or claim that DNS
links enumerate every external use of a hostname. Existing DNS permissions apply.

## Verification

Memory/PostgreSQL and protocol tests cover tenancy, management/CSRF gates,
idempotency, stale versions, capability fencing, empty owned backends, rollback,
route conflicts and retained TLS. The real two-node lifecycle smoke disables,
re-enables, deletes and restores app A while continuously checking app B over
HTTP and trusted HTTPS. Both nodes must retain the shared listener and loaded TLS.
The console uses drawers and exposes lifecycle separately from deployment state.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
