import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Steps } from "@ecraela/ui";
import { HAProxyEnrollmentProgress } from "./haproxy-enrollment";

const render = (
  node: { name: string } | null,
  reporting = false,
  terminal = false,
) =>
  renderToStaticMarkup(
    <Steps density="compact">
      <HAProxyEnrollmentProgress
        command="synthetic-install-command"
        expiresAt="2026-11-02T20:30:00+02:00"
        node={node}
        reporting={reporting}
        terminal={terminal}
        observationError={terminal ? "The enrollment expired." : null}
      />
    </Steps>,
  );
describe("enrollment progress placement", () => {
  it("keeps connection waiting beside the install command", () => {
    const steps = render(null).match(/<li\b[\s\S]*?<\/li>/g)!;
    expect(steps).toHaveLength(2);
    expect(steps[0]).toContain("synthetic-install-command");
    expect(steps[0]).toContain("Waiting for the agent to connect");
    expect(steps[1]).not.toContain("Waiting for HAProxy metrics");
  });
  it("moves waiting to metrics after the agent connects", () => {
    const steps = render({ name: "edge-eu-1" }).match(/<li\b[\s\S]*?<\/li>/g)!;
    expect(steps[0]).toContain("Agent connected");
    expect(steps[0]).not.toContain("synthetic-install-command");
    expect(steps[1]).toContain("Waiting for HAProxy metrics");
  });
  it("stops waiting when setup finishes or expires", () => {
    const completed = render({ name: "edge-eu-1" }, true);
    expect(completed).toContain("Node connected and HAProxy metrics confirmed");
    expect(completed).not.toContain("Waiting for HAProxy metrics");
    const expired = render(null, false, true);
    expect(expired).toContain("The enrollment expired.");
    expect(expired).not.toContain("Waiting for the agent to connect");
  });
});
