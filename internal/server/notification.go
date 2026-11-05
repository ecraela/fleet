package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

// Destinations are installation configuration, not browser-supplied URLs.
// Neither the URL nor the signing secret is exposed in incident API responses.
type NotificationTarget struct {
	ID             string
	OrganizationID string
	URL            string
	Secret         []byte
}

func (t NotificationTarget) digest() string {
	sum := sha256.Sum256([]byte(t.OrganizationID + "\n" + t.URL))
	return hex.EncodeToString(sum[:])
}
func validateNotificationTargets(targets []NotificationTarget) error {
	if len(targets) > 32 {
		return errors.New("at most 32 incident webhook destinations are supported")
	}
	ids, orgs := map[string]bool{}, map[string]bool{}
	for _, t := range targets {
		u, err := url.Parse(t.URL)
		if !domain.ValidID(t.ID) || !domain.ValidID(t.OrganizationID) || ids[t.ID] || orgs[t.OrganizationID] || len(t.Secret) < 32 || len(t.Secret) > 128 || len(t.URL) > 2048 || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid incident webhook configuration")
		}
		ids[t.ID] = true
		orgs[t.OrganizationID] = true
	}
	return nil
}
func LoadNotificationTargets(path string) ([]NotificationTarget, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot read incident webhook configuration")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return nil, errors.New("incident webhook configuration must be a private bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("cannot read incident webhook configuration")
	}
	var values []struct {
		ID             string `json:"id"`
		OrganizationID string `json:"organization_id"`
		URL            string `json:"url"`
		Secret         string `json:"secret"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&values) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid incident webhook configuration")
	}
	out := []NotificationTarget{}
	for _, v := range values {
		secret, err := base64.StdEncoding.Strict().DecodeString(v.Secret)
		if err != nil {
			return nil, errors.New("incident webhook secret must encode 32 to 128 random bytes")
		}
		out = append(out, NotificationTarget{ID: v.ID, OrganizationID: v.OrganizationID, URL: v.URL, Secret: secret})
	}
	if err := validateNotificationTargets(out); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Server) notificationConfigured(org string) bool {
	for _, t := range s.config.Notifications {
		if t.OrganizationID == org {
			return true
		}
	}
	return false
}
func (s *Server) deliverIncident(ctx context.Context, target NotificationTarget, delivery domain.IncidentDelivery) (bool, time.Duration) {
	// A receiver deduplicates the stable destination/sequence key. Delivery is
	// at least once: an HTTP acknowledgement can be lost before its DB checkpoint.
	body, _ := json.Marshal(struct {
		Version int                  `json:"version"`
		Event   domain.IncidentEvent `json:"event"`
	}{1, delivery.Event})
	request, err := http.NewRequestWithContext(ctx, "POST", target.URL, bytes.NewReader(body))
	if err != nil {
		return false, 5 * time.Minute
	}
	mac := hmac.New(sha256.New, target.Secret)
	mac.Write(body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Converge-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("Idempotency-Key", target.ID+":"+strconv.FormatInt(delivery.Event.Sequence, 10))
	request.Header.Set("User-Agent", "Converge-HAProxy-Incidents/1")
	response, err := s.notificationClient.Do(request)
	retry := min(5*time.Minute, 5*time.Second*time.Duration(1<<min(delivery.Attempts-1, 6)))
	if err != nil {
		return false, retry
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 4096)
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return true, 0
	}
	if response.StatusCode == 429 || response.StatusCode == 503 {
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
			retry = max(retry, min(time.Hour, time.Duration(seconds)*time.Second))
		} else if at, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
			retry = max(retry, min(time.Hour, time.Until(at)))
		}
	}
	return false, retry
}
func (s *Server) RunNotifications(ctx context.Context) {
	db, ok := s.config.Store.(store.NotificationStore)
	if !ok || s.config.Role != "manager" || len(s.config.Notifications) == 0 {
		return
	}
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		for _, target := range s.config.Notifications {
			step, cancel := context.WithTimeout(ctx, 5*time.Second)
			d, err := db.ClaimIncidentNotification(step, target.OrganizationID, target.ID, target.digest(), time.Now().UTC())
			if err == nil && d != nil {
				success, retry := s.deliverIncident(step, target, *d)
				err = db.FinishIncidentNotification(step, target.OrganizationID, *d, success, time.Now().UTC(), retry)
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Warn("HAProxy incident delivery checkpoint failed")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func newNotificationClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
