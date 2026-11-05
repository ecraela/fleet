# Development roadmap

Fleet is a public source preview. Its first binary and container release still
needs published-artifact qualification. The private predecessor's RC.14 results
do not qualify artifacts built by the Fleet publisher.

## Next release

- Publish signed, pinned Fleet artifacts with anonymous downloads and image pulls.
- Qualify a clean Linux installation, local login and enrollment from those artifacts.
- Exercise HTTP/HTTPS traffic, failures, encrypted cold restore and preserved identities and keys.
- Qualify the publisher transition for existing private installations, including rollback to the original authenticated release.
- Publish the results and supported deployment limits alongside the exact release.

The [release runbook](agent-releases.md), [hardening drills](haproxy-hardening.md)
and [publication checklist](publication.md) describe these checks. Source CI is
useful evidence, but does not replace testing downloaded binaries and images.

## Later work

Candidate work includes rate limiting and WAF integration, additional traffic
entrypoint adapters, team access/MFA and infrastructure discovery. These need
separate designs and qualification; their presence here is not a support promise.
Measured customer workloads should determine priority and capacity limits.

For current behavior, use the [operator guides](README.md#install-and-operate).
For the system's boundaries and rationale, read the [architecture](architecture.md)
and its linked design records.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
