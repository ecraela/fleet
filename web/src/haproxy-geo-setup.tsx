import { useEffect, useState } from "react";
import {
  Badge,
  Button,
  CodeBlock,
  PaginatedDataTable,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  InlineTip,
  Section,
  Select,
  Step,
  Steps,
  TextField,
} from "@ecraela/ui";
import { haproxyErrorMessage, type HAProxyClient } from "./haproxy-api";
import type { HAProxyGeographyTraffic } from "./haproxy-geography-api";
import type { HAProxyRequest } from "./haproxy-requests-api";
import {
  clientAddressLabels,
  geoSetupEvidence,
  geoSetupPolicy,
  trustedProxyRanges,
  type ClientAddressMode,
} from "./haproxy-geo-setup-model";

export function HAProxyGeoSetup({
  client,
  clusterId,
  applicationId,
  onClose,
}: {
  client: HAProxyClient;
  clusterId: string;
  applicationId: string;
  onClose: () => void;
}) {
  const [step, setStep] = useState(0);
  const [mode, setMode] = useState<ClientAddressMode>("peer");
  const [proxies, setProxies] = useState("");
  const [country, setCountry] = useState(
    "/etc/ecraela-haproxy-agent/geo/country.mmdb",
  );
  const [asn, setASN] = useState("/etc/ecraela-haproxy-agent/geo/asn.mmdb");
  const [install, setInstall] = useState("linux");
  const [since, setSince] = useState(0);
  const [data, setData] = useState<HAProxyGeographyTraffic | null>(null);
  const [proofs, setProofs] = useState<Record<string, HAProxyRequest>>({});
  const [now, setNow] = useState(Date.now());
  const [error, setError] = useState<string | null>(null);
  const generated = geoSetupPolicy(mode, proxies, country, asn);
  useEffect(() => {
    if (step !== 2) return;
    const controller = new AbortController();
    let pending = false;
    const update = async () => {
      if (pending || document.hidden) return;
      pending = true;
      try {
        const result = await client.applicationGeography(
          clusterId,
          applicationId,
          { range: "15m" },
          controller.signal,
        );
        const observed: Record<string, HAProxyRequest> = {};
        // Filter by source when supported so direct health probes cannot hide
        // an occasional forwarded request. Older managers keep their read API.
        for (const c of result.collection) {
          const page = await client.applicationRequests(
            clusterId,
            applicationId,
            {
              range: "15m",
              node: c.node_id,
              ...(result.client_source_filter ? { client_source: mode } : {}),
            },
            undefined,
            undefined,
            controller.signal,
          );
          const match = page.requests.find(
            (r) =>
              r.at_ms >= since &&
              r.geo?.source === mode &&
              r.geo.profile === c.pipeline?.geo?.profile,
          );
          if (match) observed[c.node_id] = match;
        }
        if (!controller.signal.aborted) {
          setData(result);
          setProofs((previous) => ({ ...previous, ...observed }));
          setNow(Date.now());
          setError(null);
        }
      } catch (cause) {
        if (!controller.signal.aborted) {
          setError(haproxyErrorMessage(cause));
          setNow(Date.now());
        }
      } finally {
        pending = false;
      }
    };
    void update();
    const timer = setInterval(() => void update(), 3000);
    const clock = setInterval(() => setNow(Date.now()), 1000);
    document.addEventListener("visibilitychange", update);
    return () => {
      controller.abort();
      clearInterval(timer);
      clearInterval(clock);
      document.removeEventListener("visibilitychange", update);
    };
  }, [client, clusterId, applicationId, step, since, mode]);
  return (
    <Drawer
      size="lg"
      isOpen
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DrawerDialog
        title="Client IP & Geo setup"
        description="Configure trust on your hosts, then check what the agents observe. This guide does not change the fleet."
      >
        <DrawerBody>
          <Steps density="compact">
            {[
              "Choose the traffic path",
              "Configure each node",
              "Check fresh traffic",
            ].map((title, i) => (
              <Step
                key={title}
                title={title}
                status={
                  i < step ? "complete" : i === step ? "current" : "upcoming"
                }
              />
            ))}
          </Steps>
          {step === 0 && (
            <Section
              layout="stack"
              size="sm"
              title="Choose the traffic path"
              description="Use the last hop that actually connects to HAProxy. A TCP passthrough balancer cannot sanitize HTTP headers."
            >
              <Select
                label="Client address source"
                value={mode}
                onChange={(value) =>
                  setMode(String(value) as ClientAddressMode)
                }
                options={Object.entries(clientAddressLabels).map(
                  ([id, label]) => ({ id, label }),
                )}
              />
              <InlineTip>
                {mode === "peer"
                  ? "Use for direct clients. Behind a load balancer this locates the balancer, not the visitor. All forwarding claims are ignored."
                  : mode === "xff"
                    ? "The HTTP proxy must replace incoming X-Forwarded-For or append the address it observed. The agent walks right to left and stops at the first untrusted hop. Duplicate, malformed and mixed PROXY/XFF chains remain unresolved."
                    : "Both ends must speak PROXY protocol. Only the listed transport peers may supply it; a PROXY header is not authentication. The rules in the next step allow direct probes from unlisted peers. Listed peers must send a PROXY header before HTTP or TLS."}
              </InlineTip>
              {mode !== "peer" && (
                <TextField
                  label="Trusted proxy CIDRs"
                  value={proxies}
                  onChange={setProxies}
                  description="Actual ingress ranges, separated by commas. Prefer individual /32 or /128 addresses. Use canonical networks; blanket /0 trust is refused."
                  placeholder="10.20.0.10/32, 2001:db8:20::10/128"
                  isInvalid={!!proxies && !trustedProxyRanges(proxies)}
                  errorMessage="Enter distinct canonical IPv4/IPv6 CIDRs with a nonzero prefix."
                  errorDisplay="inline"
                />
              )}
              <TextField
                label="Country MMDB path on the agent"
                value={country}
                onChange={setCountry}
                description="Leave empty to collect ASN only."
              />
              <TextField
                label="ASN MMDB path on the agent"
                value={asn}
                onChange={setASN}
                description="Leave empty to collect country only."
              />
              {!generated && (
                <InlineTip>
                  Provide at least one absolute database file path and, for
                  forwarded modes, valid trusted proxy ranges.
                </InlineTip>
              )}
              <InlineTip>
                Obtain licensed Country/ASN data yourself. Production databases
                are not bundled or downloaded. Raw client IPs and headers stay
                on the node; the consoles receive country/ASN and lookup status.
              </InlineTip>
            </Section>
          )}
          {step === 1 && generated && (
            <>
              <Section
                layout="stack"
                size="sm"
                title="1. Install the local policy"
                description="On every node, place the database files at these paths. Policy and data must be regular files, readable by the agent and not writable by its group or other users. Each database is limited to 32 MiB."
              >
                <CodeBlock
                  code={generated.policy}
                  label="Local Geo policy"
                  title="/etc/ecraela-haproxy-agent/geo.json"
                />
                <Select
                  label="Agent installation"
                  value={install}
                  onChange={(v) => setInstall(String(v))}
                  options={[
                    { id: "linux", label: "Linux service" },
                    { id: "docker", label: "Docker" },
                  ]}
                />
                {install === "linux" ? (
                  <p>
                    Add this argument to the existing service command or its
                    supported local profile. Keep the current identity
                    directory, socket and other arguments.
                  </p>
                ) : (
                  <p>
                    Mount the policy and database directory read-only at the
                    same paths inside the agent container. Add this argument to
                    its existing command. Preserve the identity volume and
                    private log socket mount; no Docker socket is needed.
                  </p>
                )}
                <CodeBlock
                  code="--request-geo-policy /etc/ecraela-haproxy-agent/geo.json"
                  language="text"
                  label="Additional agent argument"
                />
                <CodeBlock
                  code="haproxy-agent check-geo --request-geo-policy /etc/ecraela-haproxy-agent/geo.json"
                  language="text"
                  label="Read-only policy check"
                />
                <p>
                  Run the check as the agent user with its mounts. It opens only
                  the policy and databases, and prints safe metadata and the
                  profile fingerprint. Restart the existing agent after changes;
                  do not enroll it again.
                </p>
              </Section>
              <Section
                layout="stack"
                size="sm"
                title="2. Install the private logging profile"
                description="Update the agent before installing the cv4 profile. Enable request collection in the application editor. Adapt this snippet to the existing frontend and socket path, preserving routing, binds and shared TLS guards. Do not overwrite custom logging silently."
              >
                {mode === "proxy_protocol" && (
                  <InlineTip variant="warning">
                    Use conditional <code>expect-proxy</code> below, with peer
                    capture first. Do not combine it with an unconditional{" "}
                    <code>accept-proxy</code> bind. Restrict node listener
                    access at the network boundary to ingress and intended
                    probes.
                  </InlineTip>
                )}
                <CodeBlock
                  code={generated.frontend}
                  language="text"
                  label="Frontend client address and logging profile"
                />
                {mode === "proxy_protocol" && (
                  <CodeBlock
                    code="send-proxy-v2"
                    language="text"
                    label="HAProxy upstream example: append to the existing server line"
                  />
                )}
                {mode === "proxy_protocol" && (
                  <p>
                    Enable PROXY v2 in your upstream load balancer. Switch each
                    withdrawn target after its receiver and backend health are
                    ready. Pool-wide settings need a separate listener and
                    target pool for migration. Probes from trusted ranges must
                    also send PROXY or use a separate private probe listener.
                    Managed node maintenance pins its entrypoint; use the
                    ingress owner’s coordinated maintenance procedure when
                    changing that sender.
                  </p>
                )}
                <InlineTip>
                  Validate with <code>haproxy -c -f /path/to/haproxy.cfg</code>.
                  Withdraw and drain one node, reload, check health and rejoin
                  it before the next. Coordinate sender/receiver changes to
                  avoid breaking live connections. The <code>no log</code> line
                  clears inherited destinations so raw fields go only to the
                  private Unix socket.
                </InlineTip>
              </Section>
            </>
          )}
          {step === 2 && (
            <Section
              layout="stack"
              size="sm"
              title="Check fresh traffic"
              description="Send requests to this application through the intended HTTP and HTTPS entrypoint. Checks refresh every three seconds while this drawer is visible."
            >
              <InlineTip>
                Selected source: {clientAddressLabels[mode]}. Only new requests
                since starting this check, matching the agent’s current profile,
                count. Database readiness and address-source observation do not
                attest the proxy’s configuration or firewall; test spoof
                rejection at your ingress too.
              </InlineTip>
              {error && (
                <InlineTip variant="warning" role="alert">
                  {error} Retained reports are not current verification.
                </InlineTip>
              )}
              <PaginatedDataTable
                aria-label="Client address setup verification"
                itemLabel="nodes"
                rows={(data?.collection ?? []).map((c) => ({
                  ...c,
                  id: c.node_id,
                }))}
                rowLabel={(c) => c.name}
                columns={[
                  {
                    id: "node",
                    header: "Node",
                    isRowHeader: true,
                    cell: (c) => c.name,
                  },
                  {
                    id: "status",
                    header: "Observation",
                    cell: (c) => {
                      const status = geoSetupEvidence(
                        c.pipeline,
                        c.fresh && !error,
                        mode,
                        proofs[c.node_id] ? [proofs[c.node_id]!] : [],
                        since,
                        now,
                      );
                      return (
                        <Badge
                          tone={
                            status === "Address source observed"
                              ? "success"
                              : "warning"
                          }
                        >
                          {status}
                        </Badge>
                      );
                    },
                  },
                  {
                    id: "profile",
                    header: "Effective profile",
                    cell: (c) =>
                      c.pipeline?.geo ? (
                        <code title={c.pipeline.geo.profile}>
                          {c.pipeline.geo.profile.slice(0, 12)}
                        </code>
                      ) : (
                        "Not reported"
                      ),
                  },
                ]}
              />
              {!data?.collection.length && (
                <p>
                  Waiting for node collection reports. Ensure the agent’s
                  private log socket is enabled and application request
                  collection is deployed.
                </p>
              )}
              <p>
                Compare the full profile fingerprint with the local check
                output. Unknown country/ASN can be correct for a private address
                or a database miss. Earlier records are not enriched
                retroactively. Include policy and databases in the node’s
                backup.
              </p>
            </Section>
          )}
        </DrawerBody>
        <DrawerFooter>
          {step > 0 && <Button onPress={() => setStep(step - 1)}>Back</Button>}
          <Button onPress={onClose}>Close setup</Button>
          {step < 2 && (
            <Button
              variant="primary"
              isDisabled={!generated}
              onPress={() => {
                if (step === 1) {
                  setSince(Date.now());
                  setProofs({});
                  setData(null);
                }
                setStep(step + 1);
              }}
            >
              {step === 0 ? "Generate configuration" : "Check fresh traffic"}
            </Button>
          )}
        </DrawerFooter>
      </DrawerDialog>
    </Drawer>
  );
}
