package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	quest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	town "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	pvf "longheng.io/server/internal/platform/pvf"
)

// Explicit handler fixtures, not captured client traffic: op16/45/39/46 use
// established project codecs, actual PVF actors, in-memory accounts and bufferConn.
// No mark-cleared/complete helpers or completed-runtime fixtures are used.
//
// op16 is replayed twice per weekly route, and both variants are constructed
// handler requests rather than captured client traffic: one uses the reviewed
// catalogue-role selector candidate IDs, the other the native single-raid
// identities taken from etc/raid/singleraidlinksystem.etc. Both must commit the
// same native runtime owner.
const (
	lukeReplayAlias  = "alias"
	lukeReplayNative = "native"
)

var lukeReplayModes = [...]string{lukeReplayAlias, lukeReplayNative}

// Reviewed catalogue-role selector candidate IDs for single Luke, one per
// sequence step in weekly clear order, written as literals rather than derived
// from a mapping helper: they come from the offline area40 WDM / LukeStatusBoard
// catalogue roles plus the native static consumption evidence, not from captured
// op16 traffic (the empty selector page produced no client op16 for them).
// weekly 0 is the light six, weekly 1 the dark six, and 5054 is the light/dark
// shared first slot.
var lukeSelectorIDs = [...][6]uint32{
	{5054, 5055, 5057, 5059, 5064, 5061},
	{5054, 5056, 5058, 5060, 5065, 5062},
}

// Candidate slots inside the 5055..5058 window that belong to the *other* weekly
// route and not to this one. 5054 is the light/dark shared first slot, so it is
// never listed here: while it is the open one, asserting it shut would be wrong.
// The pairs cover both "same slot index" and "other slot index" against the
// current step.
var lukeForeignSelectorIDs = [...][]uint32{
	{5056, 5058},
	{5055, 5057},
}

func TestLukeCandidateBridgeTwoSingleRoutes(t *testing.T) {
	name := os.Getenv("DNFBRIDGE_LUKE_CANDIDATE_PVF")
	if name == "" {
		t.Skip("requires pinned offline Luke index candidate")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "ad7d05c70421278f7385729bcba3e81e37d99d0f3df85a87a9f0dc19c92504cb" {
		t.Fatal("candidate hash", got)
	}
	a, err := pvf.OpenBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	progressionTables, err := progression.Load(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	initialExp, err := progressionTables.ThresholdToNext(89)
	if err != nil {
		t.Fatal(err)
	}
	initialSP, err := progressionTables.TotalSkillPoints(90)
	if err != nil {
		t.Fatal(err)
	}
	initialTP, err := progressionTables.TotalTechniquePoints(90)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(ctx, a, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := raid.LoadAdmissionRule(a, "etc/raid/luke/luke.etc")
	if err != nil {
		t.Fatal(err)
	}
	prerequisite, err := quest.LoadLukeEntryRequirement(a)
	if err != nil {
		t.Fatal(err)
	}
	area, ok := towns.FindArea(int64(rule.WaitingTown), int64(rule.WaitingArea))
	if !ok {
		t.Fatal("actual waiting area missing")
	}
	m, ok := currentTownAreaMapDocument(a, area)
	if !ok || len(m.VirtualMovableArea) < 4 {
		t.Fatal("waiting map movable rectangle missing")
	}
	rect := m.VirtualMovableArea[:4]
	x, y := rect[0]+rect[2]/2, rect[1]+rect[3]/2
	if x < 0 || y < 0 || x > 32767 || y > 32767 {
		t.Fatal("waiting fixture midpoint out of bounds")
	}
	for weekly := 0; weekly < 2; weekly++ {
		for _, mode := range lukeReplayModes {
			t.Run(fmt.Sprintf("weekly_%d_%s", weekly, mode), func(t *testing.T) {
				t.Setenv(dungeonDeathRoomProofEnv, "strict")
				f := raidSafetyFixture(t, a, 1)
				s := f.service
				wire := &lukeReplayConn{}
				if dir := os.Getenv("DNFBRIDGE_LUKE_REPLAY_EVIDENCE"); dir != "" {
					t.Cleanup(func() {
						_ = f.logger.Close()
						data, err := os.ReadFile(f.logPath)
						if err != nil {
							t.Error(err)
							return
						}
						if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("weekly-%d-packet.log", weekly)), data, 0600); err != nil {
							t.Error(err)
						}
					})
				}
				s.options.currentClientVersion = "official-24.9"
				s.worldMapTable, s.worldMapResolver = table, resolver
				s.dungeonMonsterTable, s.dungeonAICharacterTable = monsters, ai
				s.townCatalog = towns
				s.dungeonChoice = func(int) (int, error) { return 0, nil }
				s.dungeonSeed = func() (uint32, error) { return 19871, nil }
				f.state.CurrentLayout, f.state.CurrentUIFlags, f.state.RouteOrRaidType = true, [3]byte{1, 0, 0}, 1
				f.state.Members[0].GroupIndex = 1
				session := f.session()
				session.conn = wire
				session.channel.ID, session.channel.Type = 32, 32
				session.loginPacketSeen, session.accountFromToken = true, true
				s.bindGameSessionCharacter(session, 19)
				policy, err := currentRaidWeeklyPolicy()
				if err != nil {
					t.Fatal(err)
				}
				epoch, err := raid.WeeklyEpoch(time.Now(), policy)
				if err != nil {
					t.Fatal(err)
				}
				c := repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Name: "LukeOffline", Job: "0", Level: 90, Stats: map[string]int64{"fatigue": 156, "town_id": int64(rule.WaitingTown), "area_id": int64(rule.WaitingArea), "pos_x": x, "pos_y": y, "direction": 5, "area_state": 3, "raid_weekly_epoch_v1_2": epoch, "raid_weekly_clears_v1_2": int64(weekly)}}
				c.Stats["exp"] = int64(initialExp)
				if err := f.repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "19", Points: repo.SkillPointState{TotalSP: initialSP, RemainingSP: initialSP, TotalTP: initialTP, RemainingTP: initialTP, SyncedLevel: 90}}); err != nil {
					t.Fatal(err)
				}
				if err := f.repos.Account.Save(ctx, repo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{}}); err != nil {
					t.Fatal(err)
				}
				if err := f.repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
				if err := f.repos.Quest.Save(ctx, repo.QuestRecord{CharacterID: "19", States: map[int64]repo.QuestState{prerequisite.QuestID: {Status: "completed"}, prerequisite.AntonQuestID: {Status: "completed"}}}); err != nil {
					t.Fatal(err)
				}
				if err := f.repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:50": {ItemID: int64(rule.ItemID), Count: rule.Count + 2}, "0:51": {ItemID: prerequisite.ItemID, Count: prerequisite.Count}}}); err != nil {
					t.Fatal(err)
				}
				state, started, err := s.startRuntimeRaidWithEntryCost(session)
				if err != nil || !started || state.Battle == nil {
					t.Fatalf("prepareCurrentRaidBattle/start: started=%t err=%v log=%s", started, err, f.logText(t))
				}
				if got, _ := f.slotCount(t, "0:50"); got != 2 {
					t.Fatal("actual ticket deduction", got)
				}
				// Clock fixture only: admission delay elapsed; retain configured duration.
				f.state.Battle.EnterAfter = time.Now().Add(-time.Second)
				f.state.Battle.Deadline = f.state.Battle.EnterAfter.Add(time.Duration(f.state.Battle.Configuration.PhaseLimitSeconds[0]) * time.Second)
				seq, err := raid.LoadSingleRaidSequence(a, 1, weekly)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("ADMITTED weekly=%d mode=%s sequence=%v actual_ticket=%d/%d", weekly, mode, seq, rule.ItemID, rule.Count)
				if len(seq) != len(lukeSelectorIDs[weekly]) {
					t.Fatalf("sequence %v does not match the literal selector table %v", seq, lukeSelectorIDs[weekly])
				}
				totalRooms, totalDeaths, totalMoves := 0, 0, 0
				for step, id := range seq {
					alias := lukeSelectorIDs[weekly][step]
					if alias == id {
						t.Fatalf("selector literal %d equals the native id; the alias rewrite would be untested", alias)
					}
					// alias mode replays the reviewed catalogue-role selector candidate IDs;
					// native mode keeps the previous native input coverage on the same
					// fixture. Both bodies are constructed here, not captured traffic.
					requested := alias
					if mode == lukeReplayNative {
						requested = id
					}
					bindDungeonSelectorOriginForTestAt(t, s, session, rule.WaitingTown, rule.WaitingArea, uint16(x), uint16(y))
					// Pre-entry op585: forced resubscribe so the frame is actually written
					// (the same-version progress projection is idempotently skipped), then
					// parsed from the real body instead of being rebuilt from a mapping
					// helper. The open selector slot of this step and its native owner row
					// must both be present and open before the entry is requested.
					wire.reset()
					if err := s.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, true); err != nil {
						t.Fatalf("pre-entry progress projection dgn=%d: %v", id, err)
					}
					states := singleSelectorStates(t, wire.bytes())
					aliasState, aliasFound := states[alias]
					nativeState, nativeFound := states[id]
					if !aliasFound || !nativeFound || aliasState != 0 || nativeState != 0 {
						t.Fatalf("pre-entry slot state dgn=%d alias=%d state=%d present=%t native=%d state=%d present=%t", id, alias, aliasState, aliasFound, id, nativeState, nativeFound)
					}
					// Refusals before the valid entry: a still-shut future slot of this weekly
					// route, plus every slot the other weekly route owns inside the reviewed
					// 5055..5058 candidate window. The shared 5054 first slot is never asserted
					// shut, because it is the open one when it is the current step.
					if step+1 < len(seq) {
						future := lukeSelectorIDs[weekly][step+1]
						if mode == lukeReplayNative {
							future = seq[step+1]
						}
						assertLukeReplaySelectRefused(t, s, session, wire, future, fmt.Sprintf("future slot step=%d dgn=%d", step, id))
					}
					for _, foreign := range lukeForeignSelectorIDs[weekly] {
						assertLukeReplaySelectRefused(t, s, session, wire, foreign, fmt.Sprintf("foreign weekly slot step=%d dgn=%d", step, id))
					}
					body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
					binary.LittleEndian.PutUint32(body, requested)
					body[4] = 2
					binary.LittleEndian.PutUint16(body[9:], 0xffff)
					wire.reset()
					if err := s.handleDungeonSelectUpper(session, body); err != nil {
						t.Fatalf("op16 dgn=%d requested=%d: %v", id, requested, err)
					}
					r := session.dungeon.runtime
					if r == nil || r.Dungeon.ID != int64(id) || r.Request.DungeonID != id || r.raidEntry == nil {
						t.Fatalf("op16 not committed dgn=%d requested=%d log=%s", id, requested, f.logText(t))
					}
					assertLukeReplayPacket(t, wire.bytes(), 29)
					if err := s.sendFinishLoadingStatus(session, make([]byte, 8)); err != nil {
						t.Fatalf("finish loading dgn=%d: %v", id, err)
					}
					visited := map[worldmap.RoomCoordinate]bool{}
					for {
						scene := r.Session.Snapshot().Scene
						visited[scene.Coordinate] = true
						totalRooms++
						t.Logf("ROOM dgn=%d map=%d xy=%s range=%t blockers=%d actors=%d", id, scene.Map.Map.ID, scene.Coordinate, scene.Map.Map.PathgateRecognizeRange.Set, len(scene.BlockingHostiles), len(r.Room.Snapshot().Monsters)+len(r.Room.Snapshot().ExtendedActors))
						refs := append([]worldmap.HostileReference(nil), scene.BlockingHostiles...)
						for _, ref := range refs {
							scene = r.Session.Snapshot().Scene
							var key uint32
							for k, v := range scene.RuntimeObjects {
								if v == ref {
									key = k
									break
								}
							}
							if key == 0 {
								t.Fatalf("blocker not announced dgn=%d map=%d ref=%+v", id, scene.Map.Map.ID, ref)
							}
							if _, _, dead := currentDungeonDefeatedActor(r, scene, key); dead {
								continue
							}
							death := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
							binary.LittleEndian.PutUint32(death, key)
							binary.LittleEndian.PutUint16(death[4:], currentSceneActorObjectKey(19))
							wire.reset()
							if err := s.handleDungeonMonsterDeath(session, death); err != nil {
								t.Fatalf("op39 map=%d key=%d: %v", scene.Map.Map.ID, key, err)
							}
							if _, _, dead := currentDungeonDefeatedActor(r, r.Session.Snapshot().Scene, key); !dead {
								t.Fatalf("op39 rejected map=%d key=%d log=%s", scene.Map.Map.ID, key, f.logText(t))
							}
							assertLukeReplayPacket(t, wire.bytes(), 38)
							totalDeaths++
						}
						scene = r.Session.Snapshot().Scene
						if !scene.Cleared {
							t.Fatalf("bridge room not clear map=%d log=%s", scene.Map.Map.ID, f.logText(t))
						}
						if scene.Boss {
							break
						}
						var next *worldmap.RoomCoordinate
						for _, room := range r.Session.Rooms() {
							if room.Coordinate == scene.Coordinate {
								for _, n := range room.Neighbors {
									if !visited[n.Coordinate] {
										if next != nil {
											t.Fatal("branch requires explicit coverage")
										}
										v := n.Coordinate
										next = &v
									}
								}
							}
						}
						if next == nil {
							t.Fatal("no next actual neighbor", id, scene.Coordinate)
						}
						move := make([]byte, dungeoncmd.MoveMapRequestSize)
						move[0], move[1] = byte(next.X), byte(next.Y)
						wire.reset()
						if err := s.handleDungeonMoveMap(session, move); err != nil {
							t.Fatalf("op45 %s -> %s: %v", scene.Coordinate, *next, err)
						}
						if r.Session.Snapshot().Scene.Coordinate != *next {
							t.Fatalf("op45 rejected dgn=%d map=%d target=%s log=%s", id, scene.Map.Map.ID, *next, f.logText(t))
						}
						assertLukeReplayPacket(t, wire.bytes(), 29)
						if err := s.sendFinishLoadingStatus(session, make([]byte, 8)); err != nil {
							t.Fatalf("finish loading map=%d: %v", r.Session.Snapshot().Scene.Map.Map.ID, err)
						}
						totalMoves++
					}
					if len(visited) != len(r.Session.Rooms()) {
						t.Fatal("not all actual rooms visited", id)
					}
					if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.settlementEntrySent {
						t.Fatalf("completion/op31 blocked dgn=%d log=%s", id, f.logText(t))
					}
					if f.state.Battle.SingleIndex != step+1 {
						t.Fatalf("raid progression blocked dgn=%d index=%d log=%s", id, f.state.Battle.SingleIndex, f.logText(t))
					}
					// Existing codec-shaped zero-statistics fixture; not a captured Luke result.
					result := make([]byte, currentDungeonPlayResultBaseSize)
					wire.reset()
					if err := s.handleDungeonSetPlayResult(session, result); err != nil {
						t.Fatalf("op46 dgn=%d: %v", id, err)
					}
					if !r.settlementPlayResultReceived {
						t.Fatalf("op46 not accepted dgn=%d log=%s", id, f.logText(t))
					}
					if r.settlementResultPlan == nil || !r.settlementResultNoticeSent || !r.settlementCharacterStateSent || !r.settlementClearRewardSent {
						session.dungeon.mu.Lock()
						diagnostic := s.produceCurrentDungeonSettlementPlanLocked(ctx, session, r)
						session.dungeon.mu.Unlock()
						t.Errorf("BLOCKED op46 result dgn=%d function=produceCurrentDungeonSettlementPlanLocked error=%v result_plan=%t op34=%t op37=%t op35=%t", id, diagnostic, r.settlementResultPlan != nil, r.settlementResultNoticeSent, r.settlementCharacterStateSent, r.settlementClearRewardSent)
					} else {
						for _, opcode := range []uint16{34, 37, 35} {
							assertLukeReplayPacket(t, wire.bytes(), opcode)
						}
					}
					t.Logf("COMPLETED dgn=%d rooms=%d op31=%t op46=%t raid_index=%d phase_complete=%t", id, len(visited), r.settlementEntrySent, r.settlementPlayResultReceived, f.state.Battle.SingleIndex, f.state.Battle.PhaseCompleted)
					// Next request starts at an explicitly rebound town selector origin.
					// No claim of native return/loading or result-card interaction is made.
				}
				// Wait for the production weekly worker, rather than racing its commit.
				deadline := time.Now().Add(3 * time.Second)
				for {
					c, _, err = f.repos.Character.Load(ctx, "19")
					if err != nil {
						t.Fatal(err)
					}
					used, e := raid.WeeklyClears(c, 2, time.Now(), policy)
					if e != nil {
						t.Fatal(e)
					}
					s.raidMu.Lock()
					running := s.raidWeeklyWorkRunning
					s.raidMu.Unlock()
					if used == weekly+1 && !running {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("weekly worker did not complete", used, running)
					}
					time.Sleep(time.Millisecond)
				}
				c, _, err = f.repos.Character.Load(ctx, "19")
				if err != nil {
					t.Fatal(err)
				}
				used, err := raid.WeeklyClears(c, 2, time.Now(), policy)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("ROUTE-END weekly=%d rooms=%d deaths=%d moves=%d phase_completed=%t weekly_after=%d", weekly, totalRooms, totalDeaths, totalMoves, f.state.Battle.PhaseCompleted, used)
				if !f.state.Battle.PhaseCompleted || used != weekly+1 {
					t.Fatal("final raid/weekly commit incomplete", used)
				}
				s.raidMu.Lock()
				finalState := cloneRuntimeRaidState(*f.state)
				s.raidMu.Unlock()
				_, projectionErr := prepareCurrentAntonResultProjection(finalState, 0)
				if projectionErr != nil {
					t.Fatalf("BLOCKED terminal raid result function=prepareCurrentAntonResultProjection mode=%d single=%t rewards_present=%t flow_break_seconds=%d error=%v", finalState.Battle.Mode, finalState.Battle.Single, finalState.Battle.AntonRewards != nil, finalState.Battle.AntonFlowConfig.PhaseBreakSeconds, projectionErr)
				}
				if err := s.sendCurrentAntonTerminalResult(session, finalState); err != nil {
					t.Fatal(err)
				}
				assertLukeReplayPacket(t, wire.bytes(), 601)
				flow := finalState.Battle.RewardFlow[0]
				if flow == nil || !flow.Single || flow.Mode != 2 || len(flow.Recipients) != 1 {
					t.Fatal("single Luke terminal reward flow missing")
				}
				before := map[uint32]int64{}
				for _, id := range []uint32{10304529, 10159143, 10159623} {
					before[id] = antonGrantedItemCount(t, f, "19", id)
				}
				if _, errs := s.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
					t.Fatal("single Luke actual reward grant", errs)
				}
				for id, amount := range map[uint32]int64{10304529: 80, 10159143: 5, 10159623: 2} {
					if got := antonGrantedItemCount(t, f, "19", id); got != before[id]+amount {
						t.Fatalf("single Luke reward item=%d got=%d before=%d added=%d", id, got, before[id], amount)
					}
				}
				if _, errs := s.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*time.Minute), func(int64) (int64, error) {
					t.Fatal("committed single Luke reward rerolled")
					return 0, nil
				}); len(errs) != 0 {
					t.Fatal(errs)
				}
				s.raidMu.Lock()
				finished := cloneRuntimeRaidState(*f.state)
				s.raidMu.Unlock()
				if finished.Battle.Phase != 0 || finished.Battle.Anton != nil || !finished.Battle.RewardFlow[0].BreakUntil.IsZero() || finished.Battle.RewardFlow[0].FinishedAt.IsZero() {
					t.Fatal("single Luke entered normal phase or failed to finish")
				}
				t.Logf("TERMINAL single Luke op601 and actual two-group rewards committed; weekly=%d; replay did not reroll", weekly+1)
			})
		}
	}
}

// Raid progress/weekly notices can write asynchronously during handler replay.
type lukeReplayConn struct {
	bufferConn
	mu sync.Mutex
}

func (c *lukeReplayConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bufferConn.Write(p)
}
func (c *lukeReplayConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.write.Bytes()...)
}
func (c *lukeReplayConn) reset() { c.mu.Lock(); defer c.mu.Unlock(); c.write.Reset() }

func assertLukeReplayPacket(t *testing.T, raw []byte, id uint16) {
	t.Helper()
	for len(raw) > 0 {
		p, rest := splitGameServerUpperPacket(t, raw)
		if p.Header.MsgID == id && p.Header.Classification == 0 {
			return
		}
		raw = rest
	}
	t.Fatalf("required class0/op%d missing", id)
}

// assertLukeReplaySelectRefused drives one op16 that must be refused before any
// runtime owner changes, and checks the existing class1/op16 {0,reason,0}
// refusal on the wire. The current runtime owner is captured before the request:
// a refusal may neither create, replace nor drop it. On the first step of a route
// that owner is still absent, from the second step on it is the previous
// completed run, which the real next entry replaces only after it is admitted.
func assertLukeReplaySelectRefused(t *testing.T, s *Service, session *gameSession, wire *lukeReplayConn, dungeonID uint32, label string) {
	t.Helper()
	before := session.dungeon.runtime
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, dungeonID)
	body[4] = 2
	binary.LittleEndian.PutUint16(body[9:], 0xffff)
	wire.reset()
	if err := s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("%s op16 dgn=%d: %v", label, dungeonID, err)
	}
	if session.dungeon.runtime != before {
		t.Fatalf("%s op16 dgn=%d changed the runtime owner", label, dungeonID)
	}
	refusals := 0
	raw := wire.bytes()
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		raw = rest
		if packet.Header.MsgID != 16 {
			continue
		}
		refusals++
		if packet.Header.Classification != 1 || len(packet.Body) != 3 || packet.Body[0] != 0 ||
			packet.Body[1] != currentSelectDungeonInvalidSelectionStateReason || packet.Body[2] != 0 {
			t.Fatalf("%s op16 dgn=%d refusal class=%d body=%v", label, dungeonID, packet.Header.Classification, packet.Body)
		}
	}
	if refusals != 1 {
		t.Fatalf("%s op16 dgn=%d refusals=%d", label, dungeonID, refusals)
	}
}
