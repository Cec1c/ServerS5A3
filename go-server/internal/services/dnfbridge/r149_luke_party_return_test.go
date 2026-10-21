package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func r149Input(t *testing.T) string {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_LUKE_PARTY_RETURN_PVF")
	if path == "" {
		t.Skip("set DNFBRIDGE_LUKE_PARTY_RETURN_PVF for exact-resource test")
	}
	t.Setenv("DNFBRIDGE_REAL_PVF_SMOKE", path)
	return path
}

func r149Send(t *testing.T, s *Service, p *gameSession, op uint16, body []byte) {
	t.Helper()
	if err := s.handleGameUpper(p, buildCurrentDungeonClientUpperForPhaseTest(t, op, body)); err != nil {
		t.Fatalf("op%d: %v", op, err)
	}
}

// Authentication, lobby membership and prior phase clears are memory fixture
// boundaries. Target entry and completion must use actual handlers and resources.
func r149Fixture(t *testing.T, id uint32) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	path := r149Input(t)
	f, leader := reviewLukePhaseTwoFixture(t)
	s := f.service
	a := s.initialEquipmentArchive
	if !knownRepairPVFChecksum(a.Snapshot().Checksum) {
		t.Fatal("wrong PVF")
	}
	if out := os.Getenv("R149_OUT"); out != "" {
		text, e := a.ReadText("etc/raid/luke/luke.etc")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(out, "luke.etc.txt"), []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
	}
	// Establish previous-map history using the actual resource graph; never
	// assign the target map's open/clear state or a completed target receipt.
	progress := &raid.LukeProgression{AntonProgression: f.state.Battle.Anton}
	open := func() bool {
		for _, d := range progress.Dungeons() {
			if d.ID == id {
				return d.State == 0
			}
		}
		return false
	}
	for _, prior := range []uint32{5311, 5313, 5057, 5058, 5059, 5060, 5064, 5065} {
		if open() {
			break
		}
		run := fmt.Sprintf("r149-history-%d", prior)
		var e error
		progress, _, e = progress.EnterDungeon(run, prior, progress.Epoch(), time.Now())
		if e != nil {
			t.Fatal("prior enter", prior, e)
		}
		progress, _, e = progress.ClearDungeon(run, prior, progress.Epoch(), time.Now())
		if e != nil {
			t.Fatal("prior clear", prior, e)
		}
		t.Logf("historical resource transition cleared %d", prior)
	}
	if !open() {
		var e error
		progress, _, e = progress.EnterDungeon("r149-history-light-throne", 5061, progress.Epoch(), time.Now())
		if e != nil {
			t.Fatal("prior throne enter", e)
		}
		// Actual luke.etc ON ENTER MAP 55845 opens the energy-save map.
		progress, e = progress.EnterMap(55845, time.Now())
		if e != nil {
			t.Fatal(e)
		}
	}
	if !open() {
		t.Fatalf("target %d still closed by real graph: %+v", id, progress.Dungeons())
	}
	f.state.Battle.Anton = progress.AntonProgression
	f.state.Battle.Dungeons = progress.Dungeons()
	s.options.pvfPath = path
	s.options.joustEventDisabled = true
	s.onlinePlayers = newOnlinePlayerManager()
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	ctx := context.Background()
	pt, err := progression.Load(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	xp, err := pt.ThresholdToNext(89)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := pt.TotalSkillPoints(90)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := pt.TotalTechniquePoints(90)
	if err != nil {
		t.Fatal(err)
	}
	for _, load := range []func(context.Context) error{s.preloadInitialEquipmentIndex, s.preloadPVFItemCatalog, s.preloadExpertJobCatalog, s.preloadAdventureGroupTable, s.preloadHonorTable, s.preloadTownCatalog, s.preloadSkillCatalog, s.preloadProfessionProfiles, s.preloadQuestCatalog, s.preloadDungeonWorldMap, s.preloadCharacterStatTable, s.preloadEquipmentStatIndex, s.preloadCombatPowerAffixCatalog} {
		if err := load(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rule, err := raid.LoadAdmissionRule(a, "etc/raid/luke/luke.etc")
	if err != nil {
		t.Fatal(err)
	}
	area, ok := s.townCatalog.FindArea(int64(rule.WaitingTown), int64(rule.WaitingArea))
	if !ok {
		t.Fatal("waiting area missing")
	}
	m, ok := currentTownAreaMapDocument(a, area)
	if !ok || len(m.VirtualMovableArea) < 4 {
		t.Fatal("waiting map bounds missing")
	}
	v := m.VirtualMovableArea
	x, y := v[0]+v[2]/2, v[1]+v[3]/2
	peer := f.session()
	peer.selectedCharacterID = 20
	peer.accountID = "account-20"
	peer.channel = leader.channel
	sessions := []*gameSession{leader, peer}
	state := alignedcmd.PartyState{PartyID: 19, UserID: 19, Members: []alignedcmd.PartyMemberState{{UserID: 19, UserState: 1, HPPercent: 100, MPPercent: 100}, {UserID: 20, UserState: 1, HPPercent: 100, MPPercent: 100}}}
	for i, p := range sessions {
		p.conn = &bufferConn{}
		p.dungeon.runtime = nil
		p.loginPacketSeen = true
		p.accountFromToken = true
		p.residentChannel = p.channel
		p.connID = fmt.Sprintf("r149-%d", p.selectedCharacterID)
		s.bindGameSessionCharacter(p, p.selectedCharacterID)
		ch := repo.CharacterRecord{CharacterID: fmt.Sprint(p.selectedCharacterID), AccountID: p.accountID, Name: fmt.Sprintf("r149-%d", i), Job: "0", Level: 90, Stats: map[string]int64{"fatigue": 156, "town_id": int64(rule.WaitingTown), "area_id": int64(rule.WaitingArea), "pos_x": x, "pos_y": y, "direction": 5, "area_state": 3}}
		ch.Stats["exp"] = int64(xp)
		if err := f.repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: ch.CharacterID, Points: repo.SkillPointState{TotalSP: sp, RemainingSP: sp, TotalTP: tp, RemainingTP: tp, SyncedLevel: 90}}); err != nil {
			t.Fatal(err)
		}
		if err := f.repos.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
		if err := f.repos.Account.Save(ctx, repo.AccountRecord{AccountID: p.accountID}); err != nil {
			t.Fatal(err)
		}
		if err := f.repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: ch.CharacterID, Slots: map[string]repo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		storeRuntimePartyState(p, state)
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: p.selectedCharacterID, TownID: rule.WaitingTown, AreaID: rule.WaitingArea, Session: p})
		bindDungeonSelectorOriginForTestAt(t, s, p, rule.WaitingTown, rule.WaitingArea, uint16(x), uint16(y))
		f.state.Members[i].GroupIndex = 1
		f.state.Members[i].SlotOrder = byte(i)
		f.state.Battle.MemberGroups[p.selectedCharacterID] = 1
	}
	if _, ok := s.bootstrapManagedRuntimePartyForSession(leader); !ok {
		t.Fatal("managed membership bootstrap")
	}
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, id)
	r149Send(t, s, leader, 16, body)
	for _, p := range sessions {
		if p.dungeon.runtime == nil {
			t.Fatalf("target%d op16 not committed for%d; log=%s", id, p.selectedCharacterID, testPacketLogText(t, s))
		}
	}
	if leader.dungeon.runtime.partyInstance == nil || leader.dungeon.runtime.partyInstance != peer.dungeon.runtime.partyInstance {
		t.Fatal("entry did not share party instance")
	}
	return f, sessions
}

func TestR149LukeEnergyResourceAndEntry(t *testing.T) {
	path := r149Input(t)
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !knownRepairPVFChecksum(a.Snapshot().Checksum) {
		t.Fatal("wrong PVF")
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{5063, 5066} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			d, ok := table.FindDungeon(int64(id))
			if !ok {
				t.Fatal("dungeon absent")
			}
			type row struct {
				Coordinate worldmap.RoomCoordinate
				Map        int64
				Path       string
				Boss       bool
			}
			rows := []row{}
			for maze := range d.Mazes {
				layout, e := worldmap.BuildDungeonLayout(resolver, int64(id), maze, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
				if e != nil {
					t.Fatal(e)
				}
				for _, room := range layout.Rooms() {
					if room.Map != nil {
						rows = append(rows, row{room.Coordinate, room.Map.Map.ID, room.Map.Map.Path, room.Boss})
					}
				}
			}
			raw, _ := json.Marshal(struct {
				ID    uint32
				Path  string
				Rooms []row
			}{id, d.Path, rows})
			t.Log(string(raw))
			f, ps := r149Fixture(t, id)
			for _, p := range ps {
				r := p.dungeon.runtime
				t.Logf("entry char=%d dungeon=%d sharedRun=%s entry=%t clear=%t", p.selectedCharacterID, r.Dungeon.ID, r.raidEntry.RunID, r.settlementEntrySent, r.settlementClearRewardSent)
			}
			_ = f
		})
	}
}

func r149ClearToResult(t *testing.T, f *raidStartGuardFixture, ps []*gameSession, hooks ...func(string) bool) {
	t.Helper()
	s := f.service
	leader := ps[0]
	r := leader.dungeon.runtime
	oldDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	t.Cleanup(func() { partyDungeonLoadingBarrierDispatch = oldDispatch })
	loaded := func() {
		for _, p := range ps {
			r149Send(t, s, p, 37, make([]byte, currentFinishLoadingLegacyRequestBodySize))
		}
	}
	loaded()
	rooms := map[worldmap.RoomCoordinate]worldmap.DungeonRoom{}
	var goal worldmap.RoomCoordinate
	for _, room := range r.Session.Rooms() {
		rooms[room.Coordinate] = room
		if room.Boss {
			goal = room.Coordinate
		}
	}
	scene, _ := r.Session.Scene()
	start := scene.Coordinate
	queue := []worldmap.RoomCoordinate{start}
	parents := map[worldmap.RoomCoordinate]worldmap.RoomCoordinate{start: start}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		for _, n := range rooms[at].Neighbors {
			if _, seen := parents[n.Coordinate]; !seen {
				parents[n.Coordinate] = at
				queue = append(queue, n.Coordinate)
			}
		}
	}
	if _, ok := parents[goal]; !ok {
		t.Fatal("boss unreachable")
	}
	route := []worldmap.RoomCoordinate{goal}
	for route[len(route)-1] != start {
		route = append(route, parents[route[len(route)-1]])
	}
	var finalKey uint32
	for i := len(route) - 1; i >= 0; i-- {
		scene, _ = r.Session.Scene()
		if scene.Coordinate != route[i] {
			t.Fatalf("route mismatch %v %v", scene.Coordinate, route[i])
		}
		snap := r.Room.Snapshot()
		keys := []uint32{}
		for _, m := range snap.Monsters {
			if m.State != runtimeDungeonMonsterDefeated {
				keys = append(keys, m.ObjectKey)
			}
		}
		for _, m := range snap.ExtendedActors {
			if m.HostileReference != nil && m.State != runtimeDungeonMonsterDefeated {
				keys = append(keys, m.ObjectKey)
			}
		}
		for _, key := range keys {
			body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
			binary.LittleEndian.PutUint32(body, key)
			binary.LittleEndian.PutUint16(body[4:], leader.selectedCharacterID)
			r149Send(t, s, leader, 39, body)
			finalKey = key
		}
		scene, _ = r.Session.Scene()
		t.Logf("cleared map%d coord%s cleared=%t hostiles=%d", scene.Map.Map.ID, scene.Coordinate, scene.Cleared, len(keys))
		if !scene.Cleared {
			t.Fatalf("room not clear: %s", testPacketLogText(t, s))
		}
		if i > 0 {
			next := route[i-1]
			move := make([]byte, dungeoncmd.MoveMapRequestSize)
			move[0], move[1] = byte(next.X), byte(next.Y)
			r149Send(t, s, leader, 45, move)
			for _, p := range ps {
				sc, _ := p.dungeon.runtime.Session.Scene()
				if sc.Coordinate != next {
					t.Fatalf("peer%d move failed: %s", p.selectedCharacterID, testPacketLogText(t, s))
				}
			}
			loaded()
		}
	}
	scene, _ = r.Session.Scene()
	for _, hook := range hooks {
		if hook("before_object") {
			return
		}
	}
	if len(keysForR149Destroy(r, scene)) > 0 {
		for _, key := range keysForR149Destroy(r, scene) {
			body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
			binary.LittleEndian.PutUint32(body, key)
			binary.LittleEndian.PutUint16(body[4:], ^uint16(0))
			r149Send(t, s, leader, 39, body)
			if os.Getenv("R149_SELF_REPORT_CONTROL") == "1" {
				r149Send(t, s, ps[1], 39, body)
			}
		}
	} else {
		r149Send(t, s, leader, 117, bossDieCheckRequestBody(leader.selectedCharacterID, uint16(finalKey)))
	}
	for _, hook := range hooks {
		if hook("after_object") {
			return
		}
	}
	for _, p := range ps {
		rr := p.dungeon.runtime
		if !rr.settlementEntrySent {
			t.Fatalf("peer%d no personal op31: %s", p.selectedCharacterID, testPacketLogText(t, s))
		}
		r149NoReward(t, p)
		frames := splitUpperFrames(t, p.conn.(*bufferConn).write.Bytes())
		if r133RegressionCountMsg(frames, 31) != 1 || r133RegressionCountMsg(frames, 38) != 0 {
			t.Fatal("passive completion must send one personal op31, no actor op38")
		}
	}
	for _, p := range ps {
		rr := p.dungeon.runtime
		body := make([]byte, currentDungeonPlayResultBaseSize)
		body[currentDungeonPlayResultClientRankPointOffset] = 73
		r149Send(t, s, p, 46, body)
		r149Send(t, s, p, 123, make([]byte, currentDungeonCharacterStatisticBodySize))
		if !s.currentRaidCompletedMapTownReturnReady(p, rr) {
			t.Logf("settlement diagnosis: %v", s.produceCurrentDungeonSettlementPlanLocked(context.Background(), p, rr))
			t.Fatalf("peer%d result not ready entry%t result%t statistic%t reward%t plan%t: %s", p.selectedCharacterID, rr.settlementEntrySent, rr.settlementPlayResultReceived, rr.settlementStatisticReceived, rr.settlementClearRewardSent, rr.settlementResultPlan != nil, testPacketLogText(t, s))
		}
	}
}

func keysForR149Destroy(r *runtimeDungeonState, scene worldmap.DungeonRoomScene) []uint32 {
	var keys []uint32
	for _, c := range r.Dungeon.Mazes[r.MazeIndex].ClearConditions {
		if c.Type == "destroy object" && c.Count == 1 {
			for _, o := range scene.PassiveObjects {
				if o.ObjectID == c.TargetID {
					keys = append(keys, uint32(c.TargetID))
				}
			}
		}
	}
	return keys
}

func TestR149LukeEnergyPartyReturn(t *testing.T) {
	for _, id := range []uint32{5063, 5066} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			f, ps := r149Fixture(t, id)
			r149ClearToResult(t, f, ps)
			oldPeer := ps[1].dungeon.runtime
			oldLeader := ps[0].dungeon.runtime
			if oldLeader.clearMapCompletionKey == "" || oldLeader.clearMapCompletionKey == oldPeer.clearMapCompletionKey {
				t.Fatal("personal settlement receipt keys not distinct")
			}
			r149Send(t, f.service, ps[0], 72, []byte{1, 2})
			if ps[0].dungeon.runtime != nil || ps[1].dungeon.runtime != nil || !oldPeer.townReturnOp24Sent {
				t.Fatalf("ready group failed shared return leader=%t peer=%t peer24=%t: %s", ps[0].dungeon.runtime != nil, ps[1].dungeon.runtime != nil, oldPeer.townReturnOp24Sent, testPacketLogText(t, f.service))
			}
			if out := os.Getenv("R149_OUT"); out != "" {
				evidence := map[string]any{"dungeon": id, "shared_run": oldPeer.raidEntry.RunID, "leader_receipt": oldLeader.clearMapCompletionKey, "peer_receipt": oldPeer.clearMapCompletionKey, "leader_plan": oldLeader.settlementResultPlan != nil, "peer_plan": oldPeer.settlementResultPlan != nil, "leader_reward_sent": oldLeader.settlementClearRewardSent, "peer_reward_sent": oldPeer.settlementClearRewardSent, "both_detached": true, "peer_op24": oldPeer.townReturnOp24Sent}
				b, e := json.MarshalIndent(evidence, "", "  ")
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(out, fmt.Sprintf("handler-evidence-%d.json", id)), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestR149LukeResourceText(t *testing.T) {
	path := r149Input(t)
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if !knownRepairPVFChecksum(a.Snapshot().Checksum) {
		t.Fatal("wrong PVF")
	}
	out := os.Getenv("R149_OUT")
	if out == "" {
		out = t.TempDir()
	}
	for _, p := range []string{"dungeon/Luke/Phase2/Energy_control.dgn", "dungeon/Luke/Phase2/Energy_save.dgn", "map/Luke_Raid/Phase2/Energy_control/74232.map", "map/Luke_Raid/Phase2/Energy_save/74247.map"} {
		text, e := a.ReadText(p)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(out, filepath.Base(p)+".txt"), []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
		t.Logf("resource %s decoded bytes=%d", p, len(text))
	}
}
