# Licensing and support

Copyright (C) 2026 Ecraela and contributors.

## AGPL-3.0-only

Except where another license is stated, Ecraela Fleet's original source code
and documentation are licensed under the **GNU Affero General Public License,
version 3 only** (`AGPL-3.0-only`). The full license is in [LICENSE](LICENSE).
The project does not include the option to use a later license version.

This covers the manager, ingest service, agent, standalone frontend and the
Ecraela UI source included in `web/ui`. It does not change the license of the
separate Converge repository. Third-party code, fonts, icons, flags, test data
and other dependencies retain their own terms and notices; see
[third-party components](docs/third-party.md).

The AGPL permits self-hosting, commercial use, modification and redistribution
subject to its terms. A business can use the AGPL edition without purchasing a
commercial license or support. Distribution of covered binaries requires
providing Corresponding Source as specified by the license. If you modify the
program and let users interact with it over a network, section 13 requires a
prominent offer of the Corresponding Source of that version to those users.
The license text governs the complete obligations.

The program is provided without warranty, to the extent permitted by law, as
described in sections 15 and 16 of the license.

## Optional commercial licensing

Ecraela offers an alternative commercial license under a separate written
agreement for code it owns or has the necessary licensing rights to offer.
Contact [Ecraela](https://ecraela.com/contact) to discuss proprietary distribution,
embedding or other requirements that need different terms from the AGPL.
A commercial license takes effect only under that agreement. Third-party
components retain their own terms under either licensing option.

## Paid support

Paid support is available by agreement for both AGPL installations and commercial
licensees. Scope, fees and response commitments are agreed separately. Contact
[Ecraela](https://ecraela.com/contact) to discuss support for your deployment.
Security reports use the private contact in [SECURITY.md](SECURITY.md).

## Contributions and release packaging

Contributors retain their copyright. An AGPL contribution does not by itself
grant Ecraela the right to offer it under commercial terms. Before merging an
external contribution into code intended for commercial licensing, maintainers
must arrange any additional rights with the contributor in writing. See
[CONTRIBUTING.md](CONTRIBUTING.md#licensing-contributions).

The standalone console links to `/legal/`, which serves the license, source archive
for that build, dependency inventory and third-party notices without a login.
Container images also retain their Alpine runtime sources and notices under
`/usr/share/haproxy-manager/`. See [distribution packaging](docs/third-party.md).

The repository and release images remain private. This decision applies to source
carrying these licensing notices; existing tags and published artifacts have not
been rewritten or republished. Before distributing a new release, include this
license, the applicable third-party notices and access to the matching
Corresponding Source. Check the source offer and legal notices in each binary,
container and frontend distribution, as described in the
[publication checklist](docs/publication.md#remaining-publication-work).
