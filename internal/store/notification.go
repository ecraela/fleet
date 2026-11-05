package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

type NotificationStore interface {
	ClaimIncidentNotification(context.Context, string, string, string, time.Time) (*domain.IncidentDelivery, error)
	FinishIncidentNotification(context.Context, string, domain.IncidentDelivery, bool, time.Time, time.Duration) error
	IncidentNotificationStatus(context.Context, string, string) (domain.NotificationStatus, error)
}
type notificationDestination struct {
	OrganizationID, Digest string
	Sequence               int64
}
type notificationRecord struct {
	DestinationID                   string
	Event                           domain.IncidentEvent
	SentSequence, DueMS, LastSentMS int64
	Attempts                        int
	LeaseID                         string
	LeaseUntilMS                    int64
	ErrorCode                       string
}

func validNotificationIdentity(org, id, digest string) bool {
	return domain.ValidID(org) && domain.ValidID(id) && domain.ValidDigest(digest)
}
func deliveryFromRecord(r notificationRecord) *domain.IncidentDelivery {
	e := r.Event
	e.Incident = cloneIncident(e.Incident)
	return &domain.IncidentDelivery{DestinationID: r.DestinationID, LeaseID: r.LeaseID, Attempts: r.Attempts, Event: e}
}

func (m *Memory) ClaimIncidentNotification(ctx context.Context, org, id, digest string, now time.Time) (*domain.IncidentDelivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validNotificationIdentity(org, id, digest) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.notificationDestinations == nil {
		m.notificationDestinations = map[string]notificationDestination{}
		m.incidentDeliveries = map[string]notificationRecord{}
	}
	d, ok := m.notificationDestinations[id]
	if !ok {
		m.notificationDestinations[id] = notificationDestination{OrganizationID: org, Digest: digest, Sequence: m.incidentSequence}
		return nil, nil
	}
	if d.OrganizationID != org || d.Digest != digest {
		return nil, domain.ErrConflict
	}
	staged := 0
	for _, e := range m.incidentEvents {
		if e.Sequence <= d.Sequence || e.Incident.OrganizationID != org {
			continue
		}
		key := id + "/" + e.Incident.ID
		r := m.incidentDeliveries[key]
		r.DestinationID = id
		r.Event = e
		r.Event.Incident = cloneIncident(e.Incident)
		r.Attempts = 0
		r.ErrorCode = ""
		r.DueMS = max(now.UnixMilli(), r.LastSentMS+IncidentCooldown.Milliseconds())
		if e.Kind == "escalated" {
			r.DueMS = now.UnixMilli()
		}
		m.incidentDeliveries[key] = r
		d.Sequence = e.Sequence
		staged++
		if staged == 100 {
			break
		}
	}
	m.notificationDestinations[id] = d
	// Catch up before claiming. Sending a partial batch could notify an old
	// opening while the matching recovery is still waiting beyond this page.
	if staged == 100 {
		return nil, nil
	}
	candidates := []string{}
	for key, r := range m.incidentDeliveries {
		if r.DestinationID == id && r.Event.Sequence > r.SentSequence && r.Attempts < 8 && r.DueMS <= now.UnixMilli() && r.LeaseUntilMS <= now.UnixMilli() {
			candidates = append(candidates, key)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return m.incidentDeliveries[candidates[i]].Event.Sequence < m.incidentDeliveries[candidates[j]].Event.Sequence
	})
	if len(candidates) == 0 {
		return nil, nil
	}
	key := candidates[0]
	r := m.incidentDeliveries[key]
	r.LeaseID = domain.NewID()
	r.LeaseUntilMS = now.Add(30 * time.Second).UnixMilli()
	r.Attempts++
	m.incidentDeliveries[key] = r
	return deliveryFromRecord(r), nil
}
func (m *Memory) FinishIncidentNotification(ctx context.Context, org string, d domain.IncidentDelivery, success bool, now time.Time, retry time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := d.DestinationID + "/" + d.Event.Incident.ID
	r, ok := m.incidentDeliveries[key]
	if !ok || r.Event.Incident.OrganizationID != org {
		return domain.ErrNotFound
	}
	if r.LeaseID != d.LeaseID {
		return domain.ErrConflict
	}
	r.LeaseID = ""
	r.LeaseUntilMS = 0
	if success {
		r.SentSequence = d.Event.Sequence
		r.LastSentMS = now.UnixMilli()
		r.ErrorCode = ""
		r.Attempts = 0
		r.DueMS = now.Add(IncidentCooldown).UnixMilli()
	} else {
		r.ErrorCode = "delivery_failed"
		r.DueMS = now.Add(retry).UnixMilli()
	}
	m.incidentDeliveries[key] = r
	return nil
}
func (m *Memory) IncidentNotificationStatus(ctx context.Context, org, cluster string) (domain.NotificationStatus, error) {
	if _, err := m.GetCluster(ctx, org, cluster); err != nil {
		return domain.NotificationStatus{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := domain.NotificationStatus{}
	for _, r := range m.incidentDeliveries {
		if r.Event.Incident.OrganizationID == org && r.Event.Incident.ClusterID == cluster && r.Event.Sequence > r.SentSequence {
			if r.Attempts >= 8 {
				out.Failed++
			} else {
				out.Pending++
			}
		}
	}
	return out, nil
}
func (p *Postgres) ClaimIncidentNotification(ctx context.Context, org, id, digest string, now time.Time) (*domain.IncidentDelivery, error) {
	if !validNotificationIdentity(org, id, digest) {
		return nil, domain.ErrInvalidInput
	}
	var out *domain.IncidentDelivery
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hm_notification_destinations(id,organization_id,config_digest,last_sequence) SELECT $1,$2,$3,COALESCE(max(sequence),0) FROM hm_incident_events WHERE organization_id=$2 ON CONFLICT(id) DO NOTHING`, id, org, digest)
		if err != nil {
			return err
		}
		var d notificationDestination
		if err := tx.QueryRow(ctx, `SELECT organization_id::text,config_digest,last_sequence FROM hm_notification_destinations WHERE id=$1 FOR UPDATE`, id).Scan(&d.OrganizationID, &d.Digest, &d.Sequence); err != nil {
			return err
		}
		if d.OrganizationID != org || d.Digest != digest {
			return domain.ErrConflict
		}
		rows, err := tx.Query(ctx, `SELECT sequence,payload FROM hm_incident_events WHERE organization_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 100`, org, d.Sequence)
		if err != nil {
			return err
		}
		events := []domain.IncidentEvent{}
		for rows.Next() {
			var e domain.IncidentEvent
			var raw []byte
			if err := rows.Scan(&e.Sequence, &raw); err != nil {
				rows.Close()
				return err
			}
			seq := e.Sequence
			if err := json.Unmarshal(raw, &e); err != nil {
				rows.Close()
				return err
			}
			e.Sequence = seq
			events = append(events, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range events {
			raw, err := json.Marshal(e)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO hm_incident_deliveries(destination_id,incident_id,organization_id,cluster_id,sequence,due_ms,payload) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(destination_id,incident_id) DO UPDATE SET sequence=excluded.sequence,payload=excluded.payload,attempts=0,error_code='',due_ms=CASE WHEN $8 THEN $6 ELSE GREATEST($6,hm_incident_deliveries.last_sent_ms+$9) END`, id, e.Incident.ID, org, e.Incident.ClusterID, e.Sequence, now.UnixMilli(), raw, e.Kind == "escalated", IncidentCooldown.Milliseconds())
			if err != nil {
				return err
			}
			d.Sequence = e.Sequence
		}
		if _, err := tx.Exec(ctx, `UPDATE hm_notification_destinations SET last_sequence=$2 WHERE id=$1`, id, d.Sequence); err != nil {
			return err
		}
		if len(events) == 100 {
			return nil
		}
		var raw []byte
		var incident string
		var attempts int
		err = tx.QueryRow(ctx, `SELECT incident_id::text,payload,attempts FROM hm_incident_deliveries WHERE destination_id=$1 AND organization_id=$2 AND sequence>sent_sequence AND due_ms<=$3 AND attempts<8 AND lease_until_ms<=$3 ORDER BY sequence LIMIT 1 FOR UPDATE SKIP LOCKED`, id, org, now.UnixMilli()).Scan(&incident, &raw, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var e domain.IncidentEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		lease := domain.NewID()
		if _, err := tx.Exec(ctx, `UPDATE hm_incident_deliveries SET lease_id=$3,lease_until_ms=$4,attempts=attempts+1 WHERE destination_id=$1 AND incident_id=$2`, id, incident, lease, now.Add(30*time.Second).UnixMilli()); err != nil {
			return err
		}
		out = &domain.IncidentDelivery{DestinationID: id, LeaseID: lease, Attempts: attempts + 1, Event: e}
		return nil
	})
	return out, err
}
func (p *Postgres) FinishIncidentNotification(ctx context.Context, org string, d domain.IncidentDelivery, success bool, now time.Time, retry time.Duration) error {
	if !domain.ValidID(org) || !domain.ValidID(d.DestinationID) || !domain.ValidID(d.LeaseID) || !domain.ValidID(d.Event.Incident.ID) {
		return domain.ErrInvalidInput
	}
	var result int64
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		var err error
		if success {
			r, e := tx.Exec(ctx, `UPDATE hm_incident_deliveries SET sent_sequence=$5,last_sent_ms=$6,due_ms=$7,attempts=0,error_code='',lease_id=NULL,lease_until_ms=0 WHERE organization_id=$1 AND destination_id=$2 AND incident_id=$3 AND lease_id=$4`, org, d.DestinationID, d.Event.Incident.ID, d.LeaseID, d.Event.Sequence, now.UnixMilli(), now.Add(IncidentCooldown).UnixMilli())
			err = e
			result = r.RowsAffected()
		} else {
			r, e := tx.Exec(ctx, `UPDATE hm_incident_deliveries SET due_ms=$5,error_code='delivery_failed',lease_id=NULL,lease_until_ms=0 WHERE organization_id=$1 AND destination_id=$2 AND incident_id=$3 AND lease_id=$4`, org, d.DestinationID, d.Event.Incident.ID, d.LeaseID, now.Add(retry).UnixMilli())
			err = e
			result = r.RowsAffected()
		}
		return err
	})
	if err == nil && result != 1 {
		return domain.ErrConflict
	}
	return err
}
func (p *Postgres) IncidentNotificationStatus(ctx context.Context, org, cluster string) (domain.NotificationStatus, error) {
	if _, err := p.GetCluster(ctx, org, cluster); err != nil {
		return domain.NotificationStatus{}, err
	}
	out := domain.NotificationStatus{}
	err := p.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE attempts<8),count(*) FILTER (WHERE attempts>=8) FROM hm_incident_deliveries WHERE organization_id=$1 AND cluster_id=$2 AND sequence>sent_sequence`, org, cluster).Scan(&out.Pending, &out.Failed)
	return out, err
}
