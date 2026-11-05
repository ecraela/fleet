package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"github.com/ecraela/fleet/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestACMETenantPermissionsAndCSRF(t *testing.T) {
	f := browserFixture(t)
	foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	input := domain.ACMEInput{RequestID: domain.NewID(), Action: "enable", Issuer: "local", Binding: domain.ListenerBinding{Name: "public", Address: "*", Port: 8443}}
	for _, cluster := range []string{f.cluster.ID, foreign.ID} {
		base := "/v1/clusters/" + cluster + "/certificates"
		for _, path := range []string{base} {
			if got := f.request("POST", path, input, false); got.Code != 401 {
				t.Fatal("anonymous write", got.Code)
			}
			if got := f.request("POST", path, input, true); cluster == foreign.ID && got.Code != 404 {
				t.Fatal("foreign write", got.Code, got.Body.String())
			}
		}
		want := 200
		if cluster == foreign.ID {
			want = 404
		}
		if got := f.request("GET", base, nil, true); got.Code != want {
			t.Fatal("tenant history", got.Code)
		}
	}
	for _, manage := range []bool{false, true} {
		pending, nonce := challenge(t, f.server)
		claims := validClaims(f, nonce)
		if manage {
			claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
		}
		created := exchange(t, f, pending, claims)
		cookie := sessionCookie(t, created)
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(created.Body.Bytes(), &session)
		for _, csrf := range []string{"", session.CSRF} {
			raw, _ := json.Marshal(input)
			req := httptest.NewRequest("POST", "/v1/clusters/"+f.cluster.ID+"/certificates", strings.NewReader(string(raw)))
			req.AddCookie(cookie)
			req.Header.Set("Origin", testBrowserOrigin)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-CSRF-Token", csrf)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, req)
			want := 403
			if manage && csrf != "" {
				want = 409
			}
			if got.Code != want {
				t.Fatal("manage/CSRF gate", manage, csrf != "", got.Code, got.Body.String())
			}
		}
	}
}

func TestACMEMachineJobsRequireLiveEnrolledIdentity(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	n, leaf, _, _ := f.enrollNode(t)
	for _, path := range []string{"/v1/identity/acme", "/v1/identity/acme/" + domain.NewID() + "/result"} {
		method := "GET"
		if strings.HasSuffix(path, "result") {
			method = "POST"
		}
		if w := f.request(method, path, nil, true); w.Code != 401 {
			t.Fatal("operator acquired machine authority", w.Code)
		}
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/identity/acme", nil)
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		return w
	}
	if w := request(); w.Code != 200 || !strings.Contains(w.Body.String(), `"job":null`) {
		t.Fatal("enrolled poll", w.Code, w.Body.String())
	}
	if e := f.store.RevokeNode(t.Context(), f.org, n.ID); e != nil {
		t.Fatal(e)
	}
	if w := request(); w.Code != 401 {
		t.Fatal("revoked issuance identity", w.Code)
	}
}
