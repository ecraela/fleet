import { Activity, createContext, useContext, type ReactNode } from "react";

/** Temporarily release modal effects while a global verification flow is open. */
export const OverlaySuspensionContext = createContext(false);

export function SuspendableOverlay({ children }: { children: ReactNode }) {
  const suspended = useContext(OverlaySuspensionContext);
  // Activity preserves form state while cleaning up inertness and focus traps.
  return (
    <Activity mode={suspended ? "hidden" : "visible"}>{children}</Activity>
  );
}
