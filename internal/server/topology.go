package server

import (
	"context"
	"net/http"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

func (s *Server) setTopology(w http.ResponseWriter, r *http.Request, org string) {
	persistence, ok := s.config.Store.(store.TopologyStore)
	if !ok {
		fail(w, 503, "topology storage unavailable")
		return
	}
	var topology domain.Topology
	if !readJSON(w, r, &topology) {
		return
	}
	if err := persistence.SetTopology(r.Context(), org, r.PathValue("cluster"), topology); err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"updated": true})
}

func (s *Server) trafficFrontend(ctx context.Context, node domain.Node) string {
	cluster, err := s.config.Store.GetCluster(ctx, node.OrganizationID, node.ClusterID)
	if err == nil && cluster.Topology != nil {
		for _, n := range cluster.Topology.Nodes {
			if n.NodeID == node.ID {
				return cluster.Topology.Frontend
			}
		}
	}
	return ""
}
