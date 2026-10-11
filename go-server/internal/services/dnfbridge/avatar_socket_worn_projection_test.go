package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 穿在身上的时装（actor 槽 0..11）在 mode1 装备创建态里通过 raw block A 下发徽章孔向量——
// 这是玩家"看着自己穿着的皮肤有几个孔"的唯一投影路径；list 1 行后的可选 blob 只负责
// 还放在装扮栏里的那件。所以 Extra 里的 avatar_socket_data 必须出现在这里：
// 客户端 2026-09-24 报的"可镶嵌皮肤没有镶嵌孔"就是这么被投影出来的。
func TestCurrentMode1EquipmentAvatarRawBlocksCarriesWornAvatarSockets(t *testing.T) {
	// 两个通用孔（M）= 皮肤 PVF 里 [avatar emblem socket num] 2 声明出来的向量。
	vector := []byte{0xEF, 0xFF, 0, 0, 0, 0, 0xEF, 0xFF, 0, 0, 0, 0}
	extra := map[string]string{
		"avatar_socket_data": hex.EncodeToString(append(vector, make([]byte, currentAvatarSocketBytes-len(vector))...)),
		"item_kind":          "avatar",
	}
	stack := dnfrepo.ItemStack{ItemID: 415580009, Count: 1, Extra: extra}
	entry := currentItemListEntryFromStack(currentSocketListAvatar, 8, stack)
	normalized := currentAvatarSocketData(extra)
	if !bytes.Equal(entry.avatarSocketData, normalized[:]) {
		t.Fatalf("list-1 projection dropped the socket vector: %x", entry.avatarSocketData)
	}

	rawA, rawB := currentMode1EquipmentAvatarRawBlocks(extra, entry, true)
	want := make([]byte, currentAvatarSocketBytes)
	copy(want, vector)
	if !bytes.Equal(rawA, want) {
		t.Fatalf("mode1 avatar raw block A = %x, want %x (worn skin would show no sockets)", rawA, want)
	}
	if len(rawB) != 0 {
		t.Fatalf("mode1 avatar raw block B = %x, want empty colour block", rawB)
	}
	if count := currentAvatarSocketOpenCountFromBytes(rawA); count != 2 {
		t.Fatalf("mode1 avatar raw block A opens %d sockets, want 2", count)
	}

	// 显式写过的 mode1 块优先（残留的实测块不能被 Extra 顶掉）。
	extra["mode1_raw_a_hex"] = "0102"
	rawA, _ = currentMode1EquipmentAvatarRawBlocks(extra, entry, true)
	if !bytes.Equal(rawA, []byte{0x01, 0x02}) {
		t.Fatalf("explicit mode1 raw block A = %x, want 0102", rawA)
	}

	// 非 avatar 行（readsRawBlocks=false）只认显式 mode1 块，不会把时装的孔位向量
	// 泄漏到普通装备行上。
	plainExtra := map[string]string{"avatar_socket_data": extra["avatar_socket_data"]}
	rawA, rawB = currentMode1EquipmentAvatarRawBlocks(plainExtra, entry, false)
	if len(rawA) != 0 || len(rawB) != 0 {
		t.Fatalf("non-avatar row raw blocks = %x/%x, want empty", rawA, rawB)
	}
}
