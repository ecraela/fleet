package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
)

// A separate connection, bounded datagram queue and sender keep log pressure
// out of metrics/control. Delivery is best effort; ambiguous writes are not
// retried and unconfirmed request counts are exposed in collection status.
func accessLoop(ctx context.Context, cfg Config, trust *x509.CertPool, receiver *accesslog.Receiver) {
	readDone := make(chan struct{})
	go func() { defer close(readDone); receiver.Run(ctx) }()
	defer func() { receiver.Close(); <-readDone }()
	sendAccessLoop(ctx, cfg, trust, receiver)
}

type accessSource interface {
	Drain() []accesslog.Event
	Snapshot() accesslog.Pipeline
	Unconfirmed(int)
}

func sendAccessLoop(ctx context.Context, cfg Config, trust *x509.CertPool, receiver accessSource) {
	u, _ := url.Parse(cfg.IngestURL)
	u.Path = "/v1/logs/write"
	// The original 256-record batch every 250 ms capped collection at 1,024
	// requests/s per node, below ordinary pilot traffic. Keep the faster
	// bounded drain below the ingest budget of 100 uploads per ten seconds;
	// a 100 ms tick can exceed that fixed window at its boundary.
	ticker := time.NewTicker(125 * time.Millisecond)
	defer ticker.Stop()
	var client *http.Client
	var certificate string
	defer func() {
		if client != nil {
			client.CloseIdleConnections()
		}
	}()
	var checked, reported time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if time.Since(checked) >= 30*time.Second || client == nil {
			checked = time.Now()
			if state, err := readState(filepath.Join(cfg.StateDir, "identity.json")); err == nil && state.CertificatePEM != certificate {
				if next, _, err := identityClient(state, trust); err == nil {
					if client != nil {
						client.CloseIdleConnections()
					}
					client, certificate = next, state.CertificatePEM
				}
			}
		}
		events := receiver.Drain()
		if len(events) == 0 && time.Since(reported) < 5*time.Second {
			continue
		}
		reported = time.Now()
		payload, _ := json.Marshal(accesslog.Batch{Events: events, Pipeline: receiver.Snapshot()})
		ok := false
		if client != nil {
			batchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			req, _ := http.NewRequestWithContext(batchCtx, "POST", u.String(), bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			if resp, err := client.Do(req); err == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				ok = resp.StatusCode == 204
			}
			cancel()
		}
		if !ok {
			receiver.Unconfirmed(len(events))
		}
	}
}
