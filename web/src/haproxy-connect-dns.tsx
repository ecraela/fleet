import {
  Button,
  CodeBlock,
  Drawer,
  DrawerDialog,
  DrawerBody,
  DrawerFooter,
  DescriptionList,
  DescriptionItem,
  InlineTip,
} from "@ecraela/ui";
import { ArrowLeft } from "@ecraela/ui/icons";
import type {
  HAProxyClient,
  HAProxyApplicationRevision,
  HAProxyApplicationHealth,
} from "./haproxy-api";
export function HAProxyConnectDNS({
  revision,
  delegation,
  cleanup,
  onClose,
}: {
  client: HAProxyClient;
  revision: HAProxyApplicationRevision;
  health?: HAProxyApplicationHealth | undefined;
  permissionReason?: string | undefined;
  onClose: () => void;
  delegation?: { issuer: string; target: string };
  cleanup?: { sharedHostname: boolean } | undefined;
}) {
  const hostname = revision.spec.hostname;
  return (
    <Drawer
      isOpen
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DrawerDialog
        onBack={delegation ? onClose : undefined}
        backLabel="Back to Automatic TLS"
        title={
          delegation
            ? "DNS delegation instructions"
            : cleanup
              ? "DNS cleanup instructions"
              : "DNS instructions"
        }
      >
        <DrawerBody>
          <DescriptionList>
            <DescriptionItem label="Application">
              {revision.spec.name}
            </DescriptionItem>
            <DescriptionItem label="Hostname">{hostname}</DescriptionItem>
          </DescriptionList>
          {delegation ? (
            <>
              <p>
                Create this record with your DNS provider using your normal DNS
                change process.
              </p>
              <CodeBlock
                language="text"
                code={`_acme-challenge.${hostname}. CNAME ${delegation.target}.`}
              />
              <p>
                Issuer: {delegation.issuer}. Certificate keys remain on the
                nodes. The agent verifies delegation before issuance.
              </p>
            </>
          ) : cleanup ? (
            <InlineTip variant="warning">
              {cleanup.sharedHostname
                ? "Another application uses this hostname. Preserve the shared DNS record."
                : "Review the hostname's DNS records with your DNS provider before removing them. Check for other services sharing the record."}
            </InlineTip>
          ) : (
            <p>
              Point an A, AAAA or CNAME record for {hostname} to your cluster's
              traffic entrypoint. Use the address of your load balancer or
              virtual IP. Complete the change through your DNS provider's normal
              review process.
            </p>
          )}
          <InlineTip>
            This standalone console does not modify DNS. An optional Converge
            connection can be added separately.
          </InlineTip>
        </DrawerBody>
        <DrawerFooter>
          <Button
            leadingIcon={delegation ? <ArrowLeft /> : undefined}
            onPress={onClose}
          >
            {delegation ? "Back to Automatic TLS" : "Close"}
          </Button>
        </DrawerFooter>
      </DrawerDialog>
    </Drawer>
  );
}
