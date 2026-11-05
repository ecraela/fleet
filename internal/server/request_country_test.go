package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestCountrySelectionKeepsOneTenantScopedQuery(t *testing.T) {
	now := time.Now()
	f, ok := parseRequestFilter(httptest.NewRequest("GET", "/?country=unknown,US,GB", nil), now)
	if !ok || f.country != "GB,US,unknown" {
		t.Fatal(f, ok)
	}
	q := requestScope("tenant-a", "cluster-a", "app-a", f)
	if !strings.Contains(q, `organization_id:="tenant-a" cluster_id:="cluster-a" application_id:="app-a"`) || !strings.Contains(q, `(country:="GB" OR country:="US" OR country:="")`) {
		t.Fatal(q)
	}
	for _, country := range []string{"GB", "US", ""} {
		if !requestCountryMatches(f.country, country) {
			t.Fatalf("selected country %q excluded", country)
		}
	}
	if requestCountryMatches(f.country, "RO") {
		t.Fatal("unselected country accepted")
	}
	for _, query := range []string{"country=GB,", "country=GB,GB", "country=gb,US", "country=GB%7Cstats,US", "country=GB&country=US", "country=" + strings.Repeat("GB,", 32) + "US"} {
		if _, valid := parseRequestFilter(httptest.NewRequest("GET", "/?"+query, nil), now); valid {
			t.Fatalf("invalid filter accepted: %s", query)
		}
	}
	for _, country := range []string{"GB", "unknown"} {
		single, valid := parseRequestFilter(httptest.NewRequest("GET", "/?country="+country, nil), now)
		if !valid || strings.Contains(requestScope("tenant-a", "cluster-a", "app-a", single), " OR ") {
			t.Fatal("single-country compatibility changed")
		}
	}
}
