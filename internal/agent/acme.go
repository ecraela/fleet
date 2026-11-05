package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func acmeJob(ctx context.Context, client *http.Client, server string) (*domain.ACMEJob, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/v1/identity/acme", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("issuance authority unavailable")
	}
	var payload struct {
		Job *domain.ACMEJob `json:"job"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &payload) != nil {
		return nil, errors.New("invalid issuance authority")
	}
	if payload.Job != nil && !payload.Job.Valid() {
		return nil, errors.New("invalid issuance job")
	}
	return payload.Job, nil
}
func acmeReport(ctx context.Context, client *http.Client, server string, result domain.ACMEResult) error {
	raw, _ := json.Marshal(result)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/v1/identity/acme/"+result.JobID+"/result", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != 200 {
		return errors.New("issuance result unavailable")
	}
	return nil
}

// A separate bounded worker keeps slow CA/DNS requests off the live control
// stream. Authority is rechecked while issuing and expires if contact is lost.
func acmeLoop(ctx context.Context, c Config, trust *x509.CertPool) {
	var lastCleanup time.Time
	for ctx.Err() == nil {
		state, err := readState(filepath.Join(c.StateDir, "identity.json"))
		if err != nil {
			return
		}
		client, _, err := identityClient(state, trust)
		if err != nil {
			return
		}
		poll, stop := context.WithTimeout(ctx, 4*time.Second)
		job, err := acmeJob(poll, client, c.ServerURL)
		stop()
		if err == nil && job != nil && job.NodeID == state.Node.ID {
			run, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			monitorDone := make(chan struct{})
			go func() {
				defer close(monitorDone)
				last := time.Now()
				ticker := time.NewTicker(3 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-done:
						return
					case <-run.Done():
						return
					case <-ticker.C:
						check, stop := context.WithTimeout(run, 3*time.Second)
						current, e := acmeJob(check, client, c.ServerURL)
						stop()
						if e == nil {
							last = time.Now()
							if current == nil || current.ID != job.ID || current.PolicyDigest != job.PolicyDigest {
								cancel()
								return
							}
						} else if time.Since(last) > 15*time.Second {
							cancel()
							return
						}
					}
				}
			}()
			result := c.issuer.Issue(run, *job)
			close(done)
			cancel()
			<-monitorDone
			report, stop := context.WithTimeout(ctx, 4*time.Second)
			_ = acmeReport(report, client, c.ServerURL, result)
			stop()
		}
		if err == nil && job == nil && time.Since(lastCleanup) > 30*time.Second {
			cleanup, stop := context.WithTimeout(ctx, 8*time.Second)
			c.issuer.Cleanup(cleanup)
			stop()
			lastCleanup = time.Now()
		}
		client.CloseIdleConnections()
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
