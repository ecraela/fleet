package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func TestIncidentWebhookIsOptInSignedAndDoesNotFollowRedirects(t *testing.T) {
	f := browserFixture(t)
	secret := bytes.Repeat([]byte{43}, 32)
	requests := 0
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		if r.Header.Get("X-Converge-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("bad signature")
		}
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing deduplication key")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credentials forwarded")
		}
		w.WriteHeader(204)
	}))
	defer destination.Close()
	f.server.notificationClient = destination.Client()
	target := NotificationTarget{ID: domain.NewID(), OrganizationID: f.org, URL: destination.URL, Secret: secret}
	e := domain.IncidentDelivery{DestinationID: target.ID, Attempts: 1, Event: domain.IncidentEvent{Sequence: 123, Kind: "opened"}}
	if f.server.notificationConfigured(f.org) {
		t.Fatal("delivery enabled without configuration")
	}
	if ok, _ := f.server.deliverIncident(t.Context(), target, e); !ok || requests != 1 {
		t.Fatal("delivery failed")
	}
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer redirect.Close()
	f.server.notificationClient = redirect.Client()
	f.server.notificationClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	target.URL = redirect.URL
	if ok, _ := f.server.deliverIncident(t.Context(), target, e); ok || requests != 1 {
		t.Fatal("redirect leaked delivery")
	}
}
func TestIncidentWebhookConfigurationIsPrivateBoundedAndTenantBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhooks.json")
	value := map[string]string{"id": domain.NewID(), "organization_id": domain.NewID(), "url": "https://alerts.example.test/incidents", "secret": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}
	write := func(mode os.FileMode) {
		raw, _ := json.Marshal([]any{value})
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(0600)
	targets, err := LoadNotificationTargets(path)
	if err != nil || len(targets) != 1 {
		t.Fatal(err)
	}
	if err := validateNotificationTargets(append(targets, targets[0])); err == nil {
		t.Fatal("duplicate org allowed")
	}
	write(0644)
	if _, err := LoadNotificationTargets(path); err == nil {
		t.Fatal("public config accepted")
	}
	write(0600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNotificationTargets(link); err == nil {
		t.Fatal("symlink accepted")
	}
	for _, u := range []string{"http://alerts.example.test", "https://user:secret@alerts.example.test", "https://alerts.example.test?token=secret", "https://alerts.example.test#fragment"} {
		value["url"] = u
		write(0600)
		if _, err := LoadNotificationTargets(path); err == nil {
			t.Fatal("unsafe target accepted", u)
		}
	}
	// Retry-After is respected and bounded without logging the receiver body.
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "120"); w.WriteHeader(429) }))
	defer receiver.Close()
	f := browserFixture(t)
	f.server.notificationClient = receiver.Client()
	target := targets[0]
	target.URL = receiver.URL
	ok, retry := f.server.deliverIncident(t.Context(), target, domain.IncidentDelivery{Attempts: 1})
	if ok || retry != 2*time.Minute {
		t.Fatal(ok, retry)
	}
}
