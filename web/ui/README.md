# Console components

This directory contains the UI components used by Ecraela Fleet. They originated
in Ecraela's Converge design system and are maintained here as source. Building
this console does not fetch or require the Converge repository or a private npm
package. Unused DNS review, MFA and other product-specific components are excluded.

Import components through `@ecraela/ui`, icons through `@ecraela/ui/icons`, and
styles through `@ecraela/ui/styles.css`. These aliases resolve locally in Vite and
TypeScript. The retained `cv-` class names and `--cv-*` theme tokens are shared
styling conventions, not a runtime integration.

Controls use React Aria Components for keyboard, focus and screen-reader behavior.
Use the existing components and semantic tokens rather than page-specific button,
input or dialog implementations. Editing uses drawers. Verify visible changes
in both themes and at desktop and narrow widths.

Inter, Geist and Commit Mono are bundled through the pinned Fontsource packages.
Icons come from the pinned `@medusajs/icons` package. The Ecraela source in this
component snapshot uses [AGPL-3.0-only](../../LICENSE), with
[optional commercial licensing](../../LICENSING.md). This applies to the copy
included here; the separate Converge repository keeps its own licensing.
Third-party package terms and notices remain separate.
