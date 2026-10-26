package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentRaidBattleRequiresDependenciesBeforeCostCommit(t *testing.T) {
	f := raidSafetyFixture(t, nil, 1)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	t.Setenv(raidStartEntryCostGuardEnv, "off")
	_, ok, err := f.service.startRuntimeRaidWithEntryCost(session)
	if ok || err == nil || f.state.Started || f.state.Battle != nil {
		t.Fatal("missing battle dependencies admitted", ok, err)
	}
	if count, _ := f.slotCount(t, "0:50"); count != 3 {
		t.Fatal("missing battle charged material", count)
	}
}

func TestCurrentRaidBattleRealPVFAdmissionAndNativeProjection(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []byte{0, 1} {
		for _, name := range []string{"ready", "short", "weekly_full", "inconsistent_weekly", "stale_owner", "normal_minimum"} {
			t.Run(strconv.Itoa(int(route))+"_"+name, func(t *testing.T) {
				t.Setenv(raidStartEntryCostGuardEnv, "off") // Official admission still commits.
				f := raidSafetyFixture(t, archive, 1)
				f.service.options.currentClientVersion = "official-24.9"
				f.state.CurrentLayout, f.state.CurrentUIFlags = true, [3]byte{1, 0, 0}
				f.state.RouteOrRaidType = route
				f.state.Members[0].GroupIndex = 1
				f.state.NameBytes = []byte("qa")
				session := f.session()
				session.loginPacketSeen, session.accountFromToken = true, true
				if route == 1 {
					session.channel.ID, session.channel.Type = 32, 32
				}
				f.service.bindGameSessionCharacter(session, 19)
				rule, err := f.service.currentRaidAdmissionRule(session)
				if err != nil {
					t.Fatal(err)
				}
				count := rule.Count + 2
				if name == "short" {
					count = 0
				}
				if err := f.repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:50": {ItemID: int64(rule.ItemID), Count: count}}}); err != nil {
					t.Fatal(err)
				}
				if route == 1 {
					// Admission fixtures satisfy the original channel requirements;
					// this does not modify any real character's quest state.
					if err := f.repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}, 12830: {Status: "completed"}}}); err != nil {
						t.Fatal(err)
					}
					inventory, _, _ := f.repos.Inventory.Load(context.Background(), "19")
					inventory.Slots["0:51"] = dnfrepo.ItemStack{ItemID: 10159142, Count: 40}
					if err := f.repos.Inventory.Save(context.Background(), inventory); err != nil {
						t.Fatal(err)
					}
				}
				policy, _ := currentRaidWeeklyPolicy()
				epoch, _ := raid.WeeklyEpoch(time.Now(), policy)
				character, _, _ := f.repos.Character.Load(context.Background(), "19")
				if name == "weekly_full" || name == "inconsistent_weekly" {
					character.Stats = map[string]int64{"raid_weekly_epoch_v1_" + strconv.Itoa(int(route)+1): epoch}
					if name == "weekly_full" {
						character.Stats["raid_weekly_clears_v1_"+strconv.Itoa(int(route)+1)] = 2
					}
					if err := f.repos.Character.Save(context.Background(), character); err != nil {
						t.Fatal(err)
					}
				}
				if name == "stale_owner" {
					session.accountFromToken = false
				}
				if name == "normal_minimum" {
					f.state.CurrentUIFlags[0], f.state.TailFlag = 0, 0
				}
				f.service.onlinePlayers = newOnlinePlayerManager()
				f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, AccountID: session.accountID, Session: session, TownID: rule.WaitingTown, AreaID: rule.WaitingArea, ChannelID: session.channel.ID, Instance: f.state.RaidKey})
				ordinaryArea := byte(1)
				if route == 1 {
					ordinaryArea = 2
				}
				f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 31, TownID: rule.WaitingTown, AreaID: ordinaryArea, ChannelID: session.channel.ID, Instance: f.state.RaidKey})
				state, ok, err := f.service.startRuntimeRaidWithEntryCost(session)
				if ok && state.Battle != nil {
					leader, found := f.service.onlinePlayers.PlayerForCharacter(19)
					ordinary, _ := f.service.onlinePlayers.PlayerForCharacter(31)
					if !found || leader.RaidBattleInstance != state.Battle.InstanceID || ordinary.RaidBattleInstance != "" {
						t.Fatal("实际开团未限定本场等待区成员", leader, ordinary)
					}
				}
				if name == "ready" {
					sequence, sequenceErr := raid.LoadSingleRaidSequence(archive, route, 0)
					if sequenceErr != nil {
						t.Fatal(sequenceErr)
					}
					if dependencyErr := raid.ValidateDungeonDependencies(archive, sequence); dependencyErr != nil {
						if ok || err == nil || !strings.Contains(err.Error(), "raid dungeon") || f.state.Started || f.state.Battle != nil {
							t.Fatal("actual missing single dungeon admitted", ok, err)
						}
						if got, _ := f.slotCount(t, "0:50"); got != count {
							t.Fatal("missing actual single dungeon charged material", got, count)
						}
						t.Logf("actual single map dependency rejected before material commit: %v", err)
						return
					}
					if route == 0 {
						config, configErr := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
						if configErr != nil {
							t.Fatal(configErr)
						}
						if _, catalogErr := raid.LoadSingleAntonHPSymbols(archive, config.WeeklyClearLimit); catalogErr != nil {
							if ok || err == nil || !strings.Contains(err.Error(), "single HP catalog unavailable") || f.state.Started || f.state.Battle != nil {
								t.Fatal("missing actual single HP catalog admitted", ok, err)
							}
							if got, _ := f.slotCount(t, "0:50"); got != count {
								t.Fatal("missing single HP catalog charged material", got, count)
							}
							t.Logf("actual single HP catalog rejected before material commit: %v", catalogErr)
							return
						}
					}
				}
				if name == "normal_minimum" && rule.MinimumMembers == 1 {
					if !ok || err != nil || state.Battle == nil || state.Battle.Single {
						t.Fatal("PVF permits one-member normal raid", ok, err)
					}
					want, _ := raid.BuildDungeonStateBody(state.Battle.Configuration.PhaseDungeons[0], 0)
					got, _ := currentRaidDungeonProjectionBody(state.Battle, 0)
					if !bytes.Equal(got, want) {
						t.Fatal("normal raid maps must use actual phase configuration")
					}
					if got, _ := f.slotCount(t, "0:50"); got != 2 {
						t.Fatal("normal admission material commit", got)
					}
					return
				}
				if name != "ready" {
					if ok || f.state.Started || f.state.Battle != nil {
						t.Fatal("invalid battle admitted", state, err)
					}
					if got, _ := f.slotCount(t, "0:50"); got != count {
						t.Fatal("failed admission charged", got, count)
					}
					return
				}
				if !ok || err != nil || state.Battle == nil || !f.state.Started || len(state.Battle.InstanceID) != 32 {
					t.Fatal("valid battle not committed", ok, err)
				}
				if got, _ := f.slotCount(t, "0:50"); got != 2 {
					t.Fatal("actual material not committed", got)
				}
				b := state.Battle
				if b.Mode != route+1 || !b.Single || b.Phase != 0 || len(b.Dungeons) != 6 || b.Dungeons[0].State != 0 || b.Dungeons[1].State != 1 {
					t.Fatal("single PVF link owner incorrect", b)
				}
				if f.service.currentRaidDungeonSelectionAllowed(session, b.Dungeons[0].ID) {
					t.Fatal("start delay ignored")
				}
				f.state.Battle.EnterAfter = time.Now().Add(-time.Second)
				if !f.service.currentRaidDungeonSelectionAllowed(session, b.Dungeons[0].ID) || f.service.currentRaidDungeonSelectionAllowed(session, b.Dungeons[1].ID) || f.service.currentRaidDungeonSelectionAllowed(session, 1) {
					t.Fatal("map selection did not enforce instance")
				}
				f.state.Battle.Deadline = time.Now().Add(-time.Second)
				if f.service.currentRaidDungeonSelectionAllowed(session, b.Dungeons[0].ID) {
					t.Fatal("expired instance accepted")
				}
				// Mutating a snapshot cannot change the online owner.
				f.state.Battle.Deadline = b.Deadline
				state.Battle.Dungeons[0].State = 2
				if f.state.Battle.Dungeons[0].State != 0 {
					t.Fatal("snapshot aliased owner")
				}
				state = cloneRuntimeRaidState(*f.state)
				f.conn.write.Reset()
				if err := f.service.sendCurrentRaidBattleState(session, state); err != nil {
					t.Fatal(err)
				}
				stream := append([]byte(nil), f.conn.write.Bytes()...)
				for _, opcode := range []uint16{585, 587, 596, 597} {
					packet, rest := splitGameServerUpperPacket(t, stream)
					if packet.Header.MsgID != opcode || packet.Header.Classification != 0 {
						t.Fatal("native battle sequence", packet.Header)
					}
					switch opcode {
					case 585:
						want, _ := currentRaidDungeonProjectionBody(state.Battle, 0)
						if !bytes.Equal(packet.Body, want) {
							t.Fatal("map projection differs from owner")
						}
					case 587:
						if !bytes.Equal(packet.Body, []byte{2, 0}) {
							t.Fatal("native battle state/phase")
						}
					case 596:
						if len(packet.Body) != 13 || binary.LittleEndian.Uint32(packet.Body[5:9]) != uint32(b.Deadline.Unix()) {
							t.Fatal("native battle timer owner")
						}
					case 597:
						if len(packet.Body) != 5 || binary.LittleEndian.Uint32(packet.Body[1:]) > b.Configuration.PhaseLimitSeconds[0]+3 {
							t.Fatal("native remaining time")
						}
					}
					stream = rest
				}
				if route == 0 {
					// Single Anton now uses the same cumulative projection owner
					// as normal raids: map, standalone reset, then actual HP values.
					for index, opcode := range []uint16{585, 583, 583} {
						packet, rest := splitGameServerUpperPacket(t, stream)
						if packet.Header.MsgID != opcode || packet.Header.Classification != 0 {
							t.Fatal("single native projection sequence", packet.Header)
						}
						switch index {
						case 0:
							want, _ := currentRaidDungeonProjectionBody(state.Battle, 0)
							if !bytes.Equal(packet.Body, want) {
								t.Fatal("single projection map differs from owner")
							}
						case 1:
							if !bytes.Equal(packet.Body, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0}) {
								t.Fatal("single reset sentinel mixed into values")
							}
						case 2:
							want, err := currentSingleAntonHPBody(state.Battle)
							if err != nil || !bytes.Equal(packet.Body, want) {
								t.Fatal("single HP snapshot differs from frozen journal", err)
							}
						}
						stream = rest
					}
				}
				if len(stream) != 0 {
					t.Fatal("unexpected notification")
				}
				_, ok, _ = f.service.startRuntimeRaidWithEntryCost(session)
				if ok {
					t.Fatal("repeated start admitted")
				}
				if got, _ := f.slotCount(t, "0:50"); got != 2 {
					t.Fatal("repeated start charged")
				}
			})
		}
	}
}
