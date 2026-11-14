import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SignedReleaseDetails } from "./haproxy-software";
import type { HAProxySoftwareRelease } from "./haproxy-api";

const release: HAProxySoftwareRelease = {
  version: "v0.1.0-rc.22",
  revision: "a".repeat(40),
  manifest_sha256: "b".repeat(64),
  publisher: "sigstore:ecraela/fleet",
  image: "ghcr.io/ecraela/fleet@sha256:" + "c".repeat(64),
  protocol: 1,
  state_schema: 1,
  managed_upgrades: true,
  notes: "Upgrade controls and full software inventory.",
  platforms: ["linux/amd64", "linux/arm64"],
  agent_digests: {
    "linux/amd64": "d".repeat(64),
    "linux/arm64": "e".repeat(64),
  },
  manager_digests: {
    "linux/amd64": "f".repeat(64),
    "linux/arm64": "0".repeat(64),
  },
  compatibility: {
    manager: { min: "v0.1.0-rc.21", max: "v0.1.0-rc.22" },
    agent: { min: "v0.1.0-rc.21", max: "v0.1.0-rc.22" },
  },
};

describe("signed release details", () => {
  it("shows notes, publisher and exact platform artifact before an agent update", () => {
    const html = renderToStaticMarkup(
      <SignedReleaseDetails
        release={release}
        platform="linux/amd64"
        role="agent"
      />,
    );
    expect(html).toContain(release.notes);
    expect(html).toContain(release.publisher);
    expect(html).toContain(release.manifest_sha256);
    expect(html).toContain(release.agent_digests["linux/amd64"]);
    expect(html).not.toContain(release.manager_digests["linux/amd64"]);
    expect(html).toContain("v0.1.0-rc.21");
  });

  it("does not imply that an unsupported platform has an artifact", () => {
    const html = renderToStaticMarkup(
      <SignedReleaseDetails
        release={release}
        platform="darwin/arm64"
        role="manager"
      />,
    );
    expect(html).toContain("No artifact for this platform");
  });
});
