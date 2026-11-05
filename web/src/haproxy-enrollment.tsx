import { useEffect, useRef, useState } from "react";
import {
  Button,
  CodeBlock,
  Drawer,
  DrawerDialog,
  DrawerBody,
  DrawerFooter,
  DialogForm,
  InlineTip,
  LoadingStatus,
  Select,
  Step,
  Steps,
  TextField,
} from "@ecraela/ui";
import {
  type HAProxyAgentSetup,
  type HAProxyCluster,
  type HAProxyEnrolledNode,
  HAProxyClient,
  haproxyErrorMessage,
} from "./haproxy-api";
import { usePermissionAction, isActionCancelled } from "./permissions";
import { ApiError } from "./api";
import { agentInstallCommand, hasReportingMetrics } from "./haproxy-install";

function errorMessage(error: unknown): string {
  return error instanceof ApiError ? error.message : haproxyErrorMessage(error);
}

export function HAProxyEnrollmentProgress({
  command,
  expiresAt,
  node,
  reporting,
  terminal,
  observationError,
}: {
  command: string | null;
  expiresAt?: string | undefined;
  node: Pick<HAProxyEnrolledNode, "name"> | null;
  reporting: boolean;
  terminal: boolean;
  observationError: string | null;
}) {
  return (
    <>
      <Step
        title={node ? "Agent connected" : "Install and connect the agent"}
        status={node ? "complete" : command ? "current" : "upcoming"}
        description={
          node
            ? `${node.name} connected with its persistent identity. Restarting the agent does not need another enrollment command.`
            : "Copy the complete command and run it on the HAProxy host."
        }
      >
        {command && !node && (
          <>
            <InlineTip label="One-use setup command">
              Keep this enrollment credential private. It expires
              {expiresAt
                ? ` at ${new Date(expiresAt).toLocaleTimeString()}`
                : " shortly"}
              . The agent keeps its identity in a private directory on the host.
            </InlineTip>
            <CodeBlock
              code={command}
              language="text"
              title="Agent install command"
              label="Agent install command"
              className="cv-haproxy-install-command"
            />
            {!terminal && (
              <LoadingStatus delayMs={1000}>
                Waiting for the agent to connect…
              </LoadingStatus>
            )}
            {observationError && (
              <InlineTip variant="warning" role="status">
                {observationError}
              </InlineTip>
            )}
          </>
        )}
      </Step>
      <Step
        title={
          reporting ? "HAProxy metrics are reporting" : "Confirm live metrics"
        }
        status={reporting ? "complete" : node ? "current" : "upcoming"}
        description={
          reporting
            ? "A fresh, successful HAProxy scrape has reached the manager. Your node is ready to inspect."
            : "Connection alone is not enough; we wait for fresh HAProxy measurements."
        }
      >
        {node && !reporting && !terminal && (
          <LoadingStatus delayMs={1000}>
            Waiting for HAProxy metrics…
          </LoadingStatus>
        )}
        {node && observationError && (
          <InlineTip variant="warning" role="status">
            {observationError}
          </InlineTip>
        )}
        {reporting && (
          <InlineTip variant="success" role="status">
            Node connected and HAProxy metrics confirmed.
          </InlineTip>
        )}
      </Step>
    </>
  );
}

export function HAProxyEnrollment({
  client,
  clusters,
  initialCluster,
  onClose,
  onConnected,
  onBusy,
}: {
  client: HAProxyClient;
  clusters: HAProxyCluster[];
  initialCluster: string;
  onClose: () => void;
  onConnected: (node: HAProxyEnrolledNode) => void;
  onBusy: (busy: boolean) => void;
}) {
  const { runAction } = usePermissionAction();
  const [setup, setSetup] = useState<HAProxyAgentSetup | null>(null);
  const [cluster, setCluster] = useState(initialCluster);
  const [name, setName] = useState("haproxy-1");
  const [metrics, setMetrics] = useState("");
  const [installMode, setInstallMode] = useState<"docker" | "linux">("docker");
  const [token, setToken] = useState<{
    token: string;
    expires_at: string;
  } | null>(null);
  const [node, setNode] = useState<HAProxyEnrolledNode | null>(null);
  const [reporting, setReporting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [observationError, setObservationError] = useState<string | null>(null);
  const [terminal, setTerminal] = useState(false);
  const [retry, setRetry] = useState(0);
  const [commandID, setCommandID] = useState(() => crypto.randomUUID());
  const lifetime = useRef<AbortController | null>(null);
  const busyRef = useRef(false);

  useEffect(() => {
    const controller = new AbortController();
    setError(null);
    lifetime.current = controller;
    void client
      .agentSetup(controller.signal)
      .then((value) => {
        if (controller.signal.aborted) return;
        setSetup(value);
        setMetrics(value.metrics_url);
        setError(null);
      })
      .catch((reason) => {
        if (!controller.signal.aborted)
          setError(
            errorMessage(reason) +
              " Ask the installation administrator to configure the agent image and connection addresses.",
          );
      });
    return () => {
      controller.abort();
      onBusy(false);
    };
  }, [client, retry, onBusy]);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const started = Date.now();
    let connected = false;
    const poll = async () => {
      if (controller.signal.aborted) return;
      try {
        const status = await client.enrollmentStatus(
          token.token,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        if (
          status.state === "expired" ||
          status.state === "revoked" ||
          status.node?.revoked_at
        ) {
          setTerminal(true);
          setObservationError(
            status.state === "expired"
              ? "This command expired before a node enrolled. Start a new setup to generate a fresh command."
              : "This enrollment or node was revoked. Start a new setup to enroll again.",
          );
          return;
        }
        if (status.state === "connected" && status.node) {
          connected = true;
          setNode(status.node);
          const snapshot = await client.snapshot(
            status.node.id,
            controller.signal,
          );
          if (controller.signal.aborted) return;
          if (hasReportingMetrics(snapshot, status.node.id)) {
            setReporting(true);
            setObservationError(null);
            return;
          }
          setObservationError(
            "The agent connected. Waiting for a successful HAProxy scrape; check that the metrics address is reachable from the agent.",
          );
        } else {
          setObservationError(null);
        }
      } catch (reason) {
        if (controller.signal.aborted) return;
        setObservationError(
          errorMessage(reason) +
            " Connection has not been confirmed; checking again shortly.",
        );
      }
      if (
        Date.now() - started >= 15 * 60_000 ||
        (!connected && Date.now() >= Date.parse(token.expires_at))
      ) {
        setTerminal(true);
        setObservationError(
          connected
            ? "The node enrolled, but reporting could not be confirmed. Check the agent logs and HAProxy metrics listener; the node remains in your fleet."
            : "The enrollment command expired. Start a new setup to try again.",
        );
        return;
      }
      if (!controller.signal.aborted)
        timer = setTimeout(() => void poll(), 3000);
    };
    void poll();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [client, token]);

  async function generate() {
    if (busyRef.current || token || !setup || !lifetime.current) return;
    let endpoint: URL;
    try {
      endpoint = new URL(metrics);
    } catch {
      setError("Enter a complete HTTP or HTTPS metrics address.");
      return;
    }
    if (
      !name.trim() ||
      new TextEncoder().encode(name.trim()).length > 255 ||
      !cluster ||
      !["http:", "https:"].includes(endpoint.protocol) ||
      endpoint.username ||
      endpoint.password
    ) {
      setError(
        "Choose a cluster, enter a node name up to 255 bytes, and use an HTTP or HTTPS metrics address without credentials.",
      );
      return;
    }
    busyRef.current = true;
    setMetrics(endpoint.href);
    setBusy(true);
    onBusy(true);
    setError(null);
    const signal = lifetime.current.signal;
    try {
      await runAction(async () => {
        await client.login(true);
        signal.throwIfAborted();
        const issued = await client.enroll(cluster);
        if (!signal.aborted) {
          setCommandID(crypto.randomUUID());
          setToken(issued);
        }
      });
    } catch (reason) {
      if (!signal.aborted && !isActionCancelled(reason))
        setError(errorMessage(reason));
    } finally {
      busyRef.current = false;
      if (!signal.aborted) {
        setBusy(false);
        onBusy(false);
      }
    }
  }

  return (
    <Drawer
      isOpen
      size="lg"
      isDismissable={!busy}
      isKeyboardDismissDisabled={busy}
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DrawerDialog title="Set up a HAProxy node" hideCloseButton={busy}>
        <DialogForm onSubmit={() => void generate()}>
          <DrawerBody>
            {error && (
              <InlineTip variant="error" role="alert">
                {error}
              </InlineTip>
            )}
            {!setup && !error && (
              <LoadingStatus delayMs={1000}>
                Loading installation settings…
              </LoadingStatus>
            )}
            <Steps density="compact">
              <Step
                title="Choose the node"
                status={token ? "complete" : "current"}
                description={
                  token
                    ? `${name.trim()} will join ${clusters.find((value) => value.id === cluster)?.name ?? "the selected cluster"}.`
                    : "The agent observes one HAProxy instance and keeps its identity on that machine."
                }
              >
                {!token && (
                  <div className="cv-haproxy">
                    <Select
                      label="Cluster"
                      value={cluster || null}
                      onChange={(value) => setCluster(String(value ?? ""))}
                      options={clusters.map((value) => ({
                        id: value.id,
                        label: value.name,
                      }))}
                      isDisabled={busy}
                    />
                    {setup?.release_version && (
                      <Select
                        label="Installation"
                        value={installMode}
                        isDisabled={busy}
                        onChange={(value) =>
                          setInstallMode(value === "linux" ? "linux" : "docker")
                        }
                        options={[
                          { id: "docker", label: "Docker Compose" },
                          { id: "linux", label: "Linux service (systemd)" },
                        ]}
                      />
                    )}
                    <TextField
                      label="Node name"
                      value={name}
                      onChange={setName}
                      isDisabled={busy}
                      autoFocus
                    />
                    <TextField
                      label="HAProxy metrics address"
                      value={metrics}
                      onChange={setMetrics}
                      isDisabled={busy || !setup}
                      description="Use the metrics listener of this specific HAProxy instance. Run one agent per instance; each address must be reachable from its agent."
                    />
                    <InlineTip label="Start with monitoring">
                      Install one agent on each existing HAProxy host. The
                      command enables observation only. After connection, Node
                      setup shows the local access needed for discovery and
                      management. Your application servers do not need agents.
                    </InlineTip>
                    {setup && (
                      <InlineTip
                        label={
                          setup.release_version
                            ? `Signed release ${setup.release_version}`
                            : setup.network === "host"
                              ? "Linux host with Docker"
                              : "Local Docker demo"
                        }
                      >
                        {setup.release_version
                          ? `Requires Python 3.11+, Cosign 3 and GitHub CLI access to the release. ${installMode === "linux" ? "Run as root on a Linux systemd host." : "Docker Compose and read access to the private image registry must already be configured."}`
                          : setup.network === "host"
                            ? "Run the command on the Linux machine hosting HAProxy. Docker must already be installed."
                            : "Run the command on the machine running this Docker demo. It observes the existing demo HAProxy; it does not provision another load balancer."}
                      </InlineTip>
                    )}
                  </div>
                )}
              </Step>
              <HAProxyEnrollmentProgress
                command={
                  token && setup && !node
                    ? agentInstallCommand(
                        setup,
                        token.token,
                        name.trim(),
                        metrics,
                        commandID,
                        installMode,
                      )
                    : null
                }
                expiresAt={token?.expires_at}
                node={node}
                reporting={reporting}
                terminal={terminal}
                observationError={observationError}
              />
            </Steps>
          </DrawerBody>
          <DrawerFooter>
            {!token ? (
              <>
                {setup ? (
                  <Button type="submit" variant="primary" isLoading={busy}>
                    Generate install command
                  </Button>
                ) : (
                  <Button onPress={() => setRetry((value) => value + 1)}>
                    Retry settings
                  </Button>
                )}
                <Button onPress={onClose} isDisabled={busy}>
                  Cancel
                </Button>
              </>
            ) : (
              <>
                {terminal && (
                  <Button
                    onPress={() => {
                      setToken(null);
                      setNode(null);
                      setReporting(false);
                      setTerminal(false);
                      setObservationError(null);
                    }}
                  >
                    Start new setup
                  </Button>
                )}
                {reporting && node && (
                  <Button variant="primary" onPress={() => onConnected(node)}>
                    View node
                  </Button>
                )}
                <Button onPress={onClose}>
                  {reporting ? "Close" : "Close setup"}
                </Button>
              </>
            )}
          </DrawerFooter>
        </DialogForm>
      </DrawerDialog>
    </Drawer>
  );
}
