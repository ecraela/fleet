import type { Organization } from "./api";
export function hasOrganizationCapability(
  organization: Organization,
  capability: string,
): boolean {
  return organization.capabilities.includes(capability);
}
