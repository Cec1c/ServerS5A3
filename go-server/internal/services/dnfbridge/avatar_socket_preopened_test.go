package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 皮肤/光环现在**出生就带 2 个孔**（PVF 声明），所以"点打孔"再也不会是第一次开孔：
// 当前 EXE 的镶嵌流程在孔已存在时不能消耗镶嵌栏开启券。这条用例钉住玩家可观察的行为——
// 已带孔的时装上点打孔，材料必须原样留着、孔位向量不变。
func TestCurrentAvatarSocketOpenOnPreSocketedAvatarKeepsMaterial(t *testing.T) {
	service, repositories := newCurrentSocketTestService(t)
	ctx := context.Background()

	// 皮肤的真实向量：两个通用孔（M）。
	const socketHex = "efff00000000efff00000000000000000000000000000000000000000000"
	preOpened := currentAvatarSocketData(map[string]string{"avatar_socket_data": socketHex})
	if count := currentAvatarSocketOpenCount(preOpened); count != 2 {
		t.Fatalf("fixture vector opens %d sockets, want 2", count)
	}

	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"1:2": {ItemID: 9101, Count: 1, Extra: map[string]string{"avatar_socket_data": socketHex}},
			"0:8": {ItemID: 6001, Count: 1},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	session := &gameSession{selectedCharacterID: 77, connID: "avatar-socket-preopened", conn: &bufferConn{}}

	result, err := service.commitCurrentAvatarSocketOpen(session, currentSocketOpenRequest{
		TargetSlot:   2,
		TargetItemID: 9101,
		MaterialSlot: 8,
	})
	if err != nil {
		t.Logf("re-opening an already socketed avatar was rejected (%v); material must still be intact", err)
	}

	inventory := mustLoadCurrentSocketInventory(t, repositories, "77")
	material, ok := inventory.Slots["0:8"]
	if !ok || material.ItemID != 6001 || material.Count != 1 {
		t.Fatalf("opening an already socketed avatar consumed the socket tool: %+v (ok=%t)", material, ok)
	}
	for _, consumed := range result.Consumed {
		if consumed.ListType == currentSocketListMain && consumed.Slot == 8 {
			t.Fatalf("already socketed avatar reported the tool as consumed: %+v", result.Consumed)
		}
	}
	data := currentAvatarSocketData(inventory.Slots["1:2"].Extra)
	if count := currentAvatarSocketOpenCount(data); count != 2 {
		t.Fatalf("socket vector after re-open = %x (opens %d), want the original two sockets", data, count)
	}
}

// 皮肤修好之后玩家真正要做的事：把徽章**镶嵌进出生就有的孔**（不用先打孔）。
// 这条钉住那个流程：徽章被消耗、孔位里落下徽章 ID、孔的个数不变。
func TestCurrentAvatarEmblemAttachIntoCreationSocketKeepsOtherSocketEmpty(t *testing.T) {
	service, repositories := newCurrentSocketTestService(t)
	ctx := context.Background()

	const socketHex = "efff00000000efff00000000000000000000000000000000000000000000"
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"1:2": {ItemID: 9101, Count: 1, Extra: map[string]string{"avatar_socket_data": socketHex}},
			"0:9": {ItemID: 5001, Count: 1},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	session := &gameSession{selectedCharacterID: 77, connID: "avatar-emblem-creation-socket", conn: &bufferConn{}}

	if _, err := service.commitCurrentAvatarEmblemAttach(session, currentEmblemAttachRequest{
		TargetSlot:   2,
		TargetItemID: 9101,
		Emblems: []currentEmblemApplyRequest{{
			EmblemSlot:   9,
			EmblemItemID: 5001,
			SocketIndex:  0,
		}},
	}); err != nil {
		t.Fatalf("attach emblem into the creation-time socket: %v", err)
	}

	inventory := mustLoadCurrentSocketInventory(t, repositories, "77")
	if stack, ok := inventory.Slots["0:9"]; ok {
		t.Fatalf("emblem item was not consumed: %+v", stack)
	}
	data := currentAvatarSocketData(inventory.Slots["1:2"].Extra)
	if count := currentAvatarSocketOpenCount(data); count != 2 {
		t.Fatalf("socket count after attach = %d (%x), want the two creation sockets kept", count, data)
	}
	if got := avatarSocketEmblemIDAt(data, 0); got != 5001 {
		t.Fatalf("socket 0 emblem = %d, want 5001 (data=%x)", got, data)
	}
	if got := avatarSocketEmblemIDAt(data, 1); got != 0 {
		t.Fatalf("socket 1 must stay empty, got emblem %d (data=%x)", got, data)
	}
}

// avatarSocketEmblemIDAt 读某个孔的徽章 ID（每个孔 6 字节：u16 类型 + u32 徽章 ID）。
func avatarSocketEmblemIDAt(data [currentAvatarSocketBytes]byte, index int) int64 {
	offset := index*currentAvatarSocketStride + 2
	if offset+4 > len(data) {
		return 0
	}
	return int64(binary.LittleEndian.Uint32(data[offset : offset+4]))
}
