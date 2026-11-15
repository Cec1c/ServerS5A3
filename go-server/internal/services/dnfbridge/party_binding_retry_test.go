package dnfbridge

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func partyBindingFixture() (*Service, *gameSession, *gameSession) {
	s, session, _ := partyLockFixture()
	resident := &gameSession{selectedCharacterID: 78, characterGeneration: 30, connID: "resident", accountID: "fixture"}
	resident.party.state = alignedcmd.PartyState{PartyID: 902, UserID: 78, IsLeader: true, MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{{UserID: 78, UserState: 1}}}
	s.gameSessions[78] = resident
	s.onlinePlayers = newOnlinePlayerManager()
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 78, Session: resident})
	return s, session, resident
}

// Let the real bind finish its existing old-character cleanup before taking the
// chosen party lock. This reaches the actual bind commit/resident-snapshot wait
// on both old and new products, rather than blocking an earlier detach helper.
func startPartyBindingAtPartyWait(t *testing.T, s *Service, session *gameSession, blocked *sync.Mutex) (uint64, <-chan error, func()) {
	t.Helper()
	session.dungeon.mu.Lock()
	dungeonHeld, partyHeld := true, false
	t.Cleanup(func() {
		if dungeonHeld {
			session.dungeon.mu.Unlock()
		}
		if partyHeld {
			blocked.Unlock()
		}
	})
	id, done := startPartyLockOperation(func() error { s.bindGameSessionCharacter(session, 78); return nil })
	awaitPartyOperationWait(t, id, "bindGameSessionCharacter", "sync.Mutex.Lock", done, &session.dungeon.mu)
	blocked.Lock()
	partyHeld = true
	session.dungeon.mu.Unlock()
	dungeonHeld = false
	awaitPartyOperationWait(t, id, "bindGameSessionCharacter", "sync.Mutex.Lock", done, blocked)
	return id, done, func() { blocked.Unlock(); partyHeld = false }
}

func TestPartyBindingLockOrderAllowsNPCAndBootstrap(t *testing.T) {
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "")
	for _, waitOn := range []string{"target", "resident"} {
		for _, callback := range []string{"constructor", "npc"} {
			t.Run(waitOn+"/"+callback, func(t *testing.T) {
				s, session, resident := partyBindingFixture()
				route := partyLockRouteCallback(t, s, session, callback)
				lock := &session.party.mu
				if waitOn == "resident" {
					lock = &resident.party.mu
				}
				_, done, release := startPartyBindingAtPartyWait(t, s, session, lock)
				// Bind resets the select/town gate before the barrier. Restore only
				// this fixture's ready town so the real NPC callback must also read
				// dungeon state, not merely reject an unready town before that lock.
				session.townMu.Lock()
				session.townSceneReadyCharacterID = 77
				session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 77, PositionValid: true}
				session.townMu.Unlock()
				_, callbackDone := startPartyLockOperation(route)
				awaitPartyOperationDone(t, callbackDone, "NPC/resolver callback blocked by bind lock inversion")
				assertPartyLockBootstrap(t, s)
				release()
				awaitPartyOperationDone(t, done, "binding did not resume after party release")
				if s.gameSessions[78] != resident || session.selectedCharacterID != 78 || session.characterGeneration == 9 || runtimePartyStateSnapshot(session).PartyID != 902 {
					t.Fatal("resident ownership, new generation or inherited party changed")
				}
			})
		}
	}
}

func TestPartyBindingRetryPreservesReservationAndRuntime(t *testing.T) {
	s, session, resident := partyBindingFixture()
	oldRuntime := &runtimeDungeonState{lifecycleToken: 3, deathReturnWaiting: true, deathReturnTimerName: "fixture-old-death"}
	session.dungeon.runtime, session.dungeon.runToken = oldRuntime, 3
	id, done, release := startPartyBindingAtPartyWait(t, s, session, &session.party.mu)
	var reservation *currentCharacterAssetReservation
	newRuntime := &runtimeDungeonState{lifecycleToken: 4, deathReturnWaiting: true, deathReturnTimerName: "fixture-new-death"}
	_, changed := startPartyLockOperation(func() error {
		var err error
		reservation, err = s.reserveCurrentCharacterAssets(context.Background(), boundGameSessionCharacter{session: resident, character: 78, generation: 30}, "fixture")
		if err != nil {
			return err
		}
		session.dungeon.mu.Lock()
		session.dungeon.runtime, session.dungeon.runToken = newRuntime, 4
		session.dungeon.mu.Unlock()
		return nil
	})
	awaitPartyOperationDone(t, changed, "binding wait retained global/dungeon locks")
	release()
	awaitPartyOperationWait(t, id, "lockCurrentCharacterBindingMutation", "chan receive", done)
	// Reservation is introduced after the first attempt, not before bind starts.
	// All other locks must be free while that reservation waits for paid install.
	_, inspected := startPartyLockOperation(func() error {
		s.mu.Lock()
		unchanged := session.selectedCharacterID == 77 && session.characterGeneration == 9 && s.gameSessions[77] == session && s.gameSessions[78] == resident
		s.mu.Unlock()
		if !unchanged {
			return fmt.Errorf("binding partially committed before reservation completion")
		}
		session.dungeon.mu.Lock()
		session.dungeon.mu.Unlock()
		session.party.mu.Lock()
		session.party.mu.Unlock()
		return nil
	})
	awaitPartyOperationDone(t, inspected, "reservation wait retained a binding lock")
	assertPartyLockBootstrap(t, s)
	s.releaseCurrentCharacterAssets(reservation)
	awaitPartyOperationDone(t, done, "binding did not resume after paid reservation")
	if session.dungeon.runtime != newRuntime || newRuntime.deathReturnWaiting || newRuntime.deathReturnTimerName != "" || newRuntime.deathReturnGeneration == 0 {
		t.Fatal("binding used the pre-wait runtime instead of cancelling the current token")
	}
	if s.gameSessions[78] != resident || session.selectedCharacterID != 78 {
		t.Fatal("reservation retry changed resident binding semantics")
	}
}

func TestPartyBindingRetryRejectsChangedIdentityAndResident(t *testing.T) {
	for _, change := range []string{"generation", "resident"} {
		t.Run(change, func(t *testing.T) {
			s, session, resident := partyBindingFixture()
			_, done, release := startPartyBindingAtPartyWait(t, s, session, &session.party.mu)
			replacement := &gameSession{selectedCharacterID: 78, characterGeneration: 40, connID: "new-resident", accountID: "fixture"}
			replacement.party.state = alignedcmd.PartyState{PartyID: 903, UserID: 78, IsLeader: true, MaxMembers: 4,
				Members: []alignedcmd.PartyMemberState{{UserID: 78, UserState: 1}}}
			_, changed := startPartyLockOperation(func() error {
				s.mu.Lock()
				if change == "generation" {
					session.characterGeneration++
				} else {
					s.gameSessions[78] = replacement
				}
				s.mu.Unlock()
				if change == "resident" {
					s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 78, Session: replacement})
				}
				return nil
			})
			awaitPartyOperationDone(t, changed, "new binding blocked behind party wait")
			release()
			awaitPartyOperationDone(t, done, "binding retry did not finish")
			if change == "generation" {
				if session.selectedCharacterID != 77 || session.characterGeneration != 10 || s.gameSessions[77] != session || s.gameSessions[78] != resident {
					t.Fatal("stale bind overwrote a newer session generation")
				}
			} else if s.gameSessions[78] != replacement || runtimePartyStateSnapshot(session).PartyID != 903 {
				t.Fatal("binding inherited or replaced the retired resident")
			}
		})
	}
}
