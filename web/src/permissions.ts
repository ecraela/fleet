// Every action is authorized by the Go API against current local permissions.
const runAction = <T>(action: () => Promise<T>): Promise<T> => action();
export function usePermissionAction() {
  return { runAction };
}
export function isActionCancelled(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}
