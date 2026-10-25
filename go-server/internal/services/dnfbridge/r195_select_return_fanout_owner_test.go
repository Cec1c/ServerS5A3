package dnfbridge

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type selectorReturnChangingCharacterRepository struct {
	dnfrepo.CharacterRepository
	onLoad func()
}

func (r *selectorReturnChangingCharacterRepository) Load(ctx context.Context, key string) (dnfrepo.CharacterRecord, bool, error) {
	record, found, err := r.CharacterRepository.Load(ctx, key)
	if r.onLoad != nil {
		change := r.onLoad
		r.onLoad = nil
		change()
	}
	return record, found, err
}

// A follower can act while its leader prepares a return. Change the owner
// during the real repository read; no stale op24 may overwrite its new state.
func TestPartySelectReturnFanoutRechecksOwnerAfterPreparation(t *testing.T) {
	for _, fault := range []string{"party", "leader", "generation", "selector", "origin", "runtime"} {
		t.Run(fault, func(t *testing.T) {
			s, leader, member := partySelectReturnFixture(t)
			t.Setenv(dungeonPartySelectReturnFanoutEnv, "on")
			repos, _ := s.repositoryGroup()
			changed := false
			repos.Character = &selectorReturnChangingCharacterRepository{CharacterRepository: repos.Character, onLoad: func() {
				changed = true
				switch fault {
				case "party":
					state := runtimePartyStateSnapshot(member)
					state.PartyID++
					storeRuntimePartyState(member, state)
				case "leader":
					state := runtimePartyStateSnapshot(leader)
					state.UserID = member.selectedCharacterID
					storeRuntimePartyState(leader, state)
				case "generation":
					s.mu.Lock()
					member.characterGeneration++
					s.mu.Unlock()
				case "selector":
					clearCurrentDungeonSelectContext(member)
				case "origin":
					member.townMu.Lock()
					member.townSelectorOriginSnapshot.PositionX++
					member.townMu.Unlock()
				case "runtime":
					member.dungeon.mu.Lock()
					member.dungeon.runtime = &runtimeDungeonState{}
					member.dungeon.mu.Unlock()
				}
			}}
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			conn := member.conn.(*bufferConn)
			conn.write.Reset()
			if sent := s.fanOutDungeonSelectReturnToPartyMembers(leader, "changed_while_preparing_return"); sent != 0 || conn.write.Len() != 0 {
				t.Fatalf("stale selector return delivered=%d bytes=%d", sent, conn.write.Len())
			}
			if !changed {
				t.Fatal("fixture did not reach the transition preparation read")
			}
			if member.confirmedDungeonReturnStatePending || member.selectorReturnPresence != nil {
				t.Fatal("stale selector return created a pending owner")
			}
		})
	}
}
