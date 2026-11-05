# Third-party components and notices

Ecraela Fleet's original source, including the Ecraela UI source in `web/ui`,
uses [AGPL-3.0-only](../LICENSE), with
[optional commercial licensing](../LICENSING.md). Third-party components retain
their own terms under either option. The exact runtime versions, provenance and
notice hashes are recorded in [legal/dependencies.json](../legal/dependencies.json).

## Reviewed distribution scope

The inventory covers the complete declared frontend runtime dependency closure
(24 packages), the 22 Go modules used by the shipped executables on Linux amd64
and arm64, the Go runtime/standard library, country flags and Geo test fixtures.
The runtime package licenses are MIT, ISC, BSD-3-Clause, Apache-2.0, 0BSD and OFL-1.1.
Original license, patent and NOTICE files are retained verbatim in
[legal/notices/](../legal/notices/). Test fixtures are included in source and are
identified separately from production datasets.

`client-only` 0.0.1 is an empty React marker that declares MIT and points to the
React project but omits a license file. The inventory preserves the MIT notice
from the bundled React package for it. Build-only tools and platform bindings are
not shipped in the manager or agent; their licenses still apply if those tools
are redistributed separately.

## Included assets

| Component         | Version/source                     | License and notice location                                                                                                        |
| ----------------- | ---------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| Country SVG flags | `flag-icons` 7.5.0                 | MIT; [provenance](../web/public/flags/README.md) and [license](../web/public/flags/LICENSE.txt) are retained in the served assets. |
| Inter Variable    | `@fontsource-variable/inter` 5.3.0 | OFL-1.1; the pinned package contains `LICENSE`.                                                                                    |
| Geist Variable    | `@fontsource-variable/geist` 5.3.0 | OFL-1.1; the pinned package contains `LICENSE`.                                                                                    |
| Commit Mono       | `@fontsource/commit-mono` 5.3.0    | OFL-1.1; the pinned package contains `LICENSE`.                                                                                    |
| Icons             | `@medusajs/icons` 2.21.2           | MIT; the pinned package contains `LICENSE`.                                                                                        |
| Geo test fixtures | MaxMind database test data         | [Fixture license](../internal/accesslog/testdata/LICENSE-MaxMind); test input, not a production Geo database.                      |

Fonts and icons are embedded in the manager's frontend bundle. Their license
texts are included in `THIRD_PARTY_NOTICES.txt`, alongside the code notices.

## Dependencies and runtime images

`web/package.json` and `web/pnpm-lock.yaml` pin the frontend dependencies. The
direct runtime code packages include React/React DOM (MIT), React Router (MIT)
and React Aria Components (Apache-2.0), alongside the assets above. Their installed
packages provide the corresponding notices used to generate the full inventory.
The Vite build rejects a rendered dependency package/version missing from that
reviewed inventory, including dependencies introduced through new imports.

`go.mod` and `go.sum` pin the Go dependencies. The update command inspects the
actual manager, agent and demo-backend dependency graphs for both supported Linux
architectures. A new module or unreviewed license fails the update. Changes to
the locks or package manifest fail ordinary builds until the inventory is updated.

The container also distributes Alpine programs and libraries, including GPL and
LGPL components. A disposable build stage reads their exact installed package
versions and aports commit identifiers before installing collection tools. It
fetches those recipe revisions and uses `abuild fetch verify unpack` to verify
the upstream checksums. Downloads prefer Alpine's official v3.24 source mirror,
with the recipe's upstream URL as fallback; both must match the original recipe
checksums. The final image retains the original sources, recipes,
patches, notices and file hashes under `/usr/share/haproxy-manager/`:

- `container-source.tar.gz`
- `container-packages.json`
- `CONTAINER_NOTICES.txt`

This covers the actual image contents; a mutable upstream branch is not used as
the source offer. The collector's Python, Git and build tools stay in the build
stage. Release verification checks both architectures' source archives and
compares the application legal files with their signed release counterparts.
See the [Alpine source recipe reference](https://wiki.alpinelinux.org/wiki/APKBUILD_Reference).

The signed self-host profile also pins separate PostgreSQL and
VictoriaMetrics/VictoriaLogs images. Those are pulled from their upstream
publishers, retain their own licenses and are not relicensed by this repository.

Production Geo datasets are supplied by the operator under the dataset provider's
terms. Test fixtures do not grant rights to a production country or ASN dataset.
See [Geo setup](geography.md).

## Regenerating and checking

After changing dependencies or adding source files:

```sh
make web-install
make legal-update
make legal-check
make web-build
```

Review the changed dependency licenses, original notice texts and explicit
`legal/source-files.json` list before committing. Builds never silently regenerate
or approve dependency notices. The source bundle contains only the reviewed file
list; identities, keys, ignored local state, build output, installed dependencies
and Git metadata are excluded. A missing source file fails the build.

The standalone login, account menu and footer link to `/legal/`. That page serves
the license, matching project source, file hashes and notices without a login or
GitHub account. The source includes public dependency locks and build instructions;
operators modifying the program should regenerate the offer as part of their build.

The packaging was first published in private RC.13. Its published-asset test
found that re-upgrading after rollback rejected a retained source archive larger
than 1 MiB. RC.13 remains unqualified. [RC.14](releases/v0.1.0-rc.14.md) corrects
that limit and passed clean published-asset installation, upgrade, rollback,
re-upgrade and encrypted recovery. Neither candidate's tag or assets were rewritten.

RC.14's signed legal files matched the manager's anonymous local downloads and
both container architectures. Its downloaded project source rebuilt without
Git metadata or Converge. The exact Alpine source archives, recipe revisions,
checksums and notices were also verified in both images. These checks cover the
published RC.14 artifacts; future dependency or packaging changes need their own
review. See the [publication checklist](publication.md#remaining-publication-work).
