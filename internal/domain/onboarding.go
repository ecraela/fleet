package domain

// NodeSetup is bounded diagnostic evidence, never a grant of management authority.
// Paths, configuration contents, endpoint URLs and credentials remain on the node.
type NodeSetup struct {
	ObservedAtMS int64        `json:"observed_at_ms"`
	Owner        string       `json:"owner"`
	Checks       []SetupCheck `json:"checks"`
}

type SetupCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

var setupCodes = map[string]map[string]string{
	"metrics":               {"metrics_ready": "ready", "metrics_unavailable": "error", "metrics_pending": "pending"},
	"discovery":             {"runtime_ready": "ready", "runtime_not_configured": "disabled", "runtime_unavailable": "error", "unsupported_runtime": "error"},
	"files":                 {"files_ready": "ready", "files_not_configured": "disabled", "files_unreadable": "error"},
	"ownership":             {"owner_manager": "ready", "owner_external": "blocked", "owner_unconfirmed": "warning", "automation_detected": "warning"},
	"runtime_control":       {"runtime_control_ready": "ready", "runtime_control_disabled": "disabled", "runtime_control_unavailable": "blocked", "runtime_admin_required": "blocked", "runtime_version_unsupported": "blocked"},
	"configuration_control": {"configuration_control_ready": "ready", "configuration_control_disabled": "disabled", "configuration_read_only": "disabled", "configuration_unavailable": "blocked", "configuration_unsupported": "blocked", "configuration_owner_required": "blocked"},
}

func SetupResult(name, code string) SetupCheck {
	return SetupCheck{Name: name, Status: setupCodes[name][code], Code: code}
}

func (s NodeSetup) Valid() bool {
	if s.ObservedAtMS <= 0 || (s.Owner != "manager" && s.Owner != "external" && s.Owner != "unconfirmed") || len(s.Checks) != len(setupCodes) {
		return false
	}
	seen := map[string]bool{}
	for _, c := range s.Checks {
		if status, ok := setupCodes[c.Name][c.Code]; !ok || c.Status != status || seen[c.Name] {
			return false
		}
		seen[c.Name] = true
	}
	return true
}
