import { ConnectionIssue, useConnectionHealth } from "./connection-health";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  StatusDot,
  TooltipText,
  AccountMenu,
  Button,
  DialogForm,
  Drawer,
  DrawerDialog,
  DrawerBody,
  DrawerFooter,
  InlineTip,
  LoadingStatus,
  Takeover,
  IconButton,
  TextField,
  Section,
  MenuItem,
  MenuSeparator,
  ThemeMenuSection,
  Link,
  focusFormError,
} from "@ecraela/ui";
import { readTheme, saveTheme } from "./theme";
import { passwordChangeErrors } from "./auth-forms";
import { localRequest, LocalAuthError, type LocalSession } from "./auth";
import { stopHAProxyClients } from "./haproxy-api";
import { HAProxyView, type ManagerConnectionStatus } from "./haproxy";
import { ConvergeConnector } from "./connector";
import { GitHub, Moon, Sun } from "@ecraela/ui/icons";
import type { Theme } from "./theme";
import ecraelaMark from "./assets/ecraela-mark.svg";

function FleetBrand() {
  return (
    <Link
      variant="plain"
      className="hm-brand"
      href="/"
      aria-label="Ecraela Fleet"
    >
      <img src={ecraelaMark} alt="" width={24} height={24} />
      <span className="hm-brand__company">Ecraela</span>
      <span className="hm-brand__divider" aria-hidden="true">
        /
      </span>
      <strong>Fleet</strong>
    </Link>
  );
}

function message(error: unknown): string {
  return error instanceof LocalAuthError
    ? error.message
    : "The manager could not connect. Retry shortly.";
}

export function App() {
  const connectionHealth = useConnectionHealth();
  const authEpoch = useRef(0);
  const [session, setSession] = useState<LocalSession | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [sessionRefreshError, setSessionRefreshError] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [theme, setTheme] = useState(readTheme);
  const [managerConnection, setManagerConnection] =
    useState<ManagerConnectionStatus>("connecting");
  const displayedConnection = connectionHealth.disconnected
    ? "disconnected"
    : connectionHealth.messages.length
      ? "degraded"
      : managerConnection;
  const expire = useCallback(() => {
    authEpoch.current++;
    stopHAProxyClients();
    setSession(null);
    setManagerConnection("connecting");
    setSessionRefreshError(false);
    setPasswordOpen(false);
  }, []);
  const check = useCallback(async () => {
    const epoch = authEpoch.current;
    try {
      const current = await localRequest<LocalSession>("session");
      if (epoch !== authEpoch.current) return;
      setSession(current);
      setSessionRefreshError(false);
      setError(null);
    } catch (e) {
      if (epoch !== authEpoch.current) return;
      if (e instanceof LocalAuthError && e.status === 401) {
        expire();
        setError(null);
      } else {
        setSessionRefreshError(true);
        setError(message(e));
      }
    } finally {
      setLoading(false);
    }
  }, [expire]);
  useEffect(() => {
    void check();
    const timer = setInterval(
      () => {
        if (document.visibilityState !== "hidden") void check();
      },
      sessionRefreshError ? 5_000 : 60_000,
    );
    const focus = () => void check();
    window.addEventListener("focus", focus);
    window.addEventListener("haproxy-session-expired", expire);
    return () => {
      clearInterval(timer);
      window.removeEventListener("focus", focus);
      window.removeEventListener("haproxy-session-expired", expire);
    };
  }, [check, expire, sessionRefreshError]);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    saveTheme(theme);
  }, [theme]);
  async function signOut() {
    if (!session) return;
    setSigningOut(true);
    try {
      await localRequest("session", { method: "DELETE" }, session);
      expire();
      setError(null);
    } catch (e) {
      setSessionRefreshError(false);
      setError(message(e));
    } finally {
      setSigningOut(false);
    }
  }
  if (loading)
    return (
      <main className="hm-loading">
        <LoadingStatus delayMs={1000}>
          Connecting to Ecraela Fleet
        </LoadingStatus>
      </main>
    );
  if (!session)
    return (
      <Login
        theme={theme}
        onThemeChange={setTheme}
        error={error}
        notice={notice}
        onLogin={(s) => {
          authEpoch.current++;
          setSession(s);
          setSessionRefreshError(false);
          setNotice(null);
          setError(null);
        }}
      />
    );
  return (
    <div className="hm-shell">
      {sessionRefreshError && (
        <ConnectionIssue
          connection
          message="Session verification is temporarily unavailable. Retrying automatically."
        />
      )}
      <header className="hm-header">
        <div className="hm-header__identity">
          <FleetBrand />
          <span className="hm-connection">
            <TooltipText
              placement="bottom"
              content={
                connectionHealth.messages.length ? (
                  <div>
                    <strong>
                      {connectionHealth.disconnected
                        ? "Manager connection unavailable"
                        : "Reconnecting"}
                    </strong>
                    <p>
                      Last loaded data stays visible. Updates resume
                      automatically.
                    </p>
                    {connectionHealth.messages.slice(0, 4).map((message) => (
                      <p key={message}>{message}</p>
                    ))}
                  </div>
                ) : (
                  {
                    connecting: "Connecting to the Fleet manager…",
                    connected:
                      "Connected to the Fleet manager. Inventory refreshed successfully.",
                    degraded:
                      "Some manager observations are incomplete. Updates continue automatically.",
                    disconnected: "Cannot connect to the Fleet manager.",
                    restricted:
                      "Your account does not have permission to read manager inventory.",
                  }[displayedConnection]
                )
              }
            >
              <StatusDot
                tone={
                  (
                    {
                      connecting: "neutral",
                      connected: "success",
                      degraded: "warning",
                      disconnected: "error",
                      restricted: "neutral",
                    } as const
                  )[displayedConnection]
                }
              />
              <span className="cv-sr-only">
                Manager connection: {displayedConnection}
              </span>
            </TooltipText>
          </span>
        </div>
        <div className="hm-account">
          <AccountMenu
            user={{ name: session.username }}
            placement="bottom end"
            isPending={signingOut}
          >
            <MenuItem href="/settings">Settings</MenuItem>
            <MenuSeparator />
            <ThemeMenuSection
              value={theme}
              onChange={(value) => {
                if (value === "dark" || value === "light") setTheme(value);
              }}
            />
            <MenuSeparator />
            <MenuItem isDisabled={signingOut} onAction={() => void signOut()}>
              {signingOut ? "Signing out…" : "Sign out"}
            </MenuItem>
          </AccountMenu>
        </div>
      </header>
      <main className="hm-content">
        {error && !sessionRefreshError && (
          <InlineTip variant="error" role="alert">
            {error}
          </InlineTip>
        )}
        <HAProxyView
          onConnectionStatus={setManagerConnection}
          key={session.user_id}
          settingsContent={
            <Section
              layout="stack"
              title="Local access"
              description="Your installation uses its own local accounts. A Converge connection is optional."
            >
              <div className="hm-access-actions">
                <Button onPress={() => setPasswordOpen(true)}>
                  Change password
                </Button>
                <ConvergeConnector session={session} />
              </div>
            </Section>
          }
          organization={{
            id: session.organization_id,
            capabilities: session.capabilities,
          }}
        />
      </main>
      <footer className="hm-legal">
        <nav aria-label="Installation links" className="hm-legal__links">
          <Link variant="plain" href="/settings">
            Settings
          </Link>
          <a className="cv-link cv-link--plain" href="/legal/">
            License &amp; source
          </a>
          <Link
            variant="plain"
            href="https://github.com/ecraela/fleet"
            isExternal
          >
            <GitHub /> GitHub
          </Link>
        </nav>
        <span className="hm-legal__version" aria-label="Release version">
          {(() => {
            const version = document.querySelector<HTMLMetaElement>(
              'meta[name="fleet-version"]',
            )?.content;
            return !version || version.startsWith("__") ? "dev" : version;
          })()}
        </span>
      </footer>
      {passwordOpen && (
        <PasswordDrawer
          session={session}
          onClose={() => setPasswordOpen(false)}
          onChanged={() => {
            expire();
            setNotice(
              "Password updated. Sign in again with your new password. All previous sessions have been signed out.",
            );
          }}
        />
      )}
    </div>
  );
}
function Login({
  theme,
  onThemeChange,
  error,
  notice,
  onLogin,
}: {
  theme: Theme;
  onThemeChange: (theme: Theme) => void;
  error: string | null;
  notice: string | null;
  onLogin: (s: LocalSession) => void;
}) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  async function submit() {
    if (busy) return;
    setFailure(null);
    if (!username.trim() || !password) {
      setFailure("Enter your username and password.");
      return;
    }
    setBusy(true);
    try {
      onLogin(
        await localRequest<LocalSession>("session", {
          method: "POST",
          body: JSON.stringify({ username, password }),
        }),
      );
      setPassword("");
    } catch (e) {
      setFailure(message(e));
      setPassword("");
    } finally {
      setBusy(false);
    }
  }
  return (
    <Takeover
      variant="card"
      brand={<FleetBrand />}
      headerActions={
        <IconButton
          variant="ghost"
          aria-label={`Switch to ${theme === "light" ? "dark" : "light"} theme`}
          onPress={() => onThemeChange(theme === "light" ? "dark" : "light")}
        >
          {theme === "light" ? <Moon /> : <Sun />}
        </IconButton>
      }
      icon={<img src={ecraelaMark} alt="" width={38} height={38} />}
      title="Welcome to Fleet"
      description="Manage your HAProxy® load balancers with your local account."
    >
      <form
        className="hm-login-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {notice && (
          <InlineTip variant="success" role="status">
            {notice}
          </InlineTip>
        )}
        {(failure ?? error) && (
          <InlineTip variant="error" role="alert">
            {failure ?? error}
          </InlineTip>
        )}
        <TextField
          label="Username"
          name="username"
          autoComplete="username"
          value={username}
          onChange={setUsername}
          isDisabled={busy}
          isRequired
          errorDisplay="inline"
          errorMessage="Enter your username."
        />
        <TextField
          label="Password"
          name="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={setPassword}
          isDisabled={busy}
          isRequired
          errorDisplay="inline"
          errorMessage="Enter your password."
        />
        <Button variant="primary" type="submit" isLoading={busy}>
          Sign in
        </Button>
        <p className="hm-login-help">
          Your installation administrator creates local accounts. If you need
          access or a password reset, contact them.
        </p>
      </form>
      <p className="hm-login-legal">
        <a className="cv-link cv-link--plain" href="/legal/">
          License and source
        </a>
      </p>
    </Takeover>
  );
}
function PasswordDrawer({
  session,
  onClose,
  onChanged,
}: {
  session: LocalSession;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [validatedFields, setValidatedFields] = useState<Set<string>>(
    new Set(),
  );
  const passwordForm = useRef<HTMLFormElement>(null);
  const validation = passwordChangeErrors(current, next, confirm);
  const fields = {
    current: validatedFields.has("current") ? validation.current : undefined,
    next: validatedFields.has("next") ? validation.next : undefined,
    confirm: validatedFields.has("confirm") ? validation.confirm : undefined,
  };
  async function submit() {
    if (busy) return;
    setError(null);
    if (
      Object.values(passwordChangeErrors(current, next, confirm)).some(Boolean)
    ) {
      setValidatedFields(
        (previous) =>
          new Set([
            ...previous,
            ...Object.entries(validation)
              .filter(([, error]) => error)
              .map(([name]) => name),
          ]),
      );
      focusFormError(passwordForm.current);
      return;
    }
    setBusy(true);
    try {
      await localRequest(
        "password",
        {
          method: "POST",
          body: JSON.stringify({
            current_password: current,
            new_password: next,
          }),
        },
        session,
      );
      onChanged();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Drawer
      isOpen
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DrawerDialog title="Change password">
        <DialogForm ref={passwordForm} onSubmit={() => void submit()}>
          <DrawerBody>
            {error && (
              <InlineTip variant="error" role="alert">
                {error}
              </InlineTip>
            )}
            <TextField
              label="Current password"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={setCurrent}
              isRequired
              isDisabled={busy}
              errorDisplay="inline"
              isInvalid={!!fields.current}
              errorMessage={fields.current}
            />
            <TextField
              label="New password"
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={setNext}
              description={
                fields.next
                  ? undefined
                  : "Use at least 15 characters. Spaces and password managers are supported."
              }
              isRequired
              isDisabled={busy}
              errorDisplay="inline"
              isInvalid={!!fields.next}
              errorMessage={fields.next}
            />
            <TextField
              label="Confirm new password"
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={setConfirm}
              isRequired
              isDisabled={busy}
              errorDisplay="inline"
              isInvalid={!!fields.confirm}
              errorMessage={fields.confirm}
            />
            <InlineTip variant="warning" title="Saving signs out every session">
              Changing your password signs out every session for this account,
              including this one.
            </InlineTip>
          </DrawerBody>
          <DrawerFooter>
            <Button variant="primary" type="submit" isLoading={busy}>
              Update password
            </Button>
            <Button isDisabled={busy} onPress={onClose}>
              Cancel
            </Button>
          </DrawerFooter>
        </DialogForm>
      </DrawerDialog>
    </Drawer>
  );
}
