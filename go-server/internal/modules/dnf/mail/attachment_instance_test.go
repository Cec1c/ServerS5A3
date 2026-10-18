package mail

import (
	"bytes"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestEquipmentFollowupMailClaimKeepsRawInstanceBeforeNewItemDefaults(t *testing.T) {
	for _, durability := range []uint16{0, 13} {
		raw := bytes.Repeat([]byte{0x35}, 119)
		binary.LittleEndian.PutUint32(raw[2:], 1001)
		binary.LittleEndian.PutUint32(raw[6:], 462346698)
		binary.LittleEndian.PutUint16(raw[11:], durability)
		a := r.MailAttachment{ItemID: 1001, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "enchant_card": "777", "avatar_socket_data": "0100020089130000"}}
		stack := mailboxAttachmentStack(a, 1, alignedcmd.MailboxItemResolution{Kind: "equipment", TargetListType: 0, DefaultExtra: map[string]string{"quality_seed": "1", "durability": "40", "max_durability": "40"}})
		if stack.Extra["quality_seed"] != "462346698" || stack.Extra["durability"] != map[uint16]string{0: "0", 13: "13"}[durability] || !bytes.Equal(stack.RawEntry, raw) || stack.Extra["enchant_card"] != "777" || stack.Extra["avatar_socket_data"] != a.Extra["avatar_socket_data"] {
			t.Fatal("领取旧实例时重置了品级、当前耐久或丢失附魔徽章", stack.Extra)
		}
	}
}
