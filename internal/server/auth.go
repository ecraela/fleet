package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/ecraela/fleet/internal/domain"
)

// OperatorCredential is a tenant-bound service principal for this first slice.
// Cloud browser federation is a separate integration; these keys never enter a
// browser or reuse a Converge organization API key.
type OperatorCredential struct {
	OrganizationID string `json:"organization_id"`
	TokenSHA256    string `json:"token_sha256"`
}

func LoadOperators(tokenFile, organizationID, credentialsFile string) ([]OperatorCredential, error) {
	var credentials []OperatorCredential
	if credentialsFile != "" {
		data, err := os.ReadFile(credentialsFile)
		if err != nil {
			return nil, err
		}
		if len(data) > 1<<20 || json.Unmarshal(data, &credentials) != nil {
			return nil, errors.New("invalid operator credentials file")
		}
	}
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return nil, err
		}
		token := strings.TrimSpace(string(data))
		if len(token) < 32 || len(token) > 256 {
			return nil, errors.New("operator token must contain 32 to 256 characters")
		}
		credentials = append(credentials, OperatorCredential{OrganizationID: organizationID, TokenSHA256: digest(token)})
	}
	if len(credentials) == 0 {
		return nil, errors.New("a tenant-bound operator credential is required")
	}
	seen := make(map[string]bool)
	for _, c := range credentials {
		if !domain.ValidID(c.OrganizationID) || !domain.ValidDigest(c.TokenSHA256) || seen[c.TokenSHA256] {
			return nil, errors.New("invalid or duplicated operator credential")
		}
		seen[c.TokenSHA256] = true
	}
	return credentials, nil
}

func (s *Server) operator(r *http.Request) (string, bool) {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") || len(value) > 263 {
		return "", false
	}
	got := digest(strings.TrimPrefix(value, "Bearer "))
	for _, c := range s.config.Operators {
		if subtle.ConstantTimeCompare([]byte(got), []byte(c.TokenSHA256)) == 1 {
			// A requested organization is a consistency guard, not authority.
			if requested := r.Header.Get("X-Organization-ID"); requested != c.OrganizationID {
				return "", false
			}
			return c.OrganizationID, true
		}
	}
	return "", false
}

func (s *Server) canReadOrganization(r *http.Request, organizationID string) bool {
	if org, ok := s.operator(r); ok {
		return org == organizationID
	}
	identity, ok := s.managementIdentity(r)
	return ok && identity.OrganizationID == organizationID && identity.Has("haproxy.read") &&
		(r.Header.Get("X-Organization-ID") == "" || r.Header.Get("X-Organization-ID") == organizationID)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
