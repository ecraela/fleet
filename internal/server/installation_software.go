package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
	"github.com/ecraela/fleet/internal/store"
)

type installationPlan struct {
	Release          software.Release  `json:"release"`
	Current          buildinfo.Info    `json:"current"`
	Action           string            `json:"action"`
	Eligible         bool              `json:"eligible"`
	Reason           string            `json:"reason,omitempty"`
	TrafficImpact    string            `json:"traffic_impact"`
	ManagementImpact string            `json:"management_impact"`
	AgentCount       int               `json:"agent_count"`
	Updater          *software.Updater `json:"updater,omitempty"`
}

func (s *Server) installationStorage(w http.ResponseWriter) (store.InstallationSoftwareStore, bool) {
	db, ok := s.config.Store.(store.InstallationSoftwareStore)
	if !ok {
		fail(w, 503, "installation software storage unavailable")
	}
	return db, ok
}

func (s *Server) installationSoftware(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.installationStorage(w)
	if !ok {
		return
	}
	updates, err := db.ListInstallationUpdates(r.Context(), org)
	if err != nil {
		storeError(w, err)
		return
	}
	var updater *software.Updater
	if value, err := db.InstallationUpdater(r.Context(), org); err == nil {
		updater = &value
	} else if !errors.Is(err, domain.ErrNotFound) {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"updates": updates, "updater": updater, "observed": s.buildInfo()})
}

func (s *Server) agentVersions(r *http.Request, org string) (map[string]string, error) {
	nodes, err := s.config.Store.ListNodes(r.Context(), org)
	if err != nil {
		return nil, err
	}
	if len(nodes) > 5000 {
		return nil, errors.New("installation exceeds upgrade preflight limit")
	}
	builds, ok := s.config.Store.(store.AgentBuildStore)
	if !ok {
		return nil, errors.New("agent build inventory unavailable")
	}
	versions := map[string]string{}
	for start := 0; start < len(nodes); start += store.FleetPageSize {
		end := min(start+store.FleetPageSize, len(nodes))
		ids := make([]string, 0, end-start)
		for _, node := range nodes[start:end] {
			if node.RevokedAt == nil {
				ids = append(ids, node.ID)
			}
		}
		observed, err := builds.AgentBuilds(r.Context(), org, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			row, found := observed[id]
			if !found || row.Build == nil || !row.Connected || time.Since(row.LastSeen) > 6*time.Second ||
				row.Build.Protocol != 1 || !software.ValidVersion(row.Build.Version) || !software.ValidRevision(row.Build.Revision) || !domain.ValidDigest(row.Build.ExecutableSHA256) {
				return nil, errors.New("all known agents need fresh compatible build discovery")
			}
			versions[id] = row.Build.Version
		}
	}
	return versions, nil
}

func (s *Server) installationPreflight(r *http.Request, org, version, action string) (installationPlan, error) {
	var plan installationPlan
	if !software.ValidVersion(version) || (action != "upgrade" && action != "rollback") {
		return plan, domain.ErrInvalidInput
	}
	catalog, err := software.LoadCatalog(s.config.ReleaseCatalogFile)
	if err != nil {
		return plan, errors.New("verified release catalog unavailable")
	}
	release, found := catalog.Release(version)
	if !found {
		return plan, domain.ErrNotFound
	}
	current := s.buildInfo()
	plan = installationPlan{Release: release, Current: current, Action: action,
		TrafficImpact:    "HAProxy continues serving the last deployed configuration",
		ManagementImpact: "Manager, ingest, console and telemetry pause while the control plane restarts"}
	if !release.ManagedUpgrades {
		plan.Reason = "target manager release does not support managed update reporting"
		return plan, nil
	}
	if org != s.config.InstallationOrganizationID || s.config.SoftwareHostTokenSHA256 == "" {
		plan.Reason = "host updater is unavailable for this installation"
		return plan, nil
	}
	db, ok := s.config.Store.(store.InstallationSoftwareStore)
	if !ok {
		plan.Reason = "installation update storage unavailable"
		return plan, nil
	}
	updater, err := db.InstallationUpdater(r.Context(), org)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return plan, err
	}
	if err == nil {
		plan.Updater = &updater
	}
	if err != nil || !updater.Ready || time.Since(updater.LastSeen) > 90*time.Second || updater.CurrentVersion != current.Version || updater.IngestVersion != current.Version {
		plan.Reason = "host updater has not reported fresh ready state for this manager"
		return plan, nil
	}
	if !software.ValidVersion(current.Version) || !software.ValidRevision(current.Revision) || !domain.ValidDigest(current.ExecutableSHA256) || current.Protocol != 1 ||
		!release.Supports(current.Platform) || !release.Compatibility.Manager.Contains(version) {
		plan.Reason = "signed compatibility or current manager build evidence is missing"
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
		history, err := db.ListInstallationUpdates(r.Context(), org)
		if err != nil {
			return plan, err
		}
		eligible := false
		for _, previous := range history {
			if previous.Status == "succeeded" && previous.TargetVersion == current.Version && previous.ExpectedVersion == version {
				eligible = true
				break
			}
		}
		if !eligible {
			plan.Reason = "target is not the retained previous managed release"
			return plan, nil
		}
	}
	active, err := db.HasActiveSoftwareUpdates(r.Context(), org)
	if err != nil {
		return plan, err
	}
	if active {
		plan.Reason = "an agent upgrade is active; finish it before upgrading the manager"
		return plan, nil
	}
	if _, err := db.ActiveInstallationUpdate(r.Context(), org); err == nil {
		plan.Reason = "an installation upgrade is already active"
		return plan, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return plan, err
	}
	versions, err := s.agentVersions(r, org)
	if err != nil {
		plan.Reason = err.Error()
		return plan, nil
	}
	for _, v := range versions {
		if !release.Compatibility.Agent.Contains(v) {
			plan.Reason = "target manager is incompatible with a known agent release"
			return plan, nil
		}
	}
	plan.AgentCount = len(versions)
	plan.Eligible = true
	return plan, nil
}

func (s *Server) planInstallationSoftware(w http.ResponseWriter, r *http.Request, org string) {
	plan, err := s.installationPreflight(r, org, r.URL.Query().Get("version"), r.URL.Query().Get("action"))
	if err != nil {
		softwarePlanError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"plan": plan})
}

func (s *Server) createInstallationSoftware(w http.ResponseWriter, r *http.Request, org string) {
	var input struct {
		RequestID       string `json:"request_id"`
		Action          string `json:"action"`
		Version         string `json:"version"`
		ExpectedVersion string `json:"expected_version"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	db, ok := s.installationStorage(w)
	if !ok {
		return
	}
	if !domain.ValidID(input.RequestID) {
		fail(w, 400, "valid request_id required")
		return
	}
	if prior, err := db.InstallationUpdateByRequest(r.Context(), org, input.RequestID); err == nil {
		if prior.Action != input.Action || prior.TargetVersion != input.Version || prior.ExpectedVersion != input.ExpectedVersion {
			fail(w, 409, "request_id is already bound to another installation update")
			return
		}
		writeJSON(w, 200, map[string]any{"update": prior})
		return
	} else if !errors.Is(err, domain.ErrNotFound) {
		storeError(w, err)
		return
	}
	plan, err := s.installationPreflight(r, org, input.Version, input.Action)
	if err != nil {
		softwarePlanError(w, err)
		return
	}
	if !plan.Eligible || plan.Current.Version != input.ExpectedVersion {
		fail(w, 409, "installation preflight no longer permits this update")
		return
	}
	u, err := db.CreateInstallationUpdate(r.Context(), org, store.InstallationRequest{
		RequestID: input.RequestID, Action: input.Action, TargetVersion: plan.Release.Version,
		TargetRevision: plan.Release.Revision, ManagerSHA256: plan.Release.ManagerDigests[plan.Current.Platform],
		Platform: plan.Current.Platform, ManifestSHA256: plan.Release.ManifestSHA256,
		Publisher: plan.Release.Publisher, ExpectedVersion: input.ExpectedVersion,
	})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"update": u})
}

func (s *Server) cancelInstallationSoftware(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.installationStorage(w)
	if !ok {
		return
	}
	u, err := db.TransitionInstallationUpdate(r.Context(), org, r.PathValue("update"), "cancelled", "")
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"update": u})
}

func (s *Server) softwareHost(r *http.Request) (string, bool) {
	if s.config.SoftwareHostTokenSHA256 == "" || s.config.InstallationOrganizationID == "" {
		return "", false
	}
	value := r.Header.Get("Authorization")
	if len(value) < len("Bearer ")+32 || len(value) > len("Bearer ")+256 || value[:len("Bearer ")] != "Bearer " {
		return "", false
	}
	sum := sha256.Sum256([]byte(value[len("Bearer "):]))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(s.config.SoftwareHostTokenSHA256)) != 1 ||
		r.Header.Get("X-Organization-ID") != s.config.InstallationOrganizationID {
		return "", false
	}
	return s.config.InstallationOrganizationID, true
}

func (s *Server) hostDesiredInstallationSoftware(w http.ResponseWriter, r *http.Request) {
	org, ok := s.softwareHost(r)
	if !ok {
		fail(w, 401, "valid host updater credential required")
		return
	}
	db, ok := s.installationStorage(w)
	if !ok {
		return
	}
	u, err := db.ActiveInstallationUpdate(r.Context(), org)
	if errors.Is(err, domain.ErrNotFound) {
		writeJSON(w, 200, map[string]any{"update": nil, "manager_build": s.buildInfo()})
		return
	}
	if err != nil {
		storeError(w, err)
		return
	}
	versions, err := s.agentVersions(r, org)
	if err != nil {
		fail(w, 409, "fresh agent cohort evidence required")
		return
	}
	writeJSON(w, 200, map[string]any{"update": u, "manager_build": s.buildInfo(), "agent_versions": versions})
}

func (s *Server) hostHeartbeatInstallationSoftware(w http.ResponseWriter, r *http.Request) {
	org, ok := s.softwareHost(r)
	if !ok {
		fail(w, 401, "valid host updater credential required")
		return
	}
	var input software.Updater
	if !readJSON(w, r, &input) {
		return
	}
	db, ok := s.installationStorage(w)
	if !ok {
		return
	}
	value, err := db.HeartbeatInstallationUpdater(r.Context(), org, input)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"updater": value})
}

func (s *Server) hostReportInstallationSoftware(w http.ResponseWriter, r *http.Request) {
	org, ok := s.softwareHost(r)
	if !ok {
		fail(w, 401, "valid host updater credential required")
		return
	}
	var input struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	db, ok := s.installationStorage(w)
	if !ok {
		return
	}
	u, err := db.ActiveInstallationUpdate(r.Context(), org)
	if err != nil {
		storeError(w, err)
		return
	}
	if u.ID != r.PathValue("update") {
		fail(w, 404, "installation update not found")
		return
	}
	if input.Status == "succeeded" || input.Status == "rolled_back" || input.Status == "recovered" {
		build := s.buildInfo()
		updater, err := db.InstallationUpdater(r.Context(), org)
		if err != nil || time.Since(updater.LastSeen) > 90*time.Second || updater.IngestVersion != build.Version {
			fail(w, 409, "fresh matching ingest build evidence required")
			return
		}
		if input.Status == "succeeded" && (build.Version != u.TargetVersion || build.Revision != u.TargetRevision || build.ExecutableSHA256 != u.ManagerSHA256 || build.Platform != u.Platform) ||
			input.Status != "succeeded" && build.Version != u.ExpectedVersion {
			fail(w, 409, "running manager build differs from reported installation result")
			return
		}
	}
	value, err := db.TransitionInstallationUpdate(r.Context(), org, u.ID, input.Status, input.ErrorCode)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"update": value})
}
