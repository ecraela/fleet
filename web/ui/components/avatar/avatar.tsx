import { useState, type ReactNode } from "react";
import { cx } from "../../utils/cx";
import { automaticAvatarColor, type AvatarColor } from "./avatar-colors";

export interface AvatarProps {
  /** Person, organization, or provider name. Emails use the local part. */
  name: string;
  src?: string | null;
  /** Optional marker for a non-person actor, such as System. */
  fallback?: ReactNode;
  size?: 24 | 32 | 40 | 80;
  /** Circles are people (two initials); squares are organizations (one). */
  shape?: "circle" | "square";
  /** Soft gradient with a hue derived from `colorSeed`. */
  tinted?: boolean;
  /** Stable value for the tint, e.g. an organization id. Defaults to `name`. */
  colorSeed?: string;
  /** Chosen tint, e.g. an organization's saved color. Wins over `colorSeed`. */
  color?: AvatarColor | null | undefined;
  className?: string;
}

/** Decorative identity marker. Place the readable name next to it. */
export function Avatar({
  name,
  src,
  fallback,
  size = 24,
  shape = "circle",
  tinted = false,
  colorSeed,
  color,
  className,
}: AvatarProps) {
  const [failedSource, setFailedSource] = useState<string | null>(null);
  return (
    <span
      aria-hidden="true"
      data-hue={
        tinted ? (color ?? automaticAvatarColor(colorSeed ?? name)) : undefined
      }
      className={cx(
        "cv-avatar",
        `cv-avatar--${size}`,
        `cv-avatar--${shape}`,
        tinted && "cv-avatar--tinted",
        className,
      )}
    >
      {src && src !== failedSource ? (
        <img
          src={src}
          alt=""
          referrerPolicy="no-referrer"
          onError={() => setFailedSource(src)}
        />
      ) : (
        (fallback ?? initials(name, shape === "square" ? 1 : 2))
      )}
    </span>
  );
}

function initials(name: string, count: 1 | 2) {
  const base = name.includes("@") ? (name.split("@")[0] ?? name) : name;
  const words = base.split(/[\s._-]+/).filter((word) => /^\p{L}/u.test(word));
  const letters =
    words.length > 1
      ? `${words[0]?.charAt(0) ?? ""}${words[1]?.charAt(0) ?? ""}`
      : (words[0] ?? base).slice(0, 2);
  return letters.slice(0, count).toUpperCase() || "?";
}
