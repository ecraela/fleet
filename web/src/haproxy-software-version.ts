const versionPattern =
  /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9a-z-]+(?:\.[0-9a-z-]+)*))?$/;

function parseVersion(value: string) {
  const match = versionPattern.exec(value);
  if (!match || value.length > 64) return null;
  const prerelease = match[4]?.split(".") ?? [];
  if (prerelease.some((part) => /^\d+$/.test(part) && /^0\d/.test(part)))
    return null;
  return {
    numbers: [BigInt(match[1]!), BigInt(match[2]!), BigInt(match[3]!)],
    prerelease,
  };
}

export function softwareAction(
  currentVersion: string | undefined,
  targetVersion: string,
): "upgrade" | "rollback" | null {
  const current = parseVersion(currentVersion ?? "");
  const target = parseVersion(targetVersion);
  if (!current || !target) return null;

  for (let i = 0; i < 3; i++) {
    if (target.numbers[i]! < current.numbers[i]!) return "rollback";
    if (target.numbers[i]! > current.numbers[i]!) return "upgrade";
  }
  if (!target.prerelease.length && current.prerelease.length) return "upgrade";
  if (target.prerelease.length && !current.prerelease.length) return "rollback";

  for (
    let i = 0;
    i < Math.min(target.prerelease.length, current.prerelease.length);
    i++
  ) {
    const left = target.prerelease[i]!;
    const right = current.prerelease[i]!;
    if (left === right) continue;
    const leftNumeric = /^\d+$/.test(left);
    const rightNumeric = /^\d+$/.test(right);
    if (leftNumeric && rightNumeric)
      return BigInt(left) < BigInt(right) ? "rollback" : "upgrade";
    if (leftNumeric) return "rollback";
    if (rightNumeric) return "upgrade";
    return left < right ? "rollback" : "upgrade";
  }
  if (target.prerelease.length < current.prerelease.length) return "rollback";
  if (target.prerelease.length > current.prerelease.length) return "upgrade";
  return null;
}
