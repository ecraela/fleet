package server

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

func (s *Server) ingestLogs(w http.ResponseWriter, r *http.Request) {
	node, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	if s.logs == nil {
		fail(w, 503, "request telemetry is not configured")
		return
	}
	if !acquire(s.logIngestion) {
		fail(w, 429, "request ingestion capacity reached")
		return
	}
	defer func() { <-s.logIngestion }()
	if !s.logBudget.Admit(node.ID, time.Now()) {
		fail(w, 429, "node request ingestion budget reached")
		return
	}
	key := "logs:" + node.ID
	s.mu.Lock()
	if s.active[key] {
		s.mu.Unlock()
		fail(w, 429, "node request upload already in progress")
		return
	}
	s.active[key] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, key); s.mu.Unlock() }()
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.Header.Get("Content-Encoding") != "" {
		fail(w, 415, "JSON request telemetry required")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, accesslog.MaxPayload))
	decoder.DisallowUnknownFields()
	var batch accesslog.Batch
	if decoder.Decode(&batch) != nil || decoder.Decode(new(any)) != io.EOF || len(batch.Events) > accesslog.MaxBatch || !batch.Pipeline.Valid(time.Now()) {
		fail(w, 400, "invalid request telemetry batch")
		return
	}
	db, ok := s.config.Store.(store.ApplicationStore)
	if !ok {
		fail(w, 503, "application storage unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2500*time.Millisecond)
	defer cancel()
	revisions, err := db.ListApplications(ctx, node.OrganizationID, node.ClusterID)
	if err != nil {
		storeError(w, err)
		return
	}
	known := map[string]bool{}
	specs := map[string]domain.HTTPApplication{}
	for _, v := range revisions {
		if v.Spec.RequestTelemetry {
			known[v.ApplicationID+":"+v.Spec.Digest()] = true
			specs[v.ApplicationID+":"+v.Spec.Digest()] = v.Spec
		}
	}
	rows := make([]map[string]any, 0, len(batch.Events)+1)
	seen := map[string]bool{}
	now := time.Now()
	stamp := func(kind string, at int64) map[string]any {
		return map[string]any{"organization_id": node.OrganizationID, "cluster_id": node.ClusterID, "node_id": node.ID, "kind": kind, "at_ms": at, "observed_at_ms": at}
	}
	for _, e := range batch.Events {
		if e.Traffic != nil && !validTrafficEvidence(specs[e.ApplicationID+":"+e.SpecDigest], *e.Traffic, e.Status) {
			fail(w, 400, "invalid traffic policy evidence")
			return
		}
		if e.Geo != nil && e.Geo.Profile != "" && (batch.Pipeline.Geo == nil || e.Geo.Profile != batch.Pipeline.Geo.Profile) {
			fail(w, 400, "request geography profile differs from collection state")
			return
		}
		if !e.Valid(now) || !strings.HasPrefix(e.ID, batch.Pipeline.BootID+".") || seen[e.ID] || !known[e.ApplicationID+":"+e.SpecDigest] {
			fail(w, 400, "invalid application request evidence")
			return
		}
		seen[e.ID] = true
		row := stamp("request", e.AtMS)
		row["event_id"], row["application_id"], row["spec_digest"], row["server"], row["method"] = e.ID, e.ApplicationID, e.SpecDigest, e.Server, e.Method
		row["status"], row["status_class"], row["bytes"], row["duration_ms"] = e.Status, strconv.Itoa(e.Status/100)+"xx", e.Bytes, e.DurationMS
		if e.Status == 0 {
			row["status_class"] = "other"
		}
		row["response_ms"], row["connect_ms"], row["queue_ms"], row["scheme"], row["termination"] = e.ResponseMS, e.ConnectMS, e.QueueMS, e.Scheme, e.Termination
		kind := e.PathKind
		if kind == "" {
			kind = "unavailable"
		}
		row["path"], row["path_kind"] = e.Path, kind
		if g := e.Geo; g != nil {
			row["geo_status"], row["client_source"], row["country"], row["asn"], row["geo_profile"] = g.Status, g.Source, g.Country, g.ASN, g.Profile
		}
		if e.Traffic != nil {
			row["traffic_rule"], row["traffic_decision"] = e.Traffic.RuleID, e.Traffic.Decision
		}
		row["message"] = "HAProxy request"
		rows = append(rows, row)
	}
	p := batch.Pipeline
	row := stamp("pipeline", p.AtMS)
	row["boot_id"], row["listening"], row["received"], row["ignored"], row["rejected"], row["dropped"], row["unconfirmed"] = p.BootID, p.Listening, p.Received, p.Ignored, p.Rejected, p.Dropped, p.Unconfirmed
	row["path_mode"], row["path_policy"] = p.PathMode, p.PathPolicy
	if p.Geo != nil {
		data, _ := json.Marshal(p.Geo)
		row["geo_profile_data"] = string(data)
	}
	row["message"] = "Request collection heartbeat"
	rows = append(rows, row)
	if _, err = s.config.Store.AuthenticateNode(ctx, node.ID); err != nil {
		fail(w, 401, "node access revoked")
		return
	}
	if s.logs.Insert(ctx, rows) != nil {
		fail(w, 503, "request telemetry storage unavailable")
		return
	}
	w.WriteHeader(204)
}
