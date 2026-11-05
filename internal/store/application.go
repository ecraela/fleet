package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed application_schema.sql
var applicationSchemaSQL string

//go:embed application_lifecycle_schema.sql
var applicationLifecycleSchemaSQL string

type applicationWriterKey struct{}
type ApplicationStore interface {
	ListApplications(context.Context, string, string) ([]domain.ApplicationRevision, error)
	DeployApplication(context.Context, string, string, string, string, domain.ApplicationInput) (domain.ApplicationRevision, error)
}

func cloneApplicationRevision(r domain.ApplicationRevision) domain.ApplicationRevision {
	r.Spec = r.Spec.Canonical()
	return r
}
func configureMemoryApplications(tx *rolloutTransaction, m *Memory, org, cluster string) {
	tx.applicationRevisions = func() ([]domain.ApplicationRevision, error) {
		out := []domain.ApplicationRevision{}
		for _, r := range m.applicationRevisions {
			if r.OrganizationID == org && r.ClusterID == cluster {
				r = cloneApplicationRevision(r)
				r.Status = m.rollouts[r.RolloutID].Status
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
		return out, nil
	}
	tx.saveApplicationRevision = func(r domain.ApplicationRevision) error {
		m.applicationRevisions[r.ID] = cloneApplicationRevision(r)
		return nil
	}
}
func configurePostgresApplications(tx *rolloutTransaction, db pgx.Tx, org, cluster string) {
	tx.applicationRevisions = func() ([]domain.ApplicationRevision, error) {
		rows, err := db.Query(tx.ctx, `SELECT a.payload,a.request_digest,r.status FROM hm_application_revisions a JOIN hm_rollouts r ON (r.id,r.cluster_id,r.organization_id)=(a.rollout_id,a.cluster_id,a.organization_id) WHERE a.organization_id=$1 AND a.cluster_id=$2 ORDER BY a.created_at DESC LIMIT 256`, org, cluster)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []domain.ApplicationRevision{}
		for rows.Next() {
			var raw []byte
			var digest, status string
			var r domain.ApplicationRevision
			if err = rows.Scan(&raw, &digest, &status); err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
			r.RequestDigest = digest
			r.Status = status
			out = append(out, r)
		}
		return out, rows.Err()
	}
	tx.saveApplicationRevision = func(r domain.ApplicationRevision) error {
		if _, err := db.Exec(tx.ctx, `INSERT INTO hm_applications(id,organization_id,cluster_id) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, r.ApplicationID, org, cluster); err != nil {
			return err
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = db.Exec(tx.ctx, `INSERT INTO hm_application_revisions(id,organization_id,cluster_id,application_id,number,request_id,request_digest,rollout_id,created_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, r.ID, org, cluster, r.ApplicationID, r.Number, r.RequestID, r.RequestDigest, r.RolloutID, r.CreatedAt, raw)
		return err
	}
}
func listApplications(ctx context.Context, p rolloutPersistence, org, cluster string) ([]domain.ApplicationRevision, error) {
	var out []domain.ApplicationRevision
	err := p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error { var err error; out, err = tx.applicationRevisions(); return err })
	return out, err
}
func deployApplication(ctx context.Context, p rolloutPersistence, org, cluster, app, rollback string, input domain.ApplicationInput) (domain.ApplicationRevision, error) {
	var out domain.ApplicationRevision
	if !domain.ValidID(input.RequestID) || input.ExpectedRevision < 0 || (app != "" && !domain.ValidID(app)) || (rollback != "" && !domain.ValidID(rollback)) || input.Entrypoint == nil {
		return out, domain.ErrInvalidInput
	}
	if input.Action != "" && (app == "" || rollback != "" || !slices.Contains([]string{"disable", "enable", "delete", "restore"}, input.Action)) {
		return out, domain.ErrInvalidInput
	}
	input.Spec = input.Spec.Canonical()
	raw, _ := json.Marshal(struct {
		App, Rollback string
		Input         domain.ApplicationInput
	}{app, rollback, input})
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	err := p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		records, err := tx.applicationRevisions()
		if err != nil {
			return err
		}
		var latest *domain.ApplicationRevision
		var restored *domain.ApplicationRevision
		ids := map[string]domain.ApplicationRevision{}
		for _, r := range records {
			if r.RequestID == input.RequestID {
				if r.RequestDigest != digest {
					return domain.ErrConflict
				}
				out = cloneApplicationRevision(r)
				return nil
			}
			if ids[r.ApplicationID].Number < r.Number {
				ids[r.ApplicationID] = r
			}
			if r.ApplicationID == app {
				v := r
				if latest == nil || r.Number > latest.Number {
					latest = &v
				}
				if r.ID == rollback {
					restored = &v
				}
			}
		}
		if _, err := tx.get(input.RequestID, true); err == nil {
			return domain.ErrConflict
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if len(records) >= 256 {
			return domain.ErrConflict
		}
		if app != "" && latest == nil {
			return domain.ErrNotFound
		}
		if latest != nil && (input.ExpectedRevision != latest.Number || latest.Number >= 64) {
			return domain.ErrConflict
		}
		if app == "" {
			live := 0
			for _, r := range ids {
				if !domain.EffectiveApplication(r, records).Deleted {
					live++
				}
			}
			if input.ExpectedRevision != 0 || live >= domain.MaxApplications || rollback != "" {
				return domain.ErrConflict
			}
			app = domain.NewID()
		}
		deleted := false
		if latest != nil {
			source := domain.EffectiveApplication(*latest, records)
			if input.Action == "" && rollback == "" && (source.Deleted || input.Spec.Disabled != source.Spec.Disabled) {
				return domain.ErrConflict
			}
			if input.Action != "" {
				input.Spec = source.Spec
				switch input.Action {
				case "disable":
					if source.Deleted || source.Spec.Disabled {
						return domain.ErrConflict
					}
					input.Spec.Disabled = true
				case "enable":
					if source.Deleted || !source.Spec.Disabled {
						return domain.ErrConflict
					}
					input.Spec.Disabled = false
				case "delete":
					if source.Deleted {
						return domain.ErrConflict
					}
					deleted = true
				case "restore":
					if !source.Deleted {
						return domain.ErrConflict
					}
					input.Spec.Disabled = false
				}
			}
		} else if input.Spec.Disabled {
			return domain.ErrInvalidInput
		}
		if rollback != "" {
			if restored == nil {
				return domain.ErrNotFound
			}
			if restored.Deleted {
				return domain.ErrConflict
			}
			input.Spec = restored.Spec
			deleted = restored.Deleted
		}
		input.Spec.ID = app
		if !input.Spec.Valid() {
			return domain.ErrInvalidInput
		}
		if tx.cluster.Topology == nil || input.Spec.Listener != tx.cluster.Topology.Frontend {
			return domain.ErrConflict
		}
		// Capture the whole shared-listener baseline. Different projections or
		// unknown ownership require operator repair, never implicit adoption.
		var base *domain.ConfigurationDocument
		for _, node := range tx.nodes {
			err = p.controlTransaction(tx.ctx, org, node.ID, func(c *controlTransaction) error {
				if !configurationEnabled(c.state, controlNow(ctx)) {
					return domain.ErrConflict
				}
				version := c.state.Discovery.Configuration.ApplicationControlVersion
				if input.Spec.TrafficPolicy != nil && (version < 6 || !input.Spec.TrafficPolicy.Supported(c.state.Discovery.Configuration.TrafficRules)) {
					return domain.ErrConflict
				}
				if (input.Spec.RequestTelemetry && (version < 5 || !c.state.Discovery.Configuration.RequestLogsAllowed)) || version < 1 || version > 6 || ((input.Action != "" || input.Spec.Disabled || deleted) && version < 2) || (input.Spec.Verification != nil && (version < 3 || (input.Spec.Verification.Entrypoint && version < 4))) {
					return domain.ErrConflict
				}
				d := c.state.Discovery.Configuration.Document.Canonical()
				comparable := d.Canonical()
				comparable.Applications = nil
				for _, a := range d.Applications {
					if a.ID != app {
						comparable.Applications = append(comparable.Applications, a)
					}
				}
				listener := false
				for _, proxy := range c.state.Discovery.Proxies {
					listener = listener || (proxy.Kind == "frontend" && proxy.Name == input.Spec.Listener && proxy.Mode == "http" && proxy.Status == "OPEN")
				}
				if !listener {
					return domain.ErrConflict
				}
				if base != nil && !base.Equal(comparable) {
					return domain.ErrConflict
				}
				for _, a := range d.Applications {
					known := false
					for _, r := range records {
						known = known || ((r.Status == "succeeded" || a.ID == app) && a.Equal(r.Spec))
					}
					if !known {
						return domain.ErrConflict
					}
				}
				base = &comparable
				return nil
			})
			if err != nil {
				return err
			}
		}
		if base == nil {
			return domain.ErrConflict
		}
		desired := base.Canonical()
		desired.Applications = nil
		for _, a := range base.Applications {
			if a.ID != app {
				desired.Applications = append(desired.Applications, a)
			}
		}
		if !deleted {
			desired.Applications = append(desired.Applications, input.Spec)
		}
		in := domain.RolloutInput{RequestID: input.RequestID, Document: desired.Canonical(), Nodes: input.Nodes, Entrypoint: input.Entrypoint, DrainTimeoutSeconds: 30}
		if !in.Valid() {
			return domain.ErrInvalidInput
		}
		writer := context.WithValue(ctx, applicationWriterKey{}, true)
		r, err := createRolloutInTransaction(writer, tx, org, cluster, in)
		if err != nil {
			return err
		}
		number := 1
		if latest != nil {
			number = latest.Number + 1
		}
		out = domain.ApplicationRevision{ID: domain.NewID(), OrganizationID: org, ClusterID: cluster, ApplicationID: app, Number: number, RequestID: input.RequestID, RequestDigest: digest, Spec: input.Spec.Canonical(), Deleted: deleted, RolloutID: r.ID, RollbackOf: rollback, ActorUserID: domain.Actor(ctx), CreatedAt: time.Now().UTC(), Status: r.Status}
		if err = tx.saveApplicationRevision(out); err != nil {
			return err
		}
		action := "application.deployed"
		if rollback != "" {
			action = "application.rollback"
		}
		if input.Action != "" {
			action = map[string]string{"disable": "application.disabled", "enable": "application.enabled", "delete": "application.deleted", "restore": "application.restored"}[input.Action]
		}
		return tx.audit(r, action)
	})
	return cloneApplicationRevision(out), err
}
func (m *Memory) ListApplications(ctx context.Context, org, cluster string) ([]domain.ApplicationRevision, error) {
	return listApplications(ctx, m, org, cluster)
}
func (p *Postgres) ListApplications(ctx context.Context, org, cluster string) ([]domain.ApplicationRevision, error) {
	return listApplications(ctx, p, org, cluster)
}
func (m *Memory) DeployApplication(ctx context.Context, org, cluster, app, rollback string, in domain.ApplicationInput) (domain.ApplicationRevision, error) {
	return deployApplication(ctx, m, org, cluster, app, rollback, in)
}
func (p *Postgres) DeployApplication(ctx context.Context, org, cluster, app, rollback string, in domain.ApplicationInput) (domain.ApplicationRevision, error) {
	return deployApplication(ctx, p, org, cluster, app, rollback, in)
}
