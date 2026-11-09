package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
	"github.com/ecraela/fleet/internal/store"
)

func (s *Server) configureSoftware() {
	s.mux.HandleFunc("GET /v1/software/releases", s.authorizeCapability("haproxy.read", s.softwareReleases))
	s.mux.HandleFunc("GET /v1/software/manager", s.authorizeCapability("haproxy.read", s.installationSoftware))
	s.mux.HandleFunc("GET /v1/software/manager/plan", s.authorizeCapability("haproxy.read", s.planInstallationSoftware))
	s.mux.HandleFunc("POST /v1/software/manager", s.authorizeCapability("haproxy.manage", s.createInstallationSoftware))
	s.mux.HandleFunc("POST /v1/software/manager/{update}/cancel", s.authorizeCapability("haproxy.manage", s.cancelInstallationSoftware))
	s.mux.HandleFunc("GET /v1/nodes/{node}/software", s.authorizeCapability("haproxy.read", s.nodeSoftware))
	s.mux.HandleFunc("GET /v1/nodes/{node}/software/plan", s.authorizeCapability("haproxy.read", s.planSoftware))
	s.mux.HandleFunc("POST /v1/nodes/{node}/software", s.authorizeCapability("haproxy.manage", s.createSoftware))
	s.mux.HandleFunc("POST /v1/nodes/{node}/software/{update}/cancel", s.authorizeCapability("haproxy.manage", s.cancelSoftware))
	s.mux.HandleFunc("GET /v1/identity/software", s.desiredSoftware)
	s.mux.HandleFunc("POST /v1/identity/software/heartbeat", s.heartbeatSoftware)
	s.mux.HandleFunc("POST /v1/identity/software/{update}/status", s.reportSoftware)
	s.mux.HandleFunc("GET /v1/host/software", s.hostDesiredInstallationSoftware)
	s.mux.HandleFunc("POST /v1/host/software/heartbeat", s.hostHeartbeatInstallationSoftware)
	s.mux.HandleFunc("POST /v1/host/software/{update}/status", s.hostReportInstallationSoftware)
}

func (s *Server) softwareStorage(w http.ResponseWriter) (store.SoftwareStore, bool) {
	db, ok := s.config.Store.(store.SoftwareStore)
	if !ok {
		fail(w, 503, "software update storage unavailable")
	}
	return db, ok
}

func (s *Server) softwareCatalog(w http.ResponseWriter) (software.Catalog, bool) {
	catalog, err := software.LoadCatalog(s.config.ReleaseCatalogFile)
	if err != nil {
		fail(w, 503, "verified release catalog unavailable")
		return software.Catalog{}, false
	}
	return catalog, true
}

func (s *Server) softwareReleases(w http.ResponseWriter, _ *http.Request, _ string) {
	catalog, ok := s.softwareCatalog(w)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"releases": catalog.Releases, "manager_build": s.buildInfo()})
}

func (s *Server) nodeSoftware(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.softwareStorage(w)
	if !ok {
		return
	}
	updates, err := db.ListSoftwareUpdates(r.Context(), org, r.PathValue("node"))
	if err != nil {
		storeError(w, err)
		return
	}
	var observed *store.AgentObservation
	var updater *software.Updater
	if value, err := db.SoftwareUpdater(r.Context(), org, r.PathValue("node")); err == nil {
		updater = &value
	} else if !errors.Is(err, domain.ErrNotFound) {
		storeError(w, err)
		return
	}
	if builds, ok := s.config.Store.(store.AgentBuildStore); ok {
		rows, err := builds.AgentBuilds(r.Context(), org, []string{r.PathValue("node")})
		if err != nil {
			storeError(w, err)
			return
		}
		if value, ok := rows[r.PathValue("node")]; ok {
			observed = &value
		}
	}
	writeJSON(w, 200, map[string]any{"updates": updates, "observed": observed, "updater": updater})
}

type softwarePlan struct {
	Release          software.Release  `json:"release"`
	Current          buildinfo.Info    `json:"current"`
	Manager          buildinfo.Info    `json:"manager"`
	Action           string            `json:"action"`
	Eligible         bool              `json:"eligible"`
	Reason           string            `json:"reason,omitempty"`
	TrafficImpact    string            `json:"traffic_impact"`
	ManagementImpact string            `json:"management_impact"`
	Updater          *software.Updater `json:"updater,omitempty"`
}

// softwarePreflight is deliberately repeated by the mutation handler. A plan is
// explanatory, never an authorization token. The host repeats the checks too.
func (s *Server) softwarePreflight(r *http.Request, org, nodeID, version, action string) (softwarePlan, error) {
	var plan softwarePlan
	if !domain.ValidID(nodeID) || !software.ValidVersion(version) || (action != "upgrade" && action != "rollback") {
		return plan, domain.ErrInvalidInput
	}
	node, err := s.config.Store.GetNode(r.Context(), org, nodeID)
	if err != nil || node.RevokedAt != nil {
		return plan, domain.ErrNotFound
	}
	catalog, err := software.LoadCatalog(s.config.ReleaseCatalogFile)
	if err != nil {
		return plan, errors.New("verified release catalog unavailable")
	}
	release, found := catalog.Release(version)
	if !found {
		return plan, domain.ErrNotFound
	}
	plan = softwarePlan{Release: release, Manager: s.buildInfo(), Action: action,
		TrafficImpact:    "HAProxy stays running; agent telemetry and management pause during restart",
		ManagementImpact: "Brief gap for this node; manager and ingest continue serving"}
	if !release.ManagedUpgrades {
		plan.Reason = "target release does not support managed upgrades"
		return plan, nil
	}
	builds, ok := s.config.Store.(store.AgentBuildStore)
	if !ok {
		plan.Reason = "agent build inventory unavailable"
		return plan, nil
	}
	rows, err := builds.AgentBuilds(r.Context(), org, []string{nodeID})
	if err != nil {
		return plan, err
	}
	observed, exists := rows[nodeID]
	if !exists || observed.Build == nil || !observed.Connected || time.Since(observed.LastSeen) > 6*time.Second {
		plan.Reason = "fresh connected agent build required"
		return plan, nil
	}
	plan.Current = *observed.Build
	current := plan.Current
	manager := plan.Manager
	db, ok := s.config.Store.(store.SoftwareStore)
	if !ok {
		plan.Reason = "update storage unavailable"
		return plan, nil
	}
	updater, err := db.SoftwareUpdater(r.Context(), org, nodeID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return plan, err
	}
	if err == nil {
		plan.Updater = &updater
	}
	if err != nil || !updater.Ready || time.Since(updater.LastSeen) > 90*time.Second || updater.CurrentVersion != current.Version {
		plan.Reason = "host updater has not reported fresh ready state for this agent"
		return plan, nil
	}
	if !software.ValidVersion(current.Version) || !software.ValidRevision(current.Revision) || !domain.ValidDigest(current.ExecutableSHA256) || current.Protocol != 1 ||
		!software.ValidVersion(manager.Version) || !software.ValidRevision(manager.Revision) || manager.Protocol != 1 ||
		!release.Supports(current.Platform) || !release.Compatibility.Manager.Contains(manager.Version) {
		plan.Reason = "signed compatibility or current build evidence is missing"
		return plan, nil
	}
	order, _ := software.Compare(version, current.Version)
	if action == "upgrade" && order <= 0 || action == "rollback" && order >= 0 {
		plan.Reason = "target version does not match the requested direction"
		return plan, nil
	}
	if action == "rollback" {
		if updater.PreviousVersion != version {
			plan.Reason = "host updater has not confirmed a retained previous release"
			return plan, nil
		}
		history, err := db.ListSoftwareUpdates(r.Context(), org, nodeID)
		if err != nil {
			return plan, err
		}
		eligible := false
		for _, update := range history {
			if update.Status == "succeeded" && update.TargetVersion == current.Version && update.ExpectedVersion == version {
				eligible = true
				break
			}
		}
		if !eligible {
			plan.Reason = "target is not the retained previous managed release"
			return plan, nil
		}
	}
	if installations, ok := s.config.Store.(store.InstallationSoftwareStore); ok {
		if _, err := installations.ActiveInstallationUpdate(r.Context(), org); err == nil {
			plan.Reason = "a manager upgrade is active; finish it before changing an agent"
			return plan, nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return plan, err
		}
	}
	plan.Eligible = true
	return plan, nil
}

func (s *Server) planSoftware(w http.ResponseWriter, r *http.Request, org string) {
	plan, err := s.softwarePreflight(r, org, r.PathValue("node"), r.URL.Query().Get("version"), r.URL.Query().Get("action"))
	if err != nil {
		softwarePlanError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"plan": plan})
}

func softwarePlanError(w http.ResponseWriter, err error) {
	if err.Error() == "verified release catalog unavailable" {
		fail(w, 503, "verified release catalog unavailable")
		return
	}
	storeError(w, err)
}

func (s *Server) createSoftware(w http.ResponseWriter, r *http.Request, org string) {
	var input struct {
		RequestID       string `json:"request_id"`
		Action          string `json:"action"`
		Version         string `json:"version"`
		ExpectedVersion string `json:"expected_version"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	db, ok := s.softwareStorage(w)
	if !ok {
		return
	}
	if !domain.ValidID(input.RequestID) {
		fail(w, 400, "valid request_id required")
		return
	}
	if prior, err := db.SoftwareUpdateByRequest(r.Context(), org, input.RequestID); err == nil {
		if prior.NodeID != r.PathValue("node") || prior.Action != input.Action || prior.TargetVersion != input.Version || prior.ExpectedVersion != input.ExpectedVersion {
			fail(w, 409, "request_id is already bound to another software update")
			return
		}
		writeJSON(w, 200, map[string]any{"update": prior})
		return
	} else if !errors.Is(err, domain.ErrNotFound) {
		storeError(w, err)
		return
	}
	plan, err := s.softwarePreflight(r, org, r.PathValue("node"), input.Version, input.Action)
	if err != nil {
		softwarePlanError(w, err)
		return
	}
	if !plan.Eligible || plan.Current.Version != input.ExpectedVersion {
		fail(w, 409, "software preflight no longer permits this update")
		return
	}
	release := plan.Release
	update, err := db.CreateSoftwareUpdate(r.Context(), org, r.PathValue("node"), store.SoftwareRequest{
		RequestID: input.RequestID, Action: input.Action, TargetVersion: release.Version, TargetRevision: release.Revision,
		AgentSHA256: release.AgentDigests[plan.Current.Platform], Platform: plan.Current.Platform,
		ManifestSHA256: release.ManifestSHA256, Publisher: release.Publisher, ExpectedVersion: input.ExpectedVersion,
	})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"update": update})
}

func (s *Server) cancelSoftware(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.softwareStorage(w)
	if !ok {
		return
	}
	update, err := db.TransitionSoftwareUpdate(r.Context(), org, r.PathValue("node"), r.PathValue("update"), "cancelled", "")
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"update": update})
}

func (s *Server) heartbeatSoftware(w http.ResponseWriter, r *http.Request) {
	node, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	var input software.Updater
	if !readJSON(w, r, &input) {
		return
	}
	db, ok := s.softwareStorage(w)
	if !ok {
		return
	}
	value, err := db.HeartbeatSoftwareUpdater(r.Context(), node.OrganizationID, node.ID, input)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"updater": value})
}

func (s *Server) desiredSoftware(w http.ResponseWriter, r *http.Request) {
	node, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	db, ok := s.softwareStorage(w)
	if !ok {
		return
	}
	update, err := db.ActiveSoftwareUpdate(r.Context(), node.OrganizationID, node.ID)
	if errors.Is(err, domain.ErrNotFound) {
		writeJSON(w, 200, map[string]any{"update": nil, "manager_build": s.buildInfo()})
		return
	}
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"update": update, "manager_build": s.buildInfo()})
}

func (s *Server) reportSoftware(w http.ResponseWriter, r *http.Request) {
	node, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	var input struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	db, ok := s.softwareStorage(w)
	if !ok {
		return
	}
	active, err := db.ActiveSoftwareUpdate(r.Context(), node.OrganizationID, node.ID)
	if err != nil {
		storeError(w, err)
		return
	}
	if active.ID != r.PathValue("update") {
		fail(w, 404, "update not found")
		return
	}
	if input.Status == "succeeded" || input.Status == "rolled_back" || input.Status == "recovered" {
		builds, ok := s.config.Store.(store.AgentBuildStore)
		if !ok {
			fail(w, 503, "agent build inventory unavailable")
			return
		}
		rows, err := builds.AgentBuilds(r.Context(), node.OrganizationID, []string{node.ID})
		if err != nil {
			storeError(w, err)
			return
		}
		observed := rows[node.ID]
		if !observed.Connected || observed.Build == nil || time.Since(observed.LastSeen) > 6*time.Second || observed.Build.Protocol != 1 || observed.Build.Platform != active.Platform {
			fail(w, 409, "fresh post-change agent discovery required")
			return
		}
		if input.Status == "succeeded" && (observed.Build.Version != active.TargetVersion || observed.Build.Revision != active.TargetRevision || observed.Build.ExecutableSHA256 != active.AgentSHA256) ||
			input.Status != "succeeded" && observed.Build.Version != active.ExpectedVersion {
			fail(w, 409, "observed agent build differs from the reported result")
			return
		}
	}
	update, err := db.TransitionSoftwareUpdate(r.Context(), node.OrganizationID, node.ID, active.ID, input.Status, input.ErrorCode)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"update": update})
}
