# Distribution notices and matching source

Status: accepted, 2026-11-03

## Decision

The standalone manager serves its AGPL license, matching project source archive,
file hashes and third-party notices at `/legal/`, without authentication. The
login page, account menu and footer expose that offer. The source archive contains
an explicit reviewed file list, includes build instructions and dependency locks,
and excludes Git metadata, installed dependencies, local state and private keys.
An added source file missing from the list fails the build. Archive entries and
compression metadata are deterministic; rebuilding unchanged inputs gives the
same archive.

Runtime notices cover the frontend's transitive production dependencies and the
Go modules used by manager, agent and demo-backend on Linux amd64 and arm64.
Original upstream license, patent and NOTICE files retain their exact contents.
Dependency lock changes and rendered frontend packages absent from the reviewed
inventory fail the build. Updating notices and the source-file list is an
explicit review step, separate from ordinary builds.

The runtime image also carries source, recipe/patch files, notices and hashes for
its separately licensed Alpine packages. The collection stage starts from the
runtime package database, fetches each recorded aports commit and verifies the
upstream source checksums through abuild. Its collection tools are not copied to
the runtime image. External storage images remain separate upstream distributions.

## Release and recovery

The signed release manifest includes `LICENSE`, `THIRD_PARTY_NOTICES.txt`,
`dependencies.json`, `source.tar.gz` and `source.json`. The verifier allows only
these exact additional names. Linux agent installs retain verified legal files
beside the executable. Self-hosted installs retain them with their authenticated
release snapshot so backup/restore preserves the offer. Older signed release
manifests remain valid; operators use the new matching installer/helper pair to
verify the expanded artifact list.

Image qualification compares executable and legal-file hashes with the signed
native release and checks the Alpine source archive against its recorded files.
Release creation requires the exact committed checkout, preventing a source offer
from claiming a revision while publishing uncommitted changes.

No release is published by this decision. Repository/package visibility,
deployments, Converge's pin, identities, keys and permissions remain unchanged.
