# Country flags

One 4:3 SVG flag per ISO 3166-1 alpha-2 code listed in `src/countries.ts`,
shown by the console's `CountryFlag` component.

Source: the `flag-icons` package, version 7.5.0, as published on the npm
registry (<https://registry.npmjs.org/flag-icons/-/flag-icons-7.5.0.tgz>);
upstream <https://github.com/lipis/flag-icons>, MIT licence (`LICENSE.txt`).
Retrieved on 2026-10-03 with `npm pack flag-icons@7.5.0`, which checks the
registry integrity
`sha512-kd+MNXviFIg5hijH766tt+3x76ele1AXlo4zDdCxIvqWZhKt4T83bOtxUOOMlTx/EcFdUMH5yvQgYlFh1EqqFg==`;
the tarball's SHA-256 was
`c0b80bf0e08006a60f56621d6bc49f8c7131f4d1fef6737a165a673431f4b518`.

Only `flags/4x3/<code>.svg` for the codes in `src/countries.ts` was copied.
The package is not a dependency and nothing else from it is used. The files
are plain vector data: no scripts, no references to other files or hosts.

To update: run `npm pack flag-icons@<version>`, record the new version and
integrity here, copy the 4x3 files for the same codes, and never hand-edit
them.
