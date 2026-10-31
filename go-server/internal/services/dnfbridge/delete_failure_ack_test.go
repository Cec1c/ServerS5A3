package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestAlignedDeleteMissingSharedMaterialSendsFailureEveryTime(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "dnf:1", Name: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "dnf:1", Slots: map[string]dnfrepo.ItemStack{"0:354": {ItemID: 3033, Count: 14}}}); err != nil {
		t.Fatal(err)
	}
	before, _, err := repos.AccountInventory.Load(ctx, "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{options: options{accountID: "dnf:1", gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 77}
	// Parsed current-client op18: list0, skill material3034, slot355,
	// count1, condition0, state2. The shared white-cube slot is absent.
	body := []byte{16, 0, 0, 0, 16, 0, 26, 10, 8, 2, 16, 0xe3, 2, 24, 0xda, 0x17, 32, 1, 32, 0, 2}
	for attempt := 0; attempt < 3; attempt++ {
		conn.write.Reset()
		handled, err := service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketDeleteItem), body)
		if err != nil || !handled {
			t.Fatalf("attempt%d handled=%t err=%v", attempt, handled, err)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.Classification != 1 || packet.Header.MsgID != 18 || !bytes.Equal(packet.Body, []byte{0, 4, 0, 0, 0, 8, 0, 24, 1}) || len(rest) != 0 {
			t.Fatalf("attempt%d header=%+v body=%x trailing=%x", attempt, packet.Header, packet.Body, rest)
		}
	}
	after, found, err := repos.AccountInventory.Load(ctx, "dnf:1")
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("shared inventory changed before=%+v after=%+v found=%t err=%v", before, after, found, err)
	}
}
