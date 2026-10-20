package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

// op140（GUILD_ALL_MEMBER_LIST）的**厂商等价应答**。
//
// 现场证据（2026-09-27）：
//   - 厂商 `D:\DNF90-test\server\runtime\logs\packet_log.txt`：客户端发 class1/op140
//     请求（body 4 字节），厂商**回** class1/op140，body = `01` + 16 个 0 字节（共 17 字节）
//     —— 即"无公会/空成员列表"的可渲染状态。
//   - 我们 `sendGuildAllMemberListState` 长期"延迟不答"（只写
//     `game-upper-guild-all-member-list-passive-deferred`），客户端因此一直显示
//     「公会 网络连接中…」，并在选人界面停在 `Loading...`（自动探针连续 8 轮都卡在这一屏，
//     操作者截图里也有同一窗口）。
//
// 修法与 op646 一致：**保持应答在线**（不能让客户端死等），内容与厂商逐字节同形。
func (s *Service) sendVendorGuildMemberListState(session *gameSession, source string) error {
	body := make([]byte, 17)
	body[0] = 0x01 // 厂商 body[0]=0x01，其余 16 字节为 0
	s.logGameEvent(session, "game-upper-guild-all-member-list-vendor-empty-answer",
		"source", source,
		"char_id", sessionSelectedCharacterID(session),
		"msg_id", uint16(dnfenum.CmdPacketGuildAllMemberList),
		"classification", 1,
		"body_len", len(body),
		"body_source", "vendor_packet_log_class1_op140_body01_then_16_zero_bytes")
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketGuildAllMemberList), body, 1)
}
