import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useReducer,
  useState,
  type ReactNode,
} from "react";

export const CONNECTION_FAILURE_DELAY = 30_000;
export interface ConnectionIssueState {
  message: string;
  connection: boolean;
  since: number;
}
export type ConnectionIssues = Record<string, ConnectionIssueState>;
export type IssueAction = {
  id: string;
  message: string | null;
  connection: boolean;
  now: number;
};
export function updateConnectionIssues(
  state: ConnectionIssues,
  action: IssueAction,
): ConnectionIssues {
  const previous = state[action.id];
  if (action.message === null) {
    if (!previous) return state;
    const next = { ...state };
    delete next[action.id];
    return next;
  }
  if (
    previous?.message === action.message &&
    previous.connection === action.connection
  )
    return state;
  return {
    ...state,
    [action.id]: {
      message: action.message,
      connection: action.connection,
      since: previous?.since ?? action.now,
    },
  };
}
export function connectionHealth(issues: ConnectionIssues, now: number) {
  const active = Object.values(issues);
  return {
    messages: [...new Set(active.map((issue) => issue.message))],
    disconnected: active.some(
      (issue) =>
        issue.connection && now - issue.since >= CONNECTION_FAILURE_DELAY,
    ),
  };
}
const HealthContext = createContext({
  messages: [] as string[],
  disconnected: false,
  report: (_id: string, _message: string | null, _connection: boolean) => {},
});
export function ConnectionHealthProvider({
  children,
}: {
  children: ReactNode;
}) {
  const [issues, dispatch] = useReducer(updateConnectionIssues, {});
  const [now, setNow] = useState(Date.now);
  const report = useCallback(
    (id: string, message: string | null, connection: boolean) => {
      const now = Date.now();
      dispatch({ id, message, connection, now });
      setNow(now);
    },
    [],
  );
  useEffect(() => {
    const deadlines = Object.values(issues)
      .filter((issue) => issue.connection)
      .map((issue) => issue.since + CONNECTION_FAILURE_DELAY)
      .filter((deadline) => deadline > now);
    if (!deadlines.length) return;
    const timer = setTimeout(
      () => setNow(Date.now()),
      Math.max(0, Math.min(...deadlines) - Date.now()),
    );
    return () => clearTimeout(timer);
  }, [issues, now]);
  const value = useMemo(
    () => ({ ...connectionHealth(issues, now), report }),
    [issues, now, report],
  );
  return (
    <HealthContext.Provider value={value}>{children}</HealthContext.Provider>
  );
}
export function useConnectionHealth() {
  return useContext(HealthContext);
}
/** Reports recoverable background failures without inserting page content. */
export function ConnectionIssue({
  message,
  connection = false,
}: {
  message: string;
  connection?: boolean;
}) {
  const id = useId();
  const { report } = useConnectionHealth();
  useEffect(() => {
    report(id, message, connection);
  }, [id, message, connection, report]);
  useEffect(() => () => report(id, null, false), [id, report]);
  return null;
}
