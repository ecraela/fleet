package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
	"github.com/ecraela/fleet/internal/store"
)

type fleetSoftwarePlan struct {
	Release          software.Release     `json:"release"`
	Manager          buildinfo.Info       `json:"manager"`
	Eligible         bool                 `json:"eligible"`
	Reason           string               `json:"reason,omitempty"`
	Fingerprint      string               `json:"fingerprint,omitempty"`
	NodeCount        int                  `json:"node_count"`
	Steps            []software.FleetStep `json:"steps"`
	ManagementImpact string               `json:"management_impact"`
}

func (s *Server) fleetSoftwareStorage(w http.ResponseWriter) (store.FleetSoftwareStore, bool) {
	db, ok := s.config.Store.(store.FleetSoftwareStore)
	if !ok {
		fail(w, 503, "fleet update storage unavailable")
	}
	return db, ok
}

// A fleet plan snapshots every known node. The manager goes first only when
// the signed target manager accepts the entire current agent cohort. This
// ensures that every intermediate state is compatible; otherwise a bridge
// release is required before the requested target can be installed.
func (s *Server) fleetPreflight(r *http.Request, org, version string) (fleetSoftwarePlan, error) {
	plan := fleetSoftwarePlan{Manager: s.buildInfo(), Steps: []software.FleetStep{},
		ManagementImpact: "Manager, ingest, console and telemetry briefly pause during their restart; HAProxy keeps serving"}
	if !software.ValidVersion(version) {
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
	plan.Release = release
	if org != s.config.InstallationOrganizationID || s.config.SoftwareHostTokenSHA256 == "" {
		plan.Reason = "installation host updater is not configured"
		return plan, nil
	}
	if !release.ManagedUpgrades {
		plan.Reason = "target release does not support managed updates"
		return plan, nil
	}
	if !software.ValidVersion(plan.Manager.Version) || !software.ValidRevision(plan.Manager.Revision) ||
		!domain.ValidDigest(plan.Manager.ExecutableSHA256) || plan.Manager.Protocol != 1 || !release.Supports(plan.Manager.Platform) {
		plan.Reason = "manager does not report a signed compatible build"
		return plan, nil
	}
	managerChange := plan.Manager.Version != version
	if managerChange {
		order, _ := software.Compare(version, plan.Manager.Version)
		check, err := s.installationPreflight(r, org, version, fleetDirection(order))
		if err != nil {
			return plan, err
		}
		if !check.Eligible {
			plan.Reason = check.Reason
			return plan, nil
		}
		plan.Steps = append(plan.Steps, software.FleetStep{Role: "manager", Name: "Manager and ingest", ExpectedVersion: plan.Manager.Version})
	} else {
		if plan.Manager.ExecutableSHA256 != release.ManagerDigests[plan.Manager.Platform] {
			plan.Reason = "running manager digest does not match the signed target release"
			return plan, nil
		}
		installations, ok := s.config.Store.(store.InstallationSoftwareStore)
		if !ok {
			plan.Reason = "installation update storage unavailable"
			return plan, nil
		}
		if _, err := installations.ActiveInstallationUpdate(r.Context(), org); err == nil {
			plan.Reason = "a manager update is already active"
			return plan, nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return plan, err
		}
		active, err := installations.HasActiveSoftwareUpdates(r.Context(), org)
		if err != nil {
			return plan, err
		}
		if active {
			plan.Reason = "an agent update is already active"
			return plan, nil
		}
	}
	nodes, err := s.config.Store.ListNodes(r.Context(), org)
	if err != nil {
		return plan, err
	}
	if len(nodes) > 5000 {
		plan.Reason = "installation exceeds upgrade preflight limit"
		return plan, nil
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].ClusterID != nodes[j].ClusterID {
			return nodes[i].ClusterID < nodes[j].ClusterID
		}
		if nodes[i].Name != nodes[j].Name {
			return nodes[i].Name < nodes[j].Name
		}
		return nodes[i].ID < nodes[j].ID
	})
	builds, ok := s.config.Store.(store.AgentBuildStore)
	if !ok {
		plan.Reason = "agent build inventory unavailable"
		return plan, nil
	}
	agents, ok := s.config.Store.(store.SoftwareStore)
	if !ok {
		plan.Reason = "agent update storage unavailable"
		return plan, nil
	}
	inventory, ok := s.config.Store.(store.SoftwareInventoryStore)
	if !ok {
		plan.Reason = "agent updater inventory unavailable"
		return plan, nil
	}
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
			return plan, err
		}
		states, err := inventory.SoftwareInventory(r.Context(), org, ids)
		if err != nil {
			return plan, err
		}
		for _, node := range nodes[start:end] {
			if node.RevokedAt != nil {
				continue
			}
			plan.NodeCount++
			row, found := observed[node.ID]
			if !found || row.Build == nil || !row.Connected || time.Since(row.LastSeen) > 6*time.Second ||
				!software.ValidVersion(row.Build.Version) || !software.ValidRevision(row.Build.Revision) ||
				!domain.ValidDigest(row.Build.ExecutableSHA256) || row.Build.Protocol != 1 {
				plan.Reason = fmt.Sprintf("%s needs a fresh signed agent observation", node.Name)
				return plan, nil
			}
			current := row.Build
			if !release.Supports(current.Platform) || !release.Compatibility.Agent.Contains(current.Version) {
				plan.Reason = fmt.Sprintf("%s requires an intermediate compatible release", node.Name)
				return plan, nil
			}
			if current.Version == version {
				if current.ExecutableSHA256 != release.AgentDigests[current.Platform] {
					plan.Reason = fmt.Sprintf("%s reports the target version with a mismatched digest", node.Name)
					return plan, nil
				}
				continue
			}
			updater := states[node.ID].Updater
			if updater == nil || !updater.Ready || time.Since(updater.LastSeen) > 90*time.Second || updater.CurrentVersion != current.Version {
				plan.Reason = fmt.Sprintf("%s needs a fresh ready host updater", node.Name)
				return plan, nil
			}
			order, _ := software.Compare(version, current.Version)
			if order < 0 {
				if updater.PreviousVersion != version {
					plan.Reason = fmt.Sprintf("%s has not retained this previous release", node.Name)
					return plan, nil
				}
				history, err := agents.ListSoftwareUpdates(r.Context(), org, node.ID)
				if err != nil {
					return plan, err
				}
				matched := false
				for _, previous := range history {
					if previous.Status == "succeeded" && previous.TargetVersion == current.Version && previous.ExpectedVersion == version {
						matched = true
						break
					}
				}
				if !matched {
					plan.Reason = fmt.Sprintf("%s has no verified previous managed release", node.Name)
					return plan, nil
				}
			}
			plan.Steps = append(plan.Steps, software.FleetStep{Role: "agent", NodeID: node.ID, ClusterID: node.ClusterID,
				Name: node.Name, ExpectedVersion: current.Version})
		}
	}
	if len(plan.Steps) == 0 {
		plan.Reason = "all manager and agent hosts already run this release"
		return plan, nil
	}
	// Request IDs are deliberately absent from the fingerprint: retries plan
	// the same observed installation, then the server assigns durable IDs.
	snapshot, _ := json.Marshal(struct {
		Manifest string               `json:"manifest"`
		Manager  string               `json:"manager"`
		Steps    []software.FleetStep `json:"steps"`
	}{release.ManifestSHA256, plan.Manager.Version, plan.Steps})
	digest := sha256.Sum256(snapshot)
	plan.Fingerprint = hex.EncodeToString(digest[:])
	plan.Eligible = true
	return plan, nil
}

func fleetDirection(order int) string {
	if order < 0 {
		return "rollback"
	}
	return "upgrade"
}

func boundedFleetReason(reason string) string {
	if len(reason) > 256 {
		reason = reason[:256]
	}
	for !utf8.ValidString(reason) {
		reason = reason[:len(reason)-1]
	}
	return reason
}

func (s *Server) planFleetSoftware(w http.ResponseWriter, r *http.Request, org string) {
	plan, err := s.fleetPreflight(r, org, r.URL.Query().Get("version"))
	if err != nil {
		softwarePlanError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"plan": plan})
}

func (s *Server) fleetSoftware(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.fleetSoftwareStorage(w)
	if !ok {
		return
	}
	updates, err := db.ListFleetUpdates(r.Context(), org)
	if err != nil {
		storeError(w, err)
		return
	}
	var current any
	if len(updates) > 0 && updates[0].NextStep < len(updates[0].Steps) {
		step := updates[0].Steps[updates[0].NextStep]
		if step.Role == "manager" {
			if child, err := s.config.Store.(store.InstallationSoftwareStore).InstallationUpdateByRequest(r.Context(), org, step.RequestID); err == nil {
				current = child
			}
		} else if child, err := s.config.Store.(store.SoftwareStore).SoftwareUpdateByRequest(r.Context(), org, step.RequestID); err == nil {
			current = child
		}
	}
	writeJSON(w, 200, map[string]any{"updates": updates, "current_update": current})
}

func (s *Server) createFleetSoftware(w http.ResponseWriter, r *http.Request, org string) {
	var input struct {
		RequestID   string `json:"request_id"`
		Version     string `json:"version"`
		Fingerprint string `json:"fingerprint"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !domain.ValidID(input.RequestID) || !software.ValidVersion(input.Version) || !domain.ValidDigest(input.Fingerprint) {
		fail(w, 400, "valid request_id, version and plan fingerprint required")
		return
	}
	db, ok := s.fleetSoftwareStorage(w)
	if !ok {
		return
	}
	if prior, err := db.FleetUpdateByRequest(r.Context(), org, input.RequestID); err == nil {
		if prior.TargetVersion != input.Version || prior.Fingerprint != input.Fingerprint {
			fail(w, 409, "request_id is already bound to another fleet update")
			return
		}
		writeJSON(w, 200, map[string]any{"update": prior})
		return
	} else if !errors.Is(err, domain.ErrNotFound) {
		storeError(w, err)
		return
	}
	plan, err := s.fleetPreflight(r, org, input.Version)
	if err != nil {
		softwarePlanError(w, err)
		return
	}
	if !plan.Eligible || plan.Fingerprint != input.Fingerprint {
		fail(w, 409, "fleet preflight changed; review the plan again")
		return
	}
	for i := range plan.Steps {
		plan.Steps[i].RequestID = domain.NewID()
	}
	update, err := db.CreateFleetUpdate(r.Context(), org, store.FleetSoftwareRequest{RequestID: input.RequestID,
		TargetVersion: input.Version, ManifestSHA256: plan.Release.ManifestSHA256,
		Fingerprint: input.Fingerprint, Steps: plan.Steps})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"update": update})
}

// RunFleetUpdates is independent of browser sessions. Stable child request IDs
// and the store's compare-and-swap step counter make restart/retry safe.
func (s *Server) RunFleetUpdates(ctx context.Context) {
	db, ok := s.config.Store.(store.FleetSoftwareStore)
	if !ok || s.config.Role != "manager" {
		return
	}
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		updates, err := db.ActiveFleetUpdates(ctx)
		if err == nil {
			for _, update := range updates {
				if err := s.advanceFleetUpdate(ctx, db, update); err != nil && ctx.Err() == nil && !errors.Is(err, domain.ErrConflict) {
					slog.Warn("fleet update checkpoint failed", "update_id", update.ID, "error", err)
				}
			}
		} else if ctx.Err() == nil {
			slog.Warn("fleet update scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (s *Server) advanceFleetUpdate(ctx context.Context, db store.FleetSoftwareStore, update software.FleetUpdate) error {
	if update.NextStep >= len(update.Steps) {
		return nil
	}
	step := update.Steps[update.NextStep]
	pause := func(reason string) error {
		_, err := db.AdvanceFleetUpdate(ctx, update.OrganizationID, update.ID, update.NextStep, update.NextStep, "paused", boundedFleetReason(reason))
		return err
	}
	catalog, err := software.LoadCatalog(s.config.ReleaseCatalogFile)
	if err != nil {
		return pause("verified release catalog unavailable")
	}
	release, found := catalog.Release(update.TargetVersion)
	if !found || release.ManifestSHA256 != update.ManifestSHA256 {
		return pause("signed target release changed or disappeared")
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost/", nil)
	if step.Role == "manager" {
		children := s.config.Store.(store.InstallationSoftwareStore)
		child, err := children.InstallationUpdateByRequest(ctx, update.OrganizationID, step.RequestID)
		if errors.Is(err, domain.ErrNotFound) {
			order, _ := software.Compare(update.TargetVersion, step.ExpectedVersion)
			plan, err := s.installationPreflight(request, update.OrganizationID, update.TargetVersion, fleetDirection(order))
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
					return pause("manager preflight can no longer find the signed target")
				}
				return err
			}
			if !plan.Eligible || plan.Current.Version != step.ExpectedVersion {
				return pause("manager preflight changed: " + plan.Reason)
			}
			_, err = children.CreateInstallationUpdate(store.FleetStepWriter(domain.WithActor(ctx, update.ActorUserID), update.ID), update.OrganizationID, store.InstallationRequest{
				RequestID: step.RequestID, Action: fleetDirection(order), TargetVersion: release.Version, TargetRevision: release.Revision,
				ManagerSHA256: release.ManagerDigests[plan.Current.Platform], Platform: plan.Current.Platform,
				ManifestSHA256: release.ManifestSHA256, Publisher: release.Publisher, ExpectedVersion: step.ExpectedVersion})
			if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrNotFound) {
				return pause("manager update conflicted with current installation state")
			}
			return err
		}
		if err != nil {
			return err
		}
		if child.Status == "succeeded" && s.buildInfo().Version != update.TargetVersion {
			return pause("manager reported completion but still runs another version")
		}
		return s.advanceFleetChild(ctx, db, update, step.Name, child.Status, child.ErrorCode, child.CreatedAt, func() error {
			_, err := children.TransitionInstallationUpdate(ctx, update.OrganizationID, child.ID, "needs_attention", "status_unconfirmed")
			return err
		})
	}
	agents := s.config.Store.(store.SoftwareStore)
	child, err := agents.SoftwareUpdateByRequest(ctx, update.OrganizationID, step.RequestID)
	if errors.Is(err, domain.ErrNotFound) {
		order, _ := software.Compare(update.TargetVersion, step.ExpectedVersion)
		plan, err := s.softwarePreflight(request, update.OrganizationID, step.NodeID, update.TargetVersion, fleetDirection(order))
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
				return pause(step.Name + " is unavailable for update")
			}
			return err
		}
		if !plan.Eligible || plan.Current.Version != step.ExpectedVersion {
			return pause(step.Name + " preflight changed: " + plan.Reason)
		}
		_, err = agents.CreateSoftwareUpdate(store.FleetStepWriter(domain.WithActor(ctx, update.ActorUserID), update.ID), update.OrganizationID, step.NodeID, store.SoftwareRequest{
			RequestID: step.RequestID, Action: fleetDirection(order), TargetVersion: release.Version, TargetRevision: release.Revision,
			AgentSHA256: release.AgentDigests[plan.Current.Platform], Platform: plan.Current.Platform,
			ManifestSHA256: release.ManifestSHA256, Publisher: release.Publisher, ExpectedVersion: step.ExpectedVersion})
		if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrNotFound) {
			return pause(step.Name + " update conflicted with current node state")
		}
		return err
	}
	if err != nil {
		return err
	}
	if child.Status == "succeeded" {
		if child.CompletedAt == nil {
			return pause(step.Name + " reported success without a completion time")
		}
		node, err := s.config.Store.GetNode(ctx, update.OrganizationID, step.NodeID)
		if err != nil || node.RevokedAt != nil {
			return pause(step.Name + " was removed before post-update health verification")
		}
		observed := []FleetNode{{Node: node}}
		healthErr := s.fleetObservations(ctx, update.OrganizationID, observed)
		if healthErr != nil || !observed[0].TelemetryFresh || !observed[0].ScrapeOK ||
			observed[0].ObservedAt == nil || !observed[0].ObservedAt.After(*child.CompletedAt) {
			if time.Since(*child.CompletedAt) > 90*time.Second {
				return pause(step.Name + " lacks a healthy post-update HAProxy scrape")
			}
			return nil
		}
	}
	return s.advanceFleetChild(ctx, db, update, step.Name, child.Status, child.ErrorCode, child.CreatedAt, func() error {
		_, err := agents.TransitionSoftwareUpdate(ctx, update.OrganizationID, step.NodeID, child.ID, "needs_attention", "status_unconfirmed")
		return err
	})
}

func (s *Server) advanceFleetChild(ctx context.Context, db store.FleetSoftwareStore, update software.FleetUpdate, name, status, code string, created time.Time, timeout func() error) error {
	if status == "succeeded" {
		next := update.NextStep + 1
		state := "running"
		if next == len(update.Steps) {
			state = "succeeded"
		}
		_, err := db.AdvanceFleetUpdate(ctx, update.OrganizationID, update.ID, update.NextStep, next, state, "")
		return err
	}
	if status == "needs_attention" || software.TerminalStatus(status) {
		reason := name + " update ended as " + status + " (" + code + ")"
		_, err := db.AdvanceFleetUpdate(ctx, update.OrganizationID, update.ID, update.NextStep, update.NextStep, "paused", boundedFleetReason(reason))
		return err
	}
	if time.Since(created) > 30*time.Minute {
		if err := timeout(); err != nil {
			return err
		}
		_, err := db.AdvanceFleetUpdate(ctx, update.OrganizationID, update.ID, update.NextStep, update.NextStep, "paused", name+" update did not finish within 30 minutes")
		return err
	}
	if update.Status == "queued" {
		_, err := db.AdvanceFleetUpdate(ctx, update.OrganizationID, update.ID, update.NextStep, update.NextStep, "running", "")
		return err
	}
	return nil
}
