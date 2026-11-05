// Package accesslog implements the opt-in, bounded request-telemetry workload.
// It has no dependency on the configuration/control workers or metric queues.
package accesslog

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

const MaxBatch = 256
const MaxPayload = 256 << 10
const QueueSize = 2048

var ErrIgnored = errors.New("unmanaged request")
var ErrInvalid = errors.New("invalid request telemetry")

type Event struct {
	ID            string           `json:"id"`
	AtMS          int64            `json:"at_ms"`
	ApplicationID string           `json:"application_id"`
	SpecDigest    string           `json:"spec_digest"`
	Server        string           `json:"server"`
	Method        string           `json:"method"`
	Status        int              `json:"status"`
	Bytes         int64            `json:"bytes"`
	DurationMS    int64            `json:"duration_ms"`
	ResponseMS    int64            `json:"response_ms"`
	ConnectMS     int64            `json:"connect_ms"`
	QueueMS       int64            `json:"queue_ms"`
	Scheme        string           `json:"scheme"`
	Termination   string           `json:"termination"`
	Path          string           `json:"path,omitempty"`
	PathKind      string           `json:"path_kind,omitempty"`
	Geo           *Geography       `json:"geo,omitempty"`
	Traffic       *TrafficDecision `json:"traffic,omitempty"`
}

type Pipeline struct {
	Listening   bool           `json:"listening"`
	BootID      string         `json:"boot_id"`
	AtMS        int64          `json:"at_ms"`
	Received    uint64         `json:"received"`
	Ignored     uint64         `json:"ignored"`
	Rejected    uint64         `json:"rejected"`
	Dropped     uint64         `json:"dropped"`
	Unconfirmed uint64         `json:"unconfirmed"`
	PathMode    string         `json:"path_mode,omitempty"`
	PathPolicy  string         `json:"path_policy,omitempty"`
	Geo         *GeoCollection `json:"geo,omitempty"`
}
type Batch struct {
	Events   []Event  `json:"events"`
	Pipeline Pipeline `json:"pipeline"`
}

func EventID(boot string, sequence uint64) string { return fmt.Sprintf("%s.%016x", boot, sequence) }
func ValidEventID(id string) bool {
	if len(id) != 53 || id[36] != '.' || !domain.ValidID(id[:36]) {
		return false
	}
	_, err := strconv.ParseUint(id[37:], 16, 64)
	return err == nil && strings.ToLower(id) == id
}
func ValidMethod(m string) bool {
	return slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE", "OTHER"}, m)
}
func (e Event) Valid(now time.Time) bool {
	return (e.Traffic == nil || e.Traffic.Valid()) && (e.Geo == nil || e.Geo.Valid()) && ValidPathEvidence(e.Path, e.PathKind) && ValidEventID(e.ID) && domain.ValidID(e.ApplicationID) && domain.ValidDigest(e.SpecDigest) &&
		(e.Server == "<NOSRV>" || domain.ValidRuntimeName(e.Server)) && ValidMethod(e.Method) &&
		(e.Status == 0 || e.Status >= 100 && e.Status <= 599) && e.Bytes >= 0 && e.Bytes <= 1<<40 &&
		e.DurationMS >= 0 && e.DurationMS <= 86400000 && e.ResponseMS >= -1 && e.ResponseMS <= 86400000 &&
		e.ConnectMS >= -1 && e.ConnectMS <= 86400000 && e.QueueMS >= -1 && e.QueueMS <= 86400000 &&
		(e.Scheme == "http" || e.Scheme == "https") && len(e.Termination) == 2 && validTermination(e.Termination) &&
		e.AtMS >= now.Add(-time.Minute).UnixMilli() && e.AtMS <= now.Add(time.Second).UnixMilli()
}
func validTermination(s string) bool {
	for _, c := range s {
		if c != '-' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}
func (p Pipeline) Valid(now time.Time) bool {
	if p.Geo != nil && !p.Geo.Valid() {
		return false
	}
	if p.PathMode != "" && p.PathMode != "off" && p.PathMode != "sanitized" && p.PathMode != "exact" || p.PathPolicy != "" && !domain.ValidDigest(p.PathPolicy) {
		return false
	}
	if !domain.ValidID(p.BootID) || p.AtMS < now.Add(-time.Minute).UnixMilli() || p.AtMS > now.Add(time.Second).UnixMilli() {
		return false
	}
	for _, n := range []uint64{p.Received, p.Ignored, p.Rejected, p.Dropped, p.Unconfirmed} {
		if n > 1<<53-1 {
			return false
		}
	}
	return true
}

// Parse accepts only the operator-installed cv1 format. It never exports the
// original datagram, even on errors. Stamp completion receipt, not TCP accept.
func Parse(raw []byte, boot string, sequence uint64, now time.Time) (Event, error) {
	return parse(raw, boot, sequence, now, nil)
}

func parse(raw []byte, boot string, sequence uint64, now time.Time, policy *PathPolicy) (Event, error) {
	return parseGeo(raw, boot, sequence, now, policy, nil)
}

func parseGeo(raw []byte, boot string, sequence uint64, now time.Time, policy *PathPolicy, geo *GeoResolver) (Event, error) {
	e := Event{}
	if len(raw) > 2048 {
		return e, ErrInvalid
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 || f[0] != "cv1" && f[0] != "cv2" && f[0] != "cv3" && f[0] != "cv4" && f[0] != "cv5" {
		return e, ErrIgnored
	}
	if f[0] == "cv1" && len(f) != 13 || f[0] == "cv2" && len(f) != 15 || (f[0] == "cv3" || f[0] == "cv4") && len(f) != 21 || f[0] == "cv5" && len(f) != 23 {
		return e, ErrInvalid
	}
	if !strings.HasPrefix(f[1], domain.ApplicationBackendPrefix) || f[2] == "-" {
		return e, ErrIgnored
	}
	id := strings.TrimPrefix(f[1], domain.ApplicationBackendPrefix)
	if len(id) != 32 {
		return e, ErrInvalid
	}
	e.ApplicationID = id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	e.ID, e.AtMS, e.SpecDigest, e.Server, e.Method, e.Termination = EventID(boot, sequence), now.UnixMilli(), f[2], f[3], f[4], f[12]
	if !ValidMethod(e.Method) {
		e.Method = "OTHER"
	}
	status, err := strconv.Atoi(f[5])
	if err != nil {
		return Event{}, ErrInvalid
	}
	e.Status = status
	for i, dst := range []*int64{&e.Bytes, &e.DurationMS, &e.ResponseMS, &e.ConnectMS, &e.QueueMS} {
		value, err := strconv.ParseInt(f[6+i], 10, 64)
		if err != nil {
			return Event{}, ErrInvalid
		}
		*dst = value
	}
	switch f[11] {
	case "0":
		e.Scheme = "http"
	case "1":
		e.Scheme = "https"
	default:
		return Event{}, ErrInvalid
	}
	if f[0] == "cv2" || f[0] == "cv3" || f[0] == "cv4" || f[0] == "cv5" {
		e.PathKind = "invalid"
		n, err := strconv.Atoi(f[13])
		if f[13] == "-" && f[14] == "-" {
			e.PathKind = "unavailable"
		} else if err == nil && n > 0 && n <= MaxPath && len(f[14]) == n*2 {
			if path, err := hex.DecodeString(f[14]); err == nil {
				e.Path, e.PathKind = policy.capture(string(path))
			}
		}
	}
	if f[0] == "cv3" || f[0] == "cv4" || f[0] == "cv5" {
		header := ""
		count, countErr := strconv.Atoi(f[18])
		n, lengthErr := strconv.Atoi(f[19])
		valid := countErr == nil && count >= 0 && count <= 16
		if f[19] == "-" && f[20] == "-" || n == 0 && f[20] == "-" {
			valid = valid && count == 0
		} else if lengthErr == nil && n > 0 && n <= MaxForwardedFor && len(f[20]) == n*2 {
			decoded, err := hex.DecodeString(f[20])
			valid = valid && err == nil
			header = string(decoded)
		} else {
			valid = false
		}
		if !valid {
			// Only a trusted XFF chain uses these fields. A malformed header
			// cannot poison direct-peer or PROXY-protocol observations.
			header, count = "", -1
		}
		g := geo.Resolve(f[15], f[16], f[17] == "1", header, count)
		if geo != nil && (f[0] == "cv3" && geo.policy.Mode == "proxy_protocol" || f[17] != "0" && f[17] != "1" && geo.policy.Mode != "off" && geo.policy.Mode != "peer") {
			g = Geography{Status: "unresolved", Source: "unknown", Profile: geo.collection.Profile}
		}
		e.Geo = &g
		// Fields split from the local record must not keep its address/header
		// backing buffer alive in the outbound queue.
		e.SpecDigest, e.Server, e.Method, e.Termination = strings.Clone(e.SpecDigest), strings.Clone(e.Server), strings.Clone(e.Method), strings.Clone(e.Termination)
	}
	if f[0] == "cv5" && (f[21] != "-" || f[22] != "-") && (f[21] != "none" || f[22] != "none") {
		e.Traffic = &TrafficDecision{RuleID: strings.Clone(f[21]), Decision: strings.Clone(f[22])}
	}
	if !e.Valid(now) {
		return Event{}, ErrInvalid
	}
	return e, nil
}
