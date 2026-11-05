// Package store persists control state separately from telemetry and Converge.
package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ecraela/fleet/internal/domain"
)

type Store interface {
	CreateCluster(context.Context, string, string) (domain.Cluster, error)
	GetCluster(context.Context, string, string) (domain.Cluster, error)
	CreateEnrollmentToken(context.Context, string, string, string, time.Time) error
	LookupEnrollment(context.Context, string, time.Time) (domain.Enrollment, error)
	RevokeEnrollmentToken(context.Context, string, string) error
	Enroll(context.Context, string, domain.Node, time.Time) (domain.Node, error)
	GetNode(context.Context, string, string) (domain.Node, error)
	AuthenticateNode(context.Context, string) (domain.Node, error)
	ListNodes(context.Context, string) ([]domain.Node, error)
	RevokeNode(context.Context, string, string) error
	Close()
}

func validName(name string) bool {
	return utf8.ValidString(name) && strings.TrimSpace(name) != "" && len(name) <= 255 && !strings.ContainsRune(name, '\x00')
}

func validateNode(node domain.Node) error {
	if !domain.ValidID(node.ID) || !validName(node.Name) ||
		(node.OrganizationID != "" && !domain.ValidID(node.OrganizationID)) ||
		(node.ClusterID != "" && !domain.ValidID(node.ClusterID)) {
		return domain.ErrInvalidInput
	}
	return nil
}

func bindNode(node domain.Node, enrollment domain.Enrollment, now time.Time) (domain.Node, error) {
	if (node.OrganizationID != "" && node.OrganizationID != enrollment.OrganizationID) ||
		(node.ClusterID != "" && node.ClusterID != enrollment.ClusterID) {
		return domain.Node{}, domain.ErrInvalidEnrollment
	}
	node.OrganizationID = enrollment.OrganizationID
	node.ClusterID = enrollment.ClusterID
	node.CreatedAt = now.UTC()
	node.RevokedAt = nil
	return node, nil
}
