import type { HAProxyApplication } from "./haproxy-api";

export type ApplicationDraft = Omit<HAProxyApplication, "servers"> & {
  servers: Array<{
    name: string;
    address: string;
    port: string;
    weight: string;
  }>;
};
export type ApplicationFieldErrors = Record<string, string>;
export const normalizedHostname = (value: string) =>
  value.trim().toLowerCase().replace(/\.$/, "");

const hostname = (value: string, underscores = false) =>
  value.length > 0 &&
  value.length <= 253 &&
  value
    .split(".")
    .every(
      (label) =>
        label.length <= 63 &&
        (underscores
          ? /^[a-z0-9_](?:[a-z0-9_-]*[a-z0-9_])?$/i
          : /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i
        ).test(label),
    );
const path = (value: string) =>
  value.length <= 128 &&
  /^\/[A-Za-z0-9/._~-]*$/.test(value) &&
  !value.includes("..") &&
  !value.includes("//");
const whole = (value: string, min: number, max: number) =>
  /^\d+$/.test(value) && Number(value) >= min && Number(value) <= max;
function address(value: string) {
  value = value.trim();
  if (hostname(value.replace(/\.$/, ""), true)) return true;
  if (!value.includes(":")) return false;
  try {
    return new URL(`http://[${value}]/`).hostname.startsWith("[");
  } catch {
    return false;
  }
}

export function applicationDraftErrors(
  draft: ApplicationDraft,
): ApplicationFieldErrors {
  const errors: ApplicationFieldErrors = {};
  if (!draft.name.trim()) errors.name = "Enter an application name.";
  else if (new TextEncoder().encode(draft.name.trim()).length > 80)
    errors.name = "The application name is too long. Shorten it.";
  else if (/[\x00-\x1f\x7f]/.test(draft.name))
    errors.name =
      "Use a single-line application name without control characters.";
  if (!hostname(normalizedHostname(draft.hostname)))
    errors.hostname =
      "Enter an exact hostname without a scheme, port or wildcard.";
  if (!path(draft.path_prefix))
    errors.path_prefix =
      "Start with / and use at most 128 letters, numbers, / . _ ~ or -. Repeated slashes and .. are not supported.";
  if (!path(draft.health_path))
    errors.health_path =
      "Enter a health-check path beginning with /, without query strings, repeated slashes or ..";
  const names = new Set<string>();
  draft.servers.forEach((server, index) => {
    const key = `servers.${index}.`;
    if (!/^[-A-Za-z0-9_.:]{1,128}$/.test(server.name))
      errors[key + "name"] =
        "Use 1–128 letters, numbers, dots, dashes, underscores or colons.";
    else if (names.has(server.name))
      errors[key + "name"] = "Use a unique name for each backend server.";
    names.add(server.name);
    if (!address(server.address))
      errors[key + "address"] =
        "Enter a hostname or IP address, without a URL or port.";
    if (!whole(server.port, 1, 65535))
      errors[key + "port"] = "Enter a whole port number from 1 to 65535.";
    if (!whole(server.weight, 0, 256))
      errors[key + "weight"] = "Enter a whole weight from 0 to 256.";
  });
  if (
    draft.servers.length &&
    !draft.servers.some((s) => whole(s.weight, 1, 256))
  )
    errors["servers.0.weight"] ??=
      "Keep at least one backend server with a positive weight.";
  return errors;
}
