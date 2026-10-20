package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Actual Anton/Luke programs and normal authenticated freeze preflight; the
// positive drop tables, scenes, member accounts and repositories are controlled
// memory fixtures. These are producer regressions, not native raid admission.
type review452GroundFixture struct {
	service      *Service
	session      *gameSession
	runtime      *runtimeDungeonState
	repositories repo.Group
	battle       *runtimeRaidBattle
	peer         *gameSession
	peerRuntime  *runtimeDungeonState
}

func review452GroundRun(t *testing.T, kind string, single, follower bool) review452GroundFixture {
	t.Helper()
	s, r, repositories := prepareCurrentDungeonDropTest(t, 0, 3227)
	s.options.currentClientVersion = "official-24.9"
	owner := &gameSession{conn: &bufferConn{}, accountID: "account-1", loginPacketSeen: true, accountFromToken: true}
	var state *runtimeRaidState
	var battle *runtimeRaidBattle
	switch kind {
	case "ordinary":
	case "luke":
		f, leader, _ := normalLukeFixture(t)
		owner = leader
		owner.conn = &bufferConn{}
		state, battle, s.raids = f.state, f.state.Battle, f.service.raids
		r.Dungeon.ID = 5054
	case "anton":
		catalog, _ := actualAntonRewardFixture(t)
		program, err := raid.LoadAntonProgram(catalog.source.(*platformpvf.Archive))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Add(-time.Minute)
		progress, err := program.BeginPhase(0, now, 452)
		if err != nil {
			t.Fatal(err)
		}
		owner.channel = channelcatalog.Channel{ID: 23, Type: 23}
		battle = &runtimeRaidBattle{InstanceID: "452000112233445566778899aabbccdd", Mode: 1, ChannelID: 23, ChannelType: 23, EnterAfter: now, Deadline: now.Add(time.Hour), Anton: progress, Dungeons: progress.Dungeons(), MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20"}}
		state = &runtimeRaidState{RaidKey: 23<<16 | 19, LeaderID: 19, Started: true, Battle: battle, Members: []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}, {CharID: 20, GroupIndex: 2}}}
		s.raids = map[uint32]*runtimeRaidState{state.RaidKey: state}
		r.Dungeon.ID = 210
	default:
		t.Fatal("unknown fixture kind")
	}
	r.Character.CharacterID, r.Character.AccountID, r.AccountID = "19", "account-1", "account-1"
	owner.connID = "review452-" + kind + "-leader"
	if err := repositories.Character.Save(context.Background(), r.Character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	s.bindGameSessionCharacter(owner, 19)
	owner.dungeon.runtime = r
	if battle != nil {
		battle.Single = single
	}
	result := review452GroundFixture{service: s, session: owner, runtime: r, repositories: repositories, battle: battle}
	var followers []*runtimePartyPreparedDungeonEntry
	if follower {
		if state == nil || single {
			t.Fatal("invalid follower fixture")
		}
		_, right, _ := prepareCurrentDungeonDropTest(t, 0, 3227)
		right.Character.CharacterID, right.Character.AccountID, right.AccountID = "20", "account-20", "account-20"
		right.Dungeon.ID = r.Dungeon.ID
		// 474/506: the freeze authenticates every prepared owner against the service
		// repositories, so the follower character/account must be persisted into the
		// same group the freeze reads. Without these two writes
		// currentRaidAuthenticated(peer) is false and admission stops at
		// "raid entry prepared owner unavailable" on both trees.
		if err := repositories.Character.Save(context.Background(), right.Character); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "20", Slots: map[string]repo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		peer := &gameSession{conn: &bufferConn{}, connID: "review452-peer", accountID: "account-20", loginPacketSeen: true, accountFromToken: true, channel: owner.channel, dungeon: dungeonSessionState{runtime: right}}
		s.bindGameSessionCharacter(peer, 20)
		state.Members[1].GroupIndex = 1
		followers = []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: right}}
		result.peer, result.peerRuntime = peer, right
	}
	if err := s.freezeCurrentRaidDungeonGroupEntry(owner, r, followers); err != nil {
		t.Fatal("normal admission fixture", err)
	}
	return result
}

func review452PositiveMonsterRules(t *testing.T, s *Service, r *runtimeDungeonState) runtimeDungeonMonster {
	t.Helper()
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
	source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
	monster := r.Room.Snapshot().Monsters[0]
	source[currentDungeonIndependentDropPath] = fmt.Sprintf("[independent drop]\n0 %d 3227 1000000 1000000 0 0 0 1 1 0 0 0 1 90 -1 0\n0 %d 9001 1000000 1000000 0 0 0 1 1 0 0 0 1 90 -1 0\n[/independent drop]\n", monster.Spawn.MonsterID, monster.Spawn.MonsterID)
	currentDungeonMonsterDropRulesByCatalog.Delete(s.dungeonMonsterTable)
	currentDungeonSpecialDropsByMonsterCatalog.Delete(s.dungeonMonsterTable)
	return monster
}

func review452GroundCount(r *runtimeDungeonState) int {
	if r.DropOwner == nil {
		return 0
	}
	return len(r.DropOwner.byObjectKey)
}

func TestReview452TeamRaidMonsterGroundDrops(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		single bool
		drift  string
	}{
		{"ordinary", false, ""}, {"anton", true, ""}, {"luke", true, ""},
		{"anton", false, ""}, {"luke", false, ""}, {"anton", false, "phase"}, {"luke", false, "phase"}, {"luke", false, "retired"},
	} {
		t.Run(fmt.Sprintf("%s/single=%t/%s", tc.kind, tc.single, tc.drift), func(t *testing.T) {
			f := review452GroundRun(t, tc.kind, tc.single, false)
			monster := review452PositiveMonsterRules(t, f.service, f.runtime)
			forbidden := tc.kind != "ordinary" && !tc.single
			if tc.drift == "phase" {
				f.battle.Phase++
				f.battle.PhaseCompleted = true
				f.battle.Deadline = time.Now().Add(-time.Minute)
			}
			if tc.drift == "retired" {
				f.service.raids = map[uint32]*runtimeRaidState{}
			}
			scene, _ := f.runtime.Session.Scene()
			visit, err := f.runtime.currentDungeonRoomVisit(scene)
			if err != nil {
				t.Fatal(err)
			}
			beforeSeed, beforeKey := visit.DropRNG, f.runtime.NextObjectKey
			wires, err := f.service.planCurrentDungeonMonsterDrops(f.runtime, monster, currentSceneActorObjectKey(19))
			if err != nil {
				t.Fatal(err)
			}
			if forbidden {
				if len(wires) != 0 || review452GroundCount(f.runtime) != 0 || f.runtime.NextObjectKey != beforeKey || visit.DropRNG != beforeSeed {
					t.Fatalf("team raid generated ground state: wires=%d ledger=%d key_changed=%t rng_changed=%t", len(wires), review452GroundCount(f.runtime), f.runtime.NextObjectKey != beforeKey, visit.DropRNG != beforeSeed)
				}
				if _, err := f.service.planCurrentDungeonMonsterDrops(f.runtime, monster, 0); err == nil {
					t.Fatal("invalid actor bypassed existing validation")
				}
			} else {
				gold, equipment := 0, 0
				for _, wire := range wires {
					id := binary.LittleEndian.Uint32(wire.Item.data[2:6])
					if id == 0 {
						gold++
					}
					if id == 9001 {
						equipment++
					}
					if f.runtime.DropOwner.byObjectKey[wire.SceneObjectKey] == nil {
						t.Fatal("positive control wire lacks ledger")
					}
				}
				if gold == 0 || equipment == 0 {
					t.Fatalf("ordinary/single positive control lost guaranteed gold/equipment: %d/%d", gold, equipment)
				}
			}
		})
	}
}

func TestReview452TeamRaidOtherGroundProducers(t *testing.T) {
	for _, kind := range []string{"ordinary", "anton", "luke"} {
		for _, single := range []bool{false, true} {
			if kind == "ordinary" && single {
				continue
			}
			for _, producer := range []string{"pvf_passive_item", "hell_equipment", "passive_registration"} {
				t.Run(fmt.Sprintf("%s/single=%t/%s", kind, single, producer), func(t *testing.T) {
					f := review452GroundRun(t, kind, single, false)
					r := f.runtime
					scene, _ := r.Session.Scene()
					beforeKey := r.NextObjectKey
					forbidden := kind != "ordinary" && !single
					switch producer {
					case "pvf_passive_item":
						doc, err := dnfpvf.Parse("review452.dgn", "[special passive object item]\n0 -1 1 3227 10000\n")
						if err != nil {
							t.Fatal(err)
						}
						r.Dungeon.SourceSections = worldmap.ParseDungeon(1, doc.Path, doc).SourceSections
						scene.SpecialPassiveObjects = []worldmap.SpecialPassiveObject{{Spawns: []worldmap.SpecialObjectSpawn{{Kind: "[item]", Code: 0, Level: 1, Params: [3]int64{-1, -1, -1}}}}}
						t.Setenv(mapOwnedPassiveObjectModeEnvName, mapOwnedPassiveObjectRegisterValue)
						if err := f.service.prepareCurrentDungeonPassiveObjectDrops(f.session, r, scene, "review452"); err != nil {
							t.Fatal(err)
						}
						registration := r.currentDungeonMapOwnedPassiveObjectRegistration(scene)
						if registration == nil || len(registration.Objects) != 1 {
							t.Fatal("ground policy removed ordinary object metadata")
						}
						rows, _ := currentDungeonPassiveObjectDropEntries(r, scene)
						if forbidden && len(rows) != 0 {
							t.Fatal("team passive item emitted a START_MAP row")
						}
					case "hell_equipment":
						catalog, err := f.service.dungeonMonsterCatalog()
						if err != nil {
							t.Fatal(err)
						}
						drops, err := catalog.DropCatalog()
						if err != nil {
							t.Fatal(err)
						}
						wires, err := f.service.registerHellPartyRewardDrops(r, scene, hellPartyRewardRequest{dropCatalog: drops}, []hellPartyRewardItem{{ItemID: 9001, Amount: 1}}, currentSceneActorObjectKey(19), 19)
						if err != nil {
							t.Fatal(err)
						}
						if forbidden && len(wires) != 0 {
							t.Fatal("team hell reward emitted equipment")
						}
					case "passive_registration":
						catalog, err := f.service.dungeonMonsterCatalog()
						if err != nil {
							t.Fatal(err)
						}
						drops, err := catalog.DropCatalog()
						if err != nil {
							t.Fatal(err)
						}
						item, err := drops.ResolveItem(3227)
						if err != nil {
							t.Fatal(err)
						}
						drop, _, err := r.registerCurrentDungeonPassiveObjectDrop(runtimeDungeonRoomKeyFromScene(scene), 0, item, 1)
						if forbidden {
							if drop != nil {
								t.Fatal("team passive registration retained an item")
							}
						} else if err != nil || drop == nil {
							t.Fatal("ordinary/single registration failed", err)
						}
					}
					if forbidden {
						if review452GroundCount(r) != 0 || r.NextObjectKey != beforeKey {
							t.Fatal("forbidden producer changed ledger/key")
						}
					} else if review452GroundCount(r) != 1 {
						t.Fatal("ordinary/single positive producer failed")
					}
				})
			}
		}
	}
}

func TestReview452TeamRaidDiscardPreservesInventory(t *testing.T) {
	for _, kind := range []string{"ordinary", "anton", "luke"} {
		for _, single := range []bool{false, true} {
			if kind == "ordinary" && single {
				continue
			}
			t.Run(fmt.Sprintf("%s/single=%t", kind, single), func(t *testing.T) {
				f := review452GroundRun(t, kind, single, false)
				inventory, found, err := f.repositories.Inventory.Load(context.Background(), "19")
				if err != nil || !found {
					t.Fatal(err, found)
				}
				if inventory.Slots == nil {
					inventory.Slots = make(map[string]repo.ItemStack)
				}
				inventory.Slots["0:121"] = currentDungeonDiscardTestStack(3227, 5)
				if err := f.repositories.Inventory.Save(context.Background(), inventory); err != nil {
					t.Fatal(err)
				}
				before, _, err := f.repositories.Inventory.Load(context.Background(), "19")
				if err != nil {
					t.Fatal(err)
				}
				key := f.runtime.NextObjectKey
				handled, err := f.service.handleCurrentDungeonDiscard(f.session, buildCurrentDungeonDiscardTestRequest(100, 200, 0, 121, 2, 0))
				if err != nil || !handled {
					t.Fatal(err, handled)
				}
				after, _, err := f.repositories.Inventory.Load(context.Background(), "19")
				if err != nil {
					t.Fatal(err)
				}
				if kind != "ordinary" && !single {
					if !reflect.DeepEqual(before, after) || review452GroundCount(f.runtime) != 0 || f.runtime.NextObjectKey != key || f.session.conn.(*bufferConn).write.Len() != 0 {
						t.Fatal("team discard consumed or published an inventory item")
					}
				} else if after.Slots["0:121"].Count != 3 || review452GroundCount(f.runtime) != 1 {
					t.Fatal("ordinary/single discard positive control failed")
				}
			})
		}
	}
}

func TestReview452TeamRaidAllPreparedParticipantsSuppressDrops(t *testing.T) {
	for _, kind := range []string{"anton", "luke"} {
		t.Run(kind, func(t *testing.T) {
			f := review452GroundRun(t, kind, false, true)
			if f.runtime.raidEntry.RunID != f.peerRuntime.raidEntry.RunID || len(f.runtime.raidEntry.Participants) != 2 {
				t.Fatal("party did not freeze common admission")
			}
			for index, r := range []*runtimeDungeonState{f.runtime, f.peerRuntime} {
				monster := review452PositiveMonsterRules(t, f.service, r)
				wires, err := f.service.planCurrentDungeonMonsterDrops(r, monster, currentSceneActorObjectKey(uint16(19+index)))
				if err != nil {
					t.Fatal(err)
				}
				if len(wires) != 0 || review452GroundCount(r) != 0 {
					t.Fatal("a legally prepared party member produced ground drops")
				}
			}
		})
	}
}
