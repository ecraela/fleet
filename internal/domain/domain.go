// Package domain defines the identities persisted by the HAProxy manager.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidInput      = errors.New("invalid input")
	ErrInvalidEnrollment = errors.New("invalid enrollment token")
	ErrRevoked           = errors.New("node revoked")
	ErrConflict          = errors.New("conflict")
)

type Cluster struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"created_at"`
	Topology       *Topology `json:"topology,omitempty"`
}

// Topology declares traffic wiring independently of enrollment. Only an explicit
// Control binding permits authorized entrypoint maintenance; URLs are display-only.
type Topology struct {
	Control    *EntrypointControl `json:"control,omitempty"`
	Entrypoint string             `json:"entrypoint"`
	Strategy   string             `json:"strategy"`
	Frontend   string             `json:"frontend"`
	Backend    string             `json:"backend"`
	Nodes      []TopologyNode     `json:"nodes"`
}

type TopologyNode struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
}

type Node struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	ClusterID      string     `json:"cluster_id"`
	Name           string     `json:"name"`
	CreatedAt      time.Time  `json:"created_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

// Enrollment contains no bearer secret or digest. Its organization and cluster
// come from the server-issued token, never from the enrolling agent.
type Enrollment struct {
	OrganizationID string
	ClusterID      string
	ExpiresAt      time.Time
}

// NewID returns a random UUID v4. crypto/rand.Read cannot fail on supported Go
// platforms; it terminates the process if the operating system RNG fails.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidID accepts canonical lowercase UUIDs so certificate and database
// identities cannot disagree through alternate text representations.
func ValidID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func ValidDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	if err != nil {
		return false
	}
	for _, c := range digest {
		if c >= 'A' && c <= 'F' {
			return false
		}
	}
	return true
}
