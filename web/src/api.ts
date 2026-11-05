// Standalone identity shape consumed by the reused HAProxy screens.
export interface Organization {
  id: string;
  capabilities: string[];
}
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly code = "",
  ) {
    super(message);
    this.name = "ApiError";
  }
}
