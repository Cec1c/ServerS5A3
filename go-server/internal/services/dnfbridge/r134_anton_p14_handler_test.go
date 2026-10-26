package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The entitlement is assembled by the existing real-PVF accepted-run fixture.
// Incoming 659/660 execute the full upper packet dispatcher. This test does
// not claim physical boss kills; a separate public map chain covers combat.
func TestR134P14AntonManualToFlowDoesNotReplay(t *testing.T) {
	for _, single := range []bool{true, false} {
		name := "team"
		if single {
			name = "single"
		}
		t.Run(name, func(t *testing.T) {
			var f *raidStartGuardFixture
			var p *gameSession
			var at time.Time
			if single {
				f, p, at = singleAntonCompletedRewardFixture(t)
			} else {
				f, p, _, at = onlineAntonTerminalFixture(t)
				// This older terminal fixture predates reward-flow setup. Load the
				// actual flow config and run its normal entitlement freeze, rather
				// than manufacturing a card/reward map or treating nil as a defect.
				config, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
				if err != nil {
					t.Fatal(err)
				}
				f.state.Battle.AntonFlowConfig = config
				if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
					t.Fatal(err)
				}
			}
			if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
				t.Fatal(done, errs)
			}
			if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 659, []byte{0})
			if len(antonNativeCardBodies(t, f)) != 2 {
				t.Fatal("manual movie completion omitted masks")
			}
			f.conn.write.Reset()
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			r134P14NoCardRestart(t, f.conn.write.Bytes(), "unchanged manual mask")
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 660, []byte{1, 2})
			want := int64(45)
			if single {
				want = 60
			}
			if got := antonGrantedItemCount(t, f, "19", 10304529); got != want {
				t.Fatalf("PVF party reward got %d want %d", got, want)
			}
			f.conn.write.Reset()
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			if single {
				r134P14NoCardRestart(t, f.conn.write.Bytes(), "unchanged committed party card")
			} else {
				// Manual660 sends one card; the full team projection legitimately
				// includes the still-unselected peer and must retain that update.
				for stream := f.conn.write.Bytes(); len(stream) > 0; {
					packet, rest := splitGameServerUpperPacket(t, stream)
					stream = rest
					if packet.Header.MsgID == 587 && len(packet.Body) == 2 && packet.Body[0] == 4 {
						t.Fatal("updated team party list restarted screen")
					}
				}
				if _, ok := antonNativeCardBodies(t, f)[1]; !ok {
					t.Fatal("changed team party list suppressed")
				}
			}
			flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			f.conn.write.Reset()
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			if _, ok := antonNativeCardBodies(t, f)[3]; !ok {
				t.Fatal("new squad display incorrectly suppressed")
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 659, []byte{1})
			dispatchAntonCardRequest(t, f, p, 660, []byte{1, 3})
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			r134P14NoCardRestart(t, f.conn.write.Bytes(), "late manual requests after squad")
			if got := antonGrantedItemCount(t, f, "19", 10304529); got != want {
				t.Fatal("duplicate party grant", got)
			}
		})
	}
}

func r134P14NoCardRestart(t *testing.T, stream []byte, where string) {
	t.Helper()
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if p.Header.Classification == 0 && (p.Header.MsgID == 600 || p.Header.MsgID == 587 && len(p.Body) == 2 && p.Body[0] == 4) {
			t.Fatalf("%s replayed reward presentation opcode=%d body=%x", where, p.Header.MsgID, p.Body)
		}
	}
}

func TestR134P14AntonManualMaskReconnectRestoresWithoutGrant(t *testing.T) {
	f, old, at := singleAntonCompletedRewardFixture(t)
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(old, state); err != nil {
		t.Fatal(err)
	}
	dispatchAntonCardRequest(t, f, old, 659, []byte{0})
	replacement := f.session()
	conn := &bufferConn{}
	replacement.conn, replacement.loginPacketSeen, replacement.accountFromToken = conn, true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonRewardFlowProjection(old, state); err == nil {
		t.Fatal("old owner projected after replacement")
	}
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil {
		t.Fatal(err)
	}
	screens, lists := 0, 0
	for data := conn.write.Bytes(); len(data) > 0; {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.Classification != 0 {
			continue
		}
		if packet.Header.MsgID == 587 && len(packet.Body) == 2 && packet.Body[0] == 4 {
			screens++
		}
		if packet.Header.MsgID == 600 && len(packet.Body) > 0 && packet.Body[0] == 1 {
			lists++
		}
	}
	if screens != 1 || lists != 1 {
		t.Fatalf("fresh owned connection restore screens=%d lists=%d", screens, lists)
	}
	conn.write.Reset()
	if err := f.service.handleCurrentAntonMovieCompletion(replacement, 659, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil {
		t.Fatal(err)
	}
	r134P14NoCardRestart(t, conn.write.Bytes(), "restored connection late movie")
	if got := antonGrantedItemCount(t, f, "19", 10304529); got != 0 {
		t.Fatal("restore minted reward", got)
	}
}

// Real current PVF plus normal 4/143/36/35/666/16/37, every announced
// blocker through39, topology moves through45, and boss117. No direct
// Complete/Announce/clear-ledger/raidEntry writes. Characters are memory fixtures.
func TestR134P14SinglePublicCombatAndSettlement(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	archive := catalog.source.(*platformpvf.Archive)
	f, owners, conns, _ := deadlineAuditV10Admit(t, archive, true)
	p, c := owners[0], conns[0]
	s := f.service
	// The inherited deadline fixture has no persisted experience. Seed this
	// memory-only character from the actual level threshold BEFORE op16; normal
	// login has already loaded/synced its skill ledger. Never weaken settlement.
	tables, err := progression.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	ch, found, err := f.repos.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	exp, err := tables.ThresholdToNext(ch.Level - 1)
	if err != nil {
		t.Fatal(err)
	}
	ch.Stats["exp"] = int64(exp)
	if err := f.repos.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	skill, found, err := f.repos.Skill.Load(context.Background(), "19")
	if err != nil || !found || skill.Points.SyncedLevel != ch.Level {
		t.Fatalf("login skill ledger: found=%t level=%d synced=%d err=%v", found, ch.Level, skill.Points.SyncedLevel, err)
	}
	sequence := append([]uint32(nil), f.state.Battle.SingleSequence...)
	var r *runtimeDungeonState
	deadlineAuditV10Event(t, s, p, func() error {
		if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: sequence[0], Difficulty: 2})); err != nil {
			return err
		}
		r = p.dungeon.runtime
		if r == nil {
			return fmt.Errorf("first actual map entry absent")
		}
		return s.sendFinishLoadingStatus(p, make([]byte, 8))
	})
	for i, id := range sequence {
		if r.Request.DungeonID != id || r.raidEntry == nil || !r.raidEntry.singleAntonMode {
			t.Fatalf("actual entry mode/id missing step=%d", i)
		}
		r134P14ReachAcceptedBoss(t, s, p, r)
		deadlineAuditV10Event(t, s, p, func() error {
			if review452GroundCount(r) != 0 {
				return fmt.Errorf("actual monster deaths produced floor loot step=%d", i)
			}
			body := make([]byte, currentDungeonPlayResultBaseSize)
			if err := s.handleDungeonSetPlayResult(p, body); err != nil {
				return err
			}
			if err := s.handleDungeonCharacterStatistic(p, make([]byte, currentDungeonCharacterStatisticBodySize)); err != nil {
				return err
			}
			if !r.settlementClearRewardSent || r.settlementResultPlan == nil {
				return fmt.Errorf("real settlement producer incomplete step=%d", i)
			}
			if r.settlementCardRewardState != nil || r.settlementCardLayoutSent || r.settlementCardScrollStateSent {
				return fmt.Errorf("ordinary cards leaked into single Anton")
			}
			return nil
		})
		t.Logf("real clear step=%d dungeon=%d floor=0 ordinary_cards=false", i, id)
		if i == len(sequence)-1 {
			break
		}
		deadlineAuditV10Event(t, s, p, func() error {
			if err := s.handleDungeonEplpCommand(p, []byte{1, 1}); err != nil {
				return err
			}
			if p.dungeon.runtime != nil {
				return fmt.Errorf("completed map selector return retained old runtime step=%d", i)
			}
			request := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: sequence[i+1], Difficulty: 2})
			if err := s.handleDungeonSelectUpper(p, request); err != nil {
				return err
			}
			r = p.dungeon.runtime
			if r == nil {
				return fmt.Errorf("next actual single map entry failed step=%d", i+1)
			}
			return s.sendFinishLoadingStatus(p, make([]byte, 8))
		})
	}
	s.raidMu.Lock()
	state := cloneRuntimeRaidState(*f.state)
	s.raidMu.Unlock()
	if !state.Battle.PhaseCompleted || len(state.Battle.ClearedRuns) != len(sequence) || state.Battle.RewardFlow[0] == nil {
		t.Fatal("real six-map completion did not create final entitlement")
	}
	if _, errs := s.commitCurrentRaidWeeklyIntents(time.Now()); len(errs) != 0 {
		t.Fatal(errs)
	}
	deadlineAuditV10Event(t, s, p, func() error { return s.sendCurrentAntonTerminalResult(p, state) })
	deadlineAuditV10Event(t, s, p, func() error {
		if err := s.handleCurrentAntonMovieCompletion(p, 659, []byte{0}); err != nil {
			return err
		}
		return s.handleCurrentAntonRewardCard(p, 660, []byte{1, 0})
	})
	if antonGrantedItemCount(t, f, "19", 10304529) != 60 {
		t.Fatal("real completed sequence did not grant actual party reward")
	}
	t.Logf("real single sequence final reward committed; wire_bytes=%d", len(c.bytes()))
}

func TestR134P14TeamPublicAdmissionAndPVFFatigueContract(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	f, owners, _, _ := deadlineAuditV10Admit(t, catalog.source.(*platformpvf.Archive), false)
	s := f.service
	if len(owners) < 2 {
		t.Fatal("actual team admission has fewer than two members")
	}
	peerID := strconv.Itoa(int(owners[1].selectedCharacterID))
	ch, _, err := f.repos.Character.Load(context.Background(), peerID)
	if err != nil {
		t.Fatal(err)
	}
	ch.Stats["fatigue"] = 0
	if err = f.repos.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	request := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: 210, Difficulty: 2})
	// A foreign admitted channel must reject the complete party before any
	// runtime is published; restoring the original fixture permits real entry.
	s.raidMu.Lock()
	f.state.Battle.ChannelID++
	s.raidMu.Unlock()
	deadlineAuditV10Event(t, s, owners[0], func() error { return s.handleDungeonSelectUpper(owners[0], request) })
	if owners[0].dungeon.runtime != nil || owners[1].dungeon.runtime != nil {
		t.Fatal("foreign channel admitted a participant")
	}
	s.raidMu.Lock()
	f.state.Battle.ChannelID--
	s.raidMu.Unlock()
	deadlineAuditV10Event(t, s, owners[0], func() error { return s.handleDungeonSelectUpper(owners[0], request) })
	for _, p := range owners[:2] {
		if p.dungeon.runtime == nil || p.dungeon.runtime.raidEntry == nil || len(p.dungeon.runtime.raidEntry.Participants) != 2 {
			t.Fatal("assembled two-member actual op16 entry missing")
		}
		if !p.dungeon.runtime.Dungeon.Metadata.NoFatigue {
			t.Fatal("real Anton210 does not declare no fatigue; reassess fixture")
		}
		deadlineAuditV10Event(t, s, p, func() error { return s.sendFinishLoadingStatus(p, make([]byte, 8)) })
	}
	ch, _, err = f.repos.Character.Load(context.Background(), peerID)
	if err != nil || ch.Stats["fatigue"] != 0 {
		t.Fatalf("no-fatigue PVF changed peer fatigue: %v %v", ch.Stats["fatigue"], err)
	}
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		s.raidMu.Lock()
		n := len(f.state.Battle.Participation)
		s.raidMu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	for _, p := range owners[:2] {
		if f.state.Battle.Participation[p.selectedCharacterID].Runtime != p.dungeon.runtime {
			t.Fatal("op37 placement did not register current participant")
		}
	}
	t.Log("actual team666 charged once; actual dungeon210 no-fatigue permits zero-fatigue peer unchanged; valid op16/op37 registered two shared-run participants")
}

func TestR134P14ActualSingleBossResources(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	sequence, err := raid.LoadSingleRaidSequence(catalog.source, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), catalog.source, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual sequence=%+v", sequence)
	topology, err := worldmap.BuildDungeonLayout(resolver, 5318, 0, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, coord := range topology.Bosses {
		room, ok := topology.Room(coord)
		if !ok || room.Map == nil {
			t.Fatal("boss room unresolved")
		}
		data, err := json.Marshal(struct {
			Coordinate worldmap.RoomCoordinate
			MapID      int64
			Path       string
			Monsters   []worldmap.MonsterSpawn
		}{coord, room.Map.Map.ID, room.Map.Map.Path, room.Map.Map.Monsters})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("actual boss room: %s", data)
		body, err := catalog.source.ReadText(room.Map.Map.Path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("actual boss map path=%s sha256=%x text=%s", room.Map.Map.Path, sha256.Sum256([]byte(body)), body)
	}
}

// Local extension of the existing public handler traversal: actual map
// 292101811 has rank [dummy] and suffix [boss]. Accept BOTH parsed markers
// when choosing the request target, without altering runtime/PVF/clear state.
func r134P14ReachAcceptedBoss(t *testing.T, s *Service, p *gameSession, r *runtimeDungeonState) {
	t.Helper()
	_, resolver, err := s.dungeonWorldMap()
	if err != nil {
		t.Fatal(err)
	}
	topology, err := worldmap.BuildDungeonLayout(resolver, r.Dungeon.ID, r.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	start := r.Session.Snapshot().Scene.Coordinate
	queue := []worldmap.RoomCoordinate{start}
	seen := map[worldmap.RoomCoordinate]bool{start: true}
	prev := map[worldmap.RoomCoordinate]worldmap.RoomCoordinate{}
	for len(queue) > 0 && !seen[r.BossCoordinate] {
		c := queue[0]
		queue = queue[1:]
		room, ok := topology.Room(c)
		if !ok {
			t.Fatal("actual path room missing")
		}
		for _, n := range room.Neighbors {
			if !seen[n.Coordinate] {
				seen[n.Coordinate] = true
				prev[n.Coordinate] = c
				queue = append(queue, n.Coordinate)
			}
		}
	}
	if !seen[r.BossCoordinate] {
		t.Fatal("actual boss unreachable")
	}
	path := []worldmap.RoomCoordinate{r.BossCoordinate}
	for path[len(path)-1] != start {
		path = append(path, prev[path[len(path)-1]])
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	clear := func() {
		deadlineAuditV10Event(t, s, p, func() error {
			scene := r.Session.Snapshot().Scene
			for _, ref := range scene.BlockingHostiles {
				var key uint32
				for k, v := range scene.RuntimeObjects {
					if v == ref {
						key = k
						break
					}
				}
				if key == 0 {
					return fmt.Errorf("actual blocker not announced: %+v", ref)
				}
				if _, _, dead := currentDungeonDefeatedActor(r, scene, key); dead {
					continue
				}
				body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
				binary.LittleEndian.PutUint32(body[0:4], key)
				binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(p.selectedCharacterID))
				if err := s.handleDungeonMonsterDeath(p, body); err != nil {
					return err
				}
				scene = r.Session.Snapshot().Scene
			}
			if !r.Session.Snapshot().Scene.Cleared {
				return fmt.Errorf("real op39 did not clear actual room")
			}
			return nil
		})
	}
	for _, c := range path[1:] {
		clear()
		if c.X < 0 || c.X > 255 || c.Y < 0 || c.Y > 255 {
			t.Fatal("actual room outside op45 field bounds")
		}
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0], body[1] = byte(c.X), byte(c.Y)
		deadlineAuditV10Event(t, s, p, func() error {
			if err := s.handleDungeonMoveMap(p, body); err != nil {
				return err
			}
			if r.Session.Snapshot().Scene.Coordinate != c {
				return fmt.Errorf("real op45 did not reach actual neighbor")
			}
			return s.sendFinishLoadingStatus(p, make([]byte, 8))
		})
	}
	var boss uint32
	deadlineAuditV10Event(t, s, p, func() error {
		for _, m := range r.Room.Snapshot().Monsters {
			if normalizeDungeonPVFSymbol(m.Spawn.Rank) == "boss" || normalizeDungeonPVFSymbol(m.Spawn.SuffixMarker) == "boss" {
				boss = m.ObjectKey
				break
			}
		}
		if boss == 0 || boss > 65535 {
			return fmt.Errorf("actual announced boss unavailable for117")
		}
		return nil
	})
	clear()
	deadlineAuditV10Event(t, s, p, func() error {
		return s.handleDungeonBossDieCheck(p, bossDieCheckRequestBody(currentSceneActorObjectKey(p.selectedCharacterID), uint16(boss)))
	})
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.bossDieCheckAccepted || !r.bossDieCheckResponseSent || !r.settlementEntrySent {
		t.Fatal("real boss117 did not own completion barriers")
	}
}
