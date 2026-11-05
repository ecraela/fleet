package domain

import (
	"encoding/json"
	"math"
)

const MaxLiveObservationBytes = 64 << 10

type LiveFrontend struct {
	BytesIn     *float64           `json:"bytes_in,omitempty"`
	BytesOut    *float64           `json:"bytes_out,omitempty"`
	Connections *float64           `json:"connections,omitempty"`
	Responses   map[string]float64 `json:"responses,omitempty"`
}

// LiveObservation is a bounded latest-value summary, not a second history store.
type LiveObservation struct {
	AtMS        int64                   `json:"at_ms"`
	ScrapeOK    bool                    `json:"scrape_ok"`
	Connections *float64                `json:"connections,omitempty"`
	Frontends   map[string]LiveFrontend `json:"frontends,omitempty"`
	LatencyMS   map[string]float64      `json:"latency_ms,omitempty"`
}

type LivePair struct {
	Current  LiveObservation  `json:"current"`
	Previous *LiveObservation `json:"previous,omitempty"`
}

func (o LiveObservation) Valid() bool {
	finite := func(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
	optional := func(v *float64) bool { return v == nil || finite(*v) }
	if o.AtMS <= 0 || o.AtMS > 1e15 || !optional(o.Connections) || len(o.Frontends) > MaxProxies || len(o.LatencyMS) > MaxProxies {
		return false
	}
	for name, f := range o.Frontends {
		if !ValidRuntimeName(name) || !optional(f.BytesIn) || !optional(f.BytesOut) || !optional(f.Connections) || len(f.Responses) > 6 {
			return false
		}
		for code, value := range f.Responses {
			if (code != "1xx" && code != "2xx" && code != "3xx" && code != "4xx" && code != "5xx" && code != "other") || !finite(value) {
				return false
			}
		}
	}
	for name, value := range o.LatencyMS {
		if !ValidRuntimeName(name) || !finite(value) {
			return false
		}
	}
	raw, err := json.Marshal(o)
	return err == nil && len(raw) <= MaxLiveObservationBytes
}
