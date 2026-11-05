import { useEffect, useState, type ReactNode } from "react";
import { Spinner } from "../../icons";

/** Indeterminate progress beside the content being loaded. */
export function LoadingStatus({
  children,
  delayMs = 0,
}: {
  children: ReactNode;
  delayMs?: number;
}) {
  const [visible, setVisible] = useState(delayMs <= 0);
  useEffect(() => {
    if (delayMs <= 0) {
      setVisible(true);
      return;
    }
    const timer = setTimeout(() => setVisible(true), delayMs);
    return () => clearTimeout(timer);
  }, [delayMs]);
  return (
    <div
      className="cv-loading-status"
      role={visible ? "status" : undefined}
      aria-hidden={visible ? undefined : true}
      data-waiting={!visible || undefined}
    >
      <Spinner className="cv-loading-status__spinner" />
      <span>{children}</span>
    </div>
  );
}
