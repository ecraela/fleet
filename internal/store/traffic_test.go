package store

import (
	"github.com/ecraela/fleet/internal/domain"
	"strings"
	"testing"
)

func TestApplicationTrafficRequiresEveryNodesLocalGrantAndDataset(t *testing.T) {
	for _, tc := range []struct {
		version                uint32
		grant, country, accept bool
	}{{5, true, true, false}, {6, false, false, false}, {6, true, false, false}, {6, true, true, true}, {7, true, true, false}} {
		r := newManagedRolloutRig(t, NewMemory())
		for i := range r.discoveries {
			c := r.discoveries[i].Configuration
			c.ApplicationControlVersion = tc.version
			c.RequestLogsAllowed = true
			if tc.grant {
				c.TrafficRules = &domain.TrafficCapabilities{Profile: strings.Repeat("a", 64), ClientSource: "peer", CountryReady: tc.country}
			}
		}
		r.refresh()
		a := appForTest("Store", "/shop")
		a.RequestTelemetry = true
		a.TrafficPolicy = &domain.TrafficPolicy{Version: 1, Mode: "observe", UnknownAction: "allow", Rules: []domain.TrafficRule{{ID: domain.NewID(), Name: "Country", Action: "block", Countries: []string{"RU"}}}}
		_, err := r.db.(ApplicationStore).DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, a, 0))
		if (err == nil) != tc.accept {
			t.Fatal(tc, err)
		}
	}
}
