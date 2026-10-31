package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestAccountDifficultySelectCharacterBAfterAClear(t *testing.T) {
	ctx := context.Background()
	table, resolver, _ := loadBridgeDungeonStaticData(t, authorityEntryPermissionPVF())
	repos := testRepositoryGroup()
	account := defaultAccountPrefix + "1"
	for i, id := range []string{"11", "12"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: account, Slot: i, Name: "difficulty-owner", Job: "0", Level: 90,
			Stats: map[string]int64{"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3, currentDungeonTutorialCompletedKey: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{worldMapTable: table, worldMapResolver: resolver, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	dungeon, found := table.FindDungeon(700)
	if !found {
		t.Fatal("夹具缺少副本")
	}
	if _, state, _, err := s.commitCurrentDungeonPermission(ctx, &gameSession{selectedCharacterID: 11, accountID: account}, &runtimeDungeonState{Dungeon: dungeon}); err != nil || state != 1 {
		t.Fatalf("角色A普通通关 state=%d err=%v", state, err)
	}
	conn := &bufferConn{}
	b := &gameSession{conn: conn, accountID: account, rosterRequested: true}
	request := binary.LittleEndian.AppendUint16(nil, 12)
	if err := s.sendUpperCSharpSelectInit(b, request); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	permission, _ := splitGameServerUpperPacket(t, rest)
	want := buildCurrentDungeonResourceStateEntriesBody([]currentDungeonResourceStateEntry{{DungeonID: 700, State: 1}})
	if ack.Header.MsgID != 4 || permission.Header.Classification != 0 || permission.Header.MsgID != 5 || !bytes.Equal(permission.Body, want) {
		t.Fatalf("角色B选角后的难度权限 ack=%+v permission=%+v body=%x", ack.Header, permission.Header, permission.Body)
	}
}

func TestAccountDifficultyClearCharacterASwitchBAndPreserveLegacy(t *testing.T) {
	ctx := context.Background()
	repos := testRepositoryGroup()
	for _, c := range []dnfrepo.CharacterRecord{{CharacterID: "11", AccountID: "same", Slot: 0}, {CharacterID: "12", AccountID: "same", Slot: 1}, {CharacterID: "21", AccountID: "other", Slot: 0}} {
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	a := &gameSession{selectedCharacterID: 11, accountID: "same"}
	b := &gameSession{selectedCharacterID: 12, accountID: "same"}
	rt := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 700, Metadata: worldmap.DungeonMetadata{DifficultyLevels: []int64{1, 1, 1, 1, 1}}}}
	if _, state, changed, err := s.commitCurrentDungeonPermission(ctx, a, rt); err != nil || !changed || state != 1 {
		t.Fatalf("A普通通关=%d changed=%t err=%v", state, changed, err)
	}
	record, found, err := s.currentAccountDungeonPermissionSnapshot(ctx, b, repos)
	if err != nil || !found || len(record.Entries) != 1 || record.Entries[0].ClearState != 1 {
		t.Fatal("B未继承冒险难度", record, err)
	}
	legacy, found, err := repos.DungeonPermission.Load(ctx, "11")
	if err != nil || !found || legacy.Entries[0].ClearState != 1 {
		t.Fatal("个人通关记录丢失")
	}
	higher := &runtimeDungeonState{Dungeon: rt.Dungeon}
	higher.Request.Difficulty = 2
	if _, state, changed, err := s.commitCurrentDungeonPermission(ctx, b, higher); err != nil || !changed || state != 3 {
		t.Fatal(err)
	}
	record, _, err = s.currentAccountDungeonPermissionSnapshot(ctx, a, repos)
	if err != nil || record.Entries[0].ClearState != 3 {
		t.Fatal("未取账号最高进度")
	}
	if _, state, _, err := s.commitCurrentDungeonPermission(ctx, a, rt); err != nil || state != 3 {
		t.Fatal("较低通关覆盖最高进度", state, err)
	}
	other := &gameSession{selectedCharacterID: 21, accountID: "other"}
	if record, found, err := s.currentAccountDungeonPermissionSnapshot(ctx, other, repos); err != nil || found || len(record.Entries) != 0 {
		t.Fatal("进度跨账号共享", record, err)
	}
}
