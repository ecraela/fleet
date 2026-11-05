import { useState, type ReactNode } from "react";
import { Globe } from "../../icons";

export function CountryFlag({
  code,
  basePath = "/flags/4x3",
}: {
  code: string;
  basePath?: string;
}) {
  const upper = code.toUpperCase();
  const [failed, setFailed] = useState<string | null>(null);
  if (!/^[A-Z]{2}$/.test(upper) || failed === upper)
    return (
      <span
        className="cv-country-flag cv-country-flag--code"
        aria-hidden="true"
      >
        {upper}
      </span>
    );
  return (
    <img
      className="cv-country-flag"
      src={`${basePath.replace(/\/$/, "")}/${upper.toLowerCase()}.svg`}
      alt=""
      width={20}
      height={15}
      loading="lazy"
      decoding="async"
      onError={() => setFailed(upper)}
    />
  );
}

export function CountryLabel({
  code,
  children,
  basePath,
}: {
  code?: string | undefined;
  children: ReactNode;
  basePath?: string;
}) {
  return (
    <span className="cv-country-label">
      {code ? (
        <CountryFlag code={code} {...(basePath ? { basePath } : {})} />
      ) : (
        <Globe />
      )}
      {children}
    </span>
  );
}
