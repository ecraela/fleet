package store

import (
	"context"
	"github.com/ecraela/fleet/internal/domain"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func connectorContract(t *testing.T, s ConnectorStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	org := domain.NewID()
	l := domain.ConnectorLink{ID: domain.NewID(), OrganizationID: org, Name: "Customer manager", PairDigest: strings.Repeat("a", 64), CreatedAt: now, PairExpiresAt: now.Add(10 * time.Minute)}
	if err := s.CreateConnector(ctx, l); err != nil {
		t.Fatal(err)
	}
	other := l
	other.ID = domain.NewID()
	if s.CreateConnector(ctx, other) == nil {
		t.Fatal("two active links in tenant")
	}
	if _, e := s.ConnectorForOrganization(ctx, domain.NewID()); e == nil {
		t.Fatal("cross tenant link read")
	}
	if _, e := s.ClaimConnector(ctx, l.ID, strings.Repeat("b", 64), strings.Repeat("c", 64), now); e == nil {
		t.Fatal("wrong pairing credential")
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for _, token := range []string{strings.Repeat("c", 64), strings.Repeat("d", 64)} {
		wg.Go(func() {
			if _, e := s.ClaimConnector(ctx, l.ID, l.PairDigest, token, now); e == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("pair code not single owner")
	}
	claimed, e := s.ConnectorForOrganization(ctx, org)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.ClaimConnector(ctx, l.ID, l.PairDigest, claimed.TokenDigest, now); e != nil {
		t.Fatal("same-identity lost-response retry failed", e)
	}
	if _, e := s.AuthenticateConnector(ctx, l.ID, strings.Repeat("e", 64)); e == nil {
		t.Fatal("wrong connector credential")
	}
	if _, e := s.AuthenticateConnector(ctx, l.ID, claimed.TokenDigest); e != nil {
		t.Fatal(e)
	}
	if s.RevokeConnector(ctx, domain.NewID(), l.ID) == nil {
		t.Fatal("cross tenant revocation")
	}
	if e := s.RevokeConnector(ctx, org, l.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.AuthenticateConnector(ctx, l.ID, claimed.TokenDigest); e == nil {
		t.Fatal("revoked connector authenticated")
	}
	if _, e := s.ClaimConnector(ctx, l.ID, l.PairDigest, claimed.TokenDigest, now); e == nil {
		t.Fatal("revoked link reclaimed")
	}
	l.ID = domain.NewID()
	l.CreatedAt = now.Add(-11 * time.Minute)
	l.PairExpiresAt = now.Add(-time.Minute)
	if e := s.CreateConnector(ctx, l); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ClaimConnector(ctx, l.ID, l.PairDigest, claimed.TokenDigest, now); e == nil {
		t.Fatal("expired pairing accepted")
	}
	l.ID = domain.NewID()
	l.CreatedAt = now
	l.PairExpiresAt = now.Add(10 * time.Minute)
	if e := s.CreateConnector(ctx, l); e != nil {
		t.Fatal("expired pending link did not release slot", e)
	}
}
func TestConnectorMemoryContract(t *testing.T)   { connectorContract(t, NewMemory()) }
func TestConnectorPostgresContract(t *testing.T) { connectorContract(t, postgresForTest(t)) }

func TestConnectorPostgresAuditImmutable(t *testing.T) {
	p := postgresForTest(t)
	ctx := t.Context()
	if err := p.RecordConnectorAudit(ctx, domain.NewID(), domain.NewID(), "connector.local_linked"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"UPDATE hm_connector_audit SET action='connector.revoked'", "DELETE FROM hm_connector_audit", "TRUNCATE hm_connector_audit"} {
		if _, err := p.pool.Exec(ctx, sql); err == nil {
			t.Fatal("audit mutation allowed", sql)
		}
	}
}
