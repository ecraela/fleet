package server

import "fmt"

// Older ingestion accepted arbitrary status labels. Reduce at storage so their
// cardinality cannot exhaust a shared response budget. A duplicate singleton is
// withheld rather than choosing one value. Preserve the complete scope identity.
func singletonStatusQuery(selector string) string {
	group := "(__name__, organization_id, cluster_id, node_id)"
	return fmt.Sprintf("max by %s (%s) and on %s (count by %s (%s) == 1)", group, selector, group, group, selector)
}
