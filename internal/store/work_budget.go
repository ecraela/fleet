package store

import (
	"context"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

const maxRunnableWork = 16
const maxTenantRunnableWork = 8
const maxTenantMaintenanceReservations = 16

func maintenanceRunnable(m domain.Maintenance) bool {
	return !m.Terminal() && m.Status != "maintenance" && m.Status != "attention"
}

func checkWorkBudget(total, tenant int) error {
	if total >= maxRunnableWork || tenant >= maxTenantRunnableWork {
		return domain.ErrConflict
	}
	return nil
}

// Called only while the owning memory transaction holds the installation lock.
func (m *Memory) admitWork(org, excludeMaintenance string) error {
	total, tenant := 0, 0
	for _, r := range m.rollouts {
		if !r.Terminal() {
			total++
			if r.OrganizationID == org {
				tenant++
			}
		}
	}
	for _, r := range m.maintenances {
		if r.ID != excludeMaintenance && maintenanceRunnable(r) {
			total++
			if r.OrganizationID == org {
				tenant++
			}
		}
	}
	return checkWorkBudget(total, tenant)
}

// The same lock covers creation and resumption. Idle safety fences stay durable
// but do not consume worker capacity belonging to other organizations.
func admitWork(ctx context.Context, db pgx.Tx, org, excludeMaintenance string) error {
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(492584126125)`); err != nil {
		return err
	}
	var total, tenant int
	err := db.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE organization_id=$1) FROM (
 SELECT organization_id FROM hm_rollouts WHERE status IN ('running','rolling_back')
 UNION ALL
 SELECT organization_id FROM hm_maintenance WHERE status IN ('withdrawing','draining','checking_rejoin','rejoining')
 AND id IS DISTINCT FROM NULLIF($2,'')::uuid
) AS runnable`, org, excludeMaintenance).Scan(&total, &tenant)
	if err != nil {
		return err
	}
	return checkWorkBudget(total, tenant)
}
