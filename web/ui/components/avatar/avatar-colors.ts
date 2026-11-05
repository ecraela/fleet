/** Tints an organization can choose, in swatch order. */
export const AVATAR_COLORS = [
  "blue",
  "violet",
  "pink",
  "red",
  "orange",
  "amber",
  "green",
  "teal",
  "gray",
] as const;

export type AvatarColor = (typeof AVATAR_COLORS)[number];

// Avatars without a chosen color keep the original four-hue hash, so existing
// organizations do not change tint.
const AUTOMATIC_COLORS = [
  "blue",
  "green",
  "amber",
  "gray",
] as const satisfies readonly AvatarColor[];

/** The tint an avatar shows when no color has been chosen. */
export function automaticAvatarColor(seed: string): AvatarColor {
  let hash = 0;
  for (const character of seed) {
    hash = (hash * 31 + (character.codePointAt(0) ?? 0)) >>> 0;
  }
  return AUTOMATIC_COLORS[hash % AUTOMATIC_COLORS.length] ?? "gray";
}

/** The least-used tint among `taken`, earliest in swatch order on ties. */
export function suggestAvatarColor(taken: Iterable<AvatarColor>): AvatarColor {
  const counts = new Map<AvatarColor, number>();
  for (const color of taken) counts.set(color, (counts.get(color) ?? 0) + 1);
  let suggestion: AvatarColor = AVATAR_COLORS[0];
  for (const color of AVATAR_COLORS) {
    if ((counts.get(color) ?? 0) < (counts.get(suggestion) ?? 0)) {
      suggestion = color;
    }
  }
  return suggestion;
}
