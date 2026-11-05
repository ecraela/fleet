package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
)

func TestGeographyIsTenantBoundAndValidatesStoredFacts(t *testing.T) {
	f, rev, node, _ := requestFixture(t)
	var calls atomic.Int32
	var corrupt atomic.Bool
	var sourceSeen atomic.Bool
	stats := map[string]string{"requests": "2", "s1": "0", "s2": "1", "s3": "0", "s4": "0", "s5": "1", "other": "0", "bytes": "32", "mean_ms": "25", "p50_ms": "25", "p95_ms": "25", "p99_ms": "25"}
	client := &http.Client{Transport: requestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		r.ParseForm()
		q := r.Form.Get("query")
		if !strings.Contains(q, `organization_id:="`+f.org+`"`) || !strings.Contains(q, `cluster_id:="`+f.cluster.ID+`"`) {
			t.Error("missing tenant scope", q)
		}
		if strings.Contains(q, "kind:=pipeline") {
			return logResponse(""), nil
		}
		if !strings.Contains(q, `application_id:="`+rev.ApplicationID+`"`) {
			t.Error("missing app scope", q)
		}
		if strings.Contains(q, `client_source:="xff"`) {
			sourceSeen.Store(true)
		}
		row := maps.Clone(stats)
		switch {
		case strings.Contains(q, "stats by (country)"):
			row["country"] = "GB"
			if corrupt.Load() {
				row["country"] = "private-client-ip"
			}
		case strings.Contains(q, "stats by (asn)"):
			row["asn"] = "15169"
		default:
			row["country_known"], row["asn_known"], row["unresolved"], row["database_unavailable"] = "2", "2", "0", "0"
		}
		b, _ := json.Marshal(row)
		return logResponse(string(b) + "\n"), nil
	})}
	var err error
	f.server.logs, err = accesslog.NewStorage("http://private-logs.invalid", client)
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/clusters/" + f.cluster.ID + "/applications/" + rev.ApplicationID + "/geography"
	if got := f.request("GET", base, nil, false); got.Code != 401 {
		t.Fatal("anonymous geography", got.Code)
	}
	foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	for _, path := range []string{"/v1/clusters/" + foreign.ID + "/applications/" + rev.ApplicationID + "/geography", "/v1/clusters/" + f.cluster.ID + "/applications/" + domain.NewID() + "/geography"} {
		if got := f.request("GET", path, nil, true); got.Code != 404 {
			t.Fatal("foreign resource", got.Code)
		}
	}
	before := calls.Load()
	for _, query := range []string{"?country=gb", "?country=GB%7Cstats", "?country=GB&country=US", "?asn=-1", "?asn=4294967296", "?asn=001", "?asn=1%7Cstats", "?node=" + domain.NewID(), "?client_ip=1.2.3.4", "?client_source=xff%7Cstats", "?client_source=forwarded", "?client_source=peer&client_source=xff", "?before=bad"} {
		if got := f.request("GET", base+query, nil, true); got.Code != 400 && got.Code != 404 {
			t.Fatal(query, got.Code)
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid filters reached private storage")
	}
	got := f.request("GET", base+"?country=GB&asn=15169&client_source=xff", nil, true)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := f.request("GET", base+"?country=GB,US,unknown", nil, true); got.Code != 200 {
		t.Fatal("multi-country geography", got.Code, got.Body.String())
	}
	var result GeographyTraffic
	if json.Unmarshal(got.Body.Bytes(), &result) != nil || !sourceSeen.Load() || !result.ClientSourceFilter || result.CountryKnown != 2 || result.ASNKnown != 2 || len(result.Countries) != 1 || result.Countries[0].Country != "GB" || len(result.Collection) != 1 || result.Collection[0].NodeID != node.ID {
		t.Fatal(result)
	}
	corrupt.Store(true)
	if got := f.request("GET", base, nil, true); got.Code != 503 {
		t.Fatal("invalid country escaped validation", got.Code)
	}
	if got := f.request("GET", base+"?country=US", nil, true); got.Code != 503 {
		t.Fatal("store result escaped country filter", got.Code)
	}
}

func TestGeoIngestionRejectsAddressesAndMismatchedProfiles(t *testing.T) {
	f, rev, _, leaf := requestFixture(t)
	profile := strings.Repeat("a", 64)
	event := accesslog.Event{ID: accesslog.EventID(domain.NewID(), 1), AtMS: time.Now().UnixMilli(), ApplicationID: rev.ApplicationID, SpecDigest: rev.Spec.Digest(), Server: "web", Method: "GET", Status: 200, Bytes: 10, DurationMS: 1, ResponseMS: 1, ConnectMS: 0, QueueMS: 0, Scheme: "https", Termination: "--", Geo: &accesslog.Geography{Status: "located", Source: "xff", Country: "GB", ASN: 15169, Profile: profile}}
	var captured []map[string]any
	f.server.logs, _ = accesslog.NewStorage("http://logs.invalid", &http.Client{Transport: requestTransport(func(r *http.Request) (*http.Response, error) {
		d := json.NewDecoder(r.Body)
		for {
			var row map[string]any
			if err := d.Decode(&row); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			captured = append(captured, row)
		}
		return logResponse(""), nil
	})})
	batch := accesslog.Batch{Events: []accesslog.Event{event}, Pipeline: accesslog.Pipeline{BootID: event.ID[:36], AtMS: event.AtMS, Listening: true, Geo: &accesslog.GeoCollection{Mode: "xff", Status: "ready", Profile: profile, Country: &accesslog.GeoDatabase{SHA256: strings.Repeat("b", 64), BuiltAt: 1700000000}}}}
	send := func(body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/logs/write", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
		w := httptest.NewRecorder()
		f.server.ingestLogs(w, r)
		return w
	}
	raw, _ := json.Marshal(batch)
	if got := send(raw); got.Code != 204 {
		t.Fatal(got.Code, got.Body.String())
	}
	if len(captured) != 2 || captured[0]["country"] != "GB" || captured[0]["geo_profile"] != profile || captured[1]["geo_profile_data"] == nil {
		t.Fatal("coarse geography was not stored", captured)
	}
	for _, field := range []string{"client_ip", "peer", "header", "xff"} {
		if _, ok := captured[0][field]; ok {
			t.Fatal("private field stored", field)
		}
	}
	batch.Events[0].Geo.Profile = strings.Repeat("c", 64)
	raw, _ = json.Marshal(batch)
	if got := send(raw); got.Code != 400 {
		t.Fatal("mismatched geography profile accepted", got.Code)
	}
	batch.Events[0].Geo.Profile = profile
	raw, _ = json.Marshal(batch)
	raw = bytes.Replace(raw, []byte(`"geo":{"status"`), []byte(`"geo":{"client_ip":"81.2.69.160","status"`), 1)
	if got := send(raw); got.Code != 400 {
		t.Fatal("client address accepted into wire schema", got.Code)
	}
}
