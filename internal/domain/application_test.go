package domain

import "testing"

func TestApplicationNormalizationAndRouteConflicts(t *testing.T) {
	a := HTTPApplication{ID: NewID(), Name: " Store ", Listener: "public", Hostname: "SHOP.Example.test.", PathPrefix: "/shop", Algorithm: "roundrobin", Servers: []ConfigurationServer{{Name: "web", Address: "backend.internal", Port: 8080, Weight: 100}}, HealthPath: "/healthz"}.Canonical()
	if !a.Valid() || a.Hostname != "shop.example.test" || a.Name != "Store" {
		t.Fatal("normalization", a)
	}
	b := a
	b.ID = NewID()
	if ValidApplications([]HTTPApplication{a, b}) {
		t.Fatal("duplicate route accepted")
	}
	b.PathPrefix = "/shop/admin"
	if !ValidApplications([]HTTPApplication{a, b}) {
		t.Fatal("longest prefix routes rejected")
	}
	for _, path := range []string{"/foo\nuse_backend bad", "/foo bar", "/../secret", "https://elsewhere.test", "/foo?secret=1"} {
		b.HealthPath = path
		if b.Valid() {
			t.Fatal("unsafe health path", path)
		}
	}
	b = a
	b.Hostname = "*.example.test"
	if b.Valid() {
		t.Fatal("wildcard accepted")
	}
	doc := ConfigurationDocument{Backends: []ConfigurationBackend{{Name: "legacy", Algorithm: "roundrobin", Servers: a.Servers}}, Applications: []HTTPApplication{a}}
	if !doc.Valid() || !doc.Equal(doc.Canonical()) || len(doc.AllBackends()) != 2 {
		t.Fatal("application document invalid")
	}
	c := doc.Canonical()
	c.Applications[0].Servers[0].Weight = 200
	if doc.Applications[0].Servers[0].Weight != 100 || doc.Equal(c) {
		t.Fatal("application intent aliases a prior revision")
	}
}

func TestLastCompletedCheckWhileNextCheckRuns(t *testing.T) {
	for _, status := range []string{"L4OK", "L6OK", "L7OK", "L7OKC", "* L4OK", "* L6OK", "* L7OK", "* L7OKC"} {
		if !successfulHealthCheck(status) {
			t.Fatal("passing last check rejected", status)
		}
	}
	for _, status := range []string{"", "* ", "INI", "* INI", "L7TOUT", "* L7TOUT", "* L7STS", "* SOCKERR", "** L7OK"} {
		if successfulHealthCheck(status) {
			t.Fatal("unknown/failed check accepted", status)
		}
	}
}
