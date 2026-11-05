package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/domain"
)

func TestAgentBuildInventoryTenantBoundary(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	n, _, _, _ := f.enrollNode(t)
	foreign := domain.NewID()
	cluster, _ := f.store.CreateCluster(t.Context(), foreign, "Foreign")
	other := enrollForFleet(t, f, foreign, cluster.ID)
	for _, node := range []domain.Node{n, other} {
		session := domain.NewID()
		if _, err := f.store.OpenControl(t.Context(), node.OrganizationID, node.ID, session); err != nil {
			t.Fatal(err)
		}
		d := discoveryForControl()
		d.Agent = &buildinfo.Info{Version: "v0.1.0", Revision: strings.Repeat("a", 40), Protocol: 1, Platform: "linux/arm64", ExecutableSHA256: strings.Repeat("b", 64)}
		if _, _, err := f.store.ReportControl(t.Context(), node.OrganizationID, node.ID, session, &d, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.store.AgentBuilds(t.Context(), f.org, []string{n.ID, other.ID})
	if err != nil || len(got) != 1 || got[n.ID].Build.Version != "v0.1.0" {
		t.Fatal(got, err)
	}
	if r := f.request("GET", "/v1/fleet", nil, false); r.Code != 401 {
		t.Fatal(r.Code)
	}
	r := f.request("GET", "/v1/fleet", nil, true)
	var fleet struct {
		Nodes []FleetNode `json:"nodes"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &fleet) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	if len(fleet.Nodes) != 1 || fleet.Nodes[0].Agent == nil || !fleet.Nodes[0].AgentFresh {
		t.Fatal(r.Body.String())
	}
	if strings.Contains(r.Body.String(), other.ID) {
		t.Fatal("foreign agent version leaked")
	}
}
