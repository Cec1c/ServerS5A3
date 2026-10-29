package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// official-24.9 的首次入场 op5 在无持久记录时曾默认发 state 1，提前覆盖了
// 原生副本缓存；首次普通通关也停在 state 1，因此不会产生递增弹窗。
// 这里固定入口权限的 authority 边界：official 只用持久记录决定 state，
// 新角色/无可用难度上限时为 0；其它 profile 保持旧的 state 1 默认值。

const authorityHighDungeonID = 7145
const authorityLowDungeonID = 3

func authorityDungeonFile(metadata string) string {
	return "[name]\n`Synthetic Dungeon`\n[minimum required level]\n10\n[basis level]\n20\n[limit party count]\n1\n" + metadata + "[maze info]\n[size]\n1 1\n[greed]\n`A`\n[map specification]\n`map` 0 0 100\n[start map]\n0 0\n[boss map]\n0 0\n"
}
func authorityStartMap(id int) string {
	return fmt.Sprintf("[map name]\n`start`\n[dungeon]\n%d\n[type]\n`[start]`\n", id)
}
func authorityBasePermissionPVF() bridgePVFSource {
	source := bridgeDungeonPVF(false)
	source[worldmap.DefaultDungeonList] += "7145 `authhigh.dgn`\n3 `authlow.dgn`\n"
	source[worldmap.DefaultMapList] += "101 `dungeon/authhigh/start.map`\n102 `dungeon/authlow/start.map`\n"
	source["dungeon/authhigh.dgn"] = authorityDungeonFile("[difficulty level]\n1 2 3 4 5\n")
	source["dungeon/authlow.dgn"] = authorityDungeonFile("[difficulty level]\n1\n")
	source["map/dungeon/authhigh/start.map"] = authorityStartMap(authorityHighDungeonID)
	source["map/dungeon/authlow/start.map"] = authorityStartMap(authorityLowDungeonID)
	return source
}

const authorityEntryCeilingTwoDungeonID = 7102

func authorityEntryPermissionPVF() bridgePVFSource {
	source := authorityBasePermissionPVF()
	// 700 改成 2 个非零难度 => maxState 1。
	source["dungeon/test.dgn"] = authorityDungeonFile("[difficulty level]\n0 1 2\n")
	// 7145（round6 fixture）= 5 个非零难度 => maxState 4。
	// 3（round6 fixture）= 单难度 => maxState 0。
	source[worldmap.DefaultDungeonList] = source[worldmap.DefaultDungeonList] +
		fmt.Sprintf("%d `auth2.dgn`\n", authorityEntryCeilingTwoDungeonID)
	source[worldmap.DefaultMapList] = source[worldmap.DefaultMapList] +
		"104 `dungeon/auth2/start.map`\n"
	source["dungeon/auth2.dgn"] = authorityDungeonFile("[difficulty level]\n1 2 3\n")
	source["map/dungeon/auth2/start.map"] = authorityStartMap(authorityEntryCeilingTwoDungeonID)
	return source
}

func authorityEntryDungeon(t *testing.T, dungeonID int64) worldmap.Dungeon {
	t.Helper()
	table, _, _ := loadBridgeDungeonStaticData(t, authorityEntryPermissionPVF())
	dungeon, found := table.FindDungeon(dungeonID)
	if !found {
		t.Fatalf("synthetic dungeon %d missing from table", dungeonID)
	}
	return dungeon
}

type authorityEntryLoadFailure struct {
	dnfrepo.DungeonPermissionRepository
}

func (authorityEntryLoadFailure) Load(context.Context, string) (dnfrepo.DungeonPermissionRecord, bool, error) {
	return dnfrepo.DungeonPermissionRecord{}, false, errors.New("permission read unavailable")
}

func newAuthorityEntryService(profile string, repositories dnfrepo.Group) (*Service, *gameSession) {
	// 共享难度按账号枚举角色，夹具也保留真实角色归属。
	if repositories.Character != nil {
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "99", AccountID: defaultAccountPrefix + "1"}); err != nil {
			panic(err)
		}
	}
	service := &Service{
		options:            options{currentClientVersion: profile},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99}
	return service, session
}

func assertAuthorityEntryState(t *testing.T, data []byte, dungeonID uint32, wantState byte) {
	t.Helper()
	packet, rest := splitGameServerUpperPacket(t, data)
	want := buildCurrentDungeonResourceStateEntriesBody(
		[]currentDungeonResourceStateEntry{{DungeonID: dungeonID, State: wantState}},
	)
	if packet.Header.Classification != 0 || packet.Header.MsgID != currentDungeonResourceStateMsgID ||
		len(rest) != 0 || !bytes.Equal(packet.Body, want) {
		t.Fatalf("op5 header=%+v body=%x want state %d (%x) trailing=%x",
			packet.Header, packet.Body, wantState, want, rest)
	}
}

func TestOfficial249DungeonEntryWithoutRecordSendsStateZero(t *testing.T) {
	repositories := testRepositoryGroup()
	service, session := newAuthorityEntryService("official-24.9", repositories)
	dungeon := authorityEntryDungeon(t, authorityHighDungeonID)
	if ceiling := currentDungeonMaxClearState(dungeon); ceiling != 4 {
		t.Fatalf("fixture dungeon %d ceiling=%d want 4", authorityHighDungeonID, ceiling)
	}

	if err := service.sendCurrentDungeonResourceState(
		session,
		&runtimeDungeonState{Dungeon: dungeon},
		"authority-entry-test",
	); err != nil {
		t.Fatal(err)
	}
	assertAuthorityEntryState(t, session.conn.(*bufferConn).write.Bytes(), uint32(authorityHighDungeonID), 0)
}

func TestOfficial249DungeonEntryKeepsPersistedStateUpToPVFCeiling(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		dungeonID int64
		ceiling   byte
		saved     byte
		want      byte
	}{
		{name: "state one", dungeonID: 700, ceiling: 1, saved: 1, want: 1},
		{name: "state two", dungeonID: authorityEntryCeilingTwoDungeonID, ceiling: 2, saved: 2, want: 2},
		{name: "state four", dungeonID: authorityHighDungeonID, ceiling: 4, saved: 4, want: 4},
		{name: "saved state capped by PVF", dungeonID: authorityEntryCeilingTwoDungeonID, ceiling: 2, saved: 4, want: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repositories := testRepositoryGroup()
			if _, _, err := repositories.DungeonPermission.UpsertMax(
				context.Background(), "99", uint32(testCase.dungeonID), testCase.saved,
			); err != nil {
				t.Fatal(err)
			}
			service, session := newAuthorityEntryService("official-24.9", repositories)
			dungeon := authorityEntryDungeon(t, testCase.dungeonID)
			if ceiling := currentDungeonMaxClearState(dungeon); ceiling != testCase.ceiling {
				t.Fatalf("fixture dungeon %d ceiling=%d want %d", testCase.dungeonID, ceiling, testCase.ceiling)
			}

			if err := service.sendCurrentDungeonResourceState(
				session,
				&runtimeDungeonState{Dungeon: dungeon},
				"authority-entry-test",
			); err != nil {
				t.Fatal(err)
			}
			assertAuthorityEntryState(t, session.conn.(*bufferConn).write.Bytes(), uint32(testCase.dungeonID), testCase.want)
		})
	}
}

func TestOfficial249DungeonEntrySingleDifficultySendsStateZero(t *testing.T) {
	repositories := testRepositoryGroup()
	if _, _, err := repositories.DungeonPermission.UpsertMax(
		context.Background(), "99", uint32(authorityLowDungeonID), 4,
	); err != nil {
		t.Fatal(err)
	}
	service, session := newAuthorityEntryService("official-24.9", repositories)
	dungeon := authorityEntryDungeon(t, authorityLowDungeonID)
	if ceiling := currentDungeonMaxClearState(dungeon); ceiling != 0 {
		t.Fatalf("fixture dungeon %d ceiling=%d want 0", authorityLowDungeonID, ceiling)
	}

	if err := service.sendCurrentDungeonResourceState(
		session,
		&runtimeDungeonState{Dungeon: dungeon},
		"authority-entry-test",
	); err != nil {
		t.Fatal(err)
	}
	assertAuthorityEntryState(t, session.conn.(*bufferConn).write.Bytes(), uint32(authorityLowDungeonID), 0)
}

func TestDungeonEntryOtherProfilesKeepLegacyStateOne(t *testing.T) {
	dungeon := authorityEntryDungeon(t, authorityHighDungeonID)
	for _, profile := range []string{"", "legacy"} {
		t.Run("profile="+profile, func(t *testing.T) {
			repositories := testRepositoryGroup()
			service, session := newAuthorityEntryService(profile, repositories)
			if err := service.sendCurrentDungeonResourceState(
				session,
				&runtimeDungeonState{Dungeon: dungeon},
				"authority-entry-test",
			); err != nil {
				t.Fatal(err)
			}
			assertAuthorityEntryState(t, session.conn.(*bufferConn).write.Bytes(), uint32(authorityHighDungeonID), 1)
		})
	}
}

func TestOfficial249DungeonEntryPermissionLoadFailureSendsNothing(t *testing.T) {
	repositories := testRepositoryGroup()
	repositories.DungeonPermission = authorityEntryLoadFailure{
		DungeonPermissionRepository: repositories.DungeonPermission,
	}
	service, session := newAuthorityEntryService("official-24.9", repositories)
	dungeon := authorityEntryDungeon(t, authorityHighDungeonID)

	if err := service.sendCurrentDungeonResourceState(
		session,
		&runtimeDungeonState{Dungeon: dungeon},
		"authority-entry-test",
	); err != nil {
		t.Fatalf("permission load failure must stay silent, got err=%v", err)
	}
	if data := session.conn.(*bufferConn).write.Bytes(); len(data) != 0 {
		t.Fatalf("permission load failure sent op5: %x", data)
	}
}
