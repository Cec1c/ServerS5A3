package dnfbridge

import (
	"encoding/binary"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 本文件是「收集箱」的**线协议**：body 形状不发明，逐字节对齐两套参考实现 +
// 厂商 9.16 参考环境实测包。
//
// 1) 每箱状态（class0 / msg_id 1027，8 + 4×N 字节）
//
//	[0]    u8   box index
//	[1]    u8   选中标志：1 = 当前选中的那一箱，0 = 缓存行
//	[2:6]  u32  距到期还剩秒数（已过期 = 0）
//	[6]    u8   状态位：到期时间解析成功 = 0，取不到/非法 = 1
//	[7]    u8   已登记条目数 N
//	[8:]   u32×N 已登记条目 id（**必须来自 PVF 的 [Item Position Info]**）
//
// 依据：
//   - 厂商 9.16 参考环境 `D:\DNF90-test\server\runtime\logs\packet_log.txt`
//     2026-09-25T10:19:36.2400554Z 同一时刻 5 条 class0/op1027：
//     `...0100000000000000` / `...0200000000000000` / `...0300000000000000` /
//     `...0400000000000000` / `...0500a7ec42040000`（box5 缓存）；
//     紧接着 class1/op1021 body=`0105`；再一条 `...0501a7ec42040000`（选中刷新）。
//     同一批事件行自带字段名：`game-collectbox-state-send box_index=1 item_count=0 body_len=8`。
//   - 86JP / A21 参考实现 `Network/Builders/Init/CollectionBoxBodyBuilder.cs`
//     BuildForBox：body[0]=Index、body[1]=1、body[2:6]=remainingSeconds、
//     body[6]=statusFlags、body[7]=itemCount、body[8+i*4]=itemId（两版完全一致）。
//
// 2) 选箱应答（class1 / msg_id 1021，2 字节）：`01 <box index>`
// （厂商实测 `0105` = 选中箱 5）。
//
// 3) 客户端选箱/开窗请求（class1 / msg_id 1021）：**请求体末字节 = 箱号**
// （86JP `EnhancedClientSession.cs`: "玩家当前打开的收集箱 PVF [Index] 值(0388
// 请求体末尾字节)"、`CollectionBoxHandler.HandleQueryCollectionBox` 读
// `body[body.Length-1]`；香港现场实测该请求 body_len=1、body=`01`）。
const (
	// currentCollectboxStateMsgID 是每箱状态的 msg id：class0 / 1027。
	//
	// 注意：dnfenum 里 1027 还挂着旧版本名 `CmdPacketFacebookLike`——那份枚举来自
	// 更早的客户端（同文件里 491 也有同类"旧版本名"注释）。当前 90CN 客户端 +
	// 厂商 9.16 服务端把这个 opcode 用作收集箱状态通道，所以这里显式命名，不复用
	// 那个会误导人的旧名字。
	currentCollectboxStateMsgID uint16 = 1027
	// currentCollectboxSelectMsgID 是选箱/查询命令（= 1021，枚举名可直接复用）。
	currentCollectboxSelectMsgID uint16 = uint16(dnfenum.CmdPacketSelectCollectbox)

	// currentCollectboxAddItemMsgID / currentCollectboxRemoveItemMsgID /
	// currentCollectboxExtendMsgID 是登记/取出/延期三个命令。它们的**请求体形态
	// 在 90CN 客户端上尚无取证**（厂商 9.16 日志里一条都没有；香港日志只有
	// op1024 body_len=8 一条），所以本实现只观测、不应答，见
	// currentCollectboxObservedMutationHandler。
	currentCollectboxAddItemMsgID       uint16 = uint16(dnfenum.CmdPacketAddCollectboxItem)
	currentCollectboxRemoveItemMsgID    uint16 = uint16(dnfenum.CmdPacketRemoveCollectboxItem)
	currentCollectboxExtendExpiryMsgID  uint16 = uint16(dnfenum.CmdPacketExtendCollectboxExpiryDate)
	currentCollectboxStateFixedBodySize        = 8
	currentCollectboxSelectedFlag              = byte(1)
	currentCollectboxUnselectedFlag            = byte(0)
	// currentCollectboxExpiryUnparsedFlag 是 body[6]：取不到/非法到期时间时为 1
	// （参考实现 BuildForBox：只有 DateTime.TryParse 失败才置 1）。
	currentCollectboxExpiryUnparsedFlag = byte(1)
	currentCollectboxExpiryParsedFlag   = byte(0)
	// currentCollectboxSelectAckOK 是应答 body[0]（厂商实测固定 1）。
	currentCollectboxSelectAckOK = byte(1)
)

// currentCollectboxRemainingSeconds 返回距 [base expiration date] 的剩余秒数。
//
// 已过期返回 0：这条口径以**厂商 9.16 实测**为准（箱 1..4 的 [base expiration date]
// 都在过去，厂商下发的就是 0）。86JP/A21 参考实现在"已过期"分支写的是 0xFFFFFFFF，
// 那是另一版客户端的口径，**不适用**于本客户端，所以这里不做兼容分支；
// 用例 `TestCurrentCollectboxStateBodyMatchesVendorCapture` 逐字节钉住厂商那 5+1 条包。
func currentCollectboxRemainingSeconds(box currentCollectboxBox, now time.Time) uint32 {
	if !box.HasBaseExpiration {
		return 0
	}
	remaining := box.BaseExpiration.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return uint32(remaining / time.Second)
}

// buildCurrentCollectboxStateBody 生成一箱的 class0/op1027 body。
//
// itemIDs 必须是本箱在 PVF 里的槛位物品 id；不属于 PVF 的会被丢弃（见
// currentCollectboxFilteredItemIDs），保证"下发的条目 id 与 PVF 对应"。
func buildCurrentCollectboxStateBody(
	box currentCollectboxBox,
	selected bool,
	itemIDs []int32,
	now time.Time,
) []byte {
	filtered := currentCollectboxFilteredItemIDs(box, itemIDs)
	body := make([]byte, currentCollectboxStateFixedBodySize+4*len(filtered))
	body[0] = byte(box.Index)
	if selected {
		body[1] = currentCollectboxSelectedFlag
	} else {
		body[1] = currentCollectboxUnselectedFlag
	}
	binary.LittleEndian.PutUint32(body[2:6], currentCollectboxRemainingSeconds(box, now))
	if box.HasBaseExpiration {
		body[6] = currentCollectboxExpiryParsedFlag
	} else {
		body[6] = currentCollectboxExpiryUnparsedFlag
	}
	body[7] = byte(len(filtered))
	for idx, itemID := range filtered {
		binary.LittleEndian.PutUint32(body[8+idx*4:], uint32(itemID))
	}
	return body
}

// buildCurrentCollectboxSelectAckBody 生成 class1/op1021 的应答 body：`01 <box>`。
func buildCurrentCollectboxSelectAckBody(boxIndex int32) []byte {
	return []byte{currentCollectboxSelectAckOK, byte(boxIndex)}
}

// parseCurrentCollectboxSelectRequest 按参考实现口径取"请求体末字节 = 箱号"。
// 空体返回 false（此时不改变服务端选择，只按当前选中箱应答）。
func parseCurrentCollectboxSelectRequest(body []byte) (int32, bool) {
	if len(body) == 0 {
		return 0, false
	}
	return int32(body[len(body)-1]), true
}

// currentCollectboxFilteredItemIDs 只保留属于本箱 PVF 槛位清单的条目 id，
// 并保持调用方给的顺序（同一 id 只发一次）。**任何不在 PVF 里的 id 都不下发**。
func currentCollectboxFilteredItemIDs(box currentCollectboxBox, itemIDs []int32) []int32 {
	if len(itemIDs) == 0 {
		return nil
	}
	seen := make(map[int32]struct{}, len(itemIDs))
	out := make([]int32, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		if itemID <= 0 || !box.HasSlotItem(itemID) {
			continue
		}
		if _, exists := seen[itemID]; exists {
			continue
		}
		seen[itemID] = struct{}{}
		out = append(out, itemID)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
