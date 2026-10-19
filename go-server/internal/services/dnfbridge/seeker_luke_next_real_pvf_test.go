package dnfbridge

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const seekerLukeNextApprovedPVFSHA256 = "9dcd31a3e6001fd5563d36dc65cbaa96bed8b782700cba30625197e27cf1e43c"

func seekerLukeNextFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func seekerLukeNextClickOp72(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketEplpCommand),
		[]byte{1, 0},
	); err != nil {
		t.Fatal(err)
	}
}

// TestSeekerLukeNextDungeonRealPVF is the explicit full-dungeon handler entry.
// Seeker runs it serially only while owning the approved PVF window. The
// completed settlement/card state is the same existing fixture used by the
// isolated diagnostic; dungeon/map/monster/sequence data all come from PVF.
func TestSeekerLukeNextDungeonRealPVF(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("SEEKER_LUKE_NEXT_REAL_PVF"))
	if path == "" {
		t.Skip("set SEEKER_LUKE_NEXT_REAL_PVF only during the serial full-PVF window")
	}
	gotSHA, err := seekerLukeNextFileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA != seekerLukeNextApprovedPVFSHA256 {
		t.Fatalf("PVF SHA256=%s want approved %s", gotSHA, seekerLukeNextApprovedPVFSHA256)
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, archive, worldmap.Options{SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	aiCharacters, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := loadCurrentLukeNormalSequence(archive)
	if err != nil {
		t.Fatal(err)
	}
	wantSequence := []uint32{5101, 5102, 5103, 5104, 5105, 5106}
	if !sequence.AutoEnter || !reflect.DeepEqual(sequence.Dungeons, wantSequence) {
		t.Fatalf("approved Luke sequence auto=%t dungeons=%v want true/%v",
			sequence.AutoEnter, sequence.Dungeons, wantSequence)
	}
	throne, ok := table.FindDungeon(5106)
	if !ok || strings.ToLower(strings.ReplaceAll(throne.Path, "\\", "/")) != "dungeon/luke_normal/solium.dgn" {
		t.Fatalf("approved throne path=%q found=%t", throne.Path, ok)
	}

	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 90,
		Stats: map[string]int64{
			"fatigue": 500, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options: options{
			currentClientVersion: "official-24.9",
			accountID:            "account-1",
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
			pvfPath:              path,
		},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		dungeonAICharacterTable: aiCharacters,
		initialEquipmentArchive: archive,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		},
	}
	logSink, readLogs := supplement45LogSink(t)
	service.packetLog, service.logger = logSink.packetLog, logSink.logger
	conn := &bufferConn{}
	channel := channelcatalog.Channel{ServerID: 1, ID: 19, Type: 1, Name: "ch.19", Port: 10019}
	session := &gameSession{
		conn: conn, connID: "qst027-luke-next-real-pvf",
		channel: channel, residentChannel: channel, selectedCharacterID: 99,
	}
	runtime, _, err := service.prepareDungeonRuntime(
		ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 5101, Difficulty: 2},
	)
	if err != nil {
		t.Fatalf("prepare real 5101: %v; logs=%s", err, readLogs())
	}
	session.dungeon.runtime = runtime
	session.townSceneReadyCharacterID = 99
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID: 99, TownID: 38, AreaID: 1,
		PositionX: 450, PositionY: 234, MovementCode: 5, PositionValid: true,
	}
	if _, ok := bindCurrentTownSelectorOrigin(session); !ok {
		t.Fatal("town origin")
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		t.Fatal(err)
	}

	for index, wantNext := range wantSequence[1:] {
		current := wantSequence[index]
		conn.write.Reset()
		completePartyRetryFixture(t, service, session)
		if _, _, err := repos.DungeonPermission.UpsertMax(ctx, "99", current, 1); err != nil {
			t.Fatal(err)
		}
		seekerLukeNextClickOp72(t, service, session)
		next := session.dungeon.runtime
		if next == nil || next == runtime || next.Dungeon.ID != int64(wantNext) ||
			next.Request.Difficulty != 2 || next.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatalf("real Luke %d -> %v, want fresh %d difficulty 2 active; logs=%s",
				current, next, wantNext, readLogs())
		}
		ids := seekerLukeNextPacketIDs(t, conn.write.Bytes())
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
				t.Fatalf("real Luke %d -> %d missing op%d packet ids=%v", current, wantNext, msgID, ids)
			}
		}
		runtime = next
	}

	conn.write.Reset()
	completePartyRetryFixture(t, service, session)
	seekerLukeNextClickOp72(t, service, session)
	final := session.dungeon.runtime
	if final == nil || final == runtime || final.Dungeon.ID != 5106 ||
		final.Request.Difficulty != 2 || final.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatalf("real Luke final retry runtime=%v want fresh 5106 difficulty 2 active; logs=%s",
			final, readLogs())
	}
}
