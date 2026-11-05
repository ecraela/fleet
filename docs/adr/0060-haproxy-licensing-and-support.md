# Source licensing, commercial licensing and paid support

Status: accepted, 2026-11-03

## Decision

The product owner selected GNU Affero General Public License version 3 only
(`AGPL-3.0-only`) for HAProxy Manager's original source code and documentation.
This includes the Go services, agent, standalone frontend and included Ecraela UI
source. The full unmodified license text is in [LICENSE](../../LICENSE); the
project's scope and version choice are stated in [LICENSING.md](../../LICENSING.md).
Third-party components keep their own licenses and notices. This decision does
not relicense the separate Converge repository.

Ecraela offers optional commercial licensing by separate written agreement for
code it owns or has the necessary rights to license that way. Paid support is a
separate offering available to AGPL users and commercial licensees. The AGPL
edition permits business use without buying either offering, subject to its
license. No commercial contract, pricing or support commitments are established
by this decision.

## Consequences

External contributors retain copyright. An AGPL submission alone does not grant
commercial relicensing rights; any additional rights must be agreed in writing
before merging the contribution into code intended for the commercial offering.
Contributor agreement terms remain a separate task.

Future release packaging must include the license, applicable dependency notices
and matching Corresponding Source, with the source offer and legal notices checked
for each distributed frontend, binary and container. The transitive dependency
and notice review remains in the [publication checklist](../publication.md).

The repository and GHCR package remain private. Existing published tags and
artifacts are unchanged. No host, deployment, Converge pin, identity, key or
permission changes accompany this decision. HAProxy operations retain their
permission-based workflow; DNS keeps its separate review process.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
