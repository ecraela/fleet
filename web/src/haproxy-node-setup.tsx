import { useState } from "react";
import {
  Badge,
  Button,
  CodeBlock,
  DataTable,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  InlineTip,
  Section,
} from "@ecraela/ui";
import type { HAProxyNodeSetup } from "./haproxy-setup";
import { setupChecks, setupMessage } from "./haproxy-setup";

export function HAProxyNodeSetupPanel({
  setup,
  connected,
  now = Date.now(),
}: {
  setup?: HAProxyNodeSetup | undefined;
  connected: boolean;
  now?: number;
}) {
  const [guide, setGuide] = useState(false);
  const fresh =
    connected &&
    !!setup &&
    now - setup.observed_at_ms <= 6000 &&
    now >= setup.observed_at_ms - 60_000;
  return (
    <Section
      layout="stack"
      size="sm"
      title="Node setup"
      description="What this agent can access on its HAProxy host. Management also requires your account permissions."
      actions={<Button onPress={() => setGuide(true)}>Setup guide</Button>}
    >
      {!setup ? (
        <InlineTip>
          Waiting for a setup report. Older agents continue working; upgrade the
          agent to see host diagnostics.
        </InlineTip>
      ) : (
        <>
          {!fresh && (
            <InlineTip variant="warning">
              Last reported setup. Reconnect the agent before relying on these
              checks.
            </InlineTip>
          )}
          <DataTable
            aria-label="HAProxy node setup checks"
            rows={setup.checks.map((check) => ({ ...check, id: check.name }))}
            rowLabel={(check) => setupChecks[check.name].label}
            columns={[
              {
                id: "capability",
                header: "Capability",
                isRowHeader: true,
                cell: (check) => setupChecks[check.name].label,
              },
              {
                id: "status",
                header: "Status",
                cell: (check) => (
                  <Badge
                    tone={
                      !fresh
                        ? "neutral"
                        : check.status === "ready"
                          ? "success"
                          : check.status === "error"
                            ? "error"
                            : ["blocked", "warning"].includes(check.status)
                              ? "warning"
                              : "neutral"
                    }
                  >
                    {!fresh
                      ? "Last reported"
                      : {
                          ready: "Ready",
                          disabled: "Not enabled",
                          blocked: "Needs setup",
                          error: "Check access",
                          warning: "Check ownership",
                          pending: "Waiting",
                        }[check.status]}
                  </Badge>
                ),
              },
              { id: "details", header: "Details", cell: setupMessage },
            ]}
          />
        </>
      )}
      <Drawer isOpen={guide} onOpenChange={setGuide} size="lg">
        <DrawerDialog
          title="Connect an existing HAProxy"
          description="Run one agent beside each HAProxy instance. Your application servers do not need agents."
        >
          <DrawerBody>
            <Section
              layout="stack"
              size="sm"
              title="1. Start with monitoring"
              description="Use the agent’s installed version and run the check as its service user, inside the same container or host network and mounts."
            >
              <p>
                Expose HAProxy’s Prometheus endpoint privately. The agent needs
                outbound access to this manager and its ingestion endpoint. Your
                HAProxy keeps serving its existing configuration.
              </p>
              <CodeBlock
                title="Read-only host check"
                label="Read-only host check"
                language="text"
                code={
                  "haproxy-agent preflight \\\n  --metrics-url http://127.0.0.1:8405/metrics"
                }
              />
              <InlineTip>
                Host checks require an agent version that supports preflight.
                They do not enroll, create identity files, change permissions or
                reload HAProxy.
              </InlineTip>
            </Section>
            <Section
              layout="stack"
              size="sm"
              title="2. Discover the current setup"
              description="Grant read access to this instance’s Runtime API Unix socket and explicit configuration files. Use paths as seen by the agent."
            >
              <CodeBlock
                title="Discovery host check"
                label="Discovery host check"
                language="text"
                code={
                  "haproxy-agent preflight \\\n  --metrics-url http://127.0.0.1:8405/metrics \\\n  --runtime-socket /run/haproxy/admin.sock \\\n  --config-file /etc/haproxy/haproxy.cfg \\\n  --configuration-owner external"
                }
              />
              <p>
                Declare external ownership while GitOps, Ansible or an ingress
                controller remains responsible for configuration. Comment
                detection is a hint; it cannot prove there is no other writer.
              </p>
            </Section>
            <Section
              layout="stack"
              size="sm"
              title="3. Enable the capabilities you need"
              description="Update the existing agent’s local service or Compose settings. Preserve its identity directory and restart only the agent."
            >
              <p>
                For drain, restore and weights, add{" "}
                <code>--allow-runtime-control</code> and grant admin access on
                the Runtime API socket. These changes last until HAProxy
                reloads.
              </p>
              <p>
                For persistent edits, first resolve other configuration writers.
                Connect a private Data Plane API 3.4 socket and its credential
                file, then explicitly grant manager ownership and configuration
                control:
              </p>
              <CodeBlock
                title="Persistent management arguments"
                label="Persistent management arguments"
                language="text"
                code={
                  "--configuration-owner manager \\\n--dataplane-socket /run/haproxy/dataplane.sock \\\n--dataplane-credentials-file /etc/haproxy-agent/dataplane.json \\\n--allow-configuration-control"
                }
              />
              <InlineTip>
                The Data Plane API must own validation and reloads. Keep its
                credentials and socket on the node. Return to Node setup to
                confirm compatibility before editing. No HAProxy review or
                approval flow is added.
              </InlineTip>
            </Section>
          </DrawerBody>
          <DrawerFooter>
            <Button onPress={() => setGuide(false)}>Close</Button>
          </DrawerFooter>
        </DrawerDialog>
      </Drawer>
    </Section>
  );
}
