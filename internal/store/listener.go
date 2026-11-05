package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed listener_schema.sql
var listenerSchemaSQL string

type listenerWriterKey struct{}
type ListenerStore interface {
	ListListeners(context.Context, string, string) ([]domain.ListenerRevision, error)
	DeployListeners(context.Context, string, string, string, domain.ListenerInput) (domain.ListenerRevision, error)
}

func cloneListenerRevision(r domain.ListenerRevision) domain.ListenerRevision {
	r.Listeners = domain.CanonicalListeners(r.Listeners)
	if r.Listeners == nil {
		r.Listeners = []domain.HTTPSListener{}
	}
	return r
}
func configureMemoryListeners(tx *rolloutTransaction, m *Memory, org, cluster string) {
	tx.listenerRevisions = func() ([]domain.ListenerRevision, error) {
		out := []domain.ListenerRevision{}
		for _, r := range m.listenerRevisions {
			if r.OrganizationID == org && r.ClusterID == cluster {
				r = cloneListenerRevision(r)
				r.Status = m.rollouts[r.RolloutID].Status
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
		return out, nil
	}
	tx.saveListenerRevision = func(r domain.ListenerRevision) error {
		m.listenerRevisions[r.ID] = cloneListenerRevision(r)
		return nil
	}
}
func configurePostgresListeners(tx *rolloutTransaction, db pgx.Tx, org, cluster string) {
	tx.listenerRevisions = func() ([]domain.ListenerRevision, error) {
		rows, err := db.Query(tx.ctx, `SELECT l.payload,l.request_digest,r.status FROM hm_listener_revisions l JOIN hm_rollouts r ON (r.id,r.cluster_id,r.organization_id)=(l.rollout_id,l.cluster_id,l.organization_id) WHERE l.organization_id=$1 AND l.cluster_id=$2 ORDER BY l.number DESC LIMIT 64`, org, cluster)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []domain.ListenerRevision{}
		for rows.Next() {
			var raw []byte
			var digest, status string
			var r domain.ListenerRevision
			if err = rows.Scan(&raw, &digest, &status); err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
			r.RequestDigest = digest
			r.Status = status
			out = append(out, cloneListenerRevision(r))
		}
		return out, rows.Err()
	}
	tx.saveListenerRevision = func(r domain.ListenerRevision) error {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = db.Exec(tx.ctx, `INSERT INTO hm_listener_revisions(id,organization_id,cluster_id,number,request_id,request_digest,rollout_id,created_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, r.ID, org, cluster, r.Number, r.RequestID, r.RequestDigest, r.RolloutID, r.CreatedAt, raw)
		return err
	}
}
func listListeners(ctx context.Context, p rolloutPersistence, org, cluster string) ([]domain.ListenerRevision, error) {
	var out []domain.ListenerRevision
	err := p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error { var err error; out, err = tx.listenerRevisions(); return err })
	return out, err
}
func deployListeners(ctx context.Context, p rolloutPersistence, org, cluster, rollback string, input domain.ListenerInput) (domain.ListenerRevision, error) {
	var out domain.ListenerRevision
	if !domain.ValidID(input.RequestID) || input.ExpectedRevision < 0 || input.Entrypoint == nil || len(input.Nodes) < 2 || len(input.Nodes) > domain.MaxRolloutNodes || (rollback != "" && !domain.ValidID(rollback)) || !domain.ValidListeners(input.Listeners) {
		return out, domain.ErrInvalidInput
	}
	input.Listeners = domain.CanonicalListeners(input.Listeners)
	raw, _ := json.Marshal(struct {
		Input    domain.ListenerInput
		Rollback string
	}{input, rollback})
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	err := p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		var err error
		out, err = deployListenersTx(ctx, tx, org, cluster, rollback, input, digest)
		return err
	})
	return cloneListenerRevision(out), err
}
func deployListenersTx(ctx context.Context, tx *rolloutTransaction, org, cluster, rollback string, input domain.ListenerInput, digest string) (domain.ListenerRevision, error) {
	var out domain.ListenerRevision
	p := tx.persistence
	perform := func() error {
		records, err := tx.listenerRevisions()
		if err != nil {
			return err
		}
		latest := 0
		var restored *domain.ListenerRevision
		for _, r := range records {
			if r.RequestID == input.RequestID {
				if r.RequestDigest != digest {
					return domain.ErrConflict
				}
				out = cloneListenerRevision(r)
				return nil
			}
			if r.Number > latest {
				latest = r.Number
			}
			if r.ID == rollback {
				copy := r
				restored = &copy
			}
		}
		if latest != input.ExpectedRevision || latest >= 1000000 {
			return domain.ErrConflict
		}
		if _, err = tx.get(input.RequestID, true); err == nil {
			return domain.ErrConflict
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if rollback != "" {
			if restored == nil || restored.Status != "succeeded" {
				return domain.ErrNotFound
			}
			input.Listeners = restored.Listeners
		}
		var base *domain.ConfigurationDocument
		for _, node := range tx.nodes {
			err = p.controlTransaction(tx.ctx, org, node.ID, func(control *controlTransaction) error {
				if !configurationEnabled(control.state, controlNow(ctx)) || control.state.Discovery.Configuration.ListenerControlVersion != 1 {
					return domain.ErrConflict
				}
				state := control.state.Discovery.Configuration
				doc := state.Document.Canonical()
				for _, l := range input.Listeners {
					if tx.cluster.Topology == nil || l.Name != tx.cluster.Topology.Frontend || !state.ListenerReady(l, doc.Applications, controlNow(ctx)) {
						return domain.ErrConflict
					}
					open := false
					for _, proxy := range control.state.Discovery.Proxies {
						open = open || (proxy.Name == l.Name && proxy.Kind == "frontend" && proxy.Mode == "http" && proxy.Status == "OPEN")
					}
					if !open {
						return domain.ErrConflict
					}
				}
				for _, l := range doc.Listeners {
					known := false
					for _, r := range records {
						for _, old := range r.Listeners {
							known = known || l.Equal(old)
						}
					}
					if !known {
						return domain.ErrConflict
					}
				}
				doc.Listeners = nil
				if base != nil && !base.Equal(doc) {
					return domain.ErrConflict
				}
				base = &doc
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
		desired.Listeners = input.Listeners
		in := domain.RolloutInput{RequestID: input.RequestID, Document: desired, Nodes: input.Nodes, Entrypoint: input.Entrypoint, DrainTimeoutSeconds: 30}
		if !in.Valid() {
			return domain.ErrInvalidInput
		}
		writer := context.WithValue(ctx, listenerWriterKey{}, true)
		r, err := createRolloutInTransaction(writer, tx, org, cluster, in)
		if err != nil {
			return err
		}
		out = domain.ListenerRevision{ID: domain.NewID(), OrganizationID: org, ClusterID: cluster, Number: latest + 1, RequestID: input.RequestID, RequestDigest: digest, Listeners: input.Listeners, RolloutID: r.ID, RollbackOf: rollback, ActorUserID: domain.Actor(ctx), CreatedAt: time.Now().UTC(), Status: r.Status}
		if err = tx.saveListenerRevision(out); err != nil {
			return err
		}
		action := "listener.deployed"
		if rollback != "" {
			action = "listener.rollback"
		}
		return tx.audit(r, action)
	}
	err := perform()
	return cloneListenerRevision(out), err
}

func (m *Memory) ListListeners(ctx context.Context, org, cluster string) ([]domain.ListenerRevision, error) {
	return listListeners(ctx, m, org, cluster)
}
func (p *Postgres) ListListeners(ctx context.Context, org, cluster string) ([]domain.ListenerRevision, error) {
	return listListeners(ctx, p, org, cluster)
}
func (m *Memory) DeployListeners(ctx context.Context, org, cluster, rollback string, in domain.ListenerInput) (domain.ListenerRevision, error) {
	return deployListeners(ctx, m, org, cluster, rollback, in)
}
func (p *Postgres) DeployListeners(ctx context.Context, org, cluster, rollback string, in domain.ListenerInput) (domain.ListenerRevision, error) {
	return deployListeners(ctx, p, org, cluster, rollback, in)
}
