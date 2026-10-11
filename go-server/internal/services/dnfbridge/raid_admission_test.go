package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFRaidEntryCostQueriesActualMaterialWithoutConsumption(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		kind byte
		item int64
	}{{23, 10096296}, {32, 10159649}} {
		repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:121": {ItemID: row.item, Count: 1}})
		ctx := context.Background()
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "20", AccountID: "account-2"}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "20", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		conn := &bufferConn{}
		session := &gameSession{conn: conn, selectedCharacterID: 19, accountID: "account-1", channel: channelcatalog.Channel{ID: 23, Type: row.kind}}
		state := &runtimeRaidState{RaidKey: 23<<16 | 19, LeaderID: 19, Members: []runtimeRaidMemberState{{CharID: 19}, {CharID: 20}}}
		service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: archive, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, raids: map[uint32]*runtimeRaidState{state.RaidKey: state}}
		if err := service.handleOnlineRaidEntryCostInfo(session, 658, []byte{0}); err != nil {
			t.Fatal(err)
		}
		packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
		if len(packets) != 2 || packets[0].Header.MsgID != 658 || packets[0].Body[0] != 1 || packets[1].Header.MsgID != 598 || packets[1].Header.Classification != 0 {
			t.Fatalf("query packets=%+v", packets)
		}
		body := packets[1].Body
		if len(body) != 14 || binary.LittleEndian.Uint32(body[:4]) != 2 || body[6] != 1 || binary.LittleEndian.Uint16(body[7:9]) != 1 || body[11] != 0 || binary.LittleEndian.Uint16(body[12:14]) != 0 {
			t.Fatalf("native ready/missing fields=%x", body)
		}
		inventory, _, _ := repos.Inventory.Load(ctx, "19")
		if inventory.Slots["0:121"].Count != 1 || state.Started {
			t.Fatal("readonly query consumed material or started raid")
		}
	}
}
