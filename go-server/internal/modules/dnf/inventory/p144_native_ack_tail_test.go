package inventory

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestP144NativeAckCommonTail(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	raw := make([]byte, currentItemListEntrySize)
	binary.LittleEndian.PutUint32(raw[2:6], 100380051)
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 0}})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
		"38:0": {ItemID: 100380051, Count: 1, RawEntry: raw}, "0:121": {ItemID: 10154439, Count: 30},
	}})
	body := make([]byte, 20)
	body[1] = 38
	binary.LittleEndian.PutUint32(body[4:8], 100380051)
	binary.LittleEndian.PutUint16(body[8:10], 121)
	binary.LittleEndian.PutUint16(body[10:12], 0xffff)
	binary.LittleEndian.PutUint32(body[16:20], 0xffffffff)
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 80, Body: body, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos,
		GuildMedalUpgradeResolver: func(int64, byte) (alignedcmd.GuildMedalUpgradePolicy, error) {
			return alignedcmd.GuildMedalUpgradePolicy{MaterialID: 10154439, MaterialCount: 18, MaterialSlotStart: 121, MaterialSlotEnd: 176, SuccessWeight: 100000, PenaltyType: 1}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.UpperResponses) != 3 {
		t.Fatalf("response count=%d", len(got.UpperResponses))
	}
	ack := got.UpperResponses[0].Body
	// 0x1fe0e3b..0x1fe0e53 always consumes u16 slot + u32 remaining.
	if len(ack) != 25 {
		t.Fatalf("current native consumes 25 bytes, handler sent %d: %x", len(ack), ack)
	}
	if ack[0] != 1 || ack[14] != 38 || binary.LittleEndian.Uint16(ack[19:21]) != 0xffff || binary.LittleEndian.Uint32(ack[21:25]) != 0 {
		t.Fatalf("bad neutral common tail: %x", ack)
	}
	if got.UpperResponses[1].Body[0] != 0 || got.UpperResponses[2].Body[0] != 38 {
		t.Fatal("ACK must precede material list0 and medal list38 updates")
	}
}
