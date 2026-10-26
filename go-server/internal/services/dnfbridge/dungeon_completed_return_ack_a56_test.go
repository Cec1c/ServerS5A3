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
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const completedAckA56ApprovedPVFSHA256 = "a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290"

func completedAckA56FileSHA256(path string) (string, error) {
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

func completedAckA56Dispatch(t *testing.T, service *Service, session *gameSession, id uint16, body []byte) {
	t.Helper()
	high, initialized := session.guild.purchaseSequence.currentHigh()
	sequence := uint16(1)
	if initialized {
		sequence = uint16(high) + 1
	}
	wire, err := dnfproto.BuildChannelPacket(id, body, sequence, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(session, wire); err != nil {
		t.Fatal(err)
	}
}

// Completed settlement and memory repositories are fixtures. Real 5104/5105
// definitions and potion come from the exact a56 PVF; formal upper packets
// cover return ACK, standard next entry and one item transaction. This does
// not execute earlier battle, client UI, cooldown or native scene transitions.
func TestRealPVFCompletedLukeReturnAckAndNextDungeonPotion(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("R115_COMPLETED_ACK_REAL_PVF"))
	if path == "" {
		t.Skip("set R115_COMPLETED_ACK_REAL_PVF only during the serial full-PVF window")
	}
	gotSHA, err := completedAckA56FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA != completedAckA56ApprovedPVFSHA256 {
		t.Fatalf("PVF SHA256=%s want approved %s", gotSHA, completedAckA56ApprovedPVFSHA256)
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
	items, err := newPVFDungeonDropCatalog(archive)
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
		CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: 2660671, Count: 4, Extra: map[string]string{"item_kind": "stackable"}}},
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
		pvfItemCatalog:          items,
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
		ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 5104, Difficulty: 2},
	)
	if err != nil {
		t.Fatalf("prepare real ordinary 5104: %v; logs=%s", err, readLogs())
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

	conn.write.Reset()
	completePartyRetryFixture(t, service, session)
	completedAckA56Dispatch(t, service, session, uint16(dnfenum.CmdPacketGiveupGame), nil)
	ackIndex, typedIndex, count := -1, -1, 0
	raw := conn.write.Bytes()
	for index := 0; len(raw) > 0; index++ {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentSceneTransitionMsgID {
			typedIndex = index
		}
		if packet.Header.Classification == 1 && packet.Header.MsgID == 42 {
			if len(packet.Body) != 1 || packet.Body[0] != 1 {
				t.Fatalf("op42 body=%x", packet.Body)
			}
			count++
			ackIndex = index
		}
		raw = rest
	}
	if count != 1 || typedIndex < 0 || ackIndex <= typedIndex {
		t.Fatalf("completed real5104 ackcount=%d typedIndex=%d ackIndex=%d; logs=%s", count, typedIndex, ackIndex, readLogs())
	}
	if session.dungeon.runtime != nil || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("completed return did not retain completed result and retire owner")
	}
	before, found, err := repos.Inventory.Load(ctx, "99")
	if err != nil || !found || before.Slots["0:65"].Count != 4 {
		t.Fatalf("return changed potion: %v %v", before, err)
	}
	if _, ok := bindCurrentTownSelectorOrigin(session); !ok {
		t.Fatal("completed real town origin unavailable")
	}
	conn.write.Reset()
	completedAckA56Dispatch(t, service, session, uint16(dnfenum.CmdPacketSelectDungeon), encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: 5105, Difficulty: 2}))
	next := session.dungeon.runtime
	if next == nil || next == runtime || next.Dungeon.ID != 5105 || next.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatalf("formal next actual5105 failed: %v; logs=%s", next, readLogs())
	}
	conn.write.Reset()
	completedAckA56Dispatch(t, service, session, uint16(dnfenum.CmdPacketUseStackable), buildRejectedUseStackableBody(65, 0, 0x11223344, 2660671))
	raw = conn.write.Bytes()
	gotUse := false
	for len(raw) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		if packet.Header.Classification == 1 && packet.Header.MsgID == 44 {
			gotUse = len(packet.Body) > 0 && packet.Body[0] == 1
		}
		raw = rest
	}
	after, found, err := repos.Inventory.Load(ctx, "99")
	if err != nil || !found || !gotUse || after.Slots["0:65"].Count != 3 {
		t.Fatalf("real5105 potion did not commit once: ack=%t count=%d err=%v logs=%s", gotUse, after.Slots["0:65"].Count, err, readLogs())
	}
	t.Log("actual a56 5104 completed fixture -> formal42/typed24/42ACK -> formal16 actual5105 -> formal44 count4to3")
}
