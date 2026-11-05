package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed incident_schema.sql
var incidentSchemaSQL string

const IncidentPageSize = 50
const IncidentRecoveryDelay = 10 * time.Second
const IncidentCooldown = 5 * time.Minute
const IncidentRetention = 30 * 24 * time.Hour

type IncidentPage struct {
	Notifications  domain.NotificationStatus `json:"notifications"`
	OrganizationID string                    `json:"organization_id"`
	ClusterID      string                    `json:"cluster_id"`
	Incidents      []domain.Incident         `json:"incidents"`
	Events         []domain.IncidentEvent    `json:"events"`
	NextCursor     int64                     `json:"next_cursor,omitempty"`
	EvaluatedAtMS  int64                     `json:"evaluated_at_ms"`
}
type IncidentStore interface {
	IncidentClusters(context.Context, string) ([]domain.Cluster, error)
	ReconcileIncidents(context.Context, string, string, domain.IncidentTelemetry, time.Time) error
	ListIncidents(context.Context, string, string, string, int64) (IncidentPage, error)
}

type incidentRecord struct {
	Incident          domain.Incident `json:"incident"`
	Key               string          `json:"key"`
	FailingSinceMS    int64           `json:"failing_since_ms"`
	FailingDurationMS int64           `json:"failing_duration_ms,omitempty"`
	RecoveringSinceMS int64           `json:"recovering_since_ms"`
	LastEvaluatedMS   int64           `json:"last_evaluated_ms"`
	LastEventMS       int64           `json:"last_event_ms"`
}
type incidentTransaction struct {
	controls   func() (map[string]domain.NodeControl, error)
	load       func() ([]incidentRecord, int64, error)
	save       func(incidentRecord) error
	event      func(domain.IncidentEvent) error
	checkpoint func(int64) error
}

func cloneIncident(r domain.Incident) domain.Incident {
	r.NodeIDs = append([]string{}, r.NodeIDs...)
	return r
}

func advanceIncident(r incidentRecord, o domain.IncidentObservation, org, cluster string, now time.Time) (incidentRecord, []domain.IncidentEvent) {
	at := now.UnixMilli()
	if at <= r.LastEvaluatedMS {
		return r, nil
	}
	if r.Incident.ID == "" {
		r = incidentRecord{Key: o.Key, Incident: domain.Incident{ID: domain.NewID(), OrganizationID: org, ClusterID: cluster, Rule: o.Rule, ResourceID: o.ResourceID, Scope: o.Scope, Scheme: o.Scheme, Status: "pending", NodeIDs: []string{}}}
	}
	previous := r.Incident
	// Healthy samples must advance. A cached observation cannot run down a
	// recovery timer; terminal verified rollback is authoritative persisted work.
	if o.State == "healthy" && !o.RecordResolved && o.ObservedAtMS <= previous.ObservedAtMS {
		o.State = "unknown"
	}
	if o.Rule == "application_health" && o.State == "failing" && o.ObservedAtMS <= previous.ObservedAtMS {
		o.State = "unknown"
	}
	continuous := r.LastEvaluatedMS > 0 && at-r.LastEvaluatedMS <= 15000
	elapsed := at - r.LastEvaluatedMS
	if !continuous {
		r.RecoveringSinceMS = 0
	}
	r.LastEvaluatedMS = at
	i := &r.Incident
	i.ResourceName = o.ResourceName
	i.Evidence = o.State
	i.ObservedAtMS = max(previous.ObservedAtMS, 0, o.ObservedAtMS)
	events := []domain.IncidentEvent{}
	event := func(kind string) {
		i.UpdatedAtMS = at
		r.LastEventMS = at
		events = append(events, domain.IncidentEvent{Kind: kind, AtMS: at, Incident: cloneIncident(*i)})
	}
	switch o.State {
	case "failing":
		r.RecoveringSinceMS = 0
		i.Code = o.Code
		i.NodeIDs = append([]string{}, o.NodeIDs...)
		i.Severity = o.Severity
		if r.FailingSinceMS == 0 {
			r.FailingSinceMS = at
			r.FailingDurationMS = 0
		} else if previous.Evidence == "failing" {
			// Slow scheduling must not suppress faults forever or count downtime
			// as observed failure. Credit at most one normal evaluation interval.
			r.FailingDurationMS = min(o.OpenAfter.Milliseconds(), r.FailingDurationMS+min(elapsed, int64(5000)))
		}
		if i.Status != "active" && r.FailingDurationMS >= o.OpenAfter.Milliseconds() {
			i.Status = "active"
			i.OpenedAtMS = at
			i.ResolvedAtMS = 0
			i.Occurrences++
			kind := "opened"
			if previous.Occurrences > 0 {
				kind = "reopened"
			}
			event(kind)
		} else if i.Status == "active" && previous.Severity != "critical" && i.Severity == "critical" {
			event("escalated")
		}
	case "healthy", "retired":
		r.FailingSinceMS = 0
		r.FailingDurationMS = 0
		if r.RecoveringSinceMS == 0 {
			r.RecoveringSinceMS = at
		}
		if i.Status == "active" && (o.State == "retired" || o.RecordResolved || at-r.RecoveringSinceMS >= IncidentRecoveryDelay.Milliseconds()) {
			i.Status = "resolved"
			i.ResolvedAtMS = at
			i.Code = o.Code
			event("resolved")
		} else if i.Status == "pending" && o.RecordResolved {
			i.Status = "resolved"
			i.OpenedAtMS = at
			i.ResolvedAtMS = at
			i.Occurrences = 1
			i.Code = o.Code
			i.Severity = o.Severity
			i.NodeIDs = append([]string{}, o.NodeIDs...)
			event("recovered")
		}
	default:
		r.FailingSinceMS = 0
		r.FailingDurationMS = 0
		r.RecoveringSinceMS = 0
	}
	if i.UpdatedAtMS == 0 {
		i.UpdatedAtMS = at
	}
	return r, events
}

func reconcileIncidents(ctx context.Context, p rolloutPersistence, org, cluster string, telemetry domain.IncidentTelemetry, now time.Time) error {
	return p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		it := tx.incidents
		records, checkpoint, err := it.load()
		if err != nil {
			return err
		}
		if now.UnixMilli() <= checkpoint {
			return nil
		}
		s := domain.IncidentSnapshot{Cluster: tx.cluster, Nodes: tx.nodes}
		if s.Controls, err = it.controls(); err != nil {
			return err
		}
		if s.Apps, err = tx.applicationRevisions(); err != nil {
			return err
		}
		if s.Rollouts, err = tx.list(); err != nil {
			return err
		}
		recoveryIDs := []string{}
		for _, r := range s.Rollouts {
			for _, n := range r.Nodes {
				if n.RecoveryMaintenanceID != "" {
					recoveryIDs = append(recoveryIDs, n.RecoveryMaintenanceID)
				}
			}
		}
		recoveries, err := tx.recoveries(recoveryIDs)
		if err != nil {
			return err
		}
		for i := range s.Rollouts {
			for j := range s.Rollouts[i].Nodes {
				n := &s.Rollouts[i].Nodes[j]
				if recovered, ok := recoveries[n.RecoveryMaintenanceID]; ok {
					n.RollbackMaintenance = &recovered
				}
			}
		}
		plan, err := tx.acmePlan()
		if err == nil {
			s.ACME = &plan
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		current := map[string]incidentRecord{}
		for _, r := range records {
			current[r.Key] = r
		}
		for _, o := range domain.IncidentObservations(s, telemetry, now) {
			r := current[o.Key]
			// Healthy/unconfigured resources need no durable row until a problem.
			if r.Incident.ID == "" && o.State != "failing" && !o.RecordResolved {
				continue
			}
			next, events := advanceIncident(r, o, org, cluster, now)
			if err := it.save(next); err != nil {
				return err
			}
			for _, e := range events {
				if err := it.event(e); err != nil {
					return err
				}
			}
			delete(current, o.Key)
		}
		for _, r := range current {
			// Absence is not recovery. Revoked nodes are explicitly retired;
			// missing/stale app or TLS evidence holds the incident open.
			state, code := "unknown", r.Incident.Code
			if s.IncidentRetired(r.Incident, now) {
				state = "retired"
				code = map[string]string{"agent_offline": "node_removed", "metrics_missing": "node_removed", "telemetry_unavailable": "no_active_nodes", "tls_expiry": "listener_removed"}[r.Incident.Rule]
			}
			if r.Incident.Rule == "deployment_failed" {
				for _, rollout := range s.Rollouts {
					if rollout.Status == "succeeded" && rollout.CreatedAt.UnixMilli() > r.Incident.OpenedAtMS {
						state, code = "retired", "superseded_by_success"
					}
				}
			}
			var events []domain.IncidentEvent
			r, events = advanceIncident(r, domain.IncidentObservation{State: state, Code: code, ResourceName: r.Incident.ResourceName}, org, cluster, now)
			if err := it.save(r); err != nil {
				return err
			}
			for _, e := range events {
				if err := it.event(e); err != nil {
					return err
				}
			}
		}
		return it.checkpoint(now.UnixMilli())
	})
}
func (m *Memory) ReconcileIncidents(ctx context.Context, org, cluster string, t domain.IncidentTelemetry, now time.Time) error {
	return reconcileIncidents(ctx, m, org, cluster, t, now)
}
func (p *Postgres) ReconcileIncidents(ctx context.Context, org, cluster string, t domain.IncidentTelemetry, now time.Time) error {
	return reconcileIncidents(ctx, p, org, cluster, t, now)
}

// The worker pages all installation clusters with a bounded cursor. This method
// is internal only; browser/operator endpoints always resolve one tenant.
func (m *Memory) IncidentClusters(ctx context.Context, after string) ([]domain.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if after != "" && !domain.ValidID(after) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Cluster{}
	for _, c := range m.clusters {
		if c.ID > after {
			out = append(out, cloneCluster(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}
func (p *Postgres) IncidentClusters(ctx context.Context, after string) ([]domain.Cluster, error) {
	if after != "" && !domain.ValidID(after) {
		return nil, domain.ErrInvalidInput
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,organization_id::text,name,created_at,topology FROM hm_clusters WHERE ($1='' OR id>NULLIF($1,'')::uuid) ORDER BY id LIMIT 50`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Cluster{}
	for rows.Next() {
		var c domain.Cluster
		if err := rows.Scan(&c.ID, &c.OrganizationID, &c.Name, &c.CreatedAt, &c.Topology); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func configureMemoryIncidents(tx *rolloutTransaction, m *Memory, org, cluster string) {
	if m.incidents == nil {
		m.incidents = map[string]incidentRecord{}
	}
	if m.incidentEvaluations == nil {
		m.incidentEvaluations = map[string]int64{}
	}
	tx.incidents = incidentTransaction{
		controls: func() (map[string]domain.NodeControl, error) {
			out := map[string]domain.NodeControl{}
			for _, n := range tx.nodes {
				c := m.controls[n.ID]
				c.Discovery = cloneDiscovery(c.Discovery)
				out[n.ID] = c
			}
			return out, nil
		},
		load: func() ([]incidentRecord, int64, error) {
			out := []incidentRecord{}
			for _, r := range m.incidents {
				if r.Incident.OrganizationID == org && r.Incident.ClusterID == cluster {
					r.Incident = cloneIncident(r.Incident)
					out = append(out, r)
				}
			}
			return out, m.incidentEvaluations[cluster], nil
		},
		save: func(r incidentRecord) error {
			r.Incident = cloneIncident(r.Incident)
			m.incidents[r.Incident.ID] = r
			return nil
		},
		event: func(e domain.IncidentEvent) error {
			m.incidentSequence++
			e.Sequence = m.incidentSequence
			e.Incident = cloneIncident(e.Incident)
			m.incidentEvents = append(m.incidentEvents, e)
			return nil
		},
		checkpoint: func(at int64) error {
			m.incidentEvaluations[cluster] = at
			cutoff := at - IncidentRetention.Milliseconds()
			m.incidentEvents = slices.DeleteFunc(m.incidentEvents, func(e domain.IncidentEvent) bool {
				return e.Incident.OrganizationID == org && e.Incident.ClusterID == cluster && e.AtMS < cutoff
			})
			for key, r := range m.incidentDeliveries {
				if r.Event.Incident.OrganizationID == org && r.Event.Incident.ClusterID == cluster && r.Event.AtMS < cutoff {
					delete(m.incidentDeliveries, key)
				}
			}
			for id, r := range m.incidents {
				if r.Incident.OrganizationID == org && r.Incident.ClusterID == cluster && r.Incident.Status != "active" && r.Incident.UpdatedAtMS < cutoff {
					delete(m.incidents, id)
				}
			}
			return nil
		},
	}
}
func configurePostgresIncidents(tx *rolloutTransaction, db pgx.Tx, org, cluster string) {
	ctx := tx.ctx
	tx.incidents = incidentTransaction{
		controls: func() (map[string]domain.NodeControl, error) {
			rows, err := db.Query(ctx, `SELECT node_id::text,COALESCE(session_id::text,''),CASE WHEN isfinite(last_seen) THEN last_seen ELSE NULL END,discovery FROM hm_node_controls WHERE organization_id=$1 AND cluster_id=$2`, org, cluster)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			out := map[string]domain.NodeControl{}
			for rows.Next() {
				var c domain.NodeControl
				var seen *time.Time
				var raw []byte
				if err := rows.Scan(&c.NodeID, &c.SessionID, &seen, &raw); err != nil {
					return nil, err
				}
				if seen != nil {
					c.LastSeen = *seen
				}
				if raw != nil {
					c.Discovery = &domain.Discovery{}
					if err := json.Unmarshal(raw, c.Discovery); err != nil {
						return nil, err
					}
				}
				out[c.NodeID] = c
			}
			return out, rows.Err()
		},
		load: func() ([]incidentRecord, int64, error) {
			var at int64
			err := db.QueryRow(ctx, `SELECT evaluated_at_ms FROM hm_incident_evaluations WHERE organization_id=$1 AND cluster_id=$2`, org, cluster).Scan(&at)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, 0, err
			}
			rows, err := db.Query(ctx, `SELECT payload FROM hm_incidents WHERE organization_id=$1 AND cluster_id=$2`, org, cluster)
			if err != nil {
				return nil, 0, err
			}
			defer rows.Close()
			out := []incidentRecord{}
			for rows.Next() {
				var raw []byte
				var r incidentRecord
				if err := rows.Scan(&raw); err != nil {
					return nil, 0, err
				}
				if err := json.Unmarshal(raw, &r); err != nil {
					return nil, 0, err
				}
				out = append(out, r)
			}
			return out, at, rows.Err()
		},
		save: func(r incidentRecord) error {
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			_, err = db.Exec(ctx, `INSERT INTO hm_incidents(id,organization_id,cluster_id,group_key,status,updated_at_ms,payload) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET status=excluded.status,updated_at_ms=excluded.updated_at_ms,payload=excluded.payload`, r.Incident.ID, org, cluster, r.Key, r.Incident.Status, r.Incident.UpdatedAtMS, raw)
			return err
		},
		event: func(e domain.IncidentEvent) error {
			raw, err := json.Marshal(e)
			if err != nil {
				return err
			}
			_, err = db.Exec(ctx, `INSERT INTO hm_incident_events(incident_id,organization_id,cluster_id,at_ms,payload) VALUES($1,$2,$3,$4,$5)`, e.Incident.ID, org, cluster, e.AtMS, raw)
			return err
		},
		checkpoint: func(at int64) error {
			var cleaned int64
			err := db.QueryRow(ctx, "SELECT cleaned_at_ms FROM hm_incident_evaluations WHERE organization_id=$1 AND cluster_id=$2", org, cluster).Scan(&cleaned)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if at-cleaned > time.Hour.Milliseconds() {
				cutoff := at - IncidentRetention.Milliseconds()
				for _, query := range []string{
					"DELETE FROM hm_incident_events WHERE organization_id=$1 AND cluster_id=$2 AND at_ms<$3",
					"DELETE FROM hm_incident_deliveries WHERE organization_id=$1 AND cluster_id=$2 AND (payload->>'at_ms')::bigint<$3",
					"DELETE FROM hm_incidents WHERE organization_id=$1 AND cluster_id=$2 AND status<>'active' AND updated_at_ms<$3",
				} {
					if _, err := db.Exec(ctx, query, org, cluster, cutoff); err != nil {
						return err
					}
				}
				cleaned = at
			}
			_, err = db.Exec(ctx, `INSERT INTO hm_incident_evaluations(cluster_id,organization_id,evaluated_at_ms,cleaned_at_ms) VALUES($1,$2,$3,$4) ON CONFLICT(cluster_id) DO UPDATE SET evaluated_at_ms=excluded.evaluated_at_ms,cleaned_at_ms=excluded.cleaned_at_ms`, cluster, org, at, cleaned)
			return err
		},
	}
}

func (m *Memory) ListIncidents(ctx context.Context, org, cluster, id string, before int64) (IncidentPage, error) {
	result := IncidentPage{OrganizationID: org, ClusterID: cluster, Incidents: []domain.Incident{}, Events: []domain.IncidentEvent{}}
	if _, err := m.GetCluster(ctx, org, cluster); err != nil {
		return result, err
	}
	if id != "" && !domain.ValidID(id) || before < 0 {
		return result, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result.EvaluatedAtMS = m.incidentEvaluations[cluster]
	for _, r := range m.incidents {
		if r.Incident.OrganizationID == org && r.Incident.ClusterID == cluster && r.Incident.Status != "pending" && (id == "" || id == r.Incident.ID) {
			result.Incidents = append(result.Incidents, cloneIncident(r.Incident))
		}
	}
	sort.Slice(result.Incidents, func(i, j int) bool {
		a, b := result.Incidents[i], result.Incidents[j]
		if a.Status != b.Status {
			return a.Status == "active"
		}
		return a.UpdatedAtMS > b.UpdatedAtMS
	})
	if len(result.Incidents) > 100 {
		result.Incidents = result.Incidents[:100]
	}
	for i := len(m.incidentEvents) - 1; i >= 0; i-- {
		e := m.incidentEvents[i]
		if e.Incident.OrganizationID == org && e.Incident.ClusterID == cluster && (id == "" || id == e.Incident.ID) && (before == 0 || e.Sequence < before) {
			e.Incident = cloneIncident(e.Incident)
			result.Events = append(result.Events, e)
		}
	}
	if len(result.Events) > IncidentPageSize {
		result.Events = result.Events[:IncidentPageSize]
		result.NextCursor = result.Events[len(result.Events)-1].Sequence
	}
	return result, nil
}
func (p *Postgres) ListIncidents(ctx context.Context, org, cluster, id string, before int64) (IncidentPage, error) {
	result := IncidentPage{OrganizationID: org, ClusterID: cluster, Incidents: []domain.Incident{}, Events: []domain.IncidentEvent{}}
	if _, err := p.GetCluster(ctx, org, cluster); err != nil {
		return result, err
	}
	if id != "" && !domain.ValidID(id) || before < 0 {
		return result, domain.ErrInvalidInput
	}
	err := p.pool.QueryRow(ctx, `SELECT evaluated_at_ms FROM hm_incident_evaluations WHERE organization_id=$1 AND cluster_id=$2`, org, cluster).Scan(&result.EvaluatedAtMS)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	rows, err := p.pool.Query(ctx, `SELECT payload FROM hm_incidents WHERE organization_id=$1 AND cluster_id=$2 AND status<>'pending' AND ($3='' OR id=NULLIF($3,'')::uuid) ORDER BY (status='active') DESC,updated_at_ms DESC,id LIMIT 100`, org, cluster, id)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var raw []byte
		var r incidentRecord
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return result, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			rows.Close()
			return result, err
		}
		result.Incidents = append(result.Incidents, r.Incident)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = p.pool.Query(ctx, `SELECT sequence,payload FROM hm_incident_events WHERE organization_id=$1 AND cluster_id=$2 AND ($3='' OR incident_id=NULLIF($3,'')::uuid) AND ($4::bigint=0 OR sequence<$4) ORDER BY sequence DESC LIMIT 51`, org, cluster, id, before)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var raw []byte
		var e domain.IncidentEvent
		if err := rows.Scan(&seq, &raw); err != nil {
			return result, err
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return result, err
		}
		e.Sequence = seq
		result.Events = append(result.Events, e)
	}
	if len(result.Events) > IncidentPageSize {
		result.Events = result.Events[:IncidentPageSize]
		result.NextCursor = result.Events[len(result.Events)-1].Sequence
	}
	return result, rows.Err()
}
