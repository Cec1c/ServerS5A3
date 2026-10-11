package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 当前客户端 op773（ENUM_CMDPACKET_WEB_VIEWER_INFO）的最小回包 owner。
//
// 现场问题：商城内点击「点券充值 / 黑钻会员」后，服务端对这条 class1 上行一个字节
// 都不回（旧行为落进 game_dispatch_upper.go 末尾的 `unhandled latest upper packet`），
// 该 ticket 应答因此缺失。本 owner 只补全**已证形状**的应答；是否由此解决现场卡死
// 属 GUI 待验，不在本文件宣称范围内。
//
// 契约证据（只读静态反汇编；对象是 v97 签名客户端 DNF.exe
// sha256=bb42898c02fb43ad30537da776f648ce26ac37df2ff47912ca7174df43c80531，
// 证据目录 调查资料/续修-D33-20261011/current-client-contracts/resume01 与 resume02）。
// 本文件每个常量都对应可复算的 VA：
//
//  1. C2S 只有一个已证字段，包体恰 1 字节（票据类型 u8）：
//     - 0x104c9d0：op773 + u8 6，受 [0x565eff8] 门控，唯一调用者 0x1143d40（商城点击）；
//     - 0x378c0d0 = CNRDOneTimeWebTicketManager::requestOneTimeWebTicket(u8 type)，
//     日志串 0x4d37b48 与 [REQUEST][TICKET][%d] 0x4d37bbc。
//     两个写出点都只写这 1 字节，其它长度没有取证。
//
//  2. 已证的票据类型只有 4、5、6：成功分支 0x1fd5360 读到的 u8 送进
//     0x378c370(type, url, code)，内部 `sub esi,4` 后连续两次 dec/je，恰好覆盖
//     4、5、6 三个页面装配分支；区间外的取值没有页面装配路径，不得冒充成功。
//
//  3. S2C 成功体按 u8 再 u32 的顺序被消费，共 6 字节
//     （COMMAND 表 handler 0x1fd5360，注册点 0x1fe8754）：
//     - 成功位：`cmp byte [ebp+0xc],0`，为 0 时 handler 直接返回，包体一个字段都不读；
//     - 非 0：0x37e82e0(n=1) 读 u8 到 [ebp-0x54]，0x37e83c0 读 u32 到 [ebp-0x58]；
//     - 两者喂给 0x378c370(type, url, code)（ret 0x28 = 1 dword type + 0x20 字节
//     std::string + 1 dword code），type=6 走 0x104ee90 + 0x104da30。
//
//  4. 下行不携带 URL。0x1fd5360 里的 URL 是客户端本地常量 0x4933b68
//     （实解为 ERROR!ERROR!）；包体里没有、也无从指定页面地址。第二张表的
//     handler 0x1feb0d0 读的是 u32 + u8（另一形状），那条路未取证，因此只登记
//     class1 通道。
//
//  5. code 从未被客户端与常量比较：0x378c370 对 edi(=[ebp+0x2c]=code) 只有 push，
//     无 cmp/jcc；0x104da30 把它当 %d 拼进本地串。故回 code=0 在布局上一定被完整
//     消费（不是"猜一个业务码"）：本 handler 不存在需要服务端提供业务码的分支。
//
// 边界：本文件只证明成功体这 6 字节会被逐字节消费；不证明点击后客户端一定退出
// 等待态、也不证明一定显示可关闭的空页——那是实机 GUI 验收结论（GUI 待验）。
const (
	// currentWebViewerTicketTypeOffset 是请求体里唯一已证字段（票据类型 u8）的偏移。
	currentWebViewerTicketTypeOffset = 0
	// currentWebViewerTicketRequestSize 是两个写出点共同证明的请求体长度；
	// 其它长度没有取证，一律按未证形状拒绝。
	currentWebViewerTicketRequestSize = 1
	// currentWebViewerTicketAckSize 是成功分支消费的应答体长度：成功位 + u8 + u32。
	currentWebViewerTicketAckSize = 6
	// currentWebViewerTicketTypeMin/Max 是已证的票据类型区间：0x378c370 的
	// `sub esi,4` 加两次 dec/je 恰好覆盖 4、5、6。
	currentWebViewerTicketTypeMin = 4
	currentWebViewerTicketTypeMax = 6
)

func webViewerGameplayModule() gameplayModuleDefinition {
	opcode := uint16(dnfenum.CmdPacketWebViewerInfo)
	return gameplayModuleDefinition{
		Name: "web-viewer",
		UpperHandlers: map[uint16]gameplayHandler{
			opcode: defaultClassGameplayHandler(
				"game-web-viewer-blocked",
				"current_exe_op773_command_class_mismatch",
				(*Service).handleCurrentWebViewerTicket,
			),
		},
	}
}

// currentWebViewerTicketTypeSupported 只认已证的票据类型 4/5/6。
func currentWebViewerTicketTypeSupported(ticketType byte) bool {
	return ticketType >= currentWebViewerTicketTypeMin && ticketType <= currentWebViewerTicketTypeMax
}

// buildCurrentWebViewerAck 按已证布局拼成功体：成功位(1) + u8 type + u32 code。
// code 恒为 0：客户端只把它格式化进本地 URL/日志，从不校验取值（见文件头 §5）。
func buildCurrentWebViewerAck(ticketType byte) []byte {
	var body packetWriter
	body.writeByte(0x01)
	body.writeByte(ticketType)
	body.writeUint32(0)
	return body.bytes()
}

// handleCurrentWebViewerTicket 补全 op773 的应答：长度与类型都命中已证取值的请求回
// 最小成功体，其余（未证长度、未证类型）回显式失败体。三条分支都必然发一个回包。
//
// 失败体只用"成功位 0 + 0 码占位"：成功位为 0 时客户端 handler 直接返回、不读包体
// 任何字段，因此这里没有可对应的客户端文案，也不冒充任何错误码语义。
func (s *Service) handleCurrentWebViewerTicket(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketWebViewerInfo)
	if len(body) != currentWebViewerTicketRequestSize {
		s.logGameEvent(session, "game-web-viewer-ticket-rejected",
			"msg_id", opcode,
			"body_len", len(body),
			"request_size", currentWebViewerTicketRequestSize,
			"reason", "unproven_current_exe_request_size")
		return s.sendGameUpperFailure(session, opcode, 0)
	}
	ticketType := body[currentWebViewerTicketTypeOffset]
	if !currentWebViewerTicketTypeSupported(ticketType) {
		s.logGameEvent(session, "game-web-viewer-ticket-rejected",
			"msg_id", opcode,
			"ticket_type", ticketType,
			"body_len", len(body),
			"supported_types", "4,5,6",
			"reason", "unsupported_current_exe_ticket_type")
		return s.sendGameUpperFailure(session, opcode, 0)
	}
	reply := buildCurrentWebViewerAck(ticketType)
	s.logGameEvent(session, "game-web-viewer-ticket-ack",
		"msg_id", opcode,
		"ticket_type", ticketType,
		"ticket_code", 0,
		"ack_body_len", len(reply),
		"body_len", len(body),
		"ack_shape", "current_exe_op773_command_success_flag_u8_then_u32",
		"downlink_url", "absent_client_uses_local_constant_0x4933b68",
		"gui_status", "pending_field_verification")
	return s.sendGameUpperRawClass(session, opcode, reply, dnfproto.DefaultChannelClassification)
}
