package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

const ApplicationBackendPrefix = "cv_app_"
const MaxApplications = 16

// HTTPApplication is safe, typed intent. It never contains raw HAProxy syntax.
type HTTPApplication struct {
	ID               string                         `json:"id"`
	Name             string                         `json:"name"`
	Listener         string                         `json:"listener"`
	Hostname         string                         `json:"hostname"`
	PathPrefix       string                         `json:"path_prefix"`
	Algorithm        string                         `json:"algorithm"`
	Servers          []ConfigurationServer          `json:"servers"`
	HealthPath       string                         `json:"health_path"`
	Disabled         bool                           `json:"disabled,omitempty"`
	RequestTelemetry bool                           `json:"request_telemetry,omitempty"`
	Verification     *ApplicationVerificationPolicy `json:"verification,omitempty"`
	TrafficPolicy    *TrafficPolicy                 `json:"traffic_policy,omitempty"`
}

func (a HTTPApplication) BackendName() string {
	return ApplicationBackendPrefix + strings.ReplaceAll(a.ID, "-", "")
}
func (a HTTPApplication) Backend() ConfigurationBackend {
	return ConfigurationBackend{Name: a.BackendName(), Algorithm: a.Algorithm, Servers: a.Servers}
}
func (a HTTPApplication) Canonical() HTTPApplication {
	a.TrafficPolicy = a.TrafficPolicy.Canonical()
	if a.Verification != nil {
		policy := *a.Verification
		a.Verification = &policy
	}
	a.Name = strings.TrimSpace(a.Name)
	a.Hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(a.Hostname), "."))
	a.Servers = append([]ConfigurationServer{}, a.Servers...)
	slices.SortFunc(a.Servers, func(a, b ConfigurationServer) int { return strings.Compare(a.Name, b.Name) })
	return a
}
func HTTPPath(value string) bool {
	if len(value) == 0 || len(value) > 128 || value[0] != '/' {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._~-", c)) {
			return false
		}
	}
	return !strings.Contains(value, "..") && !strings.Contains(value, "//")
}
func (a HTTPApplication) Valid() bool {
	if !a.TrafficPolicy.Valid(a.PathPrefix) || a.TrafficPolicy != nil && !a.RequestTelemetry {
		return false
	}
	if a.Verification != nil && !a.Verification.HTTP && !a.Verification.HTTPS {
		return false
	}
	for _, s := range a.Servers {
		if s.ForwardIncomingPort {
			return false
		}
	}
	if !ValidID(a.ID) || len(a.Name) == 0 || len(a.Name) > 80 || strings.TrimSpace(a.Name) != a.Name || !ValidRuntimeName(a.Listener) || strings.HasPrefix(a.Listener, ApplicationBackendPrefix) || !HTTPPath(a.PathPrefix) || !HTTPPath(a.HealthPath) || len(a.Servers) > 16 || a.Hostname != strings.ToLower(a.Hostname) || strings.HasSuffix(a.Hostname, ".") || len(a.Hostname) > 253 {
		return false
	}
	for _, c := range a.Name {
		if c < 32 || c == 127 {
			return false
		}
	}
	for _, label := range strings.Split(a.Hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return (ConfigurationDocument{Backends: []ConfigurationBackend{a.Backend()}}).Valid()
}
func (a HTTPApplication) Equal(b HTTPApplication) bool {
	x, _ := json.Marshal(a.Canonical())
	y, _ := json.Marshal(b.Canonical())
	return string(x) == string(y)
}
func SameApplications(a, b []HTTPApplication) bool {
	if len(a) != len(b) {
		return false
	}
	for _, app := range a {
		found := false
		for _, other := range b {
			found = found || app.Equal(other)
		}
		if !found {
			return false
		}
	}
	return true
}
func ValidApplications(apps []HTTPApplication) bool {
	if len(apps) > MaxApplications {
		return false
	}
	for i, a := range apps {
		if !a.Valid() {
			return false
		}
		for _, b := range apps[:i] {
			if a.ID == b.ID || (a.Listener == b.Listener && a.Hostname == b.Hostname && a.PathPrefix == b.PathPrefix) {
				return false
			}
		}
	}
	return true
}

type ApplicationRevision struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	ClusterID      string          `json:"cluster_id"`
	ApplicationID  string          `json:"application_id"`
	Number         int             `json:"number"`
	RequestID      string          `json:"request_id"`
	RequestDigest  string          `json:"-"`
	Spec           HTTPApplication `json:"spec"`
	RolloutID      string          `json:"rollout_id"`
	RollbackOf     string          `json:"rollback_of,omitempty"`
	ActorUserID    string          `json:"actor_user_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	Status         string          `json:"status"`
	Deleted        bool            `json:"deleted,omitempty"`
}

func (r ApplicationRevision) Lifecycle() string {
	if r.Deleted {
		return "deleted"
	}
	if r.Spec.Disabled {
		return "disabled"
	}
	return "active"
}

// EffectiveApplication retains the last successful lifecycle after compensation.
// Pending/partial revisions remain visible and must not claim completed removal.
func EffectiveApplication(r ApplicationRevision, records []ApplicationRevision) ApplicationRevision {
	if r.Status != "rolled_back" && r.Status != "rollback_failed" {
		return r
	}
	var saved ApplicationRevision
	for _, v := range records {
		if v.ApplicationID == r.ApplicationID && v.Status == "succeeded" && v.Number > saved.Number {
			saved = v
		}
	}
	if saved.ID != "" {
		return saved
	}
	return r
}

type ApplicationInput struct {
	RequestID        string                   `json:"request_id"`
	ExpectedRevision int                      `json:"expected_revision"`
	Spec             HTTPApplication          `json:"spec"`
	Nodes            []RolloutTarget          `json:"nodes"`
	Entrypoint       *RolloutEntrypointTarget `json:"entrypoint"`
	Action           string                   `json:"action,omitempty"`
}
