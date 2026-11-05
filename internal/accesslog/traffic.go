package accesslog

import (
	"github.com/ecraela/fleet/internal/domain"
	"slices"
)

// TrafficDecision contains compiler-owned identifiers and one bounded decision.
// It never contains the evaluated client address, headers or Geo map paths.
type TrafficDecision struct {
	RuleID   string `json:"rule_id"`
	Decision string `json:"decision"`
}

func (d TrafficDecision) Valid() bool {
	return (domain.ValidID(d.RuleID) || d.RuleID == "unknown") && slices.Contains([]string{"allow", "block", "would_allow", "would_block"}, d.Decision)
}
