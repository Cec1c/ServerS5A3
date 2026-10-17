package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Replay actual entry and clear permission producers, then recreate both the
// service/session and repository group from its saved permission rows. This
// proves read-side projection after reconnect, independently of session state.
func permissionLifecycle(t *testing.T, table *worldmap.Table, dungeon worldmap.Dungeon) {
	t.Helper()
	ctx := context.Background()
	repos := testRepositoryGroup()
	s, session := newAuthorityEntryService("official-24.9", repos)
	s.worldMapTable = table
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapResolver = resolver
	rt := &runtimeDungeonState{Dungeon: dungeon}
	if err := s.sendCurrentDungeonResourceState(session, rt, "permission-lifecycle-first-entry"); err != nil {
		t.Fatal(err)
	}
	assertAuthorityEntryState(t, session.conn.(*bufferConn).write.Bytes(), uint32(dungeon.ID), 0)
	before, found, err := repos.DungeonPermission.Load(ctx, "99")
	if err != nil || found || len(before.Entries) != 0 {
		t.Fatalf("entry wrote permission: %+v found=%t err=%v", before, found, err)
	}
	ceiling := currentDungeonMaxClearState(dungeon)
	if ceiling == 0 {
		t.Fatalf("fixture must have more than one difficulty: %+v", dungeon.Metadata.DifficultyLevels)
	}
	for state := byte(1); state <= ceiling; state++ {
		rt.Request = dungeoncmd.SelectDungeonRequest{Difficulty: state - 1}
		body, saved, changed, err := s.commitCurrentDungeonPermission(ctx, session, rt)
		want := buildCurrentDungeonResourceStateEntriesBody([]currentDungeonResourceStateEntry{{DungeonID: uint32(dungeon.ID), State: state}})
		if err != nil || !changed || saved != state || !bytes.Equal(body, want) {
			t.Fatalf("clear=%d saved=%d changed=%t body=%x err=%v", state-1, saved, changed, body, err)
		}
		// Repeated/lower clears cannot award or lower permission.
		for _, difficulty := range []byte{state - 1, 0} {
			rt.Request.Difficulty = difficulty
			b, got, updated, e := s.commitCurrentDungeonPermission(ctx, session, rt)
			if e != nil || updated || got != state || len(b) != 0 {
				t.Fatalf("clear replay=%d got=%d updated=%t body=%x err=%v", difficulty, got, updated, b, e)
			}
		}
		record, found, err := repos.DungeonPermission.Load(ctx, "99")
		if err != nil || !found || len(record.Entries) != 1 || record.Entries[0].ClearState != state {
			t.Fatalf("saved row state=%d record=%+v found=%t err=%v", state, record, found, err)
		}
		reloaded := testRepositoryGroup()
		for _, row := range record.Entries {
			if _, _, err := reloaded.DungeonPermission.UpsertMax(ctx, record.CharacterID, row.DungeonID, row.ClearState); err != nil {
				t.Fatal(err)
			}
		}
		fresh, reconnect := newAuthorityEntryService("official-24.9", reloaded)
		fresh.worldMapTable = table
		fresh.worldMapResolver = resolver
		if err := fresh.sendCurrentDungeonPermissionSnapshot(ctx, reconnect, "permission-lifecycle-reconnect"); err != nil {
			t.Fatal(err)
		}
		assertAuthorityEntryState(t, reconnect.conn.(*bufferConn).write.Bytes(), uint32(dungeon.ID), state)
		reconnect.conn.(*bufferConn).write.Reset()
		if err := fresh.sendCurrentDungeonResourceState(reconnect, rt, "permission-lifecycle-lower-entry"); err != nil {
			t.Fatal(err)
		}
		assertAuthorityEntryState(t, reconnect.conn.(*bufferConn).write.Bytes(), uint32(dungeon.ID), state)
		t.Logf("map=%d next=%d entry=0 clear=%s reconnect=%s", dungeon.ID, state, hex.EncodeToString(body), hex.EncodeToString(want))
	}
}

func TestOfficial249DungeonPermissionLifecycleReconnect(t *testing.T) {
	table, _, _ := loadBridgeDungeonStaticData(t, authorityEntryPermissionPVF())
	dungeon, found := table.FindDungeon(authorityHighDungeonID)
	if !found {
		t.Fatal("fixture missing")
	}
	permissionLifecycle(t, table, dungeon)
}

func TestRealPVFDungeonPermissionLifecycleReconnect(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3, 6, 7, 9, 53, 7170} {
		d, found := table.FindDungeon(id)
		if !found {
			t.Fatalf("real dungeon %d missing", id)
		}
		t.Logf("actual PVF map=%d path=%s levels=%v ceiling=%d", id, d.Path, d.Metadata.DifficultyLevels, currentDungeonMaxClearState(d))
		if currentDungeonMaxClearState(d) != 0 {
			t.Run(hex.EncodeToString([]byte{byte(id >> 8), byte(id)}), func(t *testing.T) { permissionLifecycle(t, table, d) })
		}
	}
}

func TestOfficial249DungeonEntryMissingPermissionAuthoritySendsNothing(t *testing.T) {
	for _, missing := range []string{"owner", "repository", "provider"} {
		t.Run(missing, func(t *testing.T) {
			repos := testRepositoryGroup()
			if missing == "repository" {
				repos.DungeonPermission = nil
			}
			s, session := newAuthorityEntryService("official-24.9", repos)
			switch missing {
			case "owner":
				session.selectedCharacterID = 0
			case "provider":
				s.repositoryProvider = nil
			}
			if err := s.sendCurrentDungeonResourceState(session, &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 700}}, "missing-authority"); err != nil {
				t.Fatal(err)
			}
			if session.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("missing authority changed native cache")
			}
		})
	}
}
