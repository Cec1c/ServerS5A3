package dnfbridge

import (
	"os"
	"strings"
	"time"
)

// ── 锦标赛/大会族（dungeon 120 黄龙 / 121 青龙）总开关 ─────────────────────────
//
// 现状（默认档 = 逐字节今天）：
//   - `[required item]` 不参与准入；
//   - `[tournament enemies]` / `[tournament start area]` 不参与铺怪；
//   - 副本不判清 —— 因为这两张图没有任何 `[monster]` 段，房间在构造期就被判成
//     `Cleared`（worldmap/dungeon_session.go:748-750），客户端进场即"通关"。
//
// 显式打开后才启用厂商口径的锦标赛路径。**开关解析刻意写在本文件里、直接用
// os.Getenv 读**，不改 `options.go`（该文件此刻由另一子代理并发修改，撞车成本高）。
// 本仓已有同样先例：`initial_town_login_owner.go:457` 的
// `currentLoginFallbackKeepsTown()` 也是直接 os.Getenv。
//
// 取值口径与 `readHellBlockedSlotsMode` / `readDungeonEntrySelectionMode` /
// `currentLoginFallbackKeepsTown` 一致：**只有那一个字面量才生效**，
// 未设置 / 空 / 其它任何值都是默认档。
//
// 口径收紧记录（本批）：C 批第一版还接受 `on` / `1` / `true`，与本会话其余六个
// 开关（`on` / `allow` / `preserve` / `project` / `record` / …，各自只认一个字面量）
// 不一致，已收紧成**只认 `vendor`**。收紧只影响"多认了几个别名"，不影响任何
// 默认档行为：未设置/空/`off`/未知值在收紧前后同样是默认档。
const dungeonTournamentEnvKey = "DNFBRIDGE_DUNGEON_TOURNAMENT"

// dungeonTournamentModeOff 是默认档（今天的行为）。
const dungeonTournamentModeOff = "off"

// dungeonTournamentModeVendor 是厂商口径档：准入 + 4 轮 16 队赛制 + 包序列 + 关默认通关。
// 这是**唯一**启用值。
const dungeonTournamentModeVendor = "vendor"

// currentDungeonTournamentMode 返回开关档位。默认（未设置/空/未知值）恒为 off。
func currentDungeonTournamentMode() string {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(dungeonTournamentEnvKey)))
	if value == dungeonTournamentModeVendor {
		return dungeonTournamentModeVendor
	}
	return dungeonTournamentModeOff
}

// dungeonTournamentEnabled 报告本次进程是否启用锦标赛族实现。
func dungeonTournamentEnabled() bool {
	return currentDungeonTournamentMode() == dungeonTournamentModeVendor
}

// ── 权威 opcode（**不要**改成 dnfenum 里的同名常量）──────────────────────────
//
// 厂商 `Network\Core\PacketTypesA21.cs` 是**厂商侧**的唯一真源：
//
//	1689: TOURNAMENT_REWARD_SELECT_STATE = 0x01CA, // 458
//	1690: TOURNAMENT_REWARD_SELECT       = 0x01CB, // 459
//
// NOTI 侧（同一文件）：
//
//	TOURNAMENT_INFO        = 0x0183, // 387
//	TOURNAMENT_MAP_INFO    = 0x0184, // 388
//	TOURNAMENT_CLEAR_REWARD= 0x0185, // 389
//
// ⚠️ **但真机客户端不是厂商 A21 表**。本批（收尾接线）核实到：本机线上 DNF.exe
// （`8A7AF614769D0B47D0647B77C33BCE725F2F20258651C01FC6C22AC1DB44AA4D`，
// 与 86JP 生产树 `changlog.md:400` 钉的是同一个二进制）的派发表把锦标赛族整体
// **前移 1**：
//
//	NOTI 0182 = 对阵表（厂商 387）      → a21NotiTournamentInfo        = 386
//	NOTI 0183 = 房间/地图（厂商 388）  → a21NotiTournamentMapInfo     = 387
//	NOTI 0184 = 终止/奖励（厂商 389）  → a21NotiTournamentClearReward = 388
//	CMD  01C9 = 选奖态（厂商 458）     → a21CmdTournamentRewardSelectState = 457
//	CMD  01CA = 选奖  （厂商 459）     → a21CmdTournamentRewardSelect      = 458
//
// 证据（**同族实机验收 + 客户端二进制同一性**，不是厂商代码）：
//  1. `90原神开源0.1.8\dnf90-source-oneclick-main\go-server\internal\services\dnfbridge\dungeon_tournament.go:22-30`
//     的逆向注释：`// The current 90CN NoPack.exe dispatcher registers the native
//     tournament bracket reader at 0182 and the tournament room reader at 0183.
//     90US uses the same bodies one opcode later, so these values must not be
//     copied from that server.`
//  2. 同族 `changlog.md`（2026-08-19 段）的实机记录：用厂商号 0183/0184 发出去
//     导致客户端**越界守卫崩溃**（`0x0216F100`，把 dungeon id 123 当成了房间 X 坐标）；
//     改成 0182/0183/0184 之后实机"四个对手全部渲染并可击杀"。
//  3. 同族 `changlog.md` 的实机记录：奖励界面卡死时客户端发的是 **bodyless 457**；
//     回答 9 字节选奖态之后实机"returns to Shonan and advances quest 6840"。
//  4. 本仓 `dnfenum` 的枚举值（457/458）与上面两条**完全一致**
//     （`internal/modules/dnf/dnfenum/cmdpacket.go:466-467`，且
//     `CmdPacketName` 把 457/458 分别命名为 TOURNAMENT_REWARD_SELECT_STATE / SELECT）。
//     ⇒ C 批"厂商 458/459 才是权威、本仓枚举 −1 是错的"这个结论**对真机客户端是反的**。
//
// 因此本批把**上线值**改成真机值；厂商值保留在下面 `a21Vendor*` 里作为对拍锚点。
// 默认档下这些常量一个都不会被读到（`dungeonTournamentEnabled()` 假）。
const (
	// a21NotiTournamentInfo 是真机 NOTI 对阵表号（厂商 `TOURNAMENT_INFO` 387 前移 1）。
	a21NotiTournamentInfo uint16 = 386
	// a21NotiTournamentMapInfo 是真机 NOTI 房间/地图号（厂商 `TOURNAMENT_MAP_INFO` 388 前移 1）。
	a21NotiTournamentMapInfo uint16 = 387
	// a21NotiTournamentClearReward 是真机 NOTI 终止/奖励号（厂商 `TOURNAMENT_CLEAR_REWARD` 389 前移 1）。
	a21NotiTournamentClearReward uint16 = 388
	// a21CmdTournamentRewardSelectState 是真机 CMD 选奖态号
	// （厂商 `TOURNAMENT_REWARD_SELECT_STATE` 0x01CA=458 前移 1 ⇒ 0x01C9=457）。
	// 与 `dnfenum.CmdPacketTournamentRewardSelectState` 同值，刻意不直接引用以获得编译期冗余。
	a21CmdTournamentRewardSelectState uint16 = 457
	// a21CmdTournamentRewardSelect 是真机 CMD 选奖号
	// （厂商 `TOURNAMENT_REWARD_SELECT` 0x01CB=459 前移 1 ⇒ 0x01CA=458）。
	// 与 `dnfenum.CmdPacketTournamentRewardSelect` 同值。
	a21CmdTournamentRewardSelect uint16 = 458
	// a21CmdDieAICharacter 对应厂商 CmdPacketTypeA21.DIE_AI_CHARACTER = 0x0090 = 144。
	// 本仓 dnfenum.CmdPacketDieAiCharacter 已经是 144（无偏移），这里显式写出以固定口径。
	// 真机客户端同样无偏移（该族不在 0x01Cx/0x018x 那段被前移的区段里）。
	//
	// ⚠️ **赛制推进不用它**（本批取证结论）：厂商全树对 CMD 144 只有
	// `Network\Core\PacketTypesA21.cs:1375` 那一行枚举声明 —— 没有派发表项、
	// 没有 parser、没有 handler。厂商推进轮次走的是 CMD `DIE_MONSTER`（0x0027=39）：
	// `GameProtocolHandler.cs:708` → `DungeonHandler.cs:189` →
	// `DungeonCombatHandler.cs:66` → `DungeonKillApplicationService.cs:170/:339`
	// → `DungeonInstance.cs:559-593` → `DungeonKillApplicationService.cs:444`
	// → `TournamentDungeonCoordinator.cs:126` → `TournamentDungeonRuntime.cs:257-324`。
	// 本仓同理：赛制由 `dungeon_combat_runtime.go` 的 op39 击杀路径自驱
	// （见 dungeon_tournament_flow.go）。保留该常量只为固定协议号口径。
	a21CmdDieAICharacter uint16 = 144
)

// 厂商 A21 表里的原值。**只用于对拍与文档**，不参与任何发送/派发 ——
// 真机客户端对这一段整体前移 1，照厂商值发会让客户端越界（见上面的取证据）。
const (
	a21VendorNotiTournamentInfo        uint16 = 387
	a21VendorNotiTournamentMapInfo     uint16 = 388
	a21VendorNotiTournamentClearReward uint16 = 389
	a21VendorCmdTournamentRewardState  uint16 = 458
	a21VendorCmdTournamentRewardSelect uint16 = 459
	// a21TournamentOpcodeShift 是真机相对厂商表的位移（真机 = 厂商 + 该值）。
	a21TournamentOpcodeShift = -1
)

// 锦标赛常量（厂商 Game\Dungeon\Tournament\TournamentParticipantRewardState.cs:22-24）。
const (
	tournamentCardTypeCount = 2
	tournamentCardsPerType  = 2
	tournamentPartySlotMax  = 4
)

// tournamentResultCardKeyCount 是 `[result card]` 的键数：0..5 共 6 个
// （厂商 TournamentDungeonDefinition.cs:346-354 要求齐全）。
const tournamentResultCardKeyCount = 6

// currentTournamentClearRewardBodyHeaderSize 是 NOTI 389 体头部长度：
// `u8 CompletedRounds + u32 RewardExperience + u8 0` = **6** 字节
// （厂商 `TournamentPacketBuilder.cs:85-87` 的三行 writer）。
//
// ⚠️ 这个数字在本批一度被两处测试算成 8（把结尾那个 `u8 0` 当成 2 字节），
// 于是"逐段 walk 从错误偏移开始"并误判每排的卡数。以厂商三行 writer 为准：1+4+1=6。
const currentTournamentClearRewardBodyHeaderSize = 6

// tournamentResultCardChampionKey 是"打满 4 轮"那一排卡用的 result key
// （厂商 TournamentDungeonApplicationService.cs:511-521 `resultKey: 5`）。
const tournamentResultCardChampionKey int64 = 5

// tournamentResultCardEmptyRewardKey 是"没打满 4 轮"时下排卡的哨兵 key。
// 非冠军时厂商直接把下排写成空卡（`TournamentDungeonApplicationService.cs:496-505`
// `CreateEmptyReward`），**不读权重表**；这个哨兵只用于文档与断言，不参与查表。
const tournamentResultCardEmptyRewardKey int64 = -1

// tournamentEmptyRewardItemID 对应厂商 `CreateEmptyReward()`
// （TournamentDungeonApplicationService.cs:526-532）的 `ItemId = -1`。
const tournamentEmptyRewardItemID int64 = -1

// currentTournamentRewardAutoSelectDelay 是选奖自动选择的三秒绝对超时。
//
// 厂商出处：`Network\Handlers\Dungeon\TournamentDungeonCoordinator.cs:40`
//
//	private const int RewardSelectionTimeoutMilliseconds = 3000;
//
// 它在 `:424-425` 被用作 `deadlineUtc = DateTime.UtcNow.AddMilliseconds(...)`，
// 在 `:493-549` 到期后对每个"启用且尚未选择"的卡类选 `cardIndex: 0`。
// **不要**改成别的数值：这是厂商常量，不是可调参数。
const currentTournamentRewardAutoSelectDelay = 3000 * time.Millisecond

// ── 选奖应答档（458 选中投影）────────────────────────────────────────────────
//
// `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK`：**默认档（零值）= `select`**（2026-09-29 起）
// —— 客户端自己发来的 2 字节选奖（solicited）**必须**收到 458 选中投影（7 字节）；这是真机
// 90CN 客户端拿"这张卡被哪个队伍槽位拿走"的**唯一**下行。
// `select+timer` = **危险档**（必须显式写）：额外允许 3s 兜底自动选奖路径也回 458 ——
// 该档在 2026-09-29 06:05Z 把线上客户端打崩（RST 于最后一条 458 之后 1.265s），
// 4/4 局实证，见下面 `REWARD_SOLICIT` 的取证块。
// `state` = 一键完全回退：任何路径都不发 458，只回 9 字节 457 选奖态。
//
// 现场（操作者报障）：「青龙大会还是卡在翻牌 没有结算只能返回城镇 但是在聊天栏发放了物品」。
// 国内服 `conn game-000009`（char 1，dungeon 121，2026-09-27T18:26Z）逐事件：
//
//	tournament-round-sync completed_round=4 tournament_completed=true
//	tournament-clear-experience-committed reward_experience=500000
//	tournament-clear-committed run_status=completed
//	settlement-entry-op31-suppressed terminal=2 champion=true
//	tournament-clear-reward-sent msg_id=388 body_len=48
//	RECV type=457 body_len=0                       ← 客户端问选奖态
//	SEND 457 body_len=9  {01 01 ff ff ff 01 ff ff ff}
//	3s ⇒ auto-select-fired ⇒ SEND 457（又一条 9 字节）+ 落库两张卡
//	… 之后 **再也没有任何上行**，直到玩家自己 op42 返回城镇
//
// 全日志 `grep -a 'type=458'` = **0 条**：服务端从不回这条握手，客户端的翻牌界面
// 永远推进不了，奖励只在聊天栏出现（3s 兜底自动选奖直接落库）。这条漏掉的应答就是根因。
//
// 真机二进制取证（`H:\86JP启动器\90CN\地下城与勇士\DNF.exe`，sha256
// `8A7AF614769D0B47D0647B77C33BCE725F2F20258651C01FC6C22AC1DB44AA4D`，imagebase 0x400000；
// 下面 sub_ 均为**绝对 VA**，RVA = VA − 0x400000，用 `.dsh-disasm/disasm.exe` 反汇编）：
//
//  1. 派发表注册（RVA 0x18FC711 起，形状 `push flag; push handler; push opcode; mov ecx,reg; call Register`）：
//     opcode 0x13D → handler 0x1CF5860
//     opcode 0x1C9 (457) → handler 0x1CF4FD0
//     opcode 0x1CA (458) → handler 0x1CF5010      ← **客户端确实有 458 下行 handler**
//     opcode 0x1BB → handler 0x1CFC4D0
//     ⇒ 旧注释里"真机 90CN 把 01CA 当上行命令、没有下行 handler"这一条**是错的**，
//     本批用注册表实证推翻（458 只是**双向**包：上行 2 字节、下行 7 字节）。
//  2. 457 reader（sub_1CF4FD0，RVA 0x18F4FD0）：派发器吃掉首字节成功后，
//     `mov esi,8` / `lea eax,[ebp-1]` / `push 1` / `call ReadByte`(RVA 0x30E3970，读 1 字节)
//     循环 8 次 ⇒ **恰好消费 8 字节，全部丢进同一个 1 字节栈变量**（状态内容对 UI 无影响）。
//     ⇒ 9 字节选奖态与"这局有没有真卡"无关，逐字节形状必须保持 9 字节，否则读越界。
//  3. 458 reader（sub_1CF5010，RVA 0x18F5010）：`call ReadByte` → N1，循环 N1 次读 1 字节
//     → `SetSlot(i, value, row=0)`；再 `call ReadByte` → N2，循环 N2 次 → `row=1`。
//     N1=N2=2（两组各两张卡）时正是 `1 | 2 | s s | 2 | s s` = **7 字节**，即厂商
//     `TournamentPacketBuilder.BuildTournamentRewardSelection` 与族内 0.1.8
//     `buildCurrentDungeonTournamentRewardSelection` 的形状。
//  4. SetSlot（sub_1D81E00，RVA 0x1D81E00）：`cmp byte [ebp+0x10],0`（row）、
//     `mov eax,[esi+4*idx+0xcc]`（取该排第 idx 张卡）、`cmp dword [eax],-1`（**没人拿走才写**）、
//     `cmp cl,0xff`（0xFF = 空位 ⇒ 不动）、`movsx ebx,cl` + `mov [eax],ebx`
//     ⇒ 逐卡把"拿走它的队伍槽位"写回卡槽并刷新该卡 UI（`slot+0x54` 是卡数）。
//  5. 客户端自己的 458 上行（RVA 0x1D81860，两组各一段）：`push 0x1CA` 后依次写 row(0/1)
//     与 index ⇒ **2 字节 (cardType, cardIndex)**，与本仓 `handleCurrentTournamentRewardCommand`
//     的 2 字节分支同形。
//
// ⚠️ 458 **只允许回在 solicited 路径上**。线上实证（2026-09-27 22:23–22:25 与 23:1x，
// dungeon 123）：**未受邀**的 458 之后客户端异常退出（`exception code=0xC0000005 address=03B10747`
// / `read: connection reset by peer`）。被推翻的只是"458 没有下行 handler"，不是这条崩溃观察
// —— 2026-09-29 06:05Z 那一局（`conn_id=game-002721`）以 `REWARD_SOLICIT=state-request`
// 的默认档把同一个崩溃**复现了第 4 次**（RST 于最后一条 458 之后 1.265s），
// 而 6 个"一条 458 都没发"的会话全部存活。因此默认档已于 2026-09-29 收回：
// `REWARD_ACK` 零值 = `select`（只认 solicited），`REWARD_SOLICIT` 零值 = `legacy`
// （bodyless 457 不算受邀）—— 详见下面 `REWARD_SOLICIT` 的取证块。
const dungeonTournamentRewardAckEnvKey = "DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK"

// dungeonTournamentRewardAckValueSelectTimer 是默认档（零值）：solicited 选奖回 458 选中投影，
// 且当客户端先发来体长为 0 的 457（证明翻牌界面已开、在等选奖）之后，3s 兜底自动选奖也回 458。
const dungeonTournamentRewardAckValueSelectTimer = "select+timer"

// dungeonTournamentRewardAckValueSelect 是上一版默认（回退档）：只有 solicited 路径回 458 选中投影。
const dungeonTournamentRewardAckValueSelect = "select"

// dungeonTournamentRewardAckValueState 是回退档：逐字节回到改动前（任何路径都不发 458）。
const dungeonTournamentRewardAckValueState = "state"

// dungeonTournamentRewardAckMode 是 dungeonTournamentRewardAckEnvKey 的归一化取值。
type dungeonTournamentRewardAckMode int

const (
	// dungeonTournamentRewardAckSelect 是默认档（**零值 = 安全档**，2026-09-29 起）：
	// **只有** solicited（客户端自己发来的 2 字节选奖）才回 458 选中投影。
	//
	// 为什么把默认从 `select+timer` 收回到这里：`select+timer` 会让"3s 兜底自动选奖"
	// 这条路也发 458，而 90CN 客户端收到**未经它自己索要**的 458 会在 1~2 秒内死掉
	// （0xC0000005 / RST）—— 线上 4/4 局实证，见下面的取证块"未受邀 458 ⇒ 客户端崩溃"。
	dungeonTournamentRewardAckSelect dungeonTournamentRewardAckMode = iota
	// dungeonTournamentRewardAckSelectTimer 是**危险档**（必须显式写 `select+timer` 才开）：
	// ① solicited 选奖照旧回 458 选中投影；
	// ② 客户端先发来体长为 0 的 457 之后，3s 兜底自动选奖也回 458 选中投影。
	// 这条正是 2026-09-29 06:05Z `game-002721` 那一局把客户端打崩的档位（RST 于 +1.265s）。
	dungeonTournamentRewardAckSelectTimer
	// dungeonTournamentRewardAckStateOnly 是一键完全回退档：任何路径都不发 458。
	dungeonTournamentRewardAckStateOnly
)

func (mode dungeonTournamentRewardAckMode) String() string {
	switch mode {
	case dungeonTournamentRewardAckStateOnly:
		return dungeonTournamentRewardAckValueState
	case dungeonTournamentRewardAckSelectTimer:
		return dungeonTournamentRewardAckValueSelectTimer
	default:
		return dungeonTournamentRewardAckValueSelect
	}
}

// answersSolicitedSelection 报告"客户端自己发来的 2 字节选奖要不要回 458 选中投影"。
// select+timer 与 select 都为真（= 非 state）。
func (mode dungeonTournamentRewardAckMode) answersSolicitedSelection() bool {
	return mode != dungeonTournamentRewardAckStateOnly
}

// answersTimerSelection 报告"3s 兜底自动选奖路径要不要回 458 选中投影"。
// **只有显式写出的 `select+timer` 危险档为真**；默认档 `select` 为假（发了会崩客户端）。
// 真正下发前还需要"客户端已发来体长为 0 的 457"这个前提，
// 那个前提由会话 latch（`gameSession.tournamentRewardStateRequested`）在发送点判断。
func (mode dungeonTournamentRewardAckMode) answersTimerSelection() bool {
	return mode == dungeonTournamentRewardAckSelectTimer
}

// readDungeonTournamentRewardAckMode 解析 `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK`。
// 字面量 `state` / `select+timer` 是显式档；**未设置 / 空 / 其它任何值都是默认（安全）档
// `select`**（与 `readHellBlockedSlotsMode` 同一口径：只有被承认的字面量才改变默认）。
//
// ⚠️ `select+timer` 在 2026-09-29 之前是**默认**档；因为它在 3s 兜底路径上会发出未经
// 客户端索要的 458、把 90CN 客户端打崩（4/4 局实证），它现在必须显式写出来才生效。
func readDungeonTournamentRewardAckMode(value string) dungeonTournamentRewardAckMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case dungeonTournamentRewardAckValueState:
		return dungeonTournamentRewardAckStateOnly
	case dungeonTournamentRewardAckValueSelectTimer:
		return dungeonTournamentRewardAckSelectTimer
	default:
		return dungeonTournamentRewardAckSelect
	}
}

// dungeonTournamentRewardAck 返回当前 Service 的归一化档位（零值 = select = 安全档）。
func (s *Service) dungeonTournamentRewardAck() dungeonTournamentRewardAckMode {
	if s == nil {
		return dungeonTournamentRewardAckSelect
	}
	return s.options.dungeonTournamentRewardAck
}

// ── "谁算受邀"（458 选中投影的受邀判据）────────────────────────────────────────
//
// `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_SOLICIT`：**默认档（零值）= `legacy`**（2026-09-29 起）
// —— 只有客户端自己发来的 **2 字节**选奖才算受邀；体长为 0 的 op457 **不算**。
// `state-request` = 上一版的档位（**危险，必须显式写**）：客户端发来体长为 0 的 op457
// 即视为"受邀"，于是 3s 兜底自动选奖（`tournamentRewardSelectionSourceTimer`）也能回
// 那条 7 字节 458 选中投影 —— 即使 `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK=select`。
//
// ⚠️ **2026-09-29 06:05Z 线上实测把这一档证伪了（症状从"卡住"变成"崩客户端"）**：
// `state-request` 于 2026-09-29 05:03:10 +0800 随二进制上线（`/opt/dnf90/bin/dnf90server`
// mtime 05:03:10 +0800，`systemctl show dnf90.service -p ExecMainStartTimestamp` 同刻，
// env 里**没有** `..._SOLICIT` 行 ⇒ 取默认 `state-request`）。下一局青龙大会
// `conn_id=game-002721`（char 1，dungeon 121，`t=2026-09-29T06:05:16.185Z` 起）逐行：
//
//	RECV ... type=457 ENUM_CMDPACKET_TOURNAMENT_REWARD_SELECT_STATE header_seq=143 body_len=0
//	     hex=01c9010d000000115a66a78f00            ← 客户端开口要选奖态
//	SEND ... msg_id=457 plain_body_len=9 hex=...0101ffffff01ffffff
//	3s ⇒ reward-auto-select-fired
//	SEND ... msg_id=458 plain_body_len=7 hex=...010200ff02ffff   t=06:05:19.229440582Z
//	SEND ... msg_id=458 plain_body_len=7 hex=...010200ff0200ff   t=06:05:19.272088076Z
//	RECV ... msg_id=690 ENUM_CMDPACKET_STATICS_RUNTIME_TING       t=06:05:20.426046166Z
//	EVENT kind=game-read-close ... error=read tcp ...: read: connection reset by peer
//	     t=2026-09-29T06:05:20.494985829Z          ← 客户端进程在最后一条 458 之后 1.265s 死掉
//
// 这不是孤例：全日志 7 条 `SEND msg_id=458` 分属 4 个会话
// （game-000003 / game-000034 ×2 / game-000007 ×2 / game-002721 ×2），
// **4/4 会话在收到 458 后 1~2 秒内 RST**；而**没有**收到 458 的 6 个会话
// （game-000006 / game-000016 / game-000009 / game-001151 / game-001204 / game-000012）
// 全部存活到玩家自己 op42 回城 —— 例如 game-001151 在 3s 兜底落奖后继续跑了
// `msg_id=1206` 心跳、`msg_id=123 CHARACTER_STATISTIC`、`RECV msg_id=42 GIVEUP_GAME`
// 与 `dungeon-completed-town-route-send`；game-001204 更在落奖后又跑了 6 分钟
// （`pet-satiety-settled ... source=timequeue_tick elapsed_ms=360006`）才以 `EOF` 正常关闭。
//
// 机制（真机取证，见 `dungeon_tournament_config.go` 的 458 reader/SetSlot 块）：
// 457 reader（`sub_1CF4FD0`）只是把 8 个字节全部丢进同一个栈变量、**不碰任何 UI 对象**；
// 458 reader（`sub_1CF5010`）走 `SetSlot`（`sub_1D81E00`），而 `SetSlot` 会
// `mov eax,[esi+4*idx+0xcc]` 解引用该排第 idx 张卡的指针。也就是说
// **"客户端发过 bodyless 457"并不等于"它已经把卡数组准备好了"**，只有客户端自己的
// 2 字节选奖（RVA 0x1D81860）才证明它进了可点状态 —— 而全日志
// `RECV ... type=458` = **0 条**（9/9 局只有 bodyless 457）。
//
// 因此默认档必须是 `legacy`：**不发未经索要的 458**。`state-request` 只留作 A/B 复现档。
//
// 本档**只**改变"3s 兜底路径能不能回 458"这一个判据；`REWARD_ACK=state` 仍然是一键回退到
// "任何路径都不发 458"。取值口径与全仓其它开关一致：**只有 `state-request` 那一个字面量
// 才进入危险档**，未设置 / 空 / 其它任何值都是默认（安全）档。
const dungeonTournamentRewardSolicitEnvKey = "DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_SOLICIT"

// dungeonTournamentRewardSolicitStateRequestValue 是唯一被承认的危险档取值。
const dungeonTournamentRewardSolicitStateRequestValue = "state-request"

// dungeonTournamentRewardSolicitLegacyValue 是默认（安全）档取值。
const dungeonTournamentRewardSolicitLegacyValue = "legacy"

// dungeonTournamentRewardSolicitMode 是 dungeonTournamentRewardSolicitEnvKey 的归一化取值。
type dungeonTournamentRewardSolicitMode int

const (
	// dungeonTournamentRewardSolicitLegacy 是默认档（**零值 = 安全档**）：
	// 只有客户端自己发来的 2 字节选奖才算受邀 ⇒ 3s 兜底路径一个字节的 458 都不发。
	dungeonTournamentRewardSolicitLegacy dungeonTournamentRewardSolicitMode = iota
	// dungeonTournamentRewardSolicitStateRequest 是**危险档**：bodyless 457 也算受邀。
	// 显式写 `state-request` 才生效；它复现的是 2026-09-29 06:05Z 打崩客户端的那一档。
	dungeonTournamentRewardSolicitStateRequest
)

func (mode dungeonTournamentRewardSolicitMode) String() string {
	if mode == dungeonTournamentRewardSolicitStateRequest {
		return dungeonTournamentRewardSolicitStateRequestValue
	}
	return dungeonTournamentRewardSolicitLegacyValue
}

// treatsStateRequestAsSolicitation 报告"客户端体长为 0 的 457 请求算不算受邀"。
func (mode dungeonTournamentRewardSolicitMode) treatsStateRequestAsSolicitation() bool {
	return mode == dungeonTournamentRewardSolicitStateRequest
}

// readDungeonTournamentRewardSolicitMode 解析 `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_SOLICIT`。
// 只有字面量 `state-request` 是危险档；未设置 / 空 / 其它任何值都是默认（安全）档
// `legacy`（与 `readDungeonTournamentRewardAckMode` / `readHellBlockedSlotsMode` 同一口径）。
func readDungeonTournamentRewardSolicitMode(value string) dungeonTournamentRewardSolicitMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonTournamentRewardSolicitStateRequestValue) {
		return dungeonTournamentRewardSolicitStateRequest
	}
	return dungeonTournamentRewardSolicitLegacy
}

// dungeonTournamentRewardSolicitModeFromEnv 直接读 env（不经过 options.go 的集中定义 ——
// 与本文件 `currentDungeonTournamentMode` 同一做法，避免与并发改 options.go 的批次撞车）。
func dungeonTournamentRewardSolicitModeFromEnv() dungeonTournamentRewardSolicitMode {
	return readDungeonTournamentRewardSolicitMode(os.Getenv(dungeonTournamentRewardSolicitEnvKey))
}

// tournamentRewardSelectionSource 把"谁在选奖"写进类型里（不留字符串判断的缝）：
//
//	tournamentRewardSelectionSourceClient = 客户端自己发来的 2 字节选奖（**solicited**）
//	    ⇒ 修复档下先回 458 选中投影，选奖未完成时再补 9 字节 457 选奖态；
//	tournamentRewardSelectionSourceTimer = 3s 兜底自动选奖（**unsolicited**）
//	    ⇒ 永远不发 458（未受邀 458 之后的客户端异常退出实证），只沿用今天的 457 应答。
type tournamentRewardSelectionSource int

const (
	tournamentRewardSelectionSourceClient tournamentRewardSelectionSource = iota
	tournamentRewardSelectionSourceTimer
)

// String 只用于日志字段（`source=client` / `source=timer`，与改动前逐字一致）。
func (source tournamentRewardSelectionSource) String() string {
	if source == tournamentRewardSelectionSourceTimer {
		return "timer"
	}
	return "client"
}

// solicited 报告这次选奖是不是客户端自己发起的（决定能不能回 458 选中投影）。
func (source tournamentRewardSelectionSource) solicited() bool {
	return source == tournamentRewardSelectionSourceClient
}

// 赛制常量（厂商 Game\Dungeon\Tournament\TournamentDungeonRuntime.cs:402-500）。
const (
	// tournamentNpcTeamCount 是 NPC 队数量：15 支 NPC + 1 支玩家 = 16 队单败淘汰 = 4 轮。
	tournamentNpcTeamCount = 15
	// tournamentTeamCount 是首轮队伍总数。
	tournamentTeamCount = tournamentNpcTeamCount + 1
	// tournamentRoundCount 是轮数。
	tournamentRoundCount = 4
	// tournamentFinalRound 是决赛轮号（该轮 actorType 升格为 8/3）。
	tournamentFinalRound = 4
	// tournamentRequiredStartAreas 是每个 PartyCount 至少需要的出生点数
	// （TournamentDungeonDefinition.cs:472-477）。
	tournamentRequiredStartAreas = 2
)
