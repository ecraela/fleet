# ADR 0062: Publish Ecraela Fleet from a clean repository

Status: Accepted, 5 November 2026

## Decision

Use `ecraela/fleet` as the canonical public repository and **Ecraela Fleet** as
the product name. Describe compatibility with HAProxy software and retain its
trademark attribution. The project remains HAProxy-specific.

Export reviewed tracked source into one signed initial commit. Keep the
predecessor repository, history, release tags, artifacts and images private at
their original locations. Do not fork, mirror, rename or replace that repository.

Use `github.com/ecraela/fleet` for the Go module and `ecraela/fleet` for new
release publisher identities. Regenerate protobuf bindings while retaining their
wire package names. Keep executable names, APIs, protocols and persistent state
identifiers compatible. Preserve the AGPL-3.0-only license, commercial-licensing
option, attribution and third-party notices.

## Consequences

Converge moves its pinned source dependency to the public repository. Existing
deployments retain their current signed releases and trust roots. New source CI
does not qualify a new binary release or authorize a running-fleet upgrade.

Fleet release verification remains exact and fail-closed. The repository move
does not grant implicit trust to a second publisher. Cross-publisher upgrades
need their own authenticated transition and recovery qualification, described in
[migration](../migration-to-fleet.md).

Public contribution controls require branch/tag protections, read-only workflow
tokens, pinned actions and approval for outside fork workflows. New source and
output are inspected before publication; private operator evidence is retained
outside the repository.

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet is
not affiliated with or endorsed by HAProxy Technologies.
