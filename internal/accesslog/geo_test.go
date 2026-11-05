package accesslog

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func geoPolicy(t *testing.T, mode string) (*GeoResolver, string) {
	t.Helper()
	country, _ := filepath.Abs("testdata/GeoIP2-Country-Test.mmdb")
	asn, _ := filepath.Abs("testdata/GeoLite2-ASN-Test.mmdb")
	p := GeoPolicy{Version: 1, Mode: mode, CountryDatabase: country, ASNDatabase: asn}
	if mode != "peer" {
		p.TrustedProxies = []string{"10.0.0.0/24", "fd00::/8"}
	}
	path := filepath.Join(t.TempDir(), "geo.json")
	raw, _ := json.Marshal(p)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadGeoPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if !r.Snapshot().Valid() || r.Snapshot().Status != "ready" {
		t.Fatal("invalid fixture metadata", r.Snapshot())
	}
	return r, path
}

func TestTrafficRecordExportsOnlyBoundedDecisionsAndSupportsOldFormats(t *testing.T) {
	id := domain.NewID()
	line := strings.Replace(sampleLine(), "cv1 ", "cv5 ", 1) + " - - 10.0.0.2 81.2.69.160 1 0 - - " + id + " would_block"
	e, err := Parse([]byte(line), domain.NewID(), 1, time.Now())
	if err != nil || e.Traffic == nil || e.Traffic.RuleID != id {
		t.Fatal(e, err)
	}
	raw, _ := json.Marshal(e)
	if strings.Contains(string(raw), "81.2.69.160") || strings.Contains(string(raw), "10.0.0.2") {
		t.Fatal("address exported")
	}
	if _, err := Parse([]byte(strings.Replace(line, id, "private-address", 1)), domain.NewID(), 2, time.Now()); err == nil {
		t.Fatal("arbitrary traffic identifier accepted")
	}
	line = strings.TrimSuffix(line, id+" would_block") + "none none"
	if e, err := Parse([]byte(line), domain.NewID(), 3, time.Now()); err != nil || e.Traffic != nil {
		t.Fatal("unmatched request invented decision", e, err)
	}
}
func TestGeoReadsVerifiedLocalCountryAndASNFixtures(t *testing.T) {
	r, _ := geoPolicy(t, "peer")
	for _, tc := range []struct {
		ip, country, status string
		asn                 uint32
	}{
		{"81.2.69.160", "GB", "partial", 0}, {"2001:218::", "JP", "partial", 0}, {"::ffff:81.2.69.160", "GB", "partial", 0},
		{"1.0.0.1", "", "partial", 15169}, {"10.0.0.1", "", "non_public", 0}, {"127.0.0.1", "", "non_public", 0}, {"fe80::1", "", "non_public", 0}, {"203.0.113.1", "", "not_found", 0},
	} {
		got := r.Resolve(tc.ip, "1.0.0.1", true, "1.0.0.1", 1)
		if !got.Valid() || got.Country != tc.country || got.ASN != tc.asn || got.Status != tc.status || got.Source != "peer" {
			t.Errorf("%s: %+v", tc.ip, got)
		}
	}
	metadata, _ := json.Marshal(r.Snapshot())
	if strings.Contains(string(metadata), "mmdb") || strings.Contains(string(metadata), "testdata") || r.Snapshot().Country.SHA256 == r.Snapshot().ASN.SHA256 {
		t.Fatal("metadata exposed local paths or lost database identity")
	}
}
func TestGeoTrustWalkIgnoresUntrustedHeadersAndRejectsAmbiguity(t *testing.T) {
	r, _ := geoPolicy(t, "xff")
	for _, tc := range []struct {
		peer, header, source, country, status string
		count                                 int
		proxy                                 bool
		asn                                   uint32
	}{
		{"10.0.0.2", "1.0.0.1, 10.0.0.7", "xff", "", "partial", 1, false, 15169},
		{"10.0.0.2", "1.0.0.1, 81.2.69.160", "xff", "GB", "partial", 1, false, 0},
		{"10.0.0.2", "2001:218::, fd00::2", "xff", "JP", "partial", 1, false, 0},
		{"81.2.69.160", "1.0.0.1", "peer", "GB", "partial", 1, false, 0},
		{"10.0.0.2", "unknown", "unknown", "", "unresolved", 1, false, 0},
		{"10.0.0.2", "1.0.0.1:443", "unknown", "", "unresolved", 1, false, 0},
		{"10.0.0.2", "fe80::1%eth0", "unknown", "", "unresolved", 1, false, 0},
		{"10.0.0.2", "\"1.0.0.1\"", "unknown", "", "unresolved", 1, false, 0},
		{"10.0.0.2", "1.0.0.1", "unknown", "", "unresolved", 2, false, 0},
		{"10.0.0.2", "1.0.0.1", "unknown", "", "unresolved", 1, true, 0},
		{"10.0.0.2", "", "unknown", "", "unresolved", 0, false, 0},
		{"10.0.0.2", "10.0.0.7, 10.0.0.8", "unknown", "", "unresolved", 1, false, 0},
		{"10.0.0.2", strings.Repeat("1.0.0.1,", 17), "unknown", "", "unresolved", 1, false, 0},
		{"10.0.0.2", strings.Repeat(" ", 257), "unknown", "", "unresolved", 1, false, 0},
	} {
		g := r.Resolve(tc.peer, "1.0.0.1", tc.proxy, tc.header, tc.count)
		if !g.Valid() || g.Source != tc.source || g.Country != tc.country || g.Status != tc.status || g.ASN != tc.asn {
			t.Errorf("%+v: %+v", tc, g)
		}
	}
	proxy, _ := geoPolicy(t, "proxy_protocol")
	for _, tc := range []struct {
		peer     string
		received bool
		status   string
	}{{"10.0.0.2", true, "partial"}, {"81.2.69.160", true, "unresolved"}, {"10.0.0.2", false, "unresolved"}} {
		g := proxy.Resolve(tc.peer, "81.2.69.160", tc.received, "1.0.0.1", 1)
		if !g.Valid() || g.Status != tc.status || tc.status == "partial" && (g.Source != "proxy_protocol" || g.Country != "GB") {
			t.Fatal(tc, g)
		}
	}
}
func TestGeoProxyRequiresThePostHandshakeCapture(t *testing.T) {
	r, _ := geoPolicy(t, "proxy_protocol")
	line := strings.Replace(sampleLine(), "cv1 ", "cv4 ", 1) + " - - 10.0.0.2 81.2.69.160 1 0 - -"
	now := time.Now()
	event, err := parseGeo([]byte(line), domain.NewID(), 1, now, nil, r)
	if err != nil || event.Geo == nil || event.Geo.Country != "GB" || event.Geo.Source != "proxy_protocol" {
		t.Fatal("post-handshake address was not resolved", event.Geo, err)
	}
	legacy := strings.Replace(line, "cv4 ", "cv3 ", 1)
	event, err = parseGeo([]byte(legacy), domain.NewID(), 2, now, nil, r)
	if err != nil || event.Geo == nil || event.Geo.Status != "unresolved" || event.Geo.Country != "" || event.Geo.Source != "unknown" {
		t.Fatal("legacy connection-address field was treated as a verified PROXY client", event.Geo, err)
	}
}

func TestGeoPolicyAndDatabaseFailuresNeverFallbackToGuessedLocations(t *testing.T) {
	_, path := geoPolicy(t, "peer")
	raw, _ := os.ReadFile(path)
	for _, change := range []func(*GeoPolicy){
		func(p *GeoPolicy) { p.Mode = "header" }, func(p *GeoPolicy) { p.Version = 2 },
		func(p *GeoPolicy) { p.Mode = "xff"; p.TrustedProxies = []string{"0.0.0.0/0"} },
		func(p *GeoPolicy) { p.Mode = "xff"; p.TrustedProxies = []string{"10.0.0.1/24"} },
		func(p *GeoPolicy) { p.Mode = "xff"; p.TrustedProxies = nil },
		func(p *GeoPolicy) { p.TrustedProxies = []string{"10.0.0.0/24"} },
		func(p *GeoPolicy) { p.CountryDatabase = ""; p.ASNDatabase = "" },
	} {
		var p GeoPolicy
		json.Unmarshal(raw, &p)
		change(&p)
		data, _ := json.Marshal(p)
		os.WriteFile(path, data, 0600)
		if r, err := LoadGeoPolicy(path); err == nil {
			r.Close()
			t.Fatal("unsafe local policy accepted", p)
		}
	}
	os.WriteFile(path, []byte(strings.TrimSuffix(string(raw), "}")+`,"client_ip":"private"}`), 0600)
	if _, err := LoadGeoPolicy(path); err == nil {
		t.Fatal("unknown local field accepted")
	}
	os.WriteFile(path, raw, 0666)
	os.Chmod(path, 0666)
	if _, err := LoadGeoPolicy(path); err == nil {
		t.Fatal("writable policy accepted")
	}
	os.Chmod(path, 0600)
	var p GeoPolicy
	json.Unmarshal(raw, &p)
	p.CountryDatabase = filepath.Join(t.TempDir(), "missing.mmdb")
	data, _ := json.Marshal(p)
	os.WriteFile(path, data, 0600)
	r, err := LoadGeoPolicy(path)
	if err != nil {
		t.Fatal("database failure must leave the agent's other workloads available", err)
	}
	defer r.Close()
	g := r.Resolve("1.0.0.1", "", false, "", 0)
	if !r.Snapshot().Valid() || r.Snapshot().Status != "database_unavailable" || !g.Valid() || g.Status != "database_unavailable" || g.Country != "" || g.ASN != 0 {
		t.Fatal(r.Snapshot(), g)
	}
}
func TestGeoDatabaseSnapshotSurvivesReplacementWithoutReadingMixedBytes(t *testing.T) {
	_, path := geoPolicy(t, "peer")
	raw, _ := os.ReadFile(path)
	var p GeoPolicy
	json.Unmarshal(raw, &p)
	data, _ := os.ReadFile(p.CountryDatabase)
	p.CountryDatabase = filepath.Join(t.TempDir(), "country.mmdb")
	os.WriteFile(p.CountryDatabase, data, 0600)
	raw, _ = json.Marshal(p)
	os.WriteFile(path, raw, 0600)
	r, err := LoadGeoPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	profile := r.Snapshot().Profile
	os.WriteFile(p.CountryDatabase, []byte("broken replacement"), 0600)
	if g := r.Resolve("81.2.69.160", "", false, "", 0); g.Country != "GB" || r.Snapshot().Profile != profile {
		t.Fatal("reader changed after in-place replacement", g)
	}
	newer, err := LoadGeoPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	defer newer.Close()
	if newer.Snapshot().Status != "database_unavailable" || newer.Snapshot().Profile == profile {
		t.Fatal("bad replacement presented as the previous database")
	}
}
func TestCV3ExportsCoarseFactsAndNeverTheLocalAddressOrHeader(t *testing.T) {
	r, _ := geoPolicy(t, "xff")
	header := "1.0.0.1, 81.2.69.160"
	line := strings.Replace(sampleLine(), "cv1 ", "cv3 ", 1) + fmt.Sprintf(" - - 10.0.0.2 10.0.0.2 0 1 %d %s", len(header), hex.EncodeToString([]byte(header)))
	e, err := parseGeo([]byte(line), domain.NewID(), 1, time.Now(), nil, r)
	if err != nil || e.Geo == nil || e.Geo.Country != "GB" || !e.Valid(time.Now()) {
		t.Fatal(e, err)
	}
	raw, _ := json.Marshal(Batch{Events: []Event{e}, Pipeline: Pipeline{Geo: r.Snapshot()}})
	for _, private := range []string{"10.0.0.2", "1.0.0.1", "81.2.69.160", hex.EncodeToString([]byte(header)), "client_ip", "forwarded_for"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("local input escaped into wire JSON", private)
		}
	}
	bad := strings.Replace(line, fmt.Sprintf(" %d ", len(header)), " 9999 ", 1)
	e, err = parseGeo([]byte(bad), domain.NewID(), 2, time.Now(), nil, r)
	if err != nil || e.Geo.Status != "unresolved" {
		t.Fatal("oversized forwarded input was trusted", e, err)
	}
	old, err := parseGeo([]byte(sampleLine()), domain.NewID(), 3, time.Now(), nil, r)
	if err != nil || old.Geo != nil {
		t.Fatal("legacy log invented geography", old, err)
	}
	disabled, err := Parse([]byte(line), domain.NewID(), 4, time.Now())
	if err != nil || disabled.Geo.Status != "disabled" {
		t.Fatal("unconfigured geography was enabled", disabled, err)
	}
	peer, _ := geoPolicy(t, "peer")
	e, err = parseGeo([]byte(bad), domain.NewID(), 5, time.Now(), nil, peer)
	if err != nil || e.Geo.Source != "peer" || e.Geo.Status != "non_public" {
		t.Fatal("malformed untrusted header poisoned the physical peer", e, err)
	}
	proxy, _ := geoPolicy(t, "proxy_protocol")
	proxied := strings.Replace(bad, "10.0.0.2 10.0.0.2 0", "10.0.0.2 81.2.69.160 1", 1)
	proxied = strings.Replace(proxied, "cv3 ", "cv4 ", 1)
	e, err = parseGeo([]byte(proxied), domain.NewID(), 6, time.Now(), nil, proxy)
	if err != nil || e.Geo.Source != "proxy_protocol" || e.Geo.Country != "GB" {
		t.Fatal("an unrelated header overrode trusted PROXY evidence", e, err)
	}
}
func TestGeoBatchesRespectWireBudgetWithoutLosingQueueOrder(t *testing.T) {
	r := &Receiver{queue: make(chan Event, QueueSize)}
	boot := domain.NewID()
	now := time.Now()
	for i := range 600 {
		e, err := Parse([]byte(sampleLine()), boot, uint64(i), now)
		if err != nil {
			t.Fatal(err)
		}
		e.Path = "/" + strings.Repeat("a", 511)
		e.PathKind = "exact"
		e.Server = strings.Repeat("a", 128)
		e.Geo = &Geography{Status: "located", Source: "xff", Country: "GB", ASN: 15169, Profile: strings.Repeat("b", 64)}
		r.queue <- e
	}
	count := 0
	for {
		events := r.Drain()
		if len(events) == 0 {
			break
		}
		raw, _ := json.Marshal(Batch{Events: events, Pipeline: r.Snapshot()})
		if len(raw) > MaxPayload {
			t.Fatal("oversized batch", len(raw))
		}
		for _, e := range events {
			if e.ID != EventID(boot, uint64(count)) {
				t.Fatal("event lost or reordered")
			}
			count++
		}
	}
	if count != 600 {
		t.Fatal("events lost", count)
	}
}
