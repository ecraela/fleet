package store

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

//go:embed configuration_schema.sql
var configurationSchemaSQL string

// Automatic observations cannot allocate an unlimited immutable history.
// An authorized edit can still capture its exact base before changing it.
const discoveredRevisionLimit = 1024

type ConfigurationStore interface {
	GetConfiguration(context.Context, string, string) (domain.NodeControl, []domain.ConfigurationRevision, error)
	CreateConfiguration(context.Context, string, domain.Operation, string) (domain.Operation, error)
}

func configurationEnabled(s domain.NodeControl, now time.Time) bool {
	return s.Discovery != nil && s.Discovery.Fresh(now) && s.Discovery.Configuration != nil && s.Discovery.Configuration.Fresh(now) && s.Discovery.Configuration.ControlAllowed && now.Sub(s.LastSeen) < 6*time.Second
}
func cloneRevision(r domain.ConfigurationRevision) domain.ConfigurationRevision {
	r.Document = r.Document.Canonical()
	return r
}
func cloneOperation(o domain.Operation) domain.Operation {
	if o.Configuration != nil {
		d := o.Configuration.Canonical()
		o.Configuration = &d
	}
	return o
}
func saveControlOperation(tx *controlTransaction, o domain.Operation) error {
	if err := tx.save(o); err != nil {
		return err
	}
	if o.ConfigurationRevisionID == "" {
		return nil
	}
	r, err := tx.revision(o.ConfigurationRevisionID)
	if err != nil {
		return err
	}
	r.Status = o.Status
	if o.Status == "succeeded" {
		r.Status = "applied"
	}
	r.ErrorCode = o.ErrorCode
	r.CompletedAt = o.CompletedAt
	return tx.saveRevision(r)
}
func observeConfiguration(tx *controlTransaction, captureBase bool) error {
	tx.state.ConfigurationHistoryLimited = false
	if tx.state.Discovery == nil || tx.state.Discovery.Configuration == nil || !tx.state.Discovery.Configuration.Fresh(tx.now) {
		return nil
	}
	state := tx.state.Discovery.Configuration
	if tx.state.PendingOperationID != "" {
		op, err := tx.get(tx.state.PendingOperationID, false)
		if err != nil {
			return err
		}
		if op.Kind == "configuration" && !op.Terminal() {
			return nil
		}
	}
	if tx.state.CurrentRevisionID != "" {
		r, err := tx.revision(tx.state.CurrentRevisionID)
		if err != nil {
			return err
		}
		if r.ArtifactID == state.ArtifactID {
			return nil
		}
	}
	if !captureBase {
		full, err := tx.observationHistoryFull()
		if err != nil {
			return err
		}
		if full {
			tx.state.ConfigurationHistoryLimited = true
			return nil
		}
	}
	r := domain.ConfigurationRevision{ID: domain.NewID(), OrganizationID: tx.node.OrganizationID, NodeID: tx.node.ID, Number: tx.revisionNumber + 1,
		ParentID: tx.state.CurrentRevisionID, ArtifactID: state.ArtifactID, Document: state.Document.Canonical(), Source: "discovered", Status: "observed", CreatedAt: tx.now}
	if err := tx.saveRevision(r); err != nil {
		return err
	}
	tx.state.CurrentRevisionID = r.ID
	return tx.audit("configuration.discovered", "")
}
func createConfiguration(ctx context.Context, p controlPersistence, org string, input domain.Operation, rollback string) (domain.Operation, error) {
	var out domain.Operation
	if !domain.ValidID(input.RequestID) || !domain.ValidID(input.NodeID) || !domain.ValidDigest(input.ExpectedGeneration) || !domain.ValidDigest(input.BaseArtifactID) || input.BaseVersion < 1 || (rollback != "" && !domain.ValidID(rollback)) {
		return out, domain.ErrInvalidInput
	}
	if rollback == "" && (input.Configuration == nil || !input.Configuration.Valid()) {
		return out, domain.ErrInvalidInput
	}
	err := p.controlTransaction(ctx, org, input.NodeID, func(tx *controlTransaction) error {
		if err := expirePending(tx); err != nil {
			return err
		}
		target := input.Configuration
		var restored domain.ConfigurationRevision
		if rollback != "" {
			r, err := tx.revision(rollback)
			if err != nil {
				return err
			}
			if r.ArtifactID == "" || (r.Status != "applied" && r.Status != "observed") {
				return domain.ErrConflict
			}
			restored = r
			doc := r.Document.Canonical()
			target = &doc
		}
		existing, err := tx.get(input.RequestID, true)
		if err == nil {
			if existing.NodeID != input.NodeID || existing.Kind != "configuration" || existing.BaseArtifactID != input.BaseArtifactID || existing.BaseVersion != input.BaseVersion || existing.ExpectedGeneration != input.ExpectedGeneration || existing.Configuration == nil || !existing.Configuration.Equal(*target) || existing.RollbackArtifactID != restored.ArtifactID {
				return domain.ErrConflict
			}
			revision, revisionErr := tx.revision(existing.ConfigurationRevisionID)
			if revisionErr != nil {
				return revisionErr
			}
			if revision.RollbackOf != rollback {
				return domain.ErrConflict
			}
			out = existing
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if !rolloutWriter(ctx, tx) {
			return domain.ErrConflict
		}
		now := controlNow(ctx)
		if !configurationEnabled(tx.state, now) || tx.state.PendingOperationID != "" || tx.state.Discovery.Generation != input.ExpectedGeneration {
			return domain.ErrConflict
		}
		observed := tx.state.Discovery.Configuration
		if observed.ArtifactID != input.BaseArtifactID || observed.Version != input.BaseVersion {
			return domain.ErrConflict
		}
		if rollback == "" && !target.KnownIncomingPorts(*observed.Document) {
			return domain.ErrInvalidInput
		}
		internal, _ := ctx.Value(rolloutWriterKey{}).(string)
		if internal == "" && ((!domain.SameApplications(target.Applications, observed.Document.Applications)) || !domain.SameListeners(target.Listeners, observed.Document.Listeners) || (rollback != "" && (len(observed.Document.Applications) > 0 || len(observed.Document.Listeners) > 0))) {
			return domain.ErrConflict
		}
		if rollback == "" && target.Equal(*observed.Document) {
			return domain.ErrConflict
		}
		if err := observeConfiguration(tx, true); err != nil {
			return err
		}
		input.ID = domain.NewID()
		input.Kind = "configuration"
		input.OrganizationID = org
		input.ConfigurationRevisionID = domain.NewID()
		document := target.Canonical()
		input.Configuration = &document
		input.RollbackArtifactID = restored.ArtifactID
		input.ExpectedProcessID = tx.state.Discovery.ProcessID
		input.ActorUserID = domain.Actor(ctx)
		input.CreatedAt = now
		input.Deadline = now.Add(60 * time.Second)
		if id, _ := ctx.Value(rolloutWriterKey{}).(string); id != "" {
			input.RolloutID = id
			input.Deadline = now.Add(30 * time.Second)
		}
		if id, _ := ctx.Value(maintenanceWriterKey{}).(string); id != "" {
			input.MaintenanceID = id
			input.Deadline = now.Add(30 * time.Second)
		}
		input.Status = "queued"
		revision := domain.ConfigurationRevision{ID: input.ConfigurationRevisionID, OrganizationID: org, NodeID: input.NodeID, Number: tx.revisionNumber + 1, ParentID: tx.state.CurrentRevisionID, RollbackOf: rollback,
			OperationID: input.ID, Document: document.Canonical(), Source: "edit", Status: "queued", ActorUserID: input.ActorUserID, CreatedAt: now}
		if rollback != "" {
			revision.Source = "rollback"
		}
		if !input.Valid() {
			return domain.ErrInvalidInput
		}
		if err := tx.save(input); err != nil {
			return err
		}
		if err := tx.saveRevision(revision); err != nil {
			return err
		}
		if err := tx.audit("operation.requested", input.ID); err != nil {
			return err
		}
		tx.state.PendingOperationID = input.ID
		out = input
		return nil
	})
	return cloneOperation(out), err
}
func getConfiguration(ctx context.Context, p controlPersistence, org, node string) (domain.NodeControl, []domain.ConfigurationRevision, error) {
	var state domain.NodeControl
	revisions := []domain.ConfigurationRevision{}
	err := p.controlTransaction(ctx, org, node, func(tx *controlTransaction) error {
		if err := expirePending(tx); err != nil {
			return err
		}
		state = tx.state
		state.ConfigurationHistoryLimited = false
		if state.CurrentRevisionID != "" && state.Discovery != nil && state.Discovery.Configuration != nil {
			current, err := tx.revision(state.CurrentRevisionID)
			if err != nil {
				return err
			}
			if current.ArtifactID != state.Discovery.Configuration.ArtifactID {
				full, err := tx.observationHistoryFull()
				if err != nil {
					return err
				}
				state.ConfigurationHistoryLimited = full
			}
		}
		var err error
		revisions, err = tx.revisions()
		if err == nil && state.CurrentRevisionID != "" {
			found := false
			for _, revision := range revisions {
				found = found || revision.ID == state.CurrentRevisionID
			}
			if !found {
				current, currentErr := tx.revision(state.CurrentRevisionID)
				if currentErr != nil {
					return currentErr
				}
				if len(revisions) >= 50 {
					revisions[len(revisions)-1] = current
				} else {
					revisions = append(revisions, current)
				}
			}
		}
		return err
	})
	return state, revisions, err
}
func (m *Memory) GetConfiguration(ctx context.Context, org, node string) (domain.NodeControl, []domain.ConfigurationRevision, error) {
	return getConfiguration(ctx, m, org, node)
}
func (p *Postgres) GetConfiguration(ctx context.Context, org, node string) (domain.NodeControl, []domain.ConfigurationRevision, error) {
	return getConfiguration(ctx, p, org, node)
}
func (m *Memory) CreateConfiguration(ctx context.Context, org string, o domain.Operation, rollback string) (domain.Operation, error) {
	return createConfiguration(ctx, m, org, o, rollback)
}
func (p *Postgres) CreateConfiguration(ctx context.Context, org string, o domain.Operation, rollback string) (domain.Operation, error) {
	return createConfiguration(ctx, p, org, o, rollback)
}
