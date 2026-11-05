package domain

import "testing"

func TestSetupRejectsUnboundedOrUnrecognizedDiagnosticData(t *testing.T) {
	valid := NodeSetup{ObservedAtMS: 1, Owner: "external"}
	for name, codes := range setupCodes {
		for code := range codes {
			valid.Checks = append(valid.Checks, SetupResult(name, code))
			break
		}
	}
	if !valid.Valid() {
		t.Fatal("valid diagnostics rejected")
	}
	for _, mutate := range []func(*NodeSetup){
		func(s *NodeSetup) { s.Checks = append(s.Checks, s.Checks[0]) },
		func(s *NodeSetup) { s.Checks[0] = s.Checks[1] },
		func(s *NodeSetup) { s.Checks[0].Code = "private raw diagnostics" },
		func(s *NodeSetup) { s.Checks[0].Status = "arbitrary" },
		func(s *NodeSetup) { s.Owner = "/private/config/path" },
	} {
		copy := valid
		copy.Checks = append([]SetupCheck{}, valid.Checks...)
		mutate(&copy)
		if copy.Valid() {
			t.Fatal("unrecognized setup data accepted")
		}
	}
}
