package dnfbridge

import (
	"os"
	"strings"
	"sync"
)

// 本文件是**生产**阻断名单（不是诊断开关）：把"厂商服务端从不发送、而我们会发"的
// 上层包挡在发包出口上。
//
// 为什么需要它（2026-09-27 现场证据，两轮独立复核后定案）：
//   - 症状：客户端进城后弹出决斗场任务书占位面板（`ui/pvp02/mission/missionbook.xui`、
//     未翻译的 `dnf_46688`、`0秒` 等待计时）并吞掉操作（操作者描述为"弹窗 + 卡死"）。
//   - 身份（厂商枚举名已定位）：`455` = class0/`0x01C7`
//     = `ENUM_NOTIPACKET_HOTKEY_OPTION`，枚举定义见厂商源码
//     `ServerS4A21-master/Server/DfoServer/Network/Core/PacketTypesA21.cs:462`
//     （同表 `0x01C7` 在 CmdPacket 命名空间是 `ENUM_CMDPACKET_INSERT_RANDOM_OPTION_ITEM`，
//     `:1686`；我们发的是 class0，所以命中的是 NOTI 那条，不是撞号的另一个包）。
//     我方发送点是 `game_option_settings.go` 的 `currentAccountHotkeyNoticeMsgID`
//     → `sendAccountHotkeyNotice`（`:583`），由进城链路
//     `initial_town_route_packets.go:139` 的 `gameOptionNoticeSent` 闸门内触发，
//     走 `runtime_cmd_known=false` 的原始发送路径，不是任何请求处理器。
//   - 厂商构造器与体布局（`AccountSettingsPacketBuilder.BuildHotkeyOptionBody`,
//     `ServerS4A21-master/.../Network/Builders/AccountSettingsPacketBuilder.cs:68`）：
//     `u8 key_type | u32 LE blob_len | blob_len 字节的 u16 LE 槽位[]`，即 5+2N 字节。
//     我们的 `buildAccountHotkeyNoticeBody` 与该布局**逐字节同构**，所以这不是
//     "被截断的报文"，也不是"只是 msg_id 撞号"，而是同一报文的**不同槽位载荷**：
//     其一，厂商登录相位（`AccountSettingsPacketBuilder.cs:23`）只带
//     `AccountSettings.ExtractAccountScopedHotkeySlots`＝**1 个槽位**
//     （`AccountSettings.cs:14 AccountScopedHotkeySlotCount = 1`）⇒ 体 7 B；
//     其二，厂商选角初始化相位（`Network/Builders/Init/HotkeyConfigBodyBuilder.cs:13`
//     ＋ `Game/SelectCharacter/NewCharacterInitSequence.cs:57 Raw(0x00, 0x01C7)`）
//     带**整份角色键位布局** ⇒ 默认 99 槽（`AccountSettings.cs:105 DefaultHotkeySlots`
//     = 198 B）⇒ 体 203 B。
//     我方两种体都出现过（线上 `body_len` 分布 112×203 `character_default_layout`
//     ＋ 87×7 `account_op198_prefix`）。
//   - 厂商 C# A21 服务端**抓包实证**（决定性证据，`_vendor-re` 第 3 轮复核）：
//     `86a219.3服务器/9.3/ServerS4A21-master/dist/win-x64/packet_log.txt`
//     （`=== DfoServer packet capture ===` 格式，2026-09-03）里 `type=0x01C7`
//     共 4 条 SEND，**两种体都出现过**：
//     其一 `len=670 body=(655B)`：`00 | 8A 02 00 00 | "I01\t2\nI02\t0\nI03\t3\n…"`
//     ＝ keyType 0 ＋ u32 650 ＋ **ASCII 文本键位表**（`S4A21_CN` 客户端抓包
//     `CipherPacket.log:799` 的 `packet_type=0x01C7 body_size=655` 与之逐字节一致，
//     且 `GameLog.log:1818` 记作 `[NOTI RECV] 455 (Size : 655)`）；
//     其二 `len=218 body=(203B)`：`00 | C6 00 00 00 | 02 00 00 00 03 00 01 00 4E 00 39 00 …`
//     ＝ keyType 0 ＋ u32 198 ＋ **二进制 u16 槽位表**。
//     **注意：我方 `character_default_layout` 的 203 B 体与厂商这条 203 B 体
//     逐字节相同**（我方 `hex=00c701db…0000c600000002000000030001004e0039…`）。
//     所以 455 本身不是"我们自造的包"，203 B 形状更不是"错体"。
//   - 由此得出本轮的关键判断（回归根因）：客户端要的是**整表**（203 B 二进制或
//     655 B ASCII 二者皆可），而我方线上默认发的是 `account_op198_prefix`
//     的 **7 B 体** `00 | 02 00 00 00 | "I0"` —— 只把 ASCII 表的前 2 字节当整表发出去，
//     是**截断表**。厂商只在**登录相位**用这个 2 字节前缀
//     （`AccountSettingsPacketBuilder.cs:16-23` + `AccountSettings.cs:81
//     ExtractAccountScopedHotkeySlots`），且那条路径在 C# 仓里无调用者；
//     **进城相位厂商从不发截断表**。客户端拿到截断表无法装配键位，才回落渲染
//     公告/任务书面板 ⇒ 与"屏蔽 455 面板即消失"的 A/B 结果自洽
//     （屏蔽后客户端改用自带默认表）。**待验证项**：现有 A/B 是在 7 B 档跑的，
//     尚未单独验证"203 B 厂商同字节体是否也会弹面板"；这决定应选 (a) 保持屏蔽
//     还是 (b) 改发整表。见交接报告。
//   - 口径澄清：**190~206 B↔806 B 那组数字不是 455**，那是 `173`/`0x00AD`
//     （游戏选项）——线上 173 的 `len=` 分布是 173×206 与 87×806；厂商 Go 出货版
//     的 173 恒为 806，厂商 C# 抓包的 173 为 762。与 455 无关。
//   - 厂商对拍（为什么屏蔽＝厂商等价）：厂商实际出货的 Go 服务端
//     `D:\DNF90-test\server\runtime\logs\packet_log.txt`（2026-09-25 09:12 →
//     2026-09-26 22:40，65283 行 / 6474 条 `SEND kind=game-upper` / 102 个
//     distinct msg_id）里，`msg_id=455` **全库 0 行**（SEND=0 且 RECV=0）；
//     同库 `msg_id=173` 有 15 条 SEND —— 后者证明该日志**确实会记录 class0
//     noti**，所以 455 的 0 是"真的没发"，不是"日志不记 noti"。
//     C# 参考实现里唯一"活着"的 455 调用点是选角初始化序列
//     （`NewCharacterInitSequence.cs:57`）；登录相位那个
//     （`LoginHandler.cs:158 SendAccountSettingsOnLoginAsync`，体 7 B）
//     在 C# 仓里**没有任何调用者＝死代码**（全仓 grep 只有定义行），
//     运行时真正执行的是 `LoginHandler.cs:99 → SendSelectScreenGameOptionAsync`，
//     **只发 `0x00AD`**。我方 go-server 是厂商 Go 出货版的分支
//     （模块路径 `longheng.io/server`），故"厂商等价"以 Go 出货版为准 ⇒
//     出货版从不发 455，我们发 ⇒ 触发点就是"我们比厂商多发"。
//     厂商 9.16 更新清单第 62 条「修复无公会角色登录时弹出空公告」是同一处
//     症状在厂商侧的修法。
//   - 自动化 A/B（全屏截图判读，每轮零人工）：屏蔽 `455` ⇒ 面板消失（`boss_mad≈78`，
//     截图=干净城镇，r28/r29/r30 三次一致）；不屏蔽 ⇒ 面板出现（`boss_mad≈41.6`）。
//     **判据方向提醒：`boss_mad` 越小＝面板在屏上（模板命中），越大＝干净。**
//   - 不存在"客户端死等 455"（若成立，屏蔽就不是厂商等价而是新的卡死源）：
//     `ENUM_NOTIPACKET_HOTKEY_OPTION` 是**单向下行通知**，厂商侧没有任何请求-应答
//     配对（`NewCharacterInitSequence.cs:57` 只是把它排进下行序列），客户端也从不
//     请求它 —— 我方 130+ 份 `packet_log*.txt` 全库 `RECV … msg_id=455` **0 行**，
//     即客户端从未发过 455，也就无从"等"它。
//     线上无死等症状：`dnf90.out`（13.5 MB，2026-09-24 起）里
//     `timeout`/`timedout`/`reconnect`/`dead`/`stuck`/`stalled`/`waiting for`/
//     `no reply`/`retry` 关键字计数**全为 0**，且没有任何 `level=ERROR` 行。
//     客户端重发模式与屏蔽无关：`593` 的连续重发（单连接最长 34 连发）在屏蔽上线
//     **之前**（2026-09-24/25 日志）就已大量存在，而屏蔽事件最早只出现在
//     `packet_log.20260927-010526.txt`；同一 `593` 在厂商 Go 日志里也有 36 次 RECV
//     ⇒ 那是客户端既有行为，不是"在等 455"。
//   - 现场截图：`login_na1_town.png`（2026-09-27 10:17，现网二进制 + 屏蔽生效中）
//     画面干净、无决斗场/任务书面板，且 UI 可正常点开。
//   - 只列 455：`102`(HATCH_CREATURE, 22 B) 是**宠物/生物场景状态**的真实功能包
//     （3 条既有用例依赖它），绝不能默认挡。
//
// 口径：默认阻断 `455`。`DNFBRIDGE_BLOCK_VENDOR_ABSENT_SENDS` 可改名单；
// 取 `none` / `-` / `off` 时**完全不阻断**（回退到修复前行为，用于对照或紧急回滚）；
// 留空则**回落默认名单**（＝仍然阻断 455）。
//
// 注意：这里只挡"厂商根本不发"的包。厂商会发的包（例如 441/63/143/14/646）绝不列入，
// 它们即使在我们这里出现也只按厂商的语义发送。
const vendorAbsentSendBlockEnvKey = "DNFBRIDGE_BLOCK_VENDOR_ABSENT_SENDS"

// defaultVendorAbsentSendBlock 是逐字节对拍厂商日志后的"厂商全库零收发"清单。
const defaultVendorAbsentSendBlock = "455"

var (
	vendorAbsentBlockOnce   sync.Once
	vendorAbsentBlockParsed upperSendFilter
)

func parseVendorAbsentSendBlock(raw string) upperSendFilter {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none", "-", "off":
		return upperSendFilter{}
	default:
		return parseUpperSendFilter(raw)
	}
}

func currentVendorAbsentSendBlock() upperSendFilter {
	vendorAbsentBlockOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv(vendorAbsentSendBlockEnvKey))
		if raw == "" {
			raw = defaultVendorAbsentSendBlock
		}
		vendorAbsentBlockParsed = parseVendorAbsentSendBlock(raw)
	})
	return vendorAbsentBlockParsed
}

// blockVendorAbsentSend 在发包出口挡掉"厂商从不发送"的包。返回 true 表示调用方
// 必须直接返回：不写连接（否则客户端会真的收到这块体）。
//
// 注意（2026-09-27 复核，纠正旧注释）：被挡下的包**仍然会在 packet_log 里留下
// `game-upper-send-meta` 与 `SEND kind=game-upper … msg_id=455` 两行**。
// 顺序是 `sendGameUpperRawClassCodec` 先写这两行日志（`game_wire.go:473`/`:487`），
// 再调 `writeGameUpperPacketLocked`，阻断发生在更里层（`game_dproto.go:190`）。
// 因此 packet_log 里出现 `SEND … msg_id=455 … len=23` **不等于客户端收到了它**；
// 判据要看同一时刻有没有紧随的 `game-upper-vendor-absent-blocked` 事件
// （线上两者成对出现，例如 `packet_log.txt:1271` 的 SEND 与 `:1272` 的 blocked）。
// 排查时不要只看 `SEND` 行就下"我们发出去了"的结论。
func (s *Service) blockVendorAbsentSend(session *gameSession, classification byte, msgID uint16, packetLen int) bool {
	block := currentVendorAbsentSendBlock()
	if block.Empty() || !block.Suppresses(classification, msgID) {
		return false
	}
	s.logGameEvent(session, "game-upper-vendor-absent-blocked",
		"msg_id", msgID,
		"classification", classification,
		"packet_len", packetLen,
		"switch", vendorAbsentSendBlockEnvKey,
		"block_list", defaultVendorAbsentSendBlock,
		"reason", "vendor_packet_log_has_zero_send_and_zero_recv_for_this_opcode")
	return true
}
