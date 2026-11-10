package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
)

type memoryToken struct {
	domain.Enrollment
	nodeID   string
	consumed bool
	revoked  bool
}

type auditEvent struct {
	acmePlanID     string
	maintenanceID  string
	rolloutID      string
	operationID    string
	actorUserID    string
	organizationID string
	clusterID      string
	nodeID         string
	action         string
	createdAt      time.Time
}

// Memory provides the same atomic enrollment and tenant semantics as Postgres
// for tests. Production startup must use the persistent store.
type Memory struct {
	softwareUpdates          map[string]software.Update
	softwareRequests         map[string]string
	softwareEvents           []software.Update
	softwareUpdaters         map[string]software.Updater
	installationUpdates      map[string]software.InstallationUpdate
	installationRequests     map[string]string
	installationEvents       []software.InstallationUpdate
	installationUpdater      *software.Updater
	installationUpdaterOrg   string
	live                     map[string][]byte
	connectors               map[string]domain.ConnectorLink
	local                    *localMemoryState
	notificationDestinations map[string]notificationDestination
	incidentDeliveries       map[string]notificationRecord
	incidents                map[string]incidentRecord
	incidentEvaluations      map[string]int64
	incidentEvents           []domain.IncidentEvent
	incidentSequence         int64
	acmePlans                map[string]domain.ACMEPlan
	maintenances             map[string]domain.Maintenance
	maintenanceRequests      map[string]maintenanceRequest
	rollouts                 map[string]domain.Rollout
	applicationRevisions     map[string]domain.ApplicationRevision
	listenerRevisions        map[string]domain.ListenerRevision
	revisions                map[string]domain.ConfigurationRevision
	controls                 map[string]domain.NodeControl
	operations               map[string]domain.Operation
	browser                  *browserMemoryState
	mu                       sync.Mutex
	clusters                 map[string]domain.Cluster
	nodes                    map[string]domain.Node
	tokens                   map[string]memoryToken
	audit                    []auditEvent
}

var _ Store = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{
		softwareUpdates:      make(map[string]software.Update),
		softwareRequests:     make(map[string]string),
		softwareUpdaters:     make(map[string]software.Updater),
		installationUpdates:  make(map[string]software.InstallationUpdate),
		installationRequests: make(map[string]string),
		clusters:             make(map[string]domain.Cluster),
		nodes:                make(map[string]domain.Node),
		tokens:               make(map[string]memoryToken),
	}
}

func (m *Memory) Close() {}

func (m *Memory) CreateCluster(ctx context.Context, organizationID, name string) (domain.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return domain.Cluster{}, err
	}
	if !domain.ValidID(organizationID) || !validName(name) {
		return domain.Cluster{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cluster := domain.Cluster{ID: domain.NewID(), OrganizationID: organizationID, Name: name, CreatedAt: time.Now().UTC()}
	m.clusters[cluster.ID] = cluster
	m.audit = append(m.audit, auditEvent{actorUserID: domain.Actor(ctx), organizationID: organizationID, clusterID: cluster.ID, action: "cluster.created", createdAt: cluster.CreatedAt})
	return cloneCluster(cluster), nil
}

func (m *Memory) GetCluster(ctx context.Context, organizationID, clusterID string) (domain.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return domain.Cluster{}, err
	}
	if !domain.ValidID(organizationID) || !domain.ValidID(clusterID) {
		return domain.Cluster{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cluster, ok := m.clusters[clusterID]
	if !ok || cluster.OrganizationID != organizationID {
		return domain.Cluster{}, domain.ErrNotFound
	}
	return cloneCluster(cluster), nil
}

func (m *Memory) CreateEnrollmentToken(ctx context.Context, organizationID, clusterID, digest string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(organizationID) || !domain.ValidID(clusterID) || !domain.ValidDigest(digest) || !expiresAt.After(time.Now()) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cluster, ok := m.clusters[clusterID]
	if !ok || cluster.OrganizationID != organizationID {
		return domain.ErrNotFound
	}
	if _, exists := m.tokens[digest]; exists {
		return domain.ErrConflict
	}
	m.tokens[digest] = memoryToken{Enrollment: domain.Enrollment{OrganizationID: organizationID, ClusterID: clusterID, ExpiresAt: expiresAt.UTC()}}
	m.audit = append(m.audit, auditEvent{actorUserID: domain.Actor(ctx), organizationID: organizationID, clusterID: clusterID, action: "enrollment.issued", createdAt: time.Now().UTC()})
	return nil
}

func (m *Memory) LookupEnrollment(ctx context.Context, digest string, now time.Time) (domain.Enrollment, error) {
	if err := ctx.Err(); err != nil {
		return domain.Enrollment{}, err
	}
	if !domain.ValidDigest(digest) {
		return domain.Enrollment{}, domain.ErrInvalidEnrollment
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.tokens[digest]
	now = freshTime(now)
	if !ok || token.consumed || token.revoked || !now.Before(token.ExpiresAt) {
		return domain.Enrollment{}, domain.ErrInvalidEnrollment
	}
	return token.Enrollment, nil
}

func (m *Memory) RevokeEnrollmentToken(ctx context.Context, organizationID, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(organizationID) || !domain.ValidDigest(digest) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.tokens[digest]
	if !ok || token.OrganizationID != organizationID {
		return domain.ErrNotFound
	}
	if !token.revoked {
		token.revoked = true
		m.tokens[digest] = token
		m.audit = append(m.audit, auditEvent{actorUserID: domain.Actor(ctx), organizationID: organizationID, clusterID: token.ClusterID, action: "enrollment.revoked", createdAt: time.Now().UTC()})
	}
	return nil
}

func (m *Memory) Enroll(ctx context.Context, digest string, node domain.Node, now time.Time) (domain.Node, error) {
	if err := ctx.Err(); err != nil {
		return domain.Node{}, err
	}
	if err := validateNode(node); err != nil {
		return domain.Node{}, err
	}
	if !domain.ValidDigest(digest) {
		return domain.Node{}, domain.ErrInvalidEnrollment
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.tokens[digest]
	now = freshTime(now)
	if !ok || token.consumed || token.revoked || !now.Before(token.ExpiresAt) {
		return domain.Node{}, domain.ErrInvalidEnrollment
	}
	bound, err := bindNode(node, token.Enrollment, now)
	if err != nil {
		return domain.Node{}, err
	}
	if _, exists := m.nodes[node.ID]; exists {
		return domain.Node{}, domain.ErrConflict
	}
	token.consumed = true
	token.nodeID = bound.ID
	m.tokens[digest] = token
	m.nodes[node.ID] = bound
	m.audit = append(m.audit, auditEvent{actorUserID: domain.Actor(ctx), organizationID: bound.OrganizationID, clusterID: bound.ClusterID, nodeID: bound.ID, action: "node.enrolled", createdAt: now.UTC()})
	return cloneNode(bound), nil
}

func (m *Memory) GetNode(ctx context.Context, organizationID, nodeID string) (domain.Node, error) {
	if err := ctx.Err(); err != nil {
		return domain.Node{}, err
	}
	if !domain.ValidID(organizationID) || !domain.ValidID(nodeID) {
		return domain.Node{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[nodeID]
	if !ok || node.OrganizationID != organizationID {
		return domain.Node{}, domain.ErrNotFound
	}
	return cloneNode(node), nil
}

func (m *Memory) AuthenticateNode(ctx context.Context, nodeID string) (domain.Node, error) {
	if err := ctx.Err(); err != nil {
		return domain.Node{}, err
	}
	if !domain.ValidID(nodeID) {
		return domain.Node{}, domain.ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[nodeID]
	if !ok {
		return domain.Node{}, domain.ErrNotFound
	}
	if node.RevokedAt != nil {
		return domain.Node{}, domain.ErrRevoked
	}
	return cloneNode(node), nil
}

func (m *Memory) ListNodes(ctx context.Context, organizationID string) ([]domain.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !domain.ValidID(organizationID) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	nodes := make([]domain.Node, 0)
	for _, node := range m.nodes {
		if node.OrganizationID == organizationID {
			nodes = append(nodes, cloneNode(node))
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].CreatedAt.Equal(nodes[j].CreatedAt) {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].CreatedAt.Before(nodes[j].CreatedAt)
	})
	return nodes, nil
}

func (m *Memory) RevokeNode(ctx context.Context, organizationID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(organizationID) || !domain.ValidID(nodeID) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[nodeID]
	if !ok || node.OrganizationID != organizationID {
		return domain.ErrNotFound
	}
	if node.RevokedAt == nil {
		now := time.Now().UTC()
		node.RevokedAt = &now
		m.nodes[nodeID] = node
		m.audit = append(m.audit, auditEvent{actorUserID: domain.Actor(ctx), organizationID: organizationID, clusterID: node.ClusterID, nodeID: node.ID, action: "node.revoked", createdAt: now})
	}
	return nil
}

func cloneNode(node domain.Node) domain.Node {
	if node.RevokedAt != nil {
		at := *node.RevokedAt
		node.RevokedAt = &at
	}
	return node
}

func freshTime(requested time.Time) time.Time {
	current := time.Now().UTC()
	if current.After(requested) {
		return current
	}
	return requested.UTC()
}
