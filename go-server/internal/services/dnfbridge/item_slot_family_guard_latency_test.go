package dnfbridge

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type familyGuardInventoryLoadCounter struct {
	dnfrepo.InventoryRepository
	loads int
}

func (r *familyGuardInventoryLoadCounter) Load(ctx context.Context, id string) (dnfrepo.InventoryRecord, bool, error) {
	r.loads++
	return r.InventoryRepository.Load(ctx, id)
}

// Drive the dispatcher and preserve the real transaction. Only the diagnostic
// read may disappear when it has no output sink.
func TestItemSlotFamilyGuardAvoidsSilentDiagnosticRead(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		logging     bool
		destination int16
		wantReads   int
		wantMoved   bool
	}{
		{"log_without_sink", "log", false, 13, 0, true},
		{"log_with_sink", "log", true, 13, 1, true},
		{"enforce_without_sink", "enforce", false, 130, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(itemSlotFamilyGuardEnv, tc.mode)
			s, session, _, _, _, repos := newItemSlotFamilyGuardFixture(t)
			if !tc.logging {
				s.packetLog = nil
			}
			counter := &familyGuardInventoryLoadCounter{InventoryRepository: repos.Inventory}
			repos.Inventory = counter
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			driveItemSlotFamilyGuardMove(t, s, session, itemSlotFamilyGuardMoveBody(0, 12, 0, tc.destination))
			if counter.loads != tc.wantReads {
				t.Fatalf("nontransactional inventory loads=%d, want %d", counter.loads, tc.wantReads)
			}
			record, found, err := counter.InventoryRepository.Load(context.Background(), itemSlotFamilyGuardTestCharacterID)
			if err != nil || !found {
				t.Fatalf("reload found=%v err=%v", found, err)
			}
			_, sourcePresent := record.Slots["0:12"]
			if sourcePresent == tc.wantMoved {
				t.Fatalf("source presence=%v want moved=%v", sourcePresent, tc.wantMoved)
			}
			if tc.wantMoved && record.Slots["0:13"].ItemID != 101040127 {
				t.Fatal("move did not persist the real item")
			}
			if !tc.wantMoved && record.Slots["0:130"].ItemID != 0 {
				t.Fatal("enforce mode allowed a cross-page move without a logger")
			}
		})
	}
}
