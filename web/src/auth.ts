export interface LocalSession {
  user_id: string;
  username: string;
  organization_id: string;
  role: "administrator" | "viewer";
  capabilities: string[];
  csrf_token: string;
  expires_at: string;
}
export class LocalAuthError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}
export async function localRequest<T>(
  path: string,
  init: RequestInit = {},
  session?: LocalSession,
): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  headers.set("X-HAProxy-Session", "local");
  if (init.body) headers.set("Content-Type", "application/json");
  if (session) headers.set("X-CSRF-Token", session.csrf_token);
  const response = await fetch(`/api/haproxy/v1/local/${path}`, {
    ...init,
    headers,
    credentials: "same-origin",
    redirect: "error",
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as {
      error?: string;
    } | null;
    throw new LocalAuthError(
      response.status,
      body?.error ??
        "The manager could not complete this request. Retry shortly.",
    );
  }
  return response.status === 204
    ? (undefined as T)
    : (response.json() as Promise<T>);
}
