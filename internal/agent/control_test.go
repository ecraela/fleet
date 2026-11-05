package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/haproxyruntime"
)

func journalCommand() domain.Operation {
	return domain.Operation{ID: domain.NewID(), Kind: "weight", Backend: "application", Server: "app-a", Weight: 200, ExpectedGeneration: strings.Repeat("a", 64), ExpectedState: "ready", ExpectedWeight: 100, Deadline: time.Now().Add(30 * time.Second)}
}
func TestOperationJournalPersistsIntentAndRejectsConflictingReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	journal, err := loadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	op := journalCommand()
	if _, fresh, err := journal.begin(op); err != nil || !fresh {
		t.Fatal(err)
	}
	restarted, err := loadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, fresh, err := restarted.begin(op)
	if err != nil || fresh || entry.Result != nil {
		t.Fatal("intent not preserved before execution", err)
	}
	changed := op
	changed.Weight = 250
	if _, _, err = restarted.begin(changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("conflicting duplicate accepted", err)
	}
	if err = restarted.recover(t.Context(), haproxyruntime.Client{}); err != nil {
		t.Fatal(err)
	}
	result := restarted.pending()
	if result == nil || result.Outcome != "unknown" || result.CommandDigest != op.Digest() {
		t.Fatal("unfinished command was not reconciled as unknown", result)
	}
	if err = restarted.acknowledge(op.ID); err != nil {
		t.Fatal(err)
	}
	if restarted.pending() != nil {
		t.Fatal("acknowledged result replayed")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("journal is not private")
	}
}
func TestOperationJournalBoundsResultsAndRetainsDuplicateProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	journal, _ := loadJournal(path)
	for range 80 {
		op := journalCommand()
		if _, _, err := journal.begin(op); err != nil {
			t.Fatal(err)
		}
		discovery := domain.Discovery{ObservedAtMS: time.Now().UnixMilli(), Generation: op.ExpectedGeneration, Servers: []domain.BackendServer{{Backend: op.Backend, Name: op.Server, Weight: 200}}}
		for i := 0; i < 256; i++ {
			discovery.Servers = append(discovery.Servers, domain.BackendServer{Backend: "other", Name: strings.Repeat("x", 128), Address: strings.Repeat("a", 255)})
		}
		if err := journal.complete(op.ID, domain.OperationResult{OperationID: op.ID, Outcome: "succeeded", Discovery: &discovery, CommandDigest: op.Digest()}); err != nil {
			t.Fatal(err)
		}
		if got := journal.pending(); got == nil || len(got.Discovery.Servers) != 1 {
			t.Fatal("journal retained unrelated inventory")
		}
		if err := journal.acknowledge(op.ID); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := loadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.entries) > 64 {
		t.Fatal("journal exceeds entry budget")
	}
	info, _ := os.Stat(path)
	if info.Size() > 1<<20 {
		t.Fatal("journal exceeds byte budget")
	}
}
