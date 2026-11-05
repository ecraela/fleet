package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/agent"
	"github.com/ecraela/fleet/internal/buildinfo"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		_ = json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
		return
	}
	var c agent.Config
	flag.StringVar(&c.ConfigurationOwner, "configuration-owner", "", "local configuration owner: manager or external; never inferred from discovery")
	flag.StringVar(&c.AccessLogSocket, "access-log-socket", "", "optional local Unix datagram socket for private request logs")
	flag.StringVar(&c.RequestPathPolicyFile, "request-path-policy", "", "optional absolute local path policy; paths are withheld by default")
	flag.StringVar(&c.RequestGeoPolicyFile, "request-geo-policy", os.Getenv("HAPROXY_REQUEST_GEO_POLICY"), "optional absolute local geography policy; client addresses never leave the node")
	flag.StringVar(&c.TrafficPolicyFile, "traffic-rules-policy", os.Getenv("HAPROXY_TRAFFIC_RULES_POLICY"), "host-owned client-IP policy and shared map directory grant for application traffic rules")
	flag.StringVar(&c.ServerURL, "server-url", "", "HTTPS enrollment and identity server origin")
	flag.StringVar(&c.IngestURL, "ingest-url", "", "HTTPS ingestion origin or /v1/metrics/write URL")
	flag.StringVar(&c.ServerCA, "server-ca", "", "additional server trust CA PEM file")
	flag.StringVar(&c.EnrollmentTokenFile, "enrollment-token-file", "", "private bootstrap token file; used only on first run")
	flag.StringVar(&c.StateDir, "state-dir", "/var/lib/converge-haproxy-agent", "private persistent identity directory")
	flag.StringVar(&c.Name, "name", "", "node display name")
	flag.StringVar(&c.MetricsURL, "metrics-url", "http://127.0.0.1:8405/metrics", "local HAProxy Prometheus endpoint")
	flag.DurationVar(&c.Interval, "interval", time.Second, "scrape interval, minimum 1s")
	flag.StringVar(&c.RuntimeSocket, "runtime-socket", "", "local HAProxy Unix Runtime API socket for discovery")
	flag.Func("config-file", "HAProxy configuration file to fingerprint (repeat for each dependency, up to 16)", func(value string) error { c.ConfigFiles = append(c.ConfigFiles, value); return nil })
	flag.StringVar(&c.DataplaneSocket, "dataplane-socket", "", "local Data Plane API Unix socket for persistent configuration")
	flag.StringVar(&c.DataplaneCredentialsFile, "dataplane-credentials-file", "", "private JSON file with local Data Plane API username and password")
	flag.StringVar(&c.TLSPolicyFile, "tls-policy-file", "", "local JSON grants for existing HTTPS listener bindings and certificate references")
	flag.StringVar(&c.ProbePolicyFile, "probe-policy-file", "", "local JSON application checks through explicitly configured entrypoints")
	flag.StringVar(&c.ACMEPolicyFile, "acme-policy-file", os.Getenv("HAPROXY_ACME_POLICY_FILE"), "node-local ACME DNS-01 policy; disabled by default")
	flag.BoolVar(&c.AllowConfigurationControl, "allow-configuration-control", false, "allow permission-authorized persistent backend configuration edits and rollback")
	flag.BoolVar(&c.AllowRuntimeControl, "allow-runtime-control", false, "allow permission-authorized backend-server runtime operations")
	preflight := len(os.Args) > 1 && os.Args[1] == "preflight"
	checkGeo := len(os.Args) > 1 && os.Args[1] == "check-geo"
	arguments := os.Args[1:]
	if preflight || checkGeo {
		arguments = os.Args[2:]
	}
	if err := flag.CommandLine.Parse(arguments); err != nil {
		os.Exit(2)
	}
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "haproxy-agent: unexpected positional arguments")
		os.Exit(2)
	}
	if checkGeo {
		if c.RequestGeoPolicyFile == "" {
			fmt.Fprintln(os.Stderr, "haproxy-agent: check-geo requires --request-geo-policy")
			os.Exit(1)
		}
		resolver, err := accesslog.LoadGeoPolicy(c.RequestGeoPolicyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "haproxy-agent:", err)
			os.Exit(1)
		}
		defer resolver.Close()
		report := resolver.Snapshot()
		_ = json.NewEncoder(os.Stdout).Encode(report)
		if report.Status != "ready" {
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if preflight {
		ctx, stop := context.WithTimeout(ctx, 12*time.Second)
		defer stop()
		report, err := agent.Preflight(ctx, c)
		if err != nil {
			fmt.Fprintln(os.Stderr, "haproxy-agent:", err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
		if !agent.PreflightOK(report) {
			os.Exit(1)
		}
		return
	}
	a := agent.Agent{Config: c}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "haproxy-agent:", err)
		os.Exit(1)
	}
}
