package acmeissuer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/acme"
)

func TestIssuerRecoversMissingAccountWithRetainedKey(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var base string
	lookups, registrations := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", base64.RawURLEncoding.EncodeToString([]byte("nonce-for-account-recovery")))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/dir":
			_ = json.NewEncoder(w).Encode(map[string]string{"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/order"})
		case "/nonce":
			w.WriteHeader(200)
		case "/account":
			var envelope struct{ Payload string }
			if json.NewDecoder(r.Body).Decode(&envelope) != nil {
				t.Error("invalid signed request")
				return
			}
			raw, _ := base64.RawURLEncoding.DecodeString(envelope.Payload)
			var request struct{ OnlyReturnExisting bool }
			_ = json.Unmarshal(raw, &request)
			if request.OnlyReturnExisting {
				lookups++
				if registrations == 0 {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "urn:ietf:params:acme:error:accountDoesNotExist"})
					return
				}
			} else {
				registrations++
			}
			w.Header().Set("Location", base+"/account/recovered")
			if !request.OnlyReturnExisting {
				w.WriteHeader(http.StatusCreated)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "valid"})
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	base = server.URL
	path := filepath.Join(t.TempDir(), "account.json")
	if err := saveJSON(path, savedAccount{URI: base + "/account/old"}); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{origin: base, TermsAgreed: true}
	client := &acme.Client{Key: key, DirectoryURL: base + "/dir", HTTPClient: server.Client()}
	changed, err := issuerAccount(t.Context(), profile, client, path)
	if err != nil || !changed || client.Key != key || client.KID != acme.KeyID(base+"/account/recovered") || registrations != 1 {
		t.Fatal("account recovery", changed, err, registrations)
	}
	before, _ := os.ReadFile(path)
	client = &acme.Client{Key: key, DirectoryURL: base + "/dir", HTTPClient: server.Client()}
	changed, err = issuerAccount(t.Context(), profile, client, path)
	after, _ := os.ReadFile(path)
	if err != nil || changed || lookups != 2 || registrations != 1 || string(before) != string(after) || client.Key != key {
		t.Fatal("repeated recovery replaced retained state", changed, err, lookups, registrations)
	}
}
