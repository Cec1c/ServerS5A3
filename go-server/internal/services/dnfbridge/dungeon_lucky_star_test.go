package dnfbridge

import (
	"context"
	"encoding/binary"
	dungeon "longheng.io/server/internal/modules/dnf/dungeon"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFLuckyStarEarlyDungeonEligibility(t *testing.T) {
	path := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("paired PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id    int64
		level int
	}{{6, 6}, {9, 9}} {
		dungeon, found := table.FindDungeon(row.id)
		if !found {
			t.Fatal("dungeon missing")
		}
		runtime := &runtimeDungeonState{Dungeon: dungeon, Character: repo.CharacterRecord{Level: row.level + 5}, entryCharacterLevel: row.level, ordinaryFinalRoomClearAccepted: true}
		if !currentDungeonIsRecommendedClear(runtime) {
			t.Fatalf("paired dungeon %d level %d not eligible", row.id, row.level)
		}
		runtime.entryCharacterLevel = 90
		if currentDungeonIsRecommendedClear(runtime) {
			t.Fatal("over-level early dungeon accepted")
		}
	}
	catalog, err := parseCurrentRentalCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Limit != 400 || catalog.DungeonClearPoints != 1 {
		t.Fatalf("PVF rental wallet=%+v", catalog)
	}
}

func TestLuckyStarCommittedStateRefreshPreservesWalletAndDoesNotAwardTwice(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{currentRentalPointMetadataKey: "2"}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	service := Service{options: options{accountID: "a"}, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 6}, settlementLuckyStar: dungeon.LuckyStarResult{Resolved: true, Before: 0, After: 1, Limit: 400}}
	service.sendSuitableDungeonLuckyStarState(session, runtime)
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 985 || packet.Header.Classification != 0 || len(rest) != 0 || len(packet.Body) != 8 || binary.LittleEndian.Uint32(packet.Body) != 2 {
		t.Fatalf("packet=%+v rest=%x", packet, rest)
	}
	size := conn.write.Len()
	service.sendSuitableDungeonLuckyStarState(session, runtime)
	if conn.write.Len() != size || !runtime.settlementLuckyStarStateSent {
		t.Fatal("successful refresh repeated")
	}
	account, _, _ := repos.Account.Load(ctx, "a")
	if account.Metadata[currentRentalPointMetadataKey] != "2" {
		t.Fatal("send rewrote the committed wallet")
	}
}

func TestLuckyStarRefreshDeferredStateCanRetry(t *testing.T) {
	repos := memory.NewMemoryGroup()
	conn := &bufferConn{}
	service := Service{options: options{accountID: "a"}, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	session := &gameSession{conn: conn, selectedCharacterID: 19, selectedRentalWalletStateSent: true}
	runtime := &runtimeDungeonState{settlementLuckyStar: dungeon.LuckyStarResult{Resolved: true, After: 1, Limit: 400}}
	service.sendSuitableDungeonLuckyStarState(session, runtime)
	if runtime.settlementLuckyStarStateSent || conn.write.Len() != 0 {
		t.Fatal("deferred send marked successful")
	}
	if err := repos.Account.Save(context.Background(), repo.AccountRecord{AccountID: "a", Metadata: map[string]string{currentRentalPointMetadataKey: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(context.Background(), repo.CharacterRecord{CharacterID: "19", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	service.sendSuitableDungeonLuckyStarState(session, runtime)
	if !runtime.settlementLuckyStarStateSent || conn.write.Len() == 0 {
		t.Fatal("refresh did not retry")
	}
}
