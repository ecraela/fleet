package domain

import "testing"

func configurationFixture() ConfigurationDocument {
	return ConfigurationDocument{Backends: []ConfigurationBackend{{Name: "application", Algorithm: "roundrobin", Servers: []ConfigurationServer{{Name: "app-a", Address: "backend.internal", Port: 8080, Weight: 100}}}}}
}
func TestConfigurationValidationPreservesAvailabilityAndLiteralValues(t *testing.T) {
	doc := configurationFixture()
	if !doc.Valid() {
		t.Fatal("fixture invalid")
	}
	for _, address := range []string{"backend.internal; shutdown", "$(touch /tmp/never)", "http://backend.internal", "backend\nother"} {
		bad := doc.Canonical()
		bad.Backends[0].Servers[0].Address = address
		if bad.Valid() {
			t.Fatal("non-literal endpoint accepted")
		}
	}
	for _, address := range []string{"backend.internal.", "192.0.2.1", "2001:db8::1"} {
		good := doc.Canonical()
		good.Backends[0].Servers[0].Address = address
		if !good.Valid() {
			t.Fatal("valid address rejected", address)
		}
	}
	bad := doc.Canonical()
	bad.Backends[0].Servers[0].Maintenance = true
	if bad.Valid() {
		t.Fatal("all servers could enter maintenance")
	}
	bad = doc.Canonical()
	bad.Backends[0].Servers[0].Weight = 0
	if bad.Valid() {
		t.Fatal("all balancing weight could be removed")
	}
	bad = doc.Canonical()
	bad.Backends[0].Servers = append(bad.Backends[0].Servers, bad.Backends[0].Servers[0])
	if bad.Valid() {
		t.Fatal("duplicate server accepted")
	}
}
