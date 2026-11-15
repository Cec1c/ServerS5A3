package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// These tests call production entry points, including on the old-source overlay.
// The goroutine stack is only a bounded barrier proving the real operation has
// reached its wait; it does not replace an acquisition or a production callback.
func startPartyLockOperation(fn func() error) (uint64, <-chan error) {
	ids := make(chan uint64, 1)
	done := make(chan error, 1)
	go func() {
		var header [128]byte
		n := runtime.Stack(header[:], false)
		id, _ := strconv.ParseUint(strings.Fields(string(header[:n]))[1], 10, 64)
		ids <- id
		done <- fn()
	}()
	return <-ids, done
}

func awaitPartyOperationWait(t *testing.T, id uint64, method, state string, done <-chan error, mutex ...*sync.Mutex) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	buf := make([]byte, 2<<20)
	var last string
	for {
		n := runtime.Stack(buf, true)
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.HasPrefix(stack, fmt.Sprintf("goroutine %d ", id)) {
				last = stack
				matchesMutex := len(mutex) == 0 || strings.Contains(stack, fmt.Sprintf("(%p", mutex[0]))
				if matchesMutex && strings.Contains(stack, method) && strings.Contains(stack, "["+state) {
					return
				}
			}
		}
		select {
		case err := <-done:
			t.Fatalf("operation finished before controlled %s wait: %v", method, err)
		case <-deadline.C:
			t.Fatalf("operation did not reach controlled %s wait: %s", method, last)
		case <-tick.C:
		}
	}
}

func awaitPartyOperationDone(t *testing.T, done <-chan error, message string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", message, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal(message)
	}
}

func partyLockFixture() (*Service, *gameSession, boundGameSessionCharacter) {
	session := &gameSession{selectedCharacterID: 77, characterGeneration: 9, connID: "projection-owner", accountID: "fixture"}
	s := &Service{gameSessions: map[uint16]*gameSession{77: session}, liveGameSessions: map[string]*gameSession{session.connID: session}}
	s.packetLogDisabled.Store(true)
	s.runtimePartyManagerForService()
	return s, session, boundGameSessionCharacter{session: session, character: 77, generation: 9}
}

func partyLockCreate(t *testing.T, s *Service, identity boundGameSessionCharacter) party.RuntimePartySnapshot {
	t.Helper()
	result := s.runtimePartyManagerForService().Create(party.RuntimePartyMember{
		UserID: identity.character, SessionGeneration: identity.generation,
		State: alignedcmd.PartyMemberState{UserID: identity.character, UserState: 1},
	}, alignedcmd.PartyState{MaxMembers: 4})
	if !result.OK {
		t.Fatalf("create fixture membership: %s", result.Reason)
	}
	return result.Party
}

func partyLockRouteCallback(t *testing.T, s *Service, session *gameSession, mode string) func() error {
	t.Helper()
	if mode == "constructor" {
		return func() error {
			if s.candidateUpgradeCostResolver(session) == nil {
				return fmt.Errorf("default upgrade resolver was removed")
			}
			return nil
		}
	}
	// A private empty archive plus per-archive cached rule reaches the real NPC
	// callback without resource IO. The active dungeon must reject authorization;
	// this fixture never claims a valid NPC placement or an accepted equipment quote.
	archive := &platformpvf.Archive{}
	s.initialEquipmentArchive = archive
	currentUpgradeNPCIndexMu.Lock()
	currentUpgradeNPCIndexCache[archive] = map[uint16]string{currentVerifiedUpgradeConversionNPCID: "fixture.npc"}
	currentUpgradeNPCIndexMu.Unlock()
	key := currentUpgradeNPCConversionCacheKey{archive: archive, npcID: currentVerifiedUpgradeConversionNPCID}
	currentUpgradeNPCConversionMu.Lock()
	currentUpgradeNPCConversionCache[key] = currentUpgradeNPCConversionRule{MaterialItemID: 1, UnitGold: 1, Converts: true}
	currentUpgradeNPCConversionMu.Unlock()
	t.Cleanup(func() {
		currentUpgradeNPCIndexMu.Lock()
		delete(currentUpgradeNPCIndexCache, archive)
		currentUpgradeNPCIndexMu.Unlock()
		currentUpgradeNPCConversionMu.Lock()
		delete(currentUpgradeNPCConversionCache, key)
		currentUpgradeNPCConversionMu.Unlock()
	})
	session.townSceneReadyCharacterID = session.selectedCharacterID
	session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: session.selectedCharacterID, PositionValid: true}
	session.dungeon.runtime = &runtimeDungeonState{lifecycleToken: 3}
	resolver := s.candidateUpgradeCostResolver(session)
	return func() error {
		_, err := resolver("reinforce", 1, 0, true, currentVerifiedUpgradeConversionNPCID)
		if err == nil || !strings.Contains(err.Error(), "upgrade NPC town scene not ready") {
			return fmt.Errorf("NPC authorization changed: %v", err)
		}
		return nil
	}
}

func assertPartyLockBootstrap(t *testing.T, s *Service) {
	t.Helper()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_, done := startPartyLockOperation(func() error {
		s.trackConn(server)
		defer s.untrackConn(server)
		s.handleChannelConn(context.Background(), server)
		return nil
	})
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	request := make([]byte, 43)
	request[1], request[10] = 0x0b, 1
	binary.LittleEndian.PutUint32(request[2:6], uint32(len(request)))
	if _, err := client.Write(request); err != nil {
		t.Fatalf("C02 write blocked by party projection: %v", err)
	}
	ack := make([]byte, 47)
	if _, err := io.ReadFull(client, ack); err != nil {
		t.Fatalf("C02 ACK blocked by party projection: %v", err)
	}
	if ack[1] != 0x0c || ack[10] != 1 || binary.LittleEndian.Uint32(ack[2:6]) != 47 || binary.LittleEndian.Uint32(ack[11:15]) != 0 {
		t.Fatalf("invalid complete C02 ACK: %x", ack)
	}
	_ = client.Close()
	awaitPartyOperationDone(t, done, "C02 connection cleanup stalled")
}

func TestPartyProjectionLockOrderAllowsResolverAndBootstrap(t *testing.T) {
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "")
	for _, operation := range []string{"store", "clear_absent", "clear_singleton"} {
		for _, callback := range []string{"constructor", "npc"} {
			t.Run(operation+"/"+callback, func(t *testing.T) {
				s, session, identity := partyLockFixture()
				route := partyLockRouteCallback(t, s, session, callback)
				var snapshot party.RuntimePartySnapshot
				if operation != "clear_absent" {
					snapshot = partyLockCreate(t, s, identity)
				}
				session.party.state = alignedcmd.PartyState{PartyID: 900}
				session.party.mu.Lock()
				held := true
				defer func() {
					if held {
						session.party.mu.Unlock()
					}
				}()
				method := "clearRuntimePartyProjectionForSessions"
				if operation == "store" {
					method = "storeCurrentPartyProjection"
				}
				id, done := startPartyLockOperation(func() error {
					if operation == "store" {
						if !s.storeCurrentPartyProjection(identity, snapshot.StateFor(identity.character)) {
							return fmt.Errorf("current projection rejected")
						}
					} else {
						s.clearRuntimePartyProjectionForSessions(identity, snapshot.ID)
					}
					return nil
				})
				awaitPartyOperationWait(t, id, method, "sync.Mutex.Lock", done, &session.party.mu)
				_, callbackDone := startPartyLockOperation(route)
				awaitPartyOperationDone(t, callbackDone, "Route callback blocked by projection lock inversion")
				assertPartyLockBootstrap(t, s) // target party lock is deliberately still held
				session.party.mu.Unlock()
				held = false
				awaitPartyOperationDone(t, done, "party projection did not resume")
				state := runtimePartyStateSnapshot(session)
				if operation == "store" && state.PartyID != int(snapshot.ID) || operation != "store" && state.PartyID != 0 {
					t.Fatalf("unexpected projection after wait: %+v", state)
				}
			})
		}
	}
}

func TestPartyRetiredRunKeyManagerLockOrderAllowsBinding(t *testing.T) {
	s, session, identity := partyLockFixture()
	session.dungeonPartyRun, session.dungeonPartyRunValid = dungeonPartyRunKey{PartyID: 901}, true
	manager := s.runtimePartyManagerForService()
	managerHeld := make(chan struct{})
	readBinding := make(chan struct{})
	_, managerDone := startPartyLockOperation(func() error {
		return manager.WithSoloMember(identity.character, func() error {
			close(managerHeld)
			<-readBinding
			if _, ok := s.onlineGameSessionCharacterSnapshot(identity.character); !ok {
				return fmt.Errorf("binding disappeared")
			}
			return nil
		})
	})
	<-managerHeld
	id, retired := startPartyLockOperation(func() error {
		ok, _ := s.forgetRetiredDungeonPartyRunKey(identity, 901)
		if !ok {
			return fmt.Errorf("current retired run was not cleared")
		}
		return nil
	})
	awaitPartyOperationWait(t, id, "forgetRetiredDungeonPartyRunKey", "sync.Mutex.Lock", retired)
	close(readBinding)
	awaitPartyOperationDone(t, managerDone, "manager callback blocked by retired run-key lock inversion")
	awaitPartyOperationDone(t, retired, "retired run-key commit stalled")
	if session.dungeonPartyRunValid {
		t.Fatal("retired run key survived successful cleanup")
	}
	assertPartyLockBootstrap(t, s)
}

func TestPartyProjectionRetryRejectsStaleIdentityAndMembership(t *testing.T) {
	for _, operation := range []string{"store", "clear", "retire"} {
		for _, change := range []string{"generation", "membership"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				s, session, identity := partyLockFixture()
				manager := s.runtimePartyManagerForService()
				var snapshot party.RuntimePartySnapshot
				if operation == "store" {
					snapshot = partyLockCreate(t, s, identity)
				}
				session.party.state = alignedcmd.PartyState{PartyID: 900}
				session.dungeonPartyRun, session.dungeonPartyRunValid = dungeonPartyRunKey{PartyID: 901}, true
				lock := &session.party.mu
				method := "clearRuntimePartyProjectionForSessions"
				if operation == "store" {
					method = "storeCurrentPartyProjection"
				}
				if operation == "retire" {
					lock, method = &session.dungeonPartyRunMu, "forgetRetiredDungeonPartyRunKey"
				}
				lock.Lock()
				held := true
				defer func() {
					if held {
						lock.Unlock()
					}
				}()
				id, done := startPartyLockOperation(func() error {
					switch operation {
					case "store":
						if s.storeCurrentPartyProjection(identity, snapshot.StateFor(77)) {
							return fmt.Errorf("stale store committed")
						}
					case "clear":
						s.clearRuntimePartyProjectionForSessions(identity, 0)
					case "retire":
						if ok, _ := s.forgetRetiredDungeonPartyRunKey(identity, 901); ok {
							return fmt.Errorf("stale retirement committed")
						}
					}
					return nil
				})
				awaitPartyOperationWait(t, id, method, "sync.Mutex.Lock", done, lock)
				_, changed := startPartyLockOperation(func() error {
					if change == "generation" {
						s.mu.Lock()
						session.characterGeneration++
						s.mu.Unlock()
					} else {
						if snapshot.ID != 0 && !manager.RetireSnapshot(snapshot) {
							return fmt.Errorf("fixture membership retirement failed")
						}
						result := manager.Create(party.RuntimePartyMember{UserID: 77, SessionGeneration: 9, State: alignedcmd.PartyMemberState{UserID: 77}}, alignedcmd.PartyState{MaxMembers: 4})
						if !result.OK {
							return fmt.Errorf("replacement membership failed: %s", result.Reason)
						}
					}
					return nil
				})
				awaitPartyOperationDone(t, changed, "identity/membership mutation blocked behind projection wait")
				lock.Unlock()
				held = false
				awaitPartyOperationDone(t, done, "stale projection did not finish")
				if runtimePartyStateSnapshot(session).PartyID != 900 || !session.dungeonPartyRunValid {
					t.Fatal("stale operation erased newer state")
				}
			})
		}
	}
}

func TestPartyProjectionCleanupCoversAuxiliarySessions(t *testing.T) {
	for _, singleton := range []bool{false, true} {
		t.Run(fmt.Sprint(singleton), func(t *testing.T) {
			s, primary, identity := partyLockFixture()
			var snapshotID uint16
			if singleton {
				snapshotID = partyLockCreate(t, s, identity).ID
			}
			aux := &gameSession{conn: &bufferConn{}, connID: "auxiliary", selectedCharacterID: 77, characterGeneration: 12}
			s.liveGameSessions[aux.connID] = aux
			primary.party.state, aux.party.state = alignedcmd.PartyState{PartyID: 900}, alignedcmd.PartyState{PartyID: 900}
			aux.party.mu.Lock()
			held := true
			defer func() {
				if held {
					aux.party.mu.Unlock()
				}
			}()
			id, done := startPartyLockOperation(func() error { s.clearRuntimePartyProjectionForSessions(identity, snapshotID); return nil })
			awaitPartyOperationWait(t, id, "clearRuntimePartyProjectionForSessions", "sync.Mutex.Lock", done, &aux.party.mu)
			assertPartyLockBootstrap(t, s)
			aux.party.mu.Unlock()
			held = false
			awaitPartyOperationDone(t, done, "auxiliary cleanup stalled")
			if runtimePartyStateSnapshot(primary).PartyID != 0 || runtimePartyStateSnapshot(aux).PartyID != 0 {
				t.Fatal("cleanup left an old primary/auxiliary party")
			}
		})
	}
}
