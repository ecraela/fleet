const digest = /^[a-f0-9]{64}$/;
const count = (value: unknown): value is number =>
  Number.isSafeInteger(value) && Number(value) >= 0;
export interface HAProxyGeography {
  status:
    | "located"
    | "partial"
    | "not_found"
    | "non_public"
    | "unresolved"
    | "database_unavailable"
    | "disabled"
    | "unavailable";
  source: "peer" | "xff" | "proxy_protocol" | "unknown";
  country?: string;
  asn?: number;
  profile?: string;
}
export interface HAProxyGeoDatabase {
  sha256: string;
  built_at: number;
}
export interface HAProxyGeoCollection {
  mode: "off" | "peer" | "xff" | "proxy_protocol";
  profile: string;
  status: "ready" | "disabled" | "database_unavailable";
  country_database?: HAProxyGeoDatabase;
  asn_database?: HAProxyGeoDatabase;
}
export const validCountry = (country: string) => /^[A-Z]{2}$/.test(country);
export function validCountryFilter(value: string): boolean {
  if (!value) return true;
  if (value.length > 128) return false;
  const codes = value.split(",");
  return (
    codes.length <= 32 &&
    new Set(codes).size === codes.length &&
    codes.every((code) => code === "unknown" || validCountry(code))
  );
}
export function validGeography(value: unknown): value is HAProxyGeography {
  if (!value || typeof value !== "object") return false;
  const g = value as HAProxyGeography;
  if (
    !Object.keys(g).every((k) =>
      ["status", "source", "country", "asn", "profile"].includes(k),
    ) ||
    !["peer", "xff", "proxy_protocol", "unknown"].includes(g.source) ||
    (g.profile !== undefined && g.profile !== "" && !digest.test(g.profile)) ||
    (g.country !== undefined && g.country !== "" && !validCountry(g.country)) ||
    (g.asn !== undefined && (!count(g.asn) || g.asn > 4294967295))
  )
    return false;
  const country = !!g.country,
    asn = !!g.asn;
  if (g.status === "located" || g.status === "partial")
    return (
      (g.status === "located" ? country && asn : country !== asn) &&
      g.source !== "unknown" &&
      !!g.profile
    );
  if (["not_found", "non_public", "database_unavailable"].includes(g.status))
    return !country && !asn && g.source !== "unknown" && !!g.profile;
  if (g.status === "unresolved")
    return !country && !asn && g.source === "unknown" && !!g.profile;
  return (
    ["disabled", "unavailable"].includes(g.status) &&
    !country &&
    !asn &&
    g.source === "unknown" &&
    !g.profile
  );
}
export function validGeoCollection(
  value: unknown,
): value is HAProxyGeoCollection {
  if (!value || typeof value !== "object") return false;
  const g = value as HAProxyGeoCollection;
  if (
    !["off", "peer", "xff", "proxy_protocol"].includes(g.mode) ||
    !digest.test(g.profile)
  )
    return false;
  if (g.mode === "off")
    return g.status === "disabled" && !g.country_database && !g.asn_database;
  if (
    !["ready", "database_unavailable"].includes(g.status) ||
    (g.status === "ready" && !g.country_database && !g.asn_database)
  )
    return false;
  return [g.country_database, g.asn_database].every(
    (db) =>
      !db ||
      (digest.test(db.sha256) &&
        count(db.built_at) &&
        db.built_at > 0 &&
        db.built_at <= 4102444800),
  );
}
