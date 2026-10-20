package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 当前客户端 op773（ENUM_CMDPACKET_WEB_VIEWER_INFO）的最小回包 owner。
//
// 商城网页票据有两条请求路线：实测充值先发 op65，由同模块的
// currentCeraTicketGameplayHandler 处理；op773 的已证一字节请求由本 owner 处理。
//
// 契约证据（只读静态反汇编；对象是 v97 签名客户端 DNF.exe
// sha256=bb42898c02fb43ad30537da776f648ce26ac37df2ff47912ca7174df43c80531，
// 证据目录 调查资料/续修-D33-20261011/current-client-contracts/resume01 与 resume02，
// P56 复核见 p56-cashshop-close-532）。本文件每个常量都对应可复算的 VA：
//
//  1. C2S 只有一个已证字段，包体恰 1 字节（票据类型 u8）：
//     - 0x104c9d0：op773 + u8 6，受 [0x565eff8] 门控，唯一调用者 0x1143d40（商城点击）；
//     - 0x378c0d0 = CNRDOneTimeWebTicketManager::requestOneTimeWebTicket(u8 type)，
//     日志串 0x4d37b48 与 [REQUEST][TICKET][%d] 0x4d37bbc。
//     两个写出点都只写这 1 字节，其它长度没有取证。
//
//  2. S2C 成功体的完整消费顺序（COMMAND handler 0x1FD5360，注册点 0x1fe8754）：
//     success:u8(0x1FD5388) -> u8 票据类型(0x1FD53AC 调 0x37e82e0 n=1)
//     -> 嵌套字符串(0x1FD53C2 调 0x3752120，cap=0x20 见 0x1FD53BF，fallback 0x4933b68)
//     -> u32 code(0x1FD53CB 调 0x37e83c0)。
//     0x3752120 立即转 0x3752050，后者在写 payload 前无条件先读 u32
//     stringByteLength(0x375207A)，只有 0 < length < cap 才继续读 length 个字节。
//     三个值一起送进 0x378c370(type, url, code)（ret 0x28）。
//     故长度 0 的最小成功体是 10 字节：01 | u8 type | 00000000 | 00000000。
//
//  3. 旧实现只回 6 字节（成功位 + u8 + u32）漏掉嵌套字符串长度字段：客户端按上面
//     顺序还要吃 4 字节 u32 code，而流指针 0x57f9ad0 / 剩余计数 0x57f9ad4 在
//     0x37e82e0 里没有包边界检查（0x37E82EA 只 inc 指针、dec 计数），于是这 4 字节
//     越过本包已证的字段范围。这是已证的服务端短包缺陷；是否导致该玩家的
//     商城关闭问题、后续包或内存受何影响，仍需要实机动态证据。
//
//  4. 下行带页面地址：URL 就是上面那个 packet 字符串；只有 length==0 时客户端才
//     退回本地常量 0x4933b68（实解 ERROR!ERROR!）。上限由 cap 决定：op773
//     cap=0x20，length 必须 <= 31；op65 cap=0x40，length <= 63。字符串以窄字节
//     进包（0x3752087 读原始字节），再在 0x3752168 转宽字符，因此只发纯 ASCII
//     地址在两种解释下都安全。
//
//  5. 已证的票据类型只有 4、5、6：成功体里的 u8 送进 0x378c370 后按 type-4 分派
//     （0x378C493 的 sub esi,4 加两次 dec/je 恰好覆盖 4、5、6），type=6 走
//     0x104ee90 + 0x104da30。区间外的取值没有页面装配路径，不得冒充成功。
//
//  6. code 从未被客户端与常量比较：0x378c370 对 edi(=code) 只有 push，无 cmp/jcc；
//     0x104da30 把它当 %d 拼进本地串。故回 code=0 在布局上一定被完整消费
//     （不是"猜一个业务码"）。
//
// 边界：本文件只证明这 10+len 字节会被逐字节消费；不证明点击后客户端一定退出
// 等待态、也不证明一定显示可关闭的空页——那是实机 GUI 验收结论（GUI 待验）。
// 关闭侧 0x104e170 是唯一清除 [0x565eff8] 的路径（0x104E450），它是客户端自己的
// 虚函数（0x13e6380 无直接 caller，只能经 vtable），没有取证到任何"服务端包关闭
// 网页"的链路，因此本修复不发明 gate-release opcode。
const (
	// currentWebViewerTicketTypeOffset 是请求体里唯一已证字段（票据类型 u8）的偏移。
	currentWebViewerTicketTypeOffset = 0
	// currentWebViewerTicketRequestSize 是两个写出点共同证明的请求体长度；
	// 其它长度没有取证，一律按未证形状拒绝。
	currentWebViewerTicketRequestSize = 1
	// currentWebViewerTicketStringCap 是 0x1FD53BF 传给 0x3752120 的 cap；
	// 协议只在 0 < length < cap 时读取 payload。
	currentWebViewerTicketStringCap = 0x20
	// currentWebViewerTicketAckMinSize 是 length==0 的成功体长度：
	// success(1) + u8 type(1) + stringByteLength(4) + code(4)。
	currentWebViewerTicketAckMinSize = 10
	// currentWebViewerTicketTypeMin/Max 是已证的票据类型区间：0x378c370 的
	// sub esi,4 加两次 dec/je 恰好覆盖 4、5、6。
	currentWebViewerTicketTypeMin = 4
	currentWebViewerTicketTypeMax = 6
)

// currentWebViewerTicketURL 是 op773 成功体里的页面地址。默认空网页
// （about:blank 是纯 ASCII，11 字节 < cap，用户已允许"直接打开空网页"）；
// 置空串则回落到客户端本地常量 0x4933b68。部署方可以替换成真实充值/黑钻页面。
var currentWebViewerTicketURL = "about:blank"

// currentWebViewerTicketAckURL 只在长度落在已证区间内时才使用配置地址：
// length==0 或 length>=cap 一律回落空串（cap 外没有已证读取路径，不得冒充）。
func currentWebViewerTicketAckURL() string {
	return currentWebTicketURLWithinCap(currentWebViewerTicketURL, currentWebViewerTicketStringCap)
}

// currentWebTicketURLWithinCap 是 op773/op65 共用的地址过滤：只保留纯 ASCII 且
// 严格小于 cap 的地址，其余返回空串（客户端会退回本地 fallback，布局仍然合法）。
func currentWebTicketURLWithinCap(url string, cap int) string {
	if len(url) == 0 || len(url) >= cap {
		return ""
	}
	for i := 0; i < len(url); i++ {
		if url[i] < 0x20 || url[i] > 0x7e {
			return ""
		}
	}
	return url
}

func webViewerGameplayModule() gameplayModuleDefinition {
	opcode := uint16(dnfenum.CmdPacketWebViewerInfo)
	return gameplayModuleDefinition{
		Name: "web-viewer",
		// The native empty op65 request is observed on the legacy decoder
		// (once04 packet log); its COMMAND reply uses the same proved layout.
		LegacyHandlers: map[uint16]gameplayHandler{
			uint16(dnfenum.CmdPacketGenCeraticket): func(s *Service, session *gameSession, request gameplayRequest) error {
				return s.handleCurrentCeraTicket(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			uint16(dnfenum.CmdPacketGenCeraticket): currentCeraTicketGameplayHandler(),
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

// buildCurrentWebViewerAck 按已证布局拼成功体：
// success(1) + u8 type + u32 stringByteLength + url + u32 code。
// code 恒为 0：客户端只把它格式化进本地 URL/日志，从不校验取值（见文件头 §6）。
func buildCurrentWebViewerAck(ticketType byte, url string) []byte {
	var body packetWriter
	body.writeByte(0x01)
	body.writeByte(ticketType)
	body.writeUint32(uint32(len(url)))
	if len(url) > 0 {
		body.writeBytes([]byte(url))
	}
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
	url := currentWebViewerTicketAckURL()
	reply := buildCurrentWebViewerAck(ticketType, url)
	s.logGameEvent(session, "game-web-viewer-ticket-ack",
		"msg_id", opcode,
		"ticket_type", ticketType,
		"ticket_code", 0,
		"ack_body_len", len(reply),
		"page_url_len", len(url),
		"page_url", url,
		"body_len", len(body),
		"ack_shape", "current_exe_op773_command_success_u8_then_nested_string_then_u32",
		"downlink_url", "packet_string_with_local_fallback_0x4933b68",
		"gui_status", "pending_field_verification")
	return s.sendGameUpperRawClass(session, opcode, reply, dnfproto.DefaultChannelClassification)
}
