package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Synthetic one-room ordinary dungeon. Death, monster completion, boss check,
// revive, pickup, card and exit requests all use the full upper dispatcher.
// Rating payload and card reward plan are frozen test plans, not a real-PVF
// combat/loot simulation or proof of the native unpicked-epic dialog.
func TestR134CompletedDeathReviveThenSettlementExit(t *testing.T) {
	for _, timing := range []string{"revive_before_completion", "revive_after_completion", "revive_after_result"} {
		for _, exit := range []string{"town", "retry"} {
			t.Run(timing+"/"+exit, func(t *testing.T) {
				s, session, r := newBackToVillageRuntime(t)
				authenticateRound100RetrySession(s, session)
				queue := newFakeCurrentDungeonDeathTimerQueue()
				s.gameplayTimers = queue
				conn := session.conn.(*bufferConn)
				repos, _ := s.repositoryGroup()
				coinSlot := fmt.Sprintf("0:%d", csharpReviveCoinWalletSlot)
				if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{
					coinSlot: {ItemID: int64(csharpReviveCoinItemID), Count: 3},
				}}); err != nil {
					t.Fatal(err)
				}
				dispatch := func(cmd uint16, body []byte) {
					t.Helper()
					if err := s.handleGameUpper(session, buildCurrentDungeonClientUpperForPhaseTest(t, cmd, body)); err != nil {
						t.Fatalf("op%d: %v", cmd, err)
					}
				}
				loadBag := func() dnfrepo.InventoryRecord {
					t.Helper()
					bag, found, err := repos.Inventory.Load(context.Background(), "99")
					if err != nil || !found {
						t.Fatalf("bag found=%t err=%v", found, err)
					}
					return cloneRound100Inventory(bag)
				}
				dispatch(uint16(dnfenum.CmdPacketDieCharacter), make([]byte, currentDungeonDieCharacterBodySize))
				if !r.deathStateSent || !r.deathReturnWaiting {
					t.Fatal("initial op40 must have an accepted death and timer")
				}
				staleTimer := queue.task(t, 0)
				// This synthetic owned ground item must survive completion and
				// revive. It is not evidence of the native epic-warning dialog.
				scene, _ := r.Session.Scene()
				drop := &runtimeDungeonDrop{ObjectKey: 60001, Amount: 1, SceneSlot: 1, OwnerActorObjectKey: 99,
					Room: runtimeDungeonRoomKeyFromScene(scene), Status: runtimeDungeonDropAvailable,
					Item: dungeonDropItemDefinition{ItemID: 9001, Kind: dungeonDropItemEquipment, PVFPath: "equipment/weapon/test_drop.equ", SlotStart: 9, SlotEnd: 56, Durability: 50}}
				r.DropOwner = newRuntimeDungeonDropOwner()
				if err := r.DropOwner.registerBatch([]*runtimeDungeonDrop{drop}, 2); err != nil {
					t.Fatal(err)
				}
				coinBody := make([]byte, 2)
				binary.LittleEndian.PutUint16(coinBody, currentSceneActorObjectKey(99))
				revive := func() {
					t.Helper()
					phase, result, entry := r.settlementPhase, r.settlementPlayResultReceived, r.settlementEntrySent
					conn.write.Reset()
					dispatch(uint16(dnfenum.CmdPacketUseCoin), coinBody)
					if loadBag().Slots[coinSlot].Count != 2 || r.deathStateSent {
						t.Fatalf("accepted death could not revive: status=%s entry=%t dead=%t waiting=%t coins=%d", r.Session.Snapshot().Run.Status, entry, r.deathStateSent, r.deathReturnWaiting, loadBag().Slots[coinSlot].Count)
					}
					noti, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
					ack, _ := splitGameServerUpperPacket(t, rest)
					if noti.Header.Classification != 0 || noti.Header.MsgID != currentDungeonDeathStateMsgID || !bytes.Equal(noti.Body, buildCurrentDungeonDeathStateBody(99, 1, 0)) || ack.Header.Classification != 1 || ack.Header.MsgID != uint16(dnfenum.CmdPacketUseCoin) || !bytes.Equal(ack.Body, []byte{1, currentDungeonUseCoinSuccessFlag}) {
						t.Fatal("revive must send state=1 before successful coin ACK")
					}
					if r.settlementPhase != phase || r.settlementPlayResultReceived != result || r.settlementEntrySent != entry || r.townReturnPending || session.dungeon.runtime != r {
						t.Fatal("revive changed settlement or return owner")
					}
					bag := loadBag()
					conn.write.Reset()
					dispatch(uint16(dnfenum.CmdPacketUseCoin), coinBody)
					queue.fire(staleTimer, true)
					if !reflect.DeepEqual(bag, loadBag()) || conn.write.Len() != 0 || r.townReturnPending {
						t.Fatal("coin replay or stale death timer mutated assets/scene")
					}
				}
				if timing == "revive_before_completion" {
					revive()
				}
				if _, err := r.Room.AnnounceAllActors(r.Session); err != nil {
					t.Fatal(err)
				}
				boss := r.Room.Snapshot().Monsters[0].ObjectKey
				deathBody := make([]byte, dungeoncmd.DieMonsterRequestSize)
				binary.LittleEndian.PutUint32(deathBody, boss)
				binary.LittleEndian.PutUint16(deathBody[4:6], 99)
				dispatch(uint16(dnfenum.CmdPacketDieMonster), deathBody)
				bossBody := bossDieCheckRequestBody(99, uint16(boss))
				dispatch(uint16(dnfenum.CmdPacketBossDieCheck), bossBody)
				if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.settlementEntrySent || r.deathReturnWaiting {
					t.Fatal("completed boss handler must enter settlement and cancel death return")
				}
				if timing == "revive_after_completion" {
					revive()
				}
				plan := currentDungeonSettlementPacketPlanForTest(t, r, 73)
				r.settlementResultPlan = &plan
				resultBody := make([]byte, currentDungeonPlayResultBaseSize)
				resultBody[currentDungeonPlayResultClientRankPointOffset] = 73
				dispatch(uint16(dnfenum.CmdPacketSetPlayResult), resultBody)
				dispatch(uint16(dnfenum.CmdPacketCharacterStatistic), make([]byte, currentDungeonCharacterStatisticBodySize))
				if timing == "revive_after_result" {
					revive()
				}
				before := loadBag()
				dispatch(uint16(dnfenum.CmdPacketUseCoin), coinBody)
				dispatch(uint16(dnfenum.CmdPacketBossDieCheck), bossBody)
				dispatch(uint16(dnfenum.CmdPacketSetPlayResult), resultBody)
				if drop.Status != runtimeDungeonDropAvailable || !reflect.DeepEqual(before, loadBag()) {
					t.Fatal("revive/completion/result replay consumed or awarded an unpicked drop")
				}
				conn.write.Reset()
				dispatch(uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 2})
				if session.dungeon.runtime != r || r.settlementCardExitAckSent || conn.write.Len() != 0 {
					t.Fatal("revive bypassed the unfinished free-card barrier")
				}
				dispatch(uint16(dnfenum.CmdPacketGetItem), currentDungeonPickupTestBody(drop.ObjectKey))
				picked := loadBag()
				dispatch(uint16(dnfenum.CmdPacketGetItem), currentDungeonPickupTestBody(drop.ObjectKey))
				pickedItem := picked.Slots[fmt.Sprintf("0:%d", drop.DestinationSlot)]
				if drop.Status != runtimeDungeonDropConsumed || pickedItem.ItemID != 9001 || pickedItem.Count != 1 || !reflect.DeepEqual(picked, loadBag()) {
					t.Fatal("completed-run pickup did not consume exactly once")
				}
				cardPlan, err := newDungeonCardRewardPlan(dungeonCardPlanIdentity{CharacterID: "99", DungeonID: r.Dungeon.ID, MazeIndex: r.MazeIndex, RunSeed: r.Seed}, "r134_synthetic_free_reward", dungeonCardRewardBundle{Gold: 17}, dungeonCardRewardBundle{})
				if err != nil {
					t.Fatal(err)
				}
				r.settlementCardRewardState, err = newDungeonCardState(cardPlan)
				if err != nil {
					t.Fatal(err)
				}
				dispatch(uint16(dnfenum.CmdPacketScoreScrollState), nil)
				dispatch(uint16(dnfenum.CmdPacketCardSelectRightState), nil)
				beforeFree := captureRound100RetryAssets(t, s, 700)
				dispatch(uint16(dnfenum.CmdPacketSelectCard), []byte{0, 0})
				if !r.settlementCardRewardCommitted {
					t.Fatal("free card reward was not committed")
				}
				paid := captureRound100RetryAssets(t, s, 700)
				if paid.character.Stats["gold"]-beforeFree.character.Stats["gold"] != 17 {
					t.Fatal("free card must grant the frozen 17-gold test reward exactly once")
				}
				dispatch(uint16(dnfenum.CmdPacketSelectCard), []byte{0, 0})
				if again := captureRound100RetryAssets(t, s, 700); !reflect.DeepEqual(paid.character.Stats, again.character.Stats) || !reflect.DeepEqual(paid.inventory, again.inventory) {
					t.Fatal("free-card replay granted assets twice")
				}
				conn.write.Reset()
				if exit == "town" {
					dispatch(uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 2})
					if session.dungeon.runtime != nil || !r.townReturnOp24Sent {
						t.Fatal("revived completed owner could not return")
					}
				} else {
					dispatch(uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 0})
					next := session.dungeon.runtime
					if next == nil || next == r || next.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
						t.Fatal("revived completed owner could not retry")
					}
				}
				dispatch(uint16(dnfenum.CmdPacketUseCoin), coinBody)
				if loadBag().Slots[coinSlot].Count != 2 || drop.Status != runtimeDungeonDropConsumed {
					t.Fatal("exit changed coin/pickup receipt")
				}
			})
		}
	}
}

func TestR134CompletedReviveRequiresRetainedDeathOwner(t *testing.T) {
	for _, boundary := range []string{"late_death", "wrong_target", "wrong_character", "new_run_token", "no_entry", "unproved_completion", "pending_return", "raid", "coin_save_failure"} {
		t.Run(boundary, func(t *testing.T) {
			s, session, r := newBackToVillageRuntime(t)
			q := newFakeCurrentDungeonDeathTimerQueue()
			s.gameplayTimers = q
			conn := session.conn.(*bufferConn)
			repos, _ := s.repositoryGroup()
			coinSlot := fmt.Sprintf("0:%d", csharpReviveCoinWalletSlot)
			if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{
				coinSlot: {ItemID: int64(csharpReviveCoinItemID), Count: 3},
			}}); err != nil {
				t.Fatal(err)
			}
			dispatch := func(cmd uint16, body []byte) {
				t.Helper()
				if err := s.handleGameUpper(session, buildCurrentDungeonClientUpperForPhaseTest(t, cmd, body)); err != nil {
					t.Fatal(err)
				}
			}
			if boundary != "late_death" {
				dispatch(uint16(dnfenum.CmdPacketDieCharacter), make([]byte, currentDungeonDieCharacterBodySize))
			}
			if _, err := r.Room.AnnounceAllActors(r.Session); err != nil {
				t.Fatal(err)
			}
			boss := r.Room.Snapshot().Monsters[0].ObjectKey
			death := make([]byte, dungeoncmd.DieMonsterRequestSize)
			binary.LittleEndian.PutUint32(death, boss)
			binary.LittleEndian.PutUint16(death[4:], 99)
			dispatch(uint16(dnfenum.CmdPacketDieMonster), death)
			dispatch(uint16(dnfenum.CmdPacketBossDieCheck), bossDieCheckRequestBody(99, uint16(boss)))
			if !r.settlementEntrySent || r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
				t.Fatal("completion fixture must use accepted boss handler")
			}
			coin := []byte{99, 0}
			switch boundary {
			case "late_death":
				dispatch(uint16(dnfenum.CmdPacketDieCharacter), make([]byte, currentDungeonDieCharacterBodySize))
			case "wrong_target":
				coin[0] = 100
			case "wrong_character":
				session.selectedCharacterID = 100
			case "new_run_token":
				session.dungeon.runToken++
			case "no_entry":
				r.settlementEntrySent = false
			case "unproved_completion":
				r.bossDieCheckAccepted = false
			case "pending_return":
				authenticateRound100RetrySession(s, session)
				completeSettlementExitBarriersForTest(t, s, r, session)
				plan, err := newDungeonCardRewardPlan(dungeonCardPlanIdentity{CharacterID: "99", DungeonID: r.Dungeon.ID, MazeIndex: r.MazeIndex, RunSeed: r.Seed}, "r134_pending_return", dungeonCardRewardBundle{Gold: 17}, dungeonCardRewardBundle{})
				if err != nil {
					t.Fatal(err)
				}
				r.settlementCardRewardState, err = newDungeonCardState(plan)
				if err != nil {
					t.Fatal(err)
				}
				dispatch(uint16(dnfenum.CmdPacketScoreScrollState), nil)
				dispatch(uint16(dnfenum.CmdPacketCardSelectRightState), nil)
				dispatch(uint16(dnfenum.CmdPacketSelectCard), []byte{0, 0})
				if !r.settlementCardRewardCommitted {
					t.Fatal("return fixture must finish the real free-card barrier")
				}
				writeErr := errors.New("r134 pending town transition")
				failure := &completedReturnAckOp24FailureConn{bufferConn: conn, t: t, err: writeErr, failOnce: true}
				session.conn = failure
				err = s.handleGameUpper(session, buildCurrentDungeonClientUpperForPhaseTest(t, uint16(dnfenum.CmdPacketBack2Village), nil))
				if !errors.Is(err, writeErr) || failure.failedOp24Attempts != 1 || !r.townReturnPending || r.townReturnOp24Sent || session.dungeon.runtime != r {
					t.Fatalf("op132 must retain pending town return after a failed op24: err=%v attempts=%d pending=%v", err, failure.failedOp24Attempts, r.townReturnPending)
				}
				session.conn = conn
			case "raid":
				r.raidEntry = &currentRaidDungeonEntry{}
			case "coin_save_failure":
				failing := repos
				failing.CharacterItems = pickupTestItemUnitOfWork{inventory: pickupFailingInventoryRepository{InventoryRepository: repos.Inventory, err: errors.New("r134 coin save failure")}, equipment: repos.Equipment}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return failing, true }
			}
			dead, phase, pending := r.deathStateSent, r.settlementPhase, r.townReturnPending
			conn.write.Reset()
			dispatch(uint16(dnfenum.CmdPacketUseCoin), coin)
			bag, _, err := repos.Inventory.Load(context.Background(), "99")
			if err != nil || bag.Slots[coinSlot].Count != 3 || r.deathStateSent != dead || r.settlementPhase != phase || r.townReturnPending != pending || session.dungeon.runtime != r {
				t.Fatal("rejected revive consumed coin or changed death/settlement/return owner", err)
			}
			if boundary == "coin_save_failure" {
				packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.CmdPacketUseCoin) || len(packet.Body) == 0 || packet.Body[0] != 0 || len(rest) != 0 {
					t.Fatal("failed coin transaction must only send failure ACK")
				}
				// A later request retries the original death, not a second debit.
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
				dispatch(uint16(dnfenum.CmdPacketUseCoin), coin)
				dispatch(uint16(dnfenum.CmdPacketUseCoin), coin)
				bag, _, err = repos.Inventory.Load(context.Background(), "99")
				if err != nil || bag.Slots[coinSlot].Count != 2 || r.deathStateSent {
					t.Fatal("coin transaction retry did not commit exactly once", err)
				}
			} else if conn.write.Len() != 0 {
				t.Fatal("unproved completed revive emitted packets")
			}
			if _, _, active := q.counts(); active != 0 {
				t.Fatal("completed revive created another death timer")
			}
		})
	}
}
