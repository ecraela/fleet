package store

import (
	"context"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type ConnectorStore interface {
	CreateConnector(context.Context, domain.ConnectorLink) error
	ConnectorForOrganization(context.Context, string) (domain.ConnectorLink, error)
	ClaimConnector(context.Context, string, string, string, time.Time) (domain.ConnectorLink, error)
	AuthenticateConnector(context.Context, string, string) (domain.ConnectorLink, error)
	RevokeConnector(context.Context, string, string) error
	RecordConnectorAudit(context.Context, string, string, string) error
}

func validConnector(link domain.ConnectorLink) bool {
	return domain.ValidID(link.ID) && domain.ValidID(link.OrganizationID) && validName(link.Name) &&
		domain.ValidDigest(link.PairDigest) && link.TokenDigest == "" && link.ClaimedAt == nil && link.RevokedAt == nil &&
		link.PairExpiresAt.After(link.CreatedAt) && link.PairExpiresAt.Sub(link.CreatedAt) <= 10*time.Minute
}
