package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed acme_schema.sql
var acmeSchemaSQL string
var errACMEUnavailable = errors.New("fresh ACME authority unavailable")

type ACMEStore interface {
	GetACME(context.Context, string, string) (domain.ACMEPlan, error)
	SetACME(context.Context, string, string, domain.ACMEInput) (domain.ACMEPlan, error)
	NodeACME(context.Context, string, string, string) (*domain.ACMEJob, error)
	ReportACME(context.Context, string, string, string, domain.ACMEResult) error
	AdvanceACME(context.Context) error
}

func cloneACME(p domain.ACMEPlan) domain.ACMEPlan {
	raw, _ := json.Marshal(p)
	var out domain.ACMEPlan
	_ = json.Unmarshal(raw, &out)
	return out
}
func configureMemoryACME(tx *rolloutTransaction, m *Memory, org, cluster string) {
	tx.acmePlan = func() (domain.ACMEPlan, error) {
		p, ok := m.acmePlans[cluster]
		if !ok || p.OrganizationID != org {
			return p, domain.ErrNotFound
		}
		return cloneACME(p), nil
	}
	tx.saveACME = func(p domain.ACMEPlan) error { m.acmePlans[cluster] = cloneACME(p); return nil }
	tx.auditACME = func(p domain.ACMEPlan, action string) error {
		m.audit = append(m.audit, auditEvent{organizationID: org, clusterID: cluster, acmePlanID: p.ID, actorUserID: domain.Actor(tx.ctx), action: action, createdAt: p.UpdatedAt})
		return nil
	}
}
func configurePostgresACME(tx *rolloutTransaction, db pgx.Tx, org, cluster string) {
	tx.acmePlan = func() (domain.ACMEPlan, error) {
		var p domain.ACMEPlan
		var raw []byte
		err := db.QueryRow(tx.ctx, `SELECT payload FROM hm_acme_plans WHERE organization_id=$1 AND cluster_id=$2 FOR UPDATE`, org, cluster).Scan(&raw)
		if err != nil {
			return p, normalizeError(err)
		}
		err = json.Unmarshal(raw, &p)
		return p, err
	}
	tx.saveACME = func(p domain.ACMEPlan) error {
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = db.Exec(tx.ctx, `INSERT INTO hm_acme_plans(id,organization_id,cluster_id,enabled,status,next_run_ms,updated_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(cluster_id) DO UPDATE SET enabled=excluded.enabled,status=excluded.status,next_run_ms=excluded.next_run_ms,updated_at=excluded.updated_at,payload=excluded.payload WHERE hm_acme_plans.organization_id=excluded.organization_id AND hm_acme_plans.id=excluded.id`, p.ID, org, cluster, p.Enabled, p.Status, p.NextRunMS, p.UpdatedAt, raw)
		return err
	}
	tx.auditACME = func(p domain.ACMEPlan, action string) error {
		_, err := db.Exec(tx.ctx, `INSERT INTO hm_audit_events(id,organization_id,cluster_id,acme_plan_id,action,actor_user_id,created_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7)`, domain.NewID(), org, cluster, p.ID, action, domain.Actor(tx.ctx), p.UpdatedAt)
		return err
	}
}
func getACME(ctx context.Context, p rolloutPersistence, org, cluster string) (out domain.ACMEPlan, err error) {
	err = p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error { var err error; out, err = tx.acmePlan(); return err })
	return
}

// Every generation freezes the exact nodes, hostnames, delegation and local
// policy digests. New nodes or local authority changes require an explicit edit.
func acmeSnapshot(tx *rolloutTransaction, plan domain.ACMEPlan) ([]string, []domain.ACMEChallenge, string, []domain.ACMEJob, error) {
	rollouts, err := tx.list()
	if err != nil {
		return nil, nil, "", nil, err
	}
	for _, r := range rollouts {
		if !r.Terminal() {
			return nil, nil, "", nil, errACMEUnavailable
		}
	}
	issuer, binding := plan.Issuer, plan.Binding
	if tx.cluster.Topology == nil || tx.cluster.Topology.Control == nil || binding.Name != tx.cluster.Topology.Frontend || len(tx.nodes) < 2 || len(tx.nodes) > domain.MaxRolloutNodes {
		return nil, nil, "", nil, domain.ErrConflict
	}
	var names []string
	var challenges []domain.ACMEChallenge
	var environment string
	var common *domain.ConfigurationDocument
	jobs := []domain.ACMEJob{}
	for _, node := range tx.nodes {
		var grant domain.ACMEGrant
		err := tx.persistence.controlTransaction(tx.ctx, node.OrganizationID, node.ID, func(c *controlTransaction) error {
			if !configurationEnabled(c.state, controlNow(tx.ctx)) {
				return errACMEUnavailable
			}
			state := c.state.Discovery.Configuration
			if state.NodeID != node.ID || state.ListenerControlVersion != 1 || !slices.Contains(state.ListenerBindings, binding) {
				return domain.ErrConflict
			}
			doc := state.Document.Canonical()
			if plan.Generation > 0 && plan.Status == "active" {
				last := ""
				for _, h := range plan.History {
					if h.Status == "succeeded" {
						last = h.Reference
					}
				}
				if last != "" && !slices.ContainsFunc(doc.Listeners, func(l domain.HTTPSListener) bool { return l.ListenerBinding == binding && l.CertificateRef == last }) {
					return domain.ErrConflict
				}
			}
			if common != nil && !common.Equal(doc) {
				return domain.ErrConflict
			}
			common = &doc
			local := []string{}
			for _, app := range doc.Applications {
				if app.Listener == binding.Name && !slices.Contains(local, app.Hostname) {
					local = append(local, app.Hostname)
				}
			}
			sort.Strings(local)
			if len(local) == 0 || len(local) > 8 || (names != nil && !slices.Equal(local, names)) {
				return domain.ErrConflict
			}
			names = local
			for _, g := range state.ACMEGrants {
				if g.Issuer == issuer && g.Listener == binding.Name && g.Covers(names) {
					grant = g
				}
			}
			if grant.Issuer == "" {
				return domain.ErrConflict
			}
			selected := []domain.ACMEChallenge{}
			for _, g := range grant.Challenges {
				if slices.Contains(names, g.Hostname) {
					selected = append(selected, g)
				}
			}
			slices.SortFunc(selected, func(a, b domain.ACMEChallenge) int {
				if a.Hostname < b.Hostname {
					return -1
				}
				if a.Hostname > b.Hostname {
					return 1
				}
				return 0
			})
			if challenges != nil && (!reflect.DeepEqual(challenges, selected) || environment != grant.Environment) {
				return domain.ErrConflict
			}
			challenges = selected
			environment = grant.Environment
			return nil
		})
		if err != nil {
			return nil, nil, "", nil, err
		}
		jobs = append(jobs, domain.ACMEJob{ID: domain.NewID(), NodeID: node.ID, Issuer: issuer, Listener: binding.Name, Hostnames: append([]string{}, names...), PolicyDigest: grant.PolicyDigest, Status: "pending"})
	}
	return names, challenges, environment, jobs, nil
}
func acmeGeneration(tx *rolloutTransaction, p *domain.ACMEPlan) error {
	if p.Generation >= 1000000 {
		return domain.ErrConflict
	}
	names, challenges, environment, jobs, err := acmeSnapshot(tx, *p)
	if err != nil {
		return err
	}
	if p.Generation > 0 {
		if p.BindingID != tx.cluster.Topology.BindingID() || !slices.Equal(p.Hostnames, names) || !reflect.DeepEqual(p.Challenges, challenges) || p.Environment != environment || len(p.Jobs) != len(jobs) {
			return domain.ErrConflict
		}
		for _, j := range jobs {
			if !slices.ContainsFunc(p.Jobs, func(old domain.ACMEJob) bool { return old.NodeID == j.NodeID && old.PolicyDigest == j.PolicyDigest }) {
				return domain.ErrConflict
			}
		}
	}
	p.Generation++
	p.Hostnames = names
	p.Challenges = challenges
	p.Environment = environment
	p.BindingID = tx.cluster.Topology.BindingID()
	for i := range jobs {
		jobs[i].PlanID = p.ID
		jobs[i].Generation = p.Generation
		jobs[i].Reference = domain.ACMEReference(p.ID, p.Generation)
	}
	p.Jobs = jobs
	p.Status = "issuing"
	p.ErrorCode = ""
	p.RolloutID = ""
	p.Activation = nil
	p.NextRunMS = controlNow(tx.ctx).UnixMilli()
	return nil
}
func setACME(ctx context.Context, db rolloutPersistence, org, cluster string, in domain.ACMEInput) (out domain.ACMEPlan, err error) {
	if !domain.ValidID(in.RequestID) || in.ExpectedVersion < 0 || !slices.Contains([]string{"enable", "renew", "retry", "disable"}, in.Action) {
		return out, domain.ErrInvalidInput
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	err = db.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		p, e := tx.acmePlan()
		if e != nil && !errors.Is(e, domain.ErrNotFound) {
			return e
		}
		for _, a := range p.Actions {
			if a.RequestID == in.RequestID {
				if a.Digest != digest {
					return domain.ErrConflict
				}
				out = p
				return nil
			}
		}
		if p.Version != in.ExpectedVersion {
			return domain.ErrConflict
		}
		now := controlNow(ctx)
		if p.ID == "" {
			p = domain.ACMEPlan{ID: domain.NewID(), OrganizationID: org, ClusterID: cluster, CreatedAt: now, History: []domain.ACMEHistory{}, Actions: []domain.ACMEAction{}}
		}
		action := "certificate." + in.Action
		switch in.Action {
		case "enable":
			if p.Enabled || p.Status == "deploying" || !domain.ValidRuntimeName(in.Issuer) || !in.Binding.Valid() {
				return domain.ErrConflict
			}
			for _, j := range p.Jobs {
				if p.Issuer == in.Issuer && j.ErrorCode == "acme_rate_limited" && j.NextTryMS > now.UnixMilli() {
					return domain.ErrConflict
				}
			}
			p.Issuer = in.Issuer
			p.Binding = in.Binding
			p.Generation = 0
			p.Enabled = true
			p.ActorUserID = domain.Actor(ctx)
			// Generation numbers never repeat, including after disable/re-enable.
			generation := 0
			for _, h := range p.History {
				generation = max(generation, h.Generation)
			}
			for _, j := range p.Jobs {
				generation = max(generation, j.Generation)
			}
			if e = acmeGeneration(tx, &p); e != nil {
				return e
			}
			p.Generation = max(p.Generation, generation+1)
			for i := range p.Jobs {
				p.Jobs[i].Generation = p.Generation
				p.Jobs[i].Reference = domain.ACMEReference(p.ID, p.Generation)
			}
			action = "certificate.enabled"
		case "disable":
			if !p.Enabled {
				return domain.ErrConflict
			}
			p.Enabled = false
			if p.Status != "deploying" {
				p.Status = "disabled"
			}
			p.NextRunMS = 0
			action = "certificate.disabled"
		case "renew":
			if !p.Enabled || p.Status != "active" {
				return domain.ErrConflict
			}
			if e = acmeGeneration(tx, &p); e != nil {
				return e
			}
			action = "certificate.renew_requested"
		case "retry":
			if !p.Enabled || p.Status != "error" {
				return domain.ErrConflict
			}
			if p.ErrorCode == "activation_failed" || p.ErrorCode == "authority_changed" {
				return domain.ErrConflict
			}
			for i := range p.Jobs {
				j := &p.Jobs[i]
				if j.Status == "failed" {
					if j.NextTryMS > now.UnixMilli() {
						return domain.ErrConflict
					}
					j.Status = "pending"
					j.ErrorCode = ""
				}
			}
			p.Status = "issuing"
			p.ErrorCode = ""
			p.NextRunMS = now.UnixMilli()
			action = "certificate.retry_requested"
		}
		p.Version++
		p.UpdatedAt = now
		p.Actions = append(p.Actions, domain.ACMEAction{RequestID: in.RequestID, Digest: digest, Action: in.Action, ActorUserID: domain.Actor(ctx), AtMS: now.UnixMilli()})
		if len(p.Actions) > 64 {
			p.Actions = p.Actions[len(p.Actions)-64:]
		}
		if e = tx.saveACME(p); e != nil {
			return e
		}
		if e = tx.auditACME(p, action); e != nil {
			return e
		}
		out = p
		return nil
	})
	if errors.Is(err, errACMEUnavailable) {
		err = domain.ErrConflict
	}
	return cloneACME(out), err
}
func acmeAuthority(tx *rolloutTransaction, p domain.ACMEPlan) bool {
	if !p.Enabled || tx.cluster.Topology == nil || p.BindingID != tx.cluster.Topology.BindingID() || len(tx.nodes) != len(p.Jobs) {
		return false
	}
	for _, node := range tx.nodes {
		var job *domain.ACMEJob
		for i := range p.Jobs {
			if p.Jobs[i].NodeID == node.ID {
				job = &p.Jobs[i]
			}
		}
		if job == nil {
			return false
		}
		e := tx.persistence.controlTransaction(tx.ctx, node.OrganizationID, node.ID, func(c *controlTransaction) error {
			if !configurationEnabled(c.state, controlNow(tx.ctx)) {
				return domain.ErrConflict
			}
			s := c.state.Discovery.Configuration
			names := []string{}
			for _, app := range s.Document.Applications {
				if app.Listener == p.Binding.Name && !slices.Contains(names, app.Hostname) {
					names = append(names, app.Hostname)
				}
			}
			sort.Strings(names)
			if !slices.Equal(names, p.Hostnames) {
				return domain.ErrConflict
			}
			if s.NodeID != node.ID || !slices.Contains(s.ListenerBindings, p.Binding) {
				return domain.ErrConflict
			}
			for _, g := range s.ACMEGrants {
				if g.Issuer == p.Issuer && g.Listener == p.Binding.Name && g.PolicyDigest == job.PolicyDigest && g.Covers(p.Hostnames) {
					return nil
				}
			}
			return domain.ErrConflict
		})
		if e != nil {
			return false
		}
	}
	return true
}
func nodeACME(ctx context.Context, db rolloutPersistence, org, cluster, node string) (out *domain.ACMEJob, err error) {
	err = db.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		found := slices.ContainsFunc(tx.nodes, func(n domain.Node) bool { return n.ID == node })
		if !found {
			return domain.ErrNotFound
		}
		p, e := tx.acmePlan()
		if errors.Is(e, domain.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if p.Status != "issuing" || !acmeAuthority(tx, p) {
			return nil
		}
		now := controlNow(ctx)
		for i := range p.Jobs {
			j := &p.Jobs[i]
			if j.NodeID != node || j.Status == "ready" || j.NextTryMS > now.UnixMilli() {
				continue
			}
			if j.Status == "failed" {
				return nil
			}
			if j.Status == "pending" {
				j.Status = "issuing"
				j.Attempts = min(j.Attempts+1, 1000000)
				j.DeadlineMS = now.Add(15 * time.Minute).UnixMilli()
				p.UpdatedAt = now
				if e = tx.saveACME(p); e != nil {
					return e
				}
			}
			if now.UnixMilli() < j.DeadlineMS {
				copy := *j
				out = &copy
			}
			return nil
		}
		return nil
	})
	return
}
func validACMEResult(j domain.ACMEJob, result domain.ACMEResult, now time.Time) bool {
	if result.JobID != j.ID || !domain.ACMEError(result.ErrorCode) || result.RetryAfterSeconds < 0 || result.RetryAfterSeconds > 172800 {
		return false
	}
	if result.Certificate == nil {
		return result.ErrorCode != ""
	}
	c := result.Certificate
	if result.ErrorCode != "" || !c.Usable(now) || c.Reference != j.Reference || len(c.DNSNames) != len(j.Hostnames) {
		return false
	}
	a := append([]string{}, c.DNSNames...)
	b := append([]string{}, j.Hostnames...)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b) && c.NotAfterMS <= now.Add(400*24*time.Hour).UnixMilli()
}
func reportACME(ctx context.Context, db rolloutPersistence, org, cluster, node string, result domain.ACMEResult) error {
	if !domain.ValidID(result.JobID) {
		return domain.ErrInvalidInput
	}
	return db.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		if !slices.ContainsFunc(tx.nodes, func(n domain.Node) bool { return n.ID == node }) {
			return domain.ErrNotFound
		}
		p, e := tx.acmePlan()
		if e != nil {
			return e
		}
		now := controlNow(ctx)
		for i := range p.Jobs {
			j := &p.Jobs[i]
			if j.NodeID != node || j.ID != result.JobID {
				continue
			}
			if !validACMEResult(*j, result, now) {
				return domain.ErrInvalidInput
			}
			if j.Status == "ready" {
				if reflect.DeepEqual(j.Certificate, result.Certificate) {
					return nil
				}
				return domain.ErrConflict
			}
			if !p.Enabled || p.Status != "issuing" || j.Status != "issuing" || now.UnixMilli() > j.DeadlineMS {
				return domain.ErrConflict
			}
			if result.Certificate != nil {
				j.Certificate = result.Certificate
				j.Status = "ready"
				j.ErrorCode = ""
			} else {
				j.ErrorCode = result.ErrorCode
				j.Status = "pending"
				delay := max(30, min(300, 30*(1<<min(j.Attempts-1, 4))))
				delay = max(delay, result.RetryAfterSeconds)
				j.NextTryMS = now.Add(time.Duration(delay) * time.Second).UnixMilli()
				if (j.Attempts >= 5 && !domain.ACMERetryable(result.ErrorCode)) || result.ErrorCode == "policy_unavailable" || result.ErrorCode == "certificate_storage_full" {
					j.Status = "failed"
					p.Status = "error"
					p.ErrorCode = result.ErrorCode
					p.NextRunMS = 0
				}
			}
			p.UpdatedAt = now
			return tx.saveACME(p)
		}
		return domain.ErrNotFound
	})
}
func acmeActivation(tx *rolloutTransaction, p *domain.ACMEPlan) error {
	if !acmeAuthority(tx, *p) {
		return domain.ErrConflict
	}
	in := domain.ListenerInput{RequestID: domain.NewID(), Nodes: []domain.RolloutTarget{}}
	rows, e := tx.listenerRevisions()
	if e != nil {
		return e
	}
	for _, r := range rows {
		in.ExpectedRevision = max(in.ExpectedRevision, r.Number)
	}
	l := domain.HTTPSListener{ListenerBinding: p.Binding, CertificateRef: domain.ACMEReference(p.ID, p.Generation), NodeCertificates: map[string]string{}}
	for _, j := range p.Jobs {
		if j.Status != "ready" || j.Certificate == nil || !j.Certificate.Usable(controlNow(tx.ctx)) {
			return domain.ErrConflict
		}
		l.NodeCertificates[j.NodeID] = j.Certificate.SHA256
	}
	for _, n := range tx.nodes {
		e = tx.persistence.controlTransaction(tx.ctx, n.OrganizationID, n.ID, func(c *controlTransaction) error {
			if !configurationEnabled(c.state, controlNow(tx.ctx)) {
				return domain.ErrConflict
			}
			s := c.state.Discovery.Configuration
			in.Nodes = append(in.Nodes, domain.RolloutTarget{NodeID: n.ID, ExpectedGeneration: c.state.Discovery.Generation, ExpectedArtifactID: s.ArtifactID, ExpectedVersion: s.Version})
			if len(in.Listeners) == 0 {
				for _, old := range s.Document.Listeners {
					if old.Name != l.Name {
						in.Listeners = append(in.Listeners, old)
					}
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	in.Listeners = append(in.Listeners, l)
	e = tx.persistence.controlTransaction(tx.ctx, p.OrganizationID, tx.controller.ID, func(c *controlTransaction) error {
		if !configurationEnabled(c.state, controlNow(tx.ctx)) {
			return domain.ErrConflict
		}
		s := c.state.Discovery.Configuration
		in.Entrypoint = &domain.RolloutEntrypointTarget{BindingID: p.BindingID, RolloutTarget: domain.RolloutTarget{NodeID: tx.controller.ID, ExpectedGeneration: c.state.Discovery.Generation, ExpectedArtifactID: s.ArtifactID, ExpectedVersion: s.Version}}
		return nil
	})
	if e != nil {
		return e
	}
	raw, _ := json.Marshal(struct {
		Input    domain.ListenerInput
		Rollback string
	}{in, ""})
	sum := sha256.Sum256(raw)
	ctx := domain.WithActor(tx.ctx, p.ActorUserID)
	revision, e := deployListenersTx(ctx, tx, p.OrganizationID, p.ClusterID, "", in, hex.EncodeToString(sum[:]))
	if e != nil {
		return e
	}
	p.Activation = &in
	p.RolloutID = revision.RolloutID
	p.Status = "deploying"
	p.NextRunMS = controlNow(tx.ctx).Add(3 * time.Second).UnixMilli()
	return nil
}
func advanceACME(ctx context.Context, db rolloutPersistence, org, cluster string) error {
	return db.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		p, e := tx.acmePlan()
		if e != nil {
			return e
		}
		now := controlNow(ctx)
		audit := ""
		switch p.Status {
		case "error":
			// Resume legacy plans stopped by the old five-attempt limit. Keep
			// every node's retry deadline, generation and local key reference.
			if !p.Enabled || !domain.ACMERetryable(p.ErrorCode) {
				return nil
			}
			for _, j := range p.Jobs {
				if j.Status == "failed" && !domain.ACMERetryable(j.ErrorCode) {
					return nil
				}
			}
			for i := range p.Jobs {
				if p.Jobs[i].Status == "failed" {
					p.Jobs[i].Status = "pending"
				}
			}
			p.Status = "issuing"
			p.ErrorCode = ""
			p.NextRunMS = now.Add(3 * time.Second).UnixMilli()
		case "deploying":
			r, e := tx.get(p.RolloutID, false)
			if e != nil {
				return e
			}
			if !r.Terminal() {
				p.NextRunMS = now.Add(3 * time.Second).UnixMilli()
				break
			}
			certs := map[string]domain.TLSCertificate{}
			for _, j := range p.Jobs {
				if j.Certificate != nil {
					certs[j.NodeID] = *j.Certificate
				}
			}
			p.History = append(p.History, domain.ACMEHistory{Generation: p.Generation, Reference: domain.ACMEReference(p.ID, p.Generation), RolloutID: p.RolloutID, Status: r.Status, CompletedAtMS: now.UnixMilli(), Certificates: certs})
			if len(p.History) > 20 {
				p.History = p.History[len(p.History)-20:]
			}
			p.Activation = nil
			if r.Status == "succeeded" {
				p.Status = "active"
				p.ErrorCode = ""
				p.RenewAtMS = domain.ACMERenewAt(p.ID, certs)
				p.NextRunMS = p.RenewAtMS
				audit = "certificate.activated"
			} else {
				p.Status = "error"
				p.ErrorCode = "activation_failed"
				p.NextRunMS = 0
				audit = "certificate.activation_failed"
			}
			if !p.Enabled {
				p.Status = "disabled"
				p.NextRunMS = 0
			}
		case "active":
			if !p.Enabled || now.UnixMilli() < p.RenewAtMS {
				return nil
			}
			if e = acmeGeneration(tx, &p); e != nil {
				if errors.Is(e, errACMEUnavailable) {
					p.NextRunMS = now.Add(30 * time.Second).UnixMilli()
					break
				}
				p.Status = "error"
				p.ErrorCode = "authority_changed"
				p.NextRunMS = 0
			}
		case "issuing", "ready":
			if !p.Enabled {
				return nil
			}
			ready := true
			for i := range p.Jobs {
				j := &p.Jobs[i]
				if j.Status == "pending" && j.ErrorCode == "acme_unavailable" && j.NextTryMS > now.Add(5*time.Minute).UnixMilli() {
					// Older versions backed ordinary outages off for an hour.
					// Explicit CA rate limits use a different code and stay intact.
					j.NextTryMS = now.Add(5 * time.Minute).UnixMilli()
				}
				if j.Status == "issuing" && now.UnixMilli() > j.DeadlineMS {
					j.Status = "pending"
					j.ErrorCode = "issuance_timeout"
					j.NextTryMS = now.Add(time.Minute).UnixMilli()
				}
				ready = ready && j.Status == "ready"
			}
			if p.Status == "error" {
				break
			}
			p.NextRunMS = now.Add(3 * time.Second).UnixMilli()
			if ready {
				p.Status = "ready"
				if e = acmeActivation(tx, &p); e != nil && !errors.Is(e, domain.ErrConflict) {
					return e
				}
			}
		default:
			return nil
		}
		p.UpdatedAt = now
		if e = tx.saveACME(p); e != nil {
			return e
		}
		if audit != "" {
			return tx.auditACME(p, audit)
		}
		return nil
	})
}
func (m *Memory) GetACME(c context.Context, o, k string) (domain.ACMEPlan, error) {
	return getACME(c, m, o, k)
}
func (p *Postgres) GetACME(c context.Context, o, k string) (domain.ACMEPlan, error) {
	return getACME(c, p, o, k)
}
func (m *Memory) SetACME(c context.Context, o, k string, i domain.ACMEInput) (domain.ACMEPlan, error) {
	return setACME(c, m, o, k, i)
}
func (p *Postgres) SetACME(c context.Context, o, k string, i domain.ACMEInput) (domain.ACMEPlan, error) {
	return setACME(c, p, o, k, i)
}
func (m *Memory) NodeACME(c context.Context, o, k, n string) (*domain.ACMEJob, error) {
	return nodeACME(c, m, o, k, n)
}
func (p *Postgres) NodeACME(c context.Context, o, k, n string) (*domain.ACMEJob, error) {
	return nodeACME(c, p, o, k, n)
}
func (m *Memory) ReportACME(c context.Context, o, k, n string, r domain.ACMEResult) error {
	return reportACME(c, m, o, k, n, r)
}
func (p *Postgres) ReportACME(c context.Context, o, k, n string, r domain.ACMEResult) error {
	return reportACME(c, p, o, k, n, r)
}
func (m *Memory) AdvanceACME(ctx context.Context) error {
	m.mu.Lock()
	plans := []domain.ACMEPlan{}
	for _, p := range m.acmePlans {
		if (p.Enabled && p.Status == "error" && domain.ACMERetryable(p.ErrorCode)) || ((p.Enabled || p.Status == "deploying") && p.NextRunMS > 0 && p.NextRunMS <= controlNow(ctx).UnixMilli()) {
			plans = append(plans, p)
		}
	}
	m.mu.Unlock()
	for _, p := range plans {
		if err := advanceACME(ctx, m, p.OrganizationID, p.ClusterID); err != nil {
			return err
		}
	}
	return nil
}
func (p *Postgres) AdvanceACME(ctx context.Context) error {
	rows, err := p.pool.Query(ctx, `SELECT organization_id::text,cluster_id::text FROM hm_acme_plans WHERE (enabled AND status='error' AND payload->>'error_code' IN ('acme_unavailable','acme_rate_limited','issuance_timeout')) OR ((enabled OR status='deploying') AND next_run_ms>0 AND next_run_ms<=$1) ORDER BY updated_at LIMIT 32`, controlNow(ctx).UnixMilli())
	if err != nil {
		return err
	}
	type ref struct{ o, c string }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.o, &r.c); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range refs {
		if err = advanceACME(ctx, p, r.o, r.c); err != nil {
			return err
		}
	}
	return nil
}
