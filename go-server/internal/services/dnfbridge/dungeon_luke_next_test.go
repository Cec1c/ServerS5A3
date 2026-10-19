package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func seekerLukeNextSequenceText(dungeons []uint32, autoEnter bool) string {
	ids := make([]string, len(dungeons))
	for index, id := range dungeons {
		ids[index] = fmt.Sprint(id)
	}
	marker := ""
	if autoEnter {
		marker = "[next dungeon auto enter]\n"
	}
	return "[sequential dungeon]\n28\n" +
		"[dungeon index check]\n243 244\n" +
		"[/sequential dungeon]\n" +
		"[sequential dungeon]\n42\n" +
		"[dungeon index check]\n" + strings.Join(ids, " ") + "\n" +
		marker +
		"[monster index check]\n3001\n" +
		"[/sequential dungeon]\n"
}

func seekerLukeNextSource(dungeonIDs, sequence []uint32, autoEnter bool) bridgePVFSource {
	source := bridgeDungeonPVF(true)
	mapRows := make([]string, 0, len(dungeonIDs))
	dungeonRows := make([]string, 0, len(dungeonIDs))
	worldRows := make([]string, 0, len(dungeonIDs))
	for _, id := range dungeonIDs {
		mapID := 56000 + int(id)
		mapPath := fmt.Sprintf("map/luke_normal/%d.map", mapID)
		dungeonPath := fmt.Sprintf("luke_normal/%d.dgn", id)
		if id == 5106 {
			dungeonPath = "luke_normal/solium.dgn"
		}
		mapRows = append(mapRows, fmt.Sprintf("%d `%s`", mapID, mapPath))
		dungeonRows = append(dungeonRows, fmt.Sprintf("%d `%s`", id, dungeonPath))
		worldRows = append(worldRows, fmt.Sprintf("%d -1", id))
		source[mapPath] = "[map name]\n`Synthetic Luke Room`\n" +
			fmt.Sprintf("[dungeon]\n%d\n", id) +
			"[type]\n`[start]`\n" +
			"[monster]\n3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n"
		source["dungeon/"+dungeonPath] = "[name]\n`Synthetic Luke Dungeon`\n" +
			"[minimum required level]\n1\n" +
			"[basis level]\n20\n" +
			"[no fatigue]\n" +
			"[limit party count]\n4\n" +
			"[maze info]\n" +
			"[size]\n1 1\n" +
			"[greed]\n`A`\n" +
			fmt.Sprintf("[map specification]\n`map` 0 0 %d\n", mapID) +
			"[start map]\n0 0\n" +
			"[boss map]\n0 0\n"
	}
	source[worldmap.DefaultMapList] = strings.Join(mapRows, "\n") + "\n"
	source[worldmap.DefaultDungeonList] = strings.Join(dungeonRows, "\n") + "\n"
	source[worldmap.DefaultWorldMapList] = "1 `test.wdm`\n"
	source["worldmap/test.wdm"] = "[name]\n`Synthetic Luke Area`\n" +
		"[dungeon]\n" + strings.Join(worldRows, "\n") + "\n[/dungeon]\n"
	source["etc/sequential_dungeon_info.etc"] = seekerLukeNextSequenceText(sequence, autoEnter)
	return source
}

type seekerLukeNextFixture struct {
	service *Service
	repos   dnfrepo.Group
	session *gameSession
	conn    *bufferConn
	source  bridgePVFSource
}

func newSeekerLukeNextFixture(t *testing.T, source bridgePVFSource) *seekerLukeNextFixture {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 90,
		Stats: map[string]int64{
			"fatigue": 500, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options: options{
			accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters,
		dungeonRuleSource: source,
		dungeonChoice:     func(int) (int, error) { return 0, nil },
		dungeonSeed:       func() (uint32, error) { return 1, nil },
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		},
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn: conn, connID: "qst027-next-unit",
		selectedCharacterID: 99, accountID: "account-1",
	}
	return &seekerLukeNextFixture{
		service: service, repos: repos, session: session, conn: conn, source: source,
	}
}

func (f *seekerLukeNextFixture) enterDungeon(t *testing.T, dungeonID uint32, difficulty byte) *runtimeDungeonState {
	t.Helper()
	runtime, _, err := f.service.prepareDungeonRuntime(
		context.Background(),
		f.session,
		dungeoncmd.SelectDungeonRequest{DungeonID: dungeonID, Difficulty: difficulty},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.session.dungeon.runtime = runtime
	f.session.townSceneReadyCharacterID = 99
	f.session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID: 99, TownID: 38, AreaID: 1,
		PositionX: 450, PositionY: 234, MovementCode: 5, PositionValid: true,
	}
	if _, ok := bindCurrentTownSelectorOrigin(f.session); !ok {
		t.Fatal("town origin")
	}
	if err := freezeCurrentDungeonTownReturnOrigin(f.session, runtime); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func (f *seekerLukeNextFixture) completeCurrent(t *testing.T) {
	t.Helper()
	completePartyRetryFixture(t, f.service, f.session)
}

func (f *seekerLukeNextFixture) clickRetry(t *testing.T) {
	t.Helper()
	if err := f.service.handleGameCommand(
		f.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketEplpCommand),
		[]byte{1, 0},
	); err != nil {
		t.Fatal(err)
	}
}

func seekerLukeNextPacketIDs(t *testing.T, stream []byte) []uint16 {
	t.Helper()
	var ids []uint16
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		ids = append(ids, packet.Header.MsgID)
		stream = rest
	}
	return ids
}

func TestSeekerLukeNextDungeonSequenceAdvancesAndRetriesFinal(t *testing.T) {
	ordered := []uint32{5101, 5102, 5103, 5104, 5105, 5106}
	f := newSeekerLukeNextFixture(t, seekerLukeNextSource(ordered, ordered, true))
	f.enterDungeon(t, ordered[0], 2)
	for index, current := range ordered {
		f.completeCurrent(t)
		if _, _, err := f.repos.DungeonPermission.UpsertMax(context.Background(), "99", current, 1); err != nil {
			t.Fatal(err)
		}
		f.conn.write.Reset()
		f.clickRetry(t)
		next := f.session.dungeon.runtime
		if next == nil || next.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatalf("dungeon %d did not create an active next run", current)
		}
		want := current
		reason := "final_dungeon_same_dungeon_retry"
		if index+1 < len(ordered) {
			want = ordered[index+1]
			reason = "sequence_next_dungeon"
			ids := seekerLukeNextPacketIDs(t, f.conn.write.Bytes())
			for _, msgID := range []uint16{
				uint16(dnfenum.CmdPacketEplpCommand),
				uint16(dnfenum.CmdPacketSelectDungeon),
				uint16(currentDungeonInfoNotification),
				uint16(currentDungeonStartNotification),
			} {
				found := false
				for _, got := range ids {
					found = found || got == msgID
				}
				if !found {
					t.Fatalf("%s for %d missing op%d packet ids=%v", reason, current, msgID, ids)
				}
			}
		}
		if uint32(next.Dungeon.ID) != want || next.Request.Difficulty != 2 {
			t.Fatalf("%d -> dungeon=%d difficulty=%d want %d/2", current, next.Dungeon.ID, next.Request.Difficulty, want)
		}
	}
}

func TestSeekerLukeNextDungeonSequenceErrorsKeepCompletedRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		worldIDs  []uint32
		sequence  []uint32
		autoEnter bool
	}{
		{
			name: "target_missing", worldIDs: []uint32{5101},
			sequence: []uint32{5101, 5107}, autoEnter: true,
		},
		{
			name: "current_missing", worldIDs: []uint32{5101, 5102},
			sequence: []uint32{5102}, autoEnter: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeekerLukeNextFixture(t, seekerLukeNextSource(tc.worldIDs, tc.sequence, tc.autoEnter))
			completed := f.enterDungeon(t, 5101, 2)
			f.completeCurrent(t)
			beforeCharacter, _, err := f.repos.Character.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			beforeInventory, _, err := f.repos.Inventory.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			f.conn.write.Reset()
			f.clickRetry(t)
			if f.session.dungeon.runtime != completed {
				t.Fatal("sequence error retired the completed run")
			}
			if completed.settlementCardExitAckSent {
				t.Fatal("sequence error did not re-arm the settlement action")
			}
			afterCharacter, _, err := f.repos.Character.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			afterInventory, _, err := f.repos.Inventory.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
				t.Fatal("sequence error consumed character or inventory assets")
			}
		})
	}
}

func TestSeekerLukeNextDungeonAutoEnterDisabledRetriesSameDungeon(t *testing.T) {
	ordered := []uint32{5101, 5102}
	f := newSeekerLukeNextFixture(t, seekerLukeNextSource(ordered, ordered, false))
	old := f.enterDungeon(t, 5101, 2)
	f.completeCurrent(t)
	f.clickRetry(t)
	next := f.session.dungeon.runtime
	if next == nil || next == old || next.Dungeon.ID != 5101 || next.Request.Difficulty != 2 {
		t.Fatalf("auto-enter disabled runtime=%v want fresh dungeon 5101 difficulty 2", next)
	}
	if next.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatalf("same-dungeon retry status=%v want active", next.Session.Snapshot().Run.Status)
	}
}

func TestSeekerLukeNextDungeonDoesNotAffectNonLukeRetry(t *testing.T) {
	source := bridgeDungeonPVF(false)
	source["etc/sequential_dungeon_info.etc"] = seekerLukeNextSequenceText([]uint32{700, 701}, false)
	f := newSeekerLukeNextFixture(t, source)
	old := f.enterDungeon(t, 700, 1)
	f.completeCurrent(t)
	f.clickRetry(t)
	next := f.session.dungeon.runtime
	if next == nil || next == old || next.Dungeon.ID != 700 {
		t.Fatalf("non-Luke retry changed: old=%p next=%p", old, next)
	}
}

func TestSeekerLukeNextDungeonThroneStillRequiresFiveSequenceClears(t *testing.T) {
	ordered := []uint32{5101, 5102, 5103, 5104, 5105, 5106}
	// Manual Mechanical Throne admission is sequence-order based; it must not
	// inherit the next-dungeon auto-enter marker.
	f := newSeekerLukeNextFixture(t, seekerLukeNextSource(ordered, ordered, false))
	throne, ok := f.service.worldMapTable.FindDungeon(5106)
	if !ok {
		t.Fatal("synthetic throne missing")
	}
	for index, id := range ordered[:5] {
		if err := f.service.validateCurrentLukeNormalThroneEntry(context.Background(), f.session, throne); err == nil {
			t.Fatalf("throne admitted after only %d clears", index)
		}
		if _, _, err := f.repos.DungeonPermission.UpsertMax(context.Background(), "99", id, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.validateCurrentLukeNormalThroneEntry(context.Background(), f.session, throne); err != nil {
		t.Fatalf("five complete prerequisites rejected: %v", err)
	}
}

func seekerLukeNextPartyFixture(t *testing.T, autoEnter bool) (*Service, *gameSession, *gameSession) {
	t.Helper()
	service, leader, follower, _, _, _ := partyDungeonSyncFixture(t)
	ordered := []uint32{5101, 5102}
	source := seekerLukeNextSource(ordered, ordered, autoEnter)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	service.worldMapTable, service.worldMapResolver = table, resolver
	service.dungeonMonsterTable, service.dungeonRuleSource = monsters, source
	return service, leader, follower
}

func runSeekerLukeNextPartyEntry(t *testing.T, service *Service, leader *gameSession, dungeonID uint32) {
	t.Helper()
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[:4], dungeonID)
	if err := service.handleDungeonSelectUpper(leader, body); err != nil {
		t.Fatal(err)
	}
}

func TestSeekerLukeNextDungeonPartyUsesOneLeaderOwnedTarget(t *testing.T) {
	service, leader, follower := seekerLukeNextPartyFixture(t, true)
	runSeekerLukeNextPartyEntry(t, service, leader, 5101)
	completePartyRetryFixture(t, service, leader)
	completePartyRetryFixture(t, service, follower)
	if err := service.handleGameCommand(
		leader,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketEplpCommand),
		[]byte{1, 0},
	); err != nil {
		t.Fatal(err)
	}
	lr, fr := leader.dungeon.runtime, follower.dungeon.runtime
	if lr == nil || fr == nil || lr.Dungeon.ID != 5102 || fr.Dungeon.ID != 5102 {
		t.Fatalf("party next dungeons leader=%v follower=%v", lr, fr)
	}
	if lr.partyInstance == nil || lr.partyInstance != fr.partyInstance {
		t.Fatal("party next dungeon did not share one fresh instance")
	}
}

func TestSeekerLukeNextDungeonPartyFollowerAndRepeatedClicksDoNotDuplicateEntry(t *testing.T) {
	t.Run("follower", func(t *testing.T) {
		service, leader, follower := seekerLukeNextPartyFixture(t, true)
		runSeekerLukeNextPartyEntry(t, service, leader, 5101)
		completePartyRetryFixture(t, service, leader)
		completePartyRetryFixture(t, service, follower)
		oldLeader, oldFollower := leader.dungeon.runtime, follower.dungeon.runtime
		if err := service.handleGameCommand(
			follower,
			byte(dnfenum.GameCmdCommand),
			uint16(dnfenum.CmdPacketEplpCommand),
			[]byte{1, 0},
		); err != nil {
			t.Fatal(err)
		}
		if leader.dungeon.runtime != oldLeader || follower.dungeon.runtime != oldFollower {
			t.Fatal("party follower click created or retired a run")
		}
	})
	t.Run("repeated_leader", func(t *testing.T) {
		service, leader, follower := seekerLukeNextPartyFixture(t, true)
		runSeekerLukeNextPartyEntry(t, service, leader, 5101)
		completePartyRetryFixture(t, service, leader)
		completePartyRetryFixture(t, service, follower)
		if err := service.handleGameCommand(
			leader,
			byte(dnfenum.GameCmdCommand),
			uint16(dnfenum.CmdPacketEplpCommand),
			[]byte{1, 0},
		); err != nil {
			t.Fatal(err)
		}
		enteredLeader, enteredFollower := leader.dungeon.runtime, follower.dungeon.runtime
		if err := service.handleGameCommand(
			leader,
			byte(dnfenum.GameCmdCommand),
			uint16(dnfenum.CmdPacketEplpCommand),
			[]byte{1, 0},
		); err != nil {
			t.Fatal(err)
		}
		if leader.dungeon.runtime != enteredLeader || follower.dungeon.runtime != enteredFollower {
			t.Fatal("repeated click created a second party run")
		}
	})
}

func TestSeekerLukeNormalSequenceParserRejectsMalformedApprovedData(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dungeons  []uint32
		autoEnter bool
		want      []uint32
		wantAuto  bool
		wantErr   bool
	}{
		{name: "approved", dungeons: []uint32{5101, 5102, 5106}, autoEnter: true, want: []uint32{5101, 5102, 5106}, wantAuto: true},
		{name: "missing_marker", dungeons: []uint32{5101, 5102}, want: []uint32{5101, 5102}},
		{name: "duplicate", dungeons: []uint32{5101, 5101}, autoEnter: true, wantErr: true},
		{name: "empty", autoEnter: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := bridgePVFSource{
				"etc/sequential_dungeon_info.etc": seekerLukeNextSequenceText(tc.dungeons, tc.autoEnter),
			}
			sequence, err := loadCurrentLukeNormalSequence(source)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted malformed sequence: %+v", sequence)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sequence.Dungeons, tc.want) {
				t.Fatalf("sequence=%v want=%v", sequence.Dungeons, tc.want)
			}
			if sequence.AutoEnter != tc.wantAuto {
				t.Fatalf("auto-enter=%t want=%t", sequence.AutoEnter, tc.wantAuto)
			}
		})
	}

	t.Run("duplicate_complete_blocks", func(t *testing.T) {
		text := seekerLukeNextSequenceText([]uint32{5101, 5102}, true) +
			"\n[sequential dungeon]\n42\n" +
			"[dungeon index check]\n5101 5102\n" +
			"[next dungeon auto enter]\n" +
			"[/sequential dungeon]\n"
		source := bridgePVFSource{"etc/sequential_dungeon_info.etc": text}
		if sequence, err := loadCurrentLukeNormalSequence(source); err == nil {
			t.Fatalf("accepted two complete Luke 42 blocks: %+v", sequence)
		}
	})
}
