import type {
  HAProxyRequest,
  HAProxyRequestPipeline,
} from "./haproxy-requests-api";

export const geoLogFormat =
  'log-format "cv4 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex] %[var(sess.cv_peer)] %[var(sess.cv_client)] %[fc_rcvd_proxy] %[var(txn.cv_xff_count)] %[var(txn.cv_xff),length] %[var(txn.cv_xff),hex]"';

export type ClientAddressMode = "peer" | "xff" | "proxy_protocol";
export const clientAddressLabels = {
  peer: "Direct client connection",
  xff: "HTTP proxy with X-Forwarded-For",
  proxy_protocol: "TCP load balancer with PROXY protocol",
};

// Match the agent's explicit, canonical CIDR policy. Never interpolate an
// unchecked value into the HAProxy ACL shown by the setup guide.
export function canonicalPrefix(text: string): string | null {
  const parts = text.split("/");
  if (parts.length !== 2 || !/^[1-9][0-9]{0,2}$/.test(parts[1]!)) return null;
  const address = parts[0]!,
    bits = Number(parts[1]);
  let value = 0n,
    width: number,
    canonical: string;
  if (address.includes(":")) {
    if (!/^[0-9a-f:]+$/.test(address) || bits > 128) return null;
    try {
      canonical = new URL(`http://[${address}]/`).hostname.slice(1, -1);
    } catch {
      return null;
    }
    if (canonical !== address) return null;
    const halves = address.split("::");
    const left = halves[0] ? halves[0].split(":") : [];
    const right = halves[1] ? halves[1].split(":") : [];
    const groups =
      halves.length === 2
        ? [
            ...left,
            ...Array<string>(8 - left.length - right.length).fill("0"),
            ...right,
          ]
        : left;
    for (const group of groups) value = (value << 16n) | BigInt(`0x${group}`);
    if (value >> 32n === 0xffffn) return null; // IPv4-mapped IPv6
    width = 128;
  } else {
    const groups = address.split(".");
    if (
      bits > 32 ||
      groups.length !== 4 ||
      groups.some((g) => !/^(0|[1-9][0-9]{0,2})$/.test(g) || Number(g) > 255)
    )
      return null;
    for (const group of groups) value = (value << 8n) | BigInt(group);
    canonical = address;
    width = 32;
  }
  const hostMask = (1n << BigInt(width - bits)) - 1n;
  return (value & hostMask) === 0n ? `${canonical}/${bits}` : null;
}

export function trustedProxyRanges(input: string): string[] | null {
  const parts = input.trim().split(/[\s,]+/);
  if (!input.trim() || parts.length > 64) return null;
  const ranges = parts.map(canonicalPrefix);
  if (ranges.some((p) => !p) || new Set(ranges).size !== ranges.length)
    return null;
  return ranges as string[];
}

const localPath = (p: string) =>
  p.length <= 1024 &&
  /^\/[A-Za-z0-9_./-]+$/.test(p) &&
  !p.split("/").some((s) => s === "." || s === "..") &&
  !p.includes("//") &&
  !p.endsWith("/");
export function geoSetupPolicy(
  mode: ClientAddressMode,
  proxies: string,
  country: string,
  asn: string,
): { policy: string; frontend: string } | null {
  const trusted = mode === "peer" ? [] : trustedProxyRanges(proxies);
  if (
    !trusted ||
    (!country && !asn) ||
    (country && !localPath(country)) ||
    (asn && !localPath(asn))
  )
    return null;
  const policy = JSON.stringify(
    {
      version: 1,
      mode,
      ...(trusted.length ? { trusted_proxies: trusted } : {}),
      ...(country ? { country_database: country } : {}),
      ...(asn ? { asn_database: asn } : {}),
    },
    null,
    2,
  );
  const proxyRule =
    mode === "proxy_protocol"
      ? `\ntcp-request connection expect-proxy layer4 if { var(sess.cv_peer) -m ip ${trusted.join(" ")} }`
      : "";
  const frontend = `# Inside the intended HTTP frontend; preserve its binds and TLS guards.\n# Clear inherited loggers, then use only the private agent socket.\nno log\nlog /run/haproxy/access.sock len 2048 format raw local0\ntcp-request connection set-var(sess.cv_peer) src${proxyRule}\ntcp-request session set-var(sess.cv_client) src\n# Place these HTTP captures after managed TLS guards, before header rewrites.\nhttp-request set-var(txn.cv_path) path\nhttp-request set-var(txn.cv_xff_count) req.fhdr_cnt(X-Forwarded-For)\nhttp-request set-var(txn.cv_xff) req.fhdr(X-Forwarded-For)\n${geoLogFormat}`;
  return { policy, frontend };
}

export function geoSetupEvidence(
  p: HAProxyRequestPipeline | null,
  fresh: boolean,
  mode: ClientAddressMode,
  requests: HAProxyRequest[],
  since: number,
  now: number,
): string {
  if (!fresh || !p?.listening || now - p.at_ms > 15000 || p.at_ms > now + 1000)
    return "Waiting for a fresh collection report";
  if (!p.geo || p.geo.mode === "off") return "Geo policy is not enabled";
  if (p.geo.mode !== mode) return "Agent reports a different address source";
  if (p.geo.status !== "ready") return "Check the local database files";
  if (
    !requests.some(
      (r) =>
        r.at_ms >= since &&
        r.id.startsWith(p.boot_id + ".") &&
        r.geo &&
        r.geo.profile === p.geo?.profile &&
        r.geo.source === mode,
    )
  )
    return "Waiting for a new request through this path";
  return "Address source observed";
}
