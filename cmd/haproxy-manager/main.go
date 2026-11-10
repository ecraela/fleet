package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/ecraela/fleet/internal/buildinfo"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/pki"
	"github.com/ecraela/fleet/internal/server"
	"github.com/ecraela/fleet/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("haproxy-manager stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: haproxy-manager init|migrate|serve [options]")
	}
	switch args[0] {
	case "version":
		return json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
	case "user":
		return localUser(args[1:])
	case "init":
		return initialize(args[1:])
	case "migrate":
		return migrate(args[1:])
	case "serve":
		return serve(args[1:])
	default:
		return errors.New("usage: haproxy-manager init|migrate|serve [options]")
	}
}

type names []string

func (n *names) String() string { return strings.Join(*n, ",") }
func (n *names) Set(value string) error {
	if value == "" {
		return errors.New("empty value")
	}
	*n = append(*n, value)
	return nil
}

func initialize(args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := flags.String("state-dir", "", "new installation state directory")
	var dns, ipText names
	flags.Var(&dns, "dns-name", "server DNS name (repeatable)")
	flags.Var(&ipText, "ip", "server IP address (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" || flags.NArg() != 0 {
		return errors.New("init requires --state-dir")
	}
	if len(dns) == 0 && len(ipText) == 0 {
		dns = names{"localhost"}
		ipText = names{"127.0.0.1"}
	}
	var ips []net.IP
	for _, text := range ipText {
		ip := net.ParseIP(text)
		if ip == nil {
			return errors.New("invalid --ip")
		}
		ips = append(ips, ip)
	}
	if err := os.MkdirAll(*dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(*dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("state directory must be a private directory with mode 0700")
	}
	for _, name := range []string{"pki", "admin-token", "organization-id"} {
		if _, err := os.Lstat(filepath.Join(*dir, name)); !os.IsNotExist(err) {
			return errors.New("installation state already exists; refusing to overwrite")
		}
	}
	if err := pki.Init(filepath.Join(*dir, "pki"), dns, ips); err != nil {
		return err
	}
	var token [32]byte
	_, _ = rand.Read(token[:])
	if err := writeSecret(filepath.Join(*dir, "admin-token"), base64.RawURLEncoding.EncodeToString(token[:])+"\n"); err != nil {
		return err
	}
	if err := writeSecret(filepath.Join(*dir, "organization-id"), domain.NewID()+"\n"); err != nil {
		return err
	}
	fmt.Println("Created installation PKI, tenant identity, and operator credential files in", *dir)
	return nil
}

func writeSecret(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(value); err != nil {
		return err
	}
	return f.Sync()
}

func migrate(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dsn := flags.String("database-url", "", "dedicated PostgreSQL URL (or HAPROXY_DATABASE_URL)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		*dsn = os.Getenv("HAPROXY_DATABASE_URL")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("HAPROXY_DATABASE_URL or --database-url is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.NewPostgres(ctx, *dsn)
	if err != nil {
		return errors.New("cannot connect to dedicated PostgreSQL database")
	}
	defer db.Close()
	return db.Migrate(ctx)
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	role := flags.String("role", "manager", "manager, ingest or relay")
	listen := flags.String("listen", "127.0.0.1:8443", "HTTPS listen address")
	dsn := flags.String("database-url", "", "dedicated PostgreSQL URL (or HAPROXY_DATABASE_URL)")
	pkiDir := flags.String("pki-dir", "", "installation PKI directory")
	tlsCert := flags.String("tls-cert", "", "HTTPS server certificate (default PKI/server.crt)")
	tlsKey := flags.String("tls-key", "", "HTTPS server private key (default PKI/server.key)")
	adminToken := flags.String("admin-token-file", "", "tenant operator secret file")
	org := flags.String("organization-id", os.Getenv("HAPROXY_ORGANIZATION_ID"), "organization bound to --admin-token-file")
	credentials := flags.String("operator-credentials-file", "", "JSON array of tenant-bound SHA-256 operator credentials")
	metrics := flags.String("metrics-url", "http://127.0.0.1:8428", "private VictoriaMetrics base URL")
	logs := flags.String("logs-url", os.Getenv("HAPROXY_LOGS_URL"), "optional private VictoriaLogs origin; request telemetry disabled without it")
	applyMigrations := flags.Bool("migrate", false, "explicitly apply schema migrations before serving")
	browserSecretFile := flags.String("browser-login-secret-file", os.Getenv("HAPROXY_LOGIN_SECRET_FILE"), "private file containing dedicated base64 browser handoff secret (or HAPROXY_LOGIN_SECRET)")
	browserOrigin := flags.String("browser-origin", os.Getenv("HAPROXY_BROWSER_ORIGIN"), "exact allowed browser origin; browser login is disabled without secret and origin")
	browserAudience := flags.String("browser-audience", os.Getenv("HAPROXY_LOGIN_AUDIENCE"), "browser handoff audience (default haproxy-manager)")
	browserCookiePath := flags.String("browser-cookie-path", "/api/haproxy", "host-only browser session cookie path")
	browserInsecure := flags.Bool("browser-insecure-cookies", false, "explicitly allow insecure cookies for a loopback development origin")
	localHTTP := flags.String("local-http-listen", "", "optional development HTTP console listener; requires explicit local insecure cookies and loopback origin")
	localOrigin := flags.String("local-origin", os.Getenv("HAPROXY_LOCAL_ORIGIN"), "enable standalone console and local login at this exact HTTPS origin")
	localInsecure := flags.Bool("local-insecure-cookies", false, "explicitly allow local login cookies on a loopback development origin")
	relayURL := flags.String("connector-relay-url", os.Getenv("HAPROXY_CONNECTOR_RELAY_URL"), "optional fixed HTTPS outbound Converge relay origin")
	connectorDir := flags.String("connector-state-dir", os.Getenv("HAPROXY_CONNECTOR_STATE_DIR"), "private persistent connector identity directory")
	connectorCA := flags.String("connector-ca-file", os.Getenv("HAPROXY_CONNECTOR_CA_FILE"), "optional private relay CA; system roots by default")
	webhooksFile := flags.String("incident-webhooks-file", os.Getenv("HAPROXY_INCIDENT_WEBHOOKS_FILE"), "optional private installation configuration for tenant incident webhooks")
	releaseCatalogFile := flags.String("release-catalog-file", os.Getenv("HAPROXY_RELEASE_CATALOG_FILE"), "host-verified, read-only signed release catalog")
	softwareHostTokenFile := flags.String("software-host-token-file", os.Getenv("HAPROXY_SOFTWARE_HOST_TOKEN_FILE"), "separate host-only updater credential file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		*dsn = os.Getenv("HAPROXY_DATABASE_URL")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("HAPROXY_DATABASE_URL or --database-url is required")
	}
	if flags.NArg() != 0 || *pkiDir == "" {
		return errors.New("serve requires --pki-dir")
	}
	if *tlsCert == "" {
		*tlsCert = filepath.Join(*pkiDir, "server.crt")
	}
	if *tlsKey == "" {
		*tlsKey = filepath.Join(*pkiDir, "server.key")
	}
	if *localHTTP != "" && (*role != "manager" || *localOrigin == "" || !*localInsecure) {
		return errors.New("development HTTP console requires manager, local origin and explicit local insecure cookies")
	}
	authority, err := pki.Load(*pkiDir, *role == "manager")
	if err != nil {
		return fmt.Errorf("load installation PKI: %w", err)
	}
	var operators []server.OperatorCredential
	var browser server.BrowserConfig
	if *role == "manager" || *role == "relay" {
		shared, secretErr := loadBrowserSecret(*browserSecretFile, os.Getenv("HAPROXY_LOGIN_SECRET"))
		if secretErr != nil {
			return secretErr
		}
		browser = server.BrowserConfig{Secret: shared, Origin: *browserOrigin, Audience: *browserAudience, CookiePath: *browserCookiePath, InsecureCookies: *browserInsecure}
		if *role == "manager" {
			operators, err = server.LoadOperators(*adminToken, *org, *credentials)
		}
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	db, err := store.NewPostgres(startup, *dsn)
	if err == nil && *applyMigrations {
		err = db.Migrate(startup)
	}
	cancel()
	if err != nil {
		if db != nil {
			db.Close()
		}
		return errors.New("cannot initialize dedicated PostgreSQL database")
	}
	defer db.Close()
	setup := server.AgentSetup{ReleaseVersion: os.Getenv("HAPROXY_AGENT_RELEASE_VERSION"), ReleaseURL: os.Getenv("HAPROXY_AGENT_RELEASE_URL"), Image: os.Getenv("HAPROXY_AGENT_IMAGE"), ManagerURL: os.Getenv("HAPROXY_AGENT_MANAGER_URL"), IngestURL: os.Getenv("HAPROXY_AGENT_INGEST_URL"), Network: os.Getenv("HAPROXY_AGENT_NETWORK"), MetricsURL: os.Getenv("HAPROXY_AGENT_METRICS_URL")}
	var notifications []server.NotificationTarget
	if *role == "manager" {
		notifications, err = server.LoadNotificationTargets(*webhooksFile)
		if err != nil {
			return err
		}
	}
	var softwareHostTokenDigest string
	var softwareHostOrganization string
	if *role == "manager" && *softwareHostTokenFile != "" {
		info, statErr := os.Lstat(*softwareHostTokenFile)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 512 {
			return errors.New("software host token file must be a bounded non-writable regular file")
		}
		data, readErr := os.ReadFile(*softwareHostTokenFile)
		if readErr != nil || len(strings.TrimSpace(string(data))) < 32 || len(strings.TrimSpace(string(data))) > 256 {
			return errors.New("invalid software host updater credential")
		}
		sum := sha256.Sum256([]byte(strings.TrimSpace(string(data))))
		softwareHostTokenDigest = hex.EncodeToString(sum[:])
		softwareHostOrganization = *org
	}
	app, err := server.New(server.Config{Role: *role, Store: db, Authority: authority, Operators: operators, MetricsURL: *metrics, LogsURL: *logs, Browser: browser, Local: server.LocalConfig{Origin: *localOrigin, InsecureCookies: *localInsecure}, AgentSetup: setup, ReleaseCatalogFile: *releaseCatalogFile, InstallationOrganizationID: softwareHostOrganization, SoftwareHostTokenSHA256: softwareHostTokenDigest, Notifications: notifications, Connector: server.ConnectorConfig{RelayURL: *relayURL, StateDir: *connectorDir, CAFile: *connectorCA}})
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: *listen, Handler: app, TLSConfig: app.TLSConfig(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	if *role == "manager" {
		go app.RunConnector(ctx)
		go app.RunRollouts(ctx)
		go app.RunACME(ctx)
		go app.RunMaintenance(ctx)
		go app.RunIncidents(ctx)
		go app.RunNotifications(ctx)
	}
	done := make(chan error, 2)
	go func() { done <- httpServer.ListenAndServeTLS(*tlsCert, *tlsKey) }()
	var localServer *http.Server
	if *localHTTP != "" {
		localServer = &http.Server{Addr: *localHTTP, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Development HTTP exposes the browser surface only, never node/control transport.
			if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/converge.") {
				http.NotFound(w, r)
				return
			}
			app.ServeHTTP(w, r)
		}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
		defer localServer.Close()
		go func() { done <- localServer.ListenAndServe() }()
	}
	slog.Info("HAProxy service starting", "role", *role, "address", *listen)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if localServer != nil {
			_ = localServer.Shutdown(shutdown)
		}
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
			return err
		}
		return nil
	}
}

func loadBrowserSecret(file, environment string) ([]byte, error) {
	if file != "" && environment != "" {
		return nil, errors.New("configure exactly one dedicated browser secret source")
	}
	encoded := strings.TrimSpace(environment)
	if file != "" {
		f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, errors.New("cannot open browser login secret file")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
			return nil, errors.New("browser login secret file must be private, regular and bounded")
		}
		body, err := io.ReadAll(io.LimitReader(f, 4097))
		if err != nil || len(body) > 4096 {
			return nil, errors.New("cannot read browser login secret")
		}
		encoded = strings.TrimSpace(string(body))
	}
	if encoded == "" {
		if file != "" {
			return nil, errors.New("browser login secret file is empty")
		}
		return nil, nil
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) < 32 || len(decoded) > 128 {
		return nil, errors.New("browser login secret must be base64 encoding 32 to 128 random bytes")
	}
	return decoded, nil
}
