import {
  Badge,
  DescriptionItem,
  DescriptionList,
  InlineTip,
} from "@ecraela/ui";
import type { HAProxyAgentBuild } from "./haproxy-api";

export function HAProxyAgentVersion({
  agent,
  manager,
  fresh,
}: {
  agent?: HAProxyAgentBuild | undefined;
  manager?: HAProxyAgentBuild | undefined;
  fresh: boolean;
}) {
  const development = agent?.version === "dev";
  const compatible =
    !!agent && !!manager && agent.protocol === manager.protocol;
  return (
    <>
      <DescriptionList columns={2}>
        <DescriptionItem label="Agent version">
          {agent?.version ?? "Not reported by this agent"}
        </DescriptionItem>
        <DescriptionItem label="Manager version">
          {manager?.version ?? "Not reported"}
        </DescriptionItem>
        <DescriptionItem label="Agent compatibility">
          <Badge
            tone={
              !fresh || !agent || development
                ? "neutral"
                : compatible
                  ? "success"
                  : "warning"
            }
          >
            {!fresh
              ? "Last reported"
              : !agent
                ? "Unknown"
                : development
                  ? "Development build"
                  : compatible
                    ? "Compatible protocol"
                    : "Check manager compatibility"}
          </Badge>
        </DescriptionItem>
        <DescriptionItem label="Agent platform">
          {agent?.platform ?? "Unknown"}
        </DescriptionItem>
        {agent && (
          <DescriptionItem label="Agent source revision">
            {agent.revision}
          </DescriptionItem>
        )}
      </DescriptionList>
      {agent && (
        <InlineTip variant="info">
          Upgrade or roll back using the host’s agent installer. The existing
          node identity, keys and local policies stay on that host. A reported
          version does not prove a release signature; the installer verifies it
          before activation.
        </InlineTip>
      )}
    </>
  );
}
