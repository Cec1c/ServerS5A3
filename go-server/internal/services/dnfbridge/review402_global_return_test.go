package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Actual Anton VM and production handlers/reducer. The scene is an explicitly
// synthetic ordinary map whose identifier matches the Anton entry under test;
// membership/accounts/normal committed result snapshot are memory fixtures.
func TestReview402GlobalProgressDoesNotOwnMapExit(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("fixed actual PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"other_team_same_map", "other_node", "timer_already_ticked", "phase_advanced", "energy_tick"} {
		for _, completed := range []bool{false, true} {
			for _, op := range []uint16{42, 72} {
				faults := []string{"ready"}
				if model == "other_team_same_map" && completed {
					faults = append(faults, "missing31", "missing46", "missing123", "account", "binding", "character", "runtime", "nonmember", "channel", "instance", "participants", "attempt", "missing_owner")
				}
				for _, fault := range faults {
					t.Run(fmt.Sprintf("%s/completed=%t/op%d/%s", model, completed, op, fault), func(t *testing.T) {
						mapID := int64(213)
						if model == "energy_tick" {
							mapID = 219
						}
						s, _ := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
						source := tutorialScopePVF(tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true, job: "11", dungeonReference: tutorialScopeATSwordmanDungeonReference, mapDirectory: tutorialScopeATSwordmanMapDirectory, bossRank: "[boss]"})
						for k, v := range source {
							source[k] = strings.ReplaceAll(v, "700", fmt.Sprint(mapID))
						}
						s.worldMapTable, s.worldMapResolver, s.dungeonMonsterTable = loadBridgeDungeonStaticData(t, source)
						s.options.currentClientVersion = "official-24.9"
						s.options.pvfPath = path
						s.initialEquipmentArchive = archive
						s.initialEquipmentLoadErr = nil
						s.questCatalogLoadErr = nil
						repos, ok := s.repositoryGroup()
						if !ok {
							t.Fatal("memory repos")
						}
						c, found, err := repos.Character.Load(context.Background(), "99")
						if err != nil || !found {
							t.Fatal(err, found)
						}
						// Preserve a nonempty inventory and observe that exit itself grants nothing.
						if err := repos.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:50": {ItemID: raidStartGuardTestMaterial, Count: 2}}}); err != nil {
							t.Fatal(err)
						}
						conn := &bufferConn{}
						owner := &gameSession{conn: conn, connID: "review402-memory-owner", accountID: c.AccountID, loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 23, Type: 23}, townSelectorOriginSnapshot: currentTownPositionSnapshot{CharacterID: 99, TownID: 7, AreaID: 3, PositionX: 474, PositionY: 234, PositionValid: true}, townSelectorOriginBound: true}
						s.bindGameSessionCharacter(owner, 99)
						now := time.Now().Add(-time.Minute)
						vm, err := program.BeginPhase(func() int {
							if model == "energy_tick" {
								return 1
							}
							return 0
						}(), now, 91321)
						if err != nil {
							t.Fatal(err)
						}
						for i := 0; i < 4 && model != "energy_tick"; i++ {
							vm, _, err = vm.ClearDungeon(fmt.Sprintf("earlier-fog-%d", i), 210, vm.Epoch(), now.Add(time.Second))
							if err != nil {
								t.Fatal(err)
							}
						}
						b := &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: byte(vm.Phase()), ChannelID: 23, ChannelType: 23, EnterAfter: now, Deadline: now.Add(time.Hour), Anton: vm, Dungeons: vm.Dungeons(), MemberAccounts: map[uint16]string{99: c.AccountID, 100: "memory-other-account"}, ClearedRuns: map[string]currentRaidClearedRun{}, MemberClears: map[uint16]uint32{}}
						state := &runtimeRaidState{RaidKey: 23<<16 | 99, LeaderID: 99, Started: true, Battle: b, Members: []runtimeRaidMemberState{{CharID: 99, GroupIndex: 1}, {CharID: 100, GroupIndex: 2}}}
						s.raids = map[uint32]*runtimeRaidState{state.RaidKey: state}
						r, _, err := s.prepareDungeonRuntime(context.Background(), owner, dungeoncmd.SelectDungeonRequest{DungeonID: uint32(mapID), Difficulty: 1})
						if err != nil {
							t.Fatal(err)
						}
						owner.dungeon.runtime = r
						if err := freezeCurrentDungeonTownReturnOrigin(owner, r); err != nil {
							t.Fatal(err)
						}
						if err := s.freezeCurrentRaidDungeonGroupEntry(owner, r, nil); err != nil {
							t.Fatal(err)
						}
						if r.Session.Snapshot().Run.Status != "active" {
							t.Fatal("owner was not inside an active map before global mutation")
						}
						// A different squad produces actual domain events. This boundary models
						// global producer input, without attributing its clear/reward to owner99.
						otherClear := func(id uint32, n int) {
							t.Helper()
							for i := 0; i < n; i++ {
								next, replay, e := b.Anton.ClearDungeon(fmt.Sprintf("other-squad-%d-%d", id, i), id, b.Anton.Epoch(), now.Add(2*time.Second))
								if e != nil || replay {
									t.Fatal("actual other-squad VM clear", e, replay)
								}
								b.Anton, b.Dungeons = next, next.Dungeons()
							}
						}
						switch model {
						case "energy_tick":
							otherClear(218, 1)
							next, e := b.Anton.Tick(now.Add(180 * time.Second))
							if e != nil {
								t.Fatal(e)
							}
							b.Anton, b.Dungeons = next, next.Dungeons()
						case "other_team_same_map":
							otherClear(213, 2)
						case "other_node":
							otherClear(215, 1)
						case "timer_already_ticked":
							next, e := b.Anton.Tick(now.Add(301 * time.Second))
							if e != nil {
								t.Fatal(e)
							}
							b.Anton, b.Dungeons = next, next.Dungeons()
							if next.Epoch() <= r.raidEntry.Attempt {
								t.Fatal("actual timer did not invalidate entry epoch")
							}
						case "phase_advanced":
							otherClear(213, 2)
							otherClear(215, 2)
							if !b.Anton.PhaseCleared() {
								t.Fatal("actual first phase did not complete")
							}
							next, e := b.Anton.BeginNextPhase(now.Add(3 * time.Second))
							if e != nil {
								t.Fatal(e)
							}
							b.Anton, b.Dungeons, b.Phase = next, next.Dungeons(), 1
						}
						if r.Session.Snapshot().Run.Status != "active" || len(b.ClearedRuns) != 0 || b.MemberClears[99] != 0 {
							t.Fatal("other squad forged owner completion/reward journal")
						}
						if completed {
							// Real synthetic monster death/boss handshake, not a completed flag assignment.
							// Existing generic monster fixture is completed through its generic
							// transport/resource mode, then normal official return handlers run.
							s.options.currentClientVersion = ""
							completeSettlementRuntimeForTest(t, s, r, owner)
							s.options.currentClientVersion = "official-24.9"
							completeSettlementExitBarriersForTest(t, s, r, owner)
							if !completedDungeonSettlementResultReady(r, 99) {
								t.Fatal("own normal result is not confirmed")
							}
							// Explicitly report to the real raid reducer after background mutation.
							// Rejected progress does not get reclassified as owner-earned raid clear.
							_, _, _ = s.reduceCurrentRaidDungeonClearLocked(owner, r, b.Anton.LastTime())
							if _, earned := b.ClearedRuns[r.raidEntry.RunID]; !earned {
								if s.currentRaidCompletedMapExitReady(owner, r) || s.currentCompletedDungeonActionReady(owner, r) {
									t.Fatal("town-only owner leaked retry/selector authority")
								}
								for _, action := range []byte{0, 1} {
									conn.write.Reset()
									if e := dungeonSettlementCardGameplayModule().UpperHandlers[72](s, owner, gameplayRequest{Opcode: 72, Classification: 1, Body: []byte{1, action}}); e != nil {
										t.Fatal(e)
									}
									if conn.write.Len() != 0 || owner.dungeon.runtime != r || r.settlementCardExitAckSent {
										t.Fatal("unearned map reopened retry/selector")
									}
								}
							}

						}
						// Invalid requests must not gain authority from the new local owner.
						switch fault {
						case "missing31":
							r.settlementEntrySent = false
						case "missing46":
							r.settlementPlayResultReceived = false
						case "missing123":
							r.settlementStatisticReceived = false
						case "account":
							owner.accountID = "foreign-memory-account"
						case "binding":
							s.bindGameSessionCharacter(&gameSession{conn: &bufferConn{}, connID: "review402-replacement", accountID: c.AccountID, loginPacketSeen: true, accountFromToken: true, channel: owner.channel}, 99)
						case "character":
							owner.selectedCharacterID = 100
						case "runtime":
							owner.dungeon.runtime = nil
						case "nonmember":
							state.Members = state.Members[1:]
						case "channel":
							owner.channel.ID++
						case "instance":
							b.InstanceID += "-foreign"
						case "participants":
							r.raidEntry.Participants = []uint16{100}
						case "attempt":
							r.raidEntry.Attempt++
						case "missing_owner":
							r.antonMapReturnOwner = nil
						}
						expectedRuntime := owner.dungeon.runtime
						journal := cloneRuntimeRaidBattle(b)
						invBefore, _, _ := repos.Inventory.Load(context.Background(), "99")
						conn.write.Reset()
						if op == 42 {
							err = dungeonNavigationGameplayModule().UpperHandlers[42](s, owner, gameplayRequest{Opcode: 42, Classification: 1})
						} else {
							err = dungeonSettlementCardGameplayModule().UpperHandlers[72](s, owner, gameplayRequest{Opcode: 72, Classification: 1, Body: []byte{1, 2}})
						}
						if err != nil {
							t.Fatal("actual public entrance", err)
						}
						if fault != "ready" {
							if r.townReturnOp24Sent || owner.dungeon.runtime != expectedRuntime || r.settlementCardExitAckSent {
								t.Fatal("foreign/unconfirmed map return crossed owner boundary")
							}
						} else if completed {
							if !r.townReturnOp24Sent || owner.dungeon.runtime != nil {
								t.Fatal("own confirmed completed return was denied after global progress")
							}
						} else if op == 42 {
							if !r.townReturnOp24Sent || !r.townReturnPending || owner.dungeon.runtime != r || r.Session.Snapshot().Run.Status != "active" {
								t.Fatal("active giveup lost reversible pending route")
							}
							if err := s.commitPendingDungeonReturnForSceneRequest(owner, "review402_synthetic_positive_town_scene"); err != nil {
								t.Fatal(err)
							}
							if owner.dungeon.runtime != nil || r.Session.Snapshot().Run.Status != "abandoned" {
								t.Fatal("active giveup failed positive scene commit")
							}
						} else if r.townReturnOp24Sent || owner.dungeon.runtime != r || r.Session.Snapshot().Run.Status != "active" {
							t.Fatal("result button authorized an unfinished map")
						}
						invAfter, _, _ := repos.Inventory.Load(context.Background(), "99")
						if !reflect.DeepEqual(invBefore, invAfter) || !reflect.DeepEqual(journal.ClearedRuns, b.ClearedRuns) || !reflect.DeepEqual(journal.MemberClears, b.MemberClears) || r.settlementCardRewardCommitted {
							t.Fatal("exit minted rewards or fabricated global progress")
						}
					})
				}
			}
		}
	}
}
