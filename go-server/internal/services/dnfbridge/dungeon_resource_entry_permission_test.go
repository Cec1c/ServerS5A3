package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type entryPermissionFailure struct {
	dnfrepo.DungeonPermissionRepository
	err error
}

func (p entryPermissionFailure) Load(context.Context, string) (dnfrepo.DungeonPermissionRecord, bool, error) {
	return dnfrepo.DungeonPermissionRecord{}, false, p.err
}

func TestDungeonEntryResourceStatePreservesSavedDifficulty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		saved  byte
		levels []int64
		want   byte
	}{
		{"king preserved", 4, []int64{1, 2, 3, 4, 5}, 4},
		{"saved state capped by PVF", 4, []int64{1, 2, 3}, 2},
		{"no permission keeps entry state", 0, []int64{1, 2, 3, 4, 5}, 1},
		{"single difficulty keeps entry state", 4, []int64{1}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := testRepositoryGroup()
			for _, character := range []dnfrepo.CharacterRecord{{CharacterID: "99", AccountID: defaultAccountPrefix + "1"}, {CharacterID: "98", AccountID: "other-account"}} {
				if err := repos.Character.Save(context.Background(), character); err != nil {
					t.Fatal(err)
				}
			}
			if tc.saved > 0 {
				if _, _, err := repos.DungeonPermission.UpsertMax(context.Background(), "99", 700, tc.saved); err != nil {
					t.Fatal(err)
				}
			}
			// 其他账号角色的较高记录不能被共享。
			if _, _, err := repos.DungeonPermission.UpsertMax(context.Background(), "98", 700, 4); err != nil {
				t.Fatal(err)
			}
			before, _, err := repos.DungeonPermission.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 99}
			rt := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 700, Metadata: worldmap.DungeonMetadata{DifficultyLevels: tc.levels}}}
			if err := s.sendCurrentDungeonResourceState(session, rt, "entry-permission-test"); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			want := buildCurrentDungeonResourceStateEntriesBody([]currentDungeonResourceStateEntry{{DungeonID: 700, State: tc.want}})
			if packet.Header.MsgID != currentDungeonResourceStateMsgID || packet.Header.Classification != 0 || len(rest) != 0 || !bytes.Equal(packet.Body, want) {
				t.Fatalf("entry packet=%+v body=%x want=%x trailing=%x", packet.Header, packet.Body, want, rest)
			}
			after, _, err := repos.DungeonPermission.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("entry changed persisted permission")
			}
		})
	}
}

func TestDungeonEntryResourceStateDoesNotDowngradeOnRepositoryFailure(t *testing.T) {
	repos := testRepositoryGroup()
	want := errors.New("permission read unavailable")
	repos.DungeonPermission = entryPermissionFailure{DungeonPermissionRepository: repos.DungeonPermission, err: want}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99}
	rt := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 700, Metadata: worldmap.DungeonMetadata{DifficultyLevels: []int64{1, 2, 3, 4, 5}}}}
	if err := s.sendCurrentDungeonResourceState(session, rt, "entry-permission-test"); err != nil {
		t.Fatalf("err=%v want=%v", err, want)
	}
	if conn.write.Len() != 0 {
		t.Fatal("failed permission read sent a potentially lower state")
	}
}
