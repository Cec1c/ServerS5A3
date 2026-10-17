package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestDespairResultUsesDedicatedNoticeAndRetriesAfterWriteFailure(t *testing.T) {
	s, rt, session, _, _ := prepareCompletedSettlementRuntime(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	rt.Dungeon.Path = "dungeon/towers/ztowerofdespair001.dgn"
	plan := currentDungeonSettlementPacketPlanForTest(t, rt, 0)
	rt.settlementResultPlan = &plan
	want := errors.New("tower result write failed")
	conn := &failNthDungeonWriteConn{failAt: 4, err: want}
	session.conn = conn
	request := make([]byte, currentDungeonPlayResultBaseSize)
	if err := s.handleDungeonSetPlayResult(session, request); !errors.Is(err, want) {
		t.Fatalf("first err=%v", err)
	}
	if rt.settlementDespairResultSent || !rt.settlementCharacterStateSent || !rt.settlementClearRewardSent {
		t.Fatal("failed write advanced result flag")
	}
	if err := s.handleDungeonSetPlayResult(session, request); err != nil {
		t.Fatal(err)
	}
	op34, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	op37, rest := splitGameServerUpperPacket(t, rest)
	op35, rest := splitGameServerUpperPacket(t, rest)
	op348, rest := splitGameServerUpperPacket(t, rest)
	if op34.Header.MsgID != 34 || op37.Header.MsgID != 37 || op35.Header.MsgID != 35 || op348.Header.MsgID != 348 || op348.Header.Classification != 0 || !bytes.Equal(op348.Body, []byte{0x39, 0x30, 0, 0, 1, 0, 0}) || len(rest) != 0 {
		t.Fatal("incorrect tower result projection")
	}
	size := conn.write.Len()
	if err := s.handleDungeonSetPlayResult(session, request); err != nil || conn.write.Len() != size {
		t.Fatal("completed result replay resent/failed")
	}
}

func TestRealPVFDespairSelectionUsesSavedFloor(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires fixed published PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	for floor := 1; floor <= 100; floor++ {
		d, found := table.FindDungeon(int64(11007 + floor))
		got, err := currentDespairFloor(d)
		if !found || err != nil || got != floor {
			t.Fatalf("floor %d found=%t got=%d err=%v", floor, found, got, err)
		}
	}
	repos := memory.NewMemoryGroup()
	s := &Service{worldMapTable: table, worldMapResolver: resolver, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 99, accountID: "tower-account"}
	for _, row := range []struct {
		highest int64
		want    uint32
	}{{0, 11008}, {1, 11009}, {2, 11010}, {99, 11107}, {100, 11107}} {
		c := dnfrepo.CharacterRecord{CharacterID: "99", AccountID: session.accountID, Stats: map[string]int64{dnfdungeon.DespairHighestClearedStatKey: row.highest}}
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		for _, requested := range []uint32{11008, 11009, 11107} {
			got, err := s.resolveCurrentDespairSelection(session, requested)
			if err != nil || got != row.want {
				t.Fatalf("highest=%d request=%d got=%d err=%v", row.highest, requested, got, err)
			}
		}
	}
	if got, err := s.resolveCurrentDespairSelection(session, 3); err != nil || got != 3 {
		t.Fatal("ordinary dungeon redirected")
	}
	d, _ := table.FindDungeonPath(fmt.Sprintf("dungeon/towers/ztowerofdespair%03d.dgn", 2))
	topology, err := worldmap.BuildDungeonLayout(resolver, d.ID, 0, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	if topology.Start == nil {
		t.Fatal("second floor has no start room")
	}
	room, found := topology.Room(*topology.Start)
	if !found || room.Map == nil || room.Map.Map.ID != 15131 {
		t.Fatalf("second floor start room=%+v", room)
	}
}

func TestDespairProducerPersistsFloorAndSelectorReadsCommittedProgress(t *testing.T) {
	s, rt, session, conn, _ := prepareCompletedSettlementRuntime(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	rt.Dungeon.Path = "dungeon/towers/ztowerofdespair001.dgn"
	rt.Dungeon.Metadata.BasisLevel = worldmap.OptionalInt{Set: true, Value: 1}
	rt.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
	rt.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 1
	repos, _ := s.repositoryGroup()
	character, _, _ := repos.Character.Load(context.Background(), "99")
	character.Level = 1
	character.Stats["exp"] = 90
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	rt.Character = character
	rt.settlementPlayResultReceived = true
	rt.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
	rt.startedAt = time.Unix(1700000000, 0)
	rt.clearMapCompletionAt = rt.startedAt.Add(12345 * time.Millisecond)
	resources := currentDungeonSettlementTestResources(t)
	rt.settlementDespairRewards = &dnfdungeon.DespairRewards{}
	if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(context.Background(), session, rt, resources); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := repos.Character.Load(context.Background(), "99")
	if stored.Stats[dnfdungeon.DespairHighestClearedStatKey] != 1 {
		t.Fatal("floor not persisted")
	}
	if got := rt.settlementResultPlan.DespairResultBody; !bytes.Equal(got, []byte{0x39, 0x30, 0, 0, 1, 0, 0}) {
		t.Fatalf("body=%x", got)
	}
	firstExp := stored.Stats["exp"]
	rt.settlementResultPlan = nil
	if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(context.Background(), session, rt, resources); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = repos.Character.Load(context.Background(), "99")
	if stored.Stats[dnfdungeon.DespairHighestClearedStatKey] != 1 || stored.Stats["exp"] != firstExp {
		t.Fatal("producer retry awarded twice")
	}
	// Simulate a new selector connection: no dungeon runtime/cache to reuse.
	fresh := &gameSession{selectedCharacterID: 99, conn: conn}
	conn.write.Reset()
	if err := s.sendCurrentDungeonContextOp27(fresh, "tower_test_reconnect"); err != nil {
		t.Fatal(err)
	}
	op27, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if op27.Header.MsgID != 27 || len(rest) != 0 || binary.LittleEndian.Uint16(op27.Body[15:17]) != 2 {
		t.Fatalf("selector=%x", op27.Body)
	}
}

func TestDespairFloorRequiresResourceMarkerAndExactPath(t *testing.T) {
	for _, row := range []struct {
		path    string
		marker  int64
		want    int
		invalid bool
	}{
		{"dungeon/towers/ztowerofdespair001.dgn", 1, 1, false},
		{"dungeon/towers/ZTowerOfDespair100.dgn", 1, 100, false},
		{"dungeon/towers/ztowerofdespair000.dgn", 1, 0, true},
		{"dungeon/towers/sao/towerofdespair001.dgn", 1, 0, true},
		{"dungeon/towers/grief/5115_001.dgn", 0, 0, false},
		{"dungeon/towers/ztowerofdespair001.dgn", 0, 0, false},
	} {
		d := worldmap.Dungeon{Path: row.path}
		d.Metadata.IntegerValues = map[string]int64{"tower of despair": row.marker}
		floor, err := currentDespairFloor(d)
		if (err != nil) != row.invalid || floor != row.want {
			t.Fatalf("%s floor=%d err=%v", row.path, floor, err)
		}
	}
}
