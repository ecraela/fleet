package store

import (
	"testing"

	"github.com/ecraela/fleet/internal/domain"
)

func TestApplicationTelemetryRequiresNewAgentAndLocalOptIn(t *testing.T) {
	for _, test := range []struct {
		version         uint32
		allowed, accept bool
	}{{4, false, false}, {4, true, false}, {5, false, false}, {5, true, true}, {6, true, true}, {7, true, false}} {
		r := newManagedRolloutRig(t, NewMemory())
		for i := range r.discoveries {
			r.discoveries[i].Configuration.ApplicationControlVersion = test.version
			r.discoveries[i].Configuration.RequestLogsAllowed = test.allowed
		}
		r.refresh()
		spec := appForTest("Store", "/shop")
		spec.RequestTelemetry = true
		v, err := r.db.(ApplicationStore).DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, spec, 0))
		if (err == nil) != test.accept {
			t.Fatal(test, err)
		}
		if test.accept && (!v.Spec.RequestTelemetry || v.Spec.Digest() == (func() domain.HTTPApplication { spec.ID = v.ApplicationID; spec.RequestTelemetry = false; return spec })().Digest()) {
			t.Fatal("collection opt-in did not version the application")
		}
	}
}
