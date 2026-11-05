package connectwire

import "testing"

func TestTrafficStatisticsHaveOnlyApplicationScopedReadRoutes(t *testing.T) {
	id := "00000000-0000-0000-0000-000000000001"
	for _, p := range []string{"/v1/clusters/" + id + "/applications/" + id + "/traffic-rules", "/v1/clusters/" + id + "/applications/" + id + "/versions/" + id + "/traffic-rules"} {
		if !Allowed("GET", p) {
			t.Fatal("missing read route", p)
		}
		for _, m := range []string{"POST", "DELETE", "PUT", "CONNECT"} {
			if Allowed(m, p) {
				t.Fatal("statistics mutation accepted", m, p)
			}
		}
	}
	for _, p := range []string{"/v1/clusters/" + id + "/listeners/versions/" + id + "/traffic-rules", "/v1/nodes/" + id + "/configuration/versions/" + id + "/traffic-rules"} {
		if Allowed("GET", p) {
			t.Fatal("unrelated resource gained route", p)
		}
	}
}

func TestConnectorSurfaceExcludesMachineCredentialsAndGenericTunnels(t *testing.T) {
	for _, path := range []string{"/v1/fleet", "/v1/clusters", "/v1/nodes/00000000-0000-0000-0000-000000000001/history"} {
		if !Allowed("GET", path) {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"/v1/local/session", "/v1/local/connector", "/v1/browser/session", "/v1/connector", "/v1/connector/claim", "/healthz", "/v1/enroll", "/v1/metrics/write", "/v1/identity/renew", "/v3/services/haproxy/configuration/raw", "https://internal.invalid/v1/fleet", "/v1/../v1/fleet", "/v1/fleet/", "/v1/%66leet"} {
		if Allowed("GET", path) {
			t.Fatal("tunneled forbidden path", path)
		}
	}
	if Allowed("CONNECT", "/v1/fleet") || Allowed("TRACE", "/v1/fleet") {
		t.Fatal("generic HTTP tunnel allowed")
	}
}

func TestGeographyUsesTheBoundedReadOnlyConnectorSurface(t *testing.T) {
	path := "/v1/clusters/00000000-0000-0000-0000-000000000001/applications/00000000-0000-0000-0000-000000000002/geography"
	if !Allowed("GET", path) {
		t.Fatal("geography not forwarded")
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "CONNECT"} {
		if Allowed(method, path) {
			t.Fatal("geography accepted a mutation", method)
		}
	}
}
