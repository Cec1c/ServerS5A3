package inventory

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 即时刷新回归（2026-10-15，DOC-067 / DOC-105：蓝锁图标不实时显示、重选角色后才出现）。
//
// 根因在客户端侧：当前下发客户端的入站准入位图（EXE .rebuild 0x6E83610，位表 0x6E92300）
// 没有 713/714 位，class1/713、class1/714 的成功 ACK 在派发前就被丢掉（客户端 handler
// 0x1faee80/0x1faeee0 根本没跑），item+0x2aa 只能等重选角色时由登录整表 op13 的 0x77
// 行 0x60 投影出来。位图补丁走客户端候选（父代理线）。
//
// 服务端这一侧的即时刷新 = 同一条 Result 里再排一条 class0/0x000D 整容器刷新：桥接会把
// 它的 body 换成会话权威投影（钱包行 0:0、账号共享格 0:354..0:365、品级种子/孔位/符文都在
// 里面），故未打补丁的当前客户端也能立刻看到蓝锁，且不会退回会抹金币/降品级的模块快照。
// 本用例钉住结果形状：
//   - DOC-067 支持列表 0/1/2：ACK 在前、被点名的整容器刷新在后；
//   - 其它列表（宠物/账号金库/穿戴栏）没有位置锁语义，只回单条 ACK。
//
// 旧代码（无整容器刷新）在本用例上 RED：713/714 只回 class1 一条，客户端准入位图又会把
// 它丢掉，玩家要重选角色才看得到蓝锁。
func TestSortLockAckSchedulesAuthoritativeContainerRefreshForSupportedLists(t *testing.T) {
	cases := []struct {
		name        string
		listType    byte
		wantRefresh bool
	}{
		{"main bag", listTypeMain, true},
		{"avatar", listTypeAvatar, true},
		{"personal cargo", listTypePersonalCargo, true},
		{"pet inventory", listTypePet, false},
		{"account cargo", listTypeAccountCargo, false},
		{"equipment/worn", listTypeEquipment, false},
	}
	for _, tc := range cases {
		for _, locked := range []bool{true, false} {
			opcode := uint16(dnfenum.CmdPacketRequestItemSortLock)
			operation := "request_item_sort_lock"
			if !locked {
				opcode = uint16(dnfenum.CmdPacketRequestItemSortUnlock)
				operation = "request_item_sort_unlock"
			}
			cmd := Command{
				Operation:           operation,
				AccountID:           "acc",
				SelectedCharacterID: 77,
				SourceListType:      tc.listType,
				SourceSlotIndex:     14,
				SortLock:            locked,
				SortLockRequested:   true,
			}
			result := ownerAppliedSortLockResult(opcode, cmd, SortLockResult{
				CharacterID: "77",
				ListType:    tc.listType,
				SlotIndex:   14,
				Locked:      locked,
				Changed:     true,
			})
			if !result.Handled || !result.ResponseAllowed {
				t.Fatalf("%s locked=%t: handled=%t allowed=%t", tc.name, locked, result.Handled, result.ResponseAllowed)
			}
			wantFrames := 1
			if tc.wantRefresh {
				wantFrames = 2
			}
			if len(result.UpperResponses) != wantFrames {
				t.Fatalf("%s locked=%t: frames=%d want %d", tc.name, locked, len(result.UpperResponses), wantFrames)
			}
			ack := result.UpperResponses[0]
			if ack.MsgID != opcode || ack.Classification != dnfproto.DefaultChannelClassification || !ack.AllowCodec {
				t.Fatalf("%s locked=%t: ACK header=%+v", tc.name, locked, ack)
			}
			// 客户端 handler 0x1faee80/0x1faeee0 先读 success 字节，再读 u8 list + u16 slot；
			// 回包必须用**同一 opcode**（713 只写 1、714 只写 0，不是 toggle）。
			if want := []byte{1, tc.listType, 14, 0}; !bytes.Equal(ack.Body, want) {
				t.Fatalf("%s locked=%t: ACK body=%x want %x", tc.name, locked, ack.Body, want)
			}
			if !tc.wantRefresh {
				if result.AuthoritativeListRefreshes != nil {
					t.Fatalf("%s: unexpected authoritative refresh %v", tc.name, result.AuthoritativeListRefreshes)
				}
				if len(result.PostActions) != 0 {
					t.Fatalf("%s: unexpected post actions %+v", tc.name, result.PostActions)
				}
				continue
			}
			refresh := result.UpperResponses[1]
			if refresh.MsgID != msgItemListRefresh || refresh.Classification != 0 || !refresh.AllowCodec {
				t.Fatalf("%s locked=%t: refresh header=%+v", tc.name, locked, refresh)
			}
			// 占位包体只给桥接认出列表号；on-wire 包体由桥接的权威投影替换（见 dnfbridge 回归）。
			if !bytes.Equal(refresh.Body, []byte{tc.listType}) {
				t.Fatalf("%s locked=%t: placeholder body=%x want [%d]", tc.name, locked, refresh.Body, tc.listType)
			}
			if !bytes.Equal(result.AuthoritativeListRefreshes, []byte{tc.listType}) {
				t.Fatalf("%s locked=%t: AuthoritativeListRefreshes=%v want [%d]", tc.name, locked, result.AuthoritativeListRefreshes, tc.listType)
			}
		}
	}
}

// 幂等命中（Changed=false）与失败路径分开钉：幂等仍然回 ACK + 整容器刷新（客户端要的状态
// 已经成立，图标也必须立刻显示）；失败路径在 handler 层就不开回包（见 ownerBlockedResult）。
func TestSortLockIdempotentAckStillSchedulesContainerRefresh(t *testing.T) {
	cmd := Command{
		Operation:           "request_item_sort_lock",
		AccountID:           "acc",
		SelectedCharacterID: 77,
		SourceListType:      listTypeMain,
		SourceSlotIndex:     14,
		SortLock:            true,
		SortLockRequested:   true,
	}
	result := ownerAppliedSortLockResult(uint16(dnfenum.CmdPacketRequestItemSortLock), cmd, SortLockResult{
		CharacterID: "77", ListType: listTypeMain, SlotIndex: 14, Locked: true, Changed: false,
	})
	if len(result.UpperResponses) != 2 || !bytes.Equal(result.AuthoritativeListRefreshes, []byte{listTypeMain}) {
		t.Fatalf("idempotent reply = %+v", result.UpperResponses)
	}
}
