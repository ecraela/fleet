import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { HAProxyAgentVersion } from "./haproxy-agent-version";

const build = {
  version: "v0.1.0",
  revision: "a".repeat(40),
  platform: "linux/arm64",
  protocol: 1,
  executable_sha256: "b".repeat(64),
};

describe("HAProxy executable versions", () => {
  it("shows agent and manager separately and does not assert signature verification", () => {
    const html = renderToStaticMarkup(
      <HAProxyAgentVersion
        agent={build}
        manager={{ ...build, version: "v0.1.1" }}
        fresh
      />,
    );
    expect(html).toContain("v0.1.0");
    expect(html).toContain("v0.1.1");
    expect(html).toContain("Compatible protocol");
    expect(html).toContain("does not prove a release signature");
  });
  it("keeps old agents usable and marks stale, development and incompatible builds honestly", () => {
    expect(renderToStaticMarkup(<HAProxyAgentVersion fresh />)).toContain(
      "Not reported by this agent",
    );
    expect(
      renderToStaticMarkup(
        <HAProxyAgentVersion agent={build} manager={build} fresh={false} />,
      ),
    ).toContain("Last reported");
    expect(
      renderToStaticMarkup(
        <HAProxyAgentVersion
          agent={{ ...build, version: "dev" }}
          manager={build}
          fresh
        />,
      ),
    ).toContain("Development build");
    expect(
      renderToStaticMarkup(
        <HAProxyAgentVersion
          agent={build}
          manager={{ ...build, protocol: 2 }}
          fresh
        />,
      ),
    ).toContain("Check manager compatibility");
  });
});
