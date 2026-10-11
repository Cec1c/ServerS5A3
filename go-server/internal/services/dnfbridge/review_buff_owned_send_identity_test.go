package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

// Candidate-only transport guards, not part of shared legacy business RED.
func reviewOwnedSendSeed(t *testing.T) (repo.Group, *Service, *gameSession, *bufferConn) {
	t.Helper()
	r := newSkillSwitchInventoryRepos(t)
	if err := r.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 12, 0, 54))
	reviewCurrentBuffSnapshot(t, conn, 38, true)
	return r, s, session, conn
}
func TestReviewBuffOwnedReapplyRejectsStaleGenerationAfterSnapshot(t *testing.T) {
	for _, name := range []string{"changed_character", "changed_generation", "replaced_peer", "unbound_peer", "changed_account"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			r, s, session, conn := reviewOwnedSendSeed(t)
			session.characterGeneration = 11
			s.gameSessions = map[uint16]*gameSession{1: session}
			before, _, _ := r.Settings.Load(context.Background(), "character:1:buff_swap_native")
			invoked := false
			s.repositoryProvider = func() (repo.Group, bool) {
				invoked = true
				s.mu.Lock()
				defer s.mu.Unlock()
				switch name {
				case "changed_character":
					delete(s.gameSessions, 1)
					session.selectedCharacterID = 2
					session.characterGeneration = 12
					s.gameSessions[2] = session
				case "changed_generation":
					session.characterGeneration = 12
				case "replaced_peer":
					s.gameSessions[1] = &gameSession{selectedCharacterID: 1, characterGeneration: 12, accountID: "dnf:10001"}
				case "unbound_peer":
					delete(s.gameSessions, 1)
				case "changed_account":
					session.accountID = "dnf:other"
				}
				return r, true
			}
			if err := s.sendBuffSwapReapply(session, "late_snapshot_guard"); err != nil {
				t.Fatal(err)
			}
			after, _, _ := r.Settings.Load(context.Background(), "character:1:buff_swap_native")
			if !invoked || conn.write.Len() != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("stale snapshot sent or state changed: provider=%v bytes=%d", invoked, conn.write.Len())
			}
		})
	}
}
func TestReviewBuffOwnedReapplyPreservesCurrentPeerAndZeroFixture(t *testing.T) {
	for _, name := range []string{"current_formal_peer", "historical_zero_unindexed_fixture", "formal_unbound_before_read", "foreign_current_peer_before_read"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			r, s, session, conn := reviewOwnedSendSeed(t)
			allow := true
			switch name {
			case "current_formal_peer":
				session.characterGeneration = 11
				s.gameSessions = map[uint16]*gameSession{1: session}
			case "formal_unbound_before_read":
				session.characterGeneration = 11
				allow = false
			case "foreign_current_peer_before_read":
				s.gameSessions = map[uint16]*gameSession{1: {selectedCharacterID: 1, characterGeneration: 12}}
				allow = false
			}
			reads := 0
			s.repositoryProvider = func() (repo.Group, bool) { reads++; return r, true }
			if err := s.sendBuffSwapReapply(session, "stable_or_unbound"); err != nil {
				t.Fatal(err)
			}
			if allow {
				if reads != 1 {
					t.Fatal("current sender lost snapshot")
				}
				reviewCurrentBuffSnapshot(t, conn, 38, true)
			} else if reads != 0 || conn.write.Len() != 0 {
				t.Fatalf("stale peer read/sent: reads%d bytes%d", reads, conn.write.Len())
			}
		})
	}
}
