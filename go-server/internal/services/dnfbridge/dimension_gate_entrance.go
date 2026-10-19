package dnfbridge

import (
	"fmt"
	"os"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 以下文档和 806/8 字节实现仅适用于旧厂商档。公开 v84 EXE 的实际通知是
// class0/812（0x01049E00），读取 u32/u32/u8。v141 已确认这三个字段为基础已用、
// 额外可用、每周隐藏领主计数，但门控和每周状态尚未上线。official-24.9 在发送入口跳过
// 旧档；不能把旧默认 5/0 冒充共享持久计数，也不能直接改 opcode 并补一个猜测字节。
//
// 旧档背景：操作者已知问题清单第 67 条，魔界裂缝（次元门 / DimensionGate）每日入场次数在客户端
// 显示 **0/0**。根因不是数值算错，而是这一条下行报文我方**完全没有实现**：
// `grep -i 'DimensionGate|dimensiongate|dimension_gate' go-server/**/*.go` 零命中，
// 客户端拿不到任何 NOTI，只能保持初值 0/0。
//
// 厂商依据（本机厂商源码，**S4A21-完整包-20260923\源码\服务端\Server\DfoServer\**）：
//
//  1. 报文：`Network\Core\PacketTypesA21.cs:813`
//     `DIMENSION_GATE_ENTRANCE_INFO = 0x0326, // 806：ENUM_NOTIPACKET_DIMENSION_GATE_ENTRANCE_INFO`
//  2. 包体：`Network\Builders\Init\DimensionGateEntranceInfoBodyBuilder.cs:49-55`
//     ```csharp
//     internal static byte[] Build(int remainingCount, int extraCount) {
//     var writer = new GamePacketWriter();
//     writer.WriteUInt32((uint)Math.Max(0, remainingCount));
//     writer.WriteUInt32((uint)Math.Max(0, extraCount));
//     return writer.ToArray();
//     }
//     ```
//     即**固定 8 字节**：`u32 LE remaining_count + u32 LE extra_count`，两值都先 `Math.Max(0, ·)`。
//  3. 取值：同文件 `:38-45` —— `DimensionGateEntryLimitConfigProvider.Get()` 拿到默认档，
//     再交给 `DungeonEntryLimitService.LoadDimensionGateLimit(characterId, default, extra)`。
//  4. 配置：`Game\Dungeon\DimensionGateEntryLimitConfigProvider.cs:28`
//     路径 `etc/dimensiongate.etc`；`:29-30` fallback `DailyDefaultEnterCount=5` /
//     `DailyDefaultExtraEnterCount=0`；`:44-54` 按 tag `daily default enter count`、
//     `daily default extra enter count` 取**第一个整数**，读不到才回落 fallback；
//     `:15-18` 构造时同样 `Math.Max(0, ·)`。
//  5. 发送时机：`Network\Builders\Init\InitPacketBuilderRegistry.cs:106`
//     `Register(new DimensionGateEntranceInfoBodyBuilder(_database));`
//     注册顺序即发送顺序：`UserPosition`(101) → `Cera`(102) → `PetCreatureWelcomeMessage`(103)
//     → `UnitedServerFriendInfo`(104) → `WeddingInfo`(105) → **DimensionGateEntranceInfo(106)**
//     → `StrikerSupportTagCharacter`(107)。落在登录/进城的 init 包序列里。
//
// ⚠️ **opcode 偏移风险（另一子代理的实测发现）**：我方
// `internal/modules/dnf/dnfenum/cmdpacket.go` 在 `0x032x` 区段整体比厂商**新 +1~+2**
// （厂商 `BET_FATIGUE = 0x0322` = 802，我方 `CmdPacketBetFatigue` = 803；
// 我方 `CmdPacketIncubatingSystemReward` = 806 与厂商 806 **撞号但不同业务**）。
// 因此本文件**只以厂商十六进制 `0x0326` / 十进制 806 为准**，禁止引用我方枚举名当依据。
//
// ⚠️ **本次只发默认档**：厂商用角色级表
// `character_dimensiongate_records(character_id, day_id, current_count, extra_count, used_count)`
// 记真实次数，且 `day_id != 今天 ⇒ 视为无记录、回落 default`（隐式重置）。
// 我方仓储是 MySQL，schema 变更风险大，**本次不新建表、不写数据库**，所以对"今天还没有
// 记录的角色"，发出去的默认档**与厂商对无记录角色的行为逐字节一致**。
// 真正接每日次数（含 day_id 跨天重置）列为后续项，见
// `entry_limit_source_note` 事件字段。
const (
	// currentDimensionGateEntranceMsgID 厂商十六进制 0x0326（十进制 806）。
	// 不要用我方 dnfenum 的名字反推这个数字，原因见上面的偏移风险说明。
	currentDimensionGateEntranceMsgID uint16 = 0x0326

	// currentDimensionGateEntranceEnv 一键回退开关。默认档 = **发**（修之前是完全不发，
	// 不发就等于没修）。取值 off/0/false/no/legacy/disable/disabled = 逐字节回到"不发"。
	currentDimensionGateEntranceEnv = "DNFBRIDGE_DIMENSION_GATE_ENTRANCE"

	// 厂商 `DimensionGateEntryLimitConfigProvider.cs:28` 的 PVF 路径与 tag 名（照抄，不改写）。
	currentDimensionGateConfigPath         = "etc/dimensiongate.etc"
	currentDimensionGateEnterCountTag      = "daily default enter count"
	currentDimensionGateExtraEnterCountTag = "daily default extra enter count"

	// 厂商 `DimensionGateEntryLimitConfigProvider.cs:29-30` 的 fallback，照抄，不臆造别的数字。
	currentDimensionGateVendorFallbackEnterCount = 5
	currentDimensionGateVendorFallbackExtraCount = 0

	// 厂商 `Build()` 固定写两个 u32。
	currentDimensionGateEntranceBodyLen = 8

	// body_source 取值：真档 PVF 优先，读不到才回落厂商 fallback。
	currentDimensionGateEntryLimitSourceVendorPVF        = "vendor_pvf_etc_dimensiongate_etc"
	currentDimensionGateEntryLimitSourceVendorPVFPartial = "vendor_pvf_etc_dimensiongate_etc_partial_vendor_default_fallback"
	currentDimensionGateEntryLimitSourceFallback         = "vendor_default_fallback"

	// PVF 侧状态，进事件日志便于现场判断走的是真档还是 fallback。
	currentDimensionGatePVFStatusReadOK      = "read_ok"
	currentDimensionGatePVFStatusOpenFailed  = "open_failed"
	currentDimensionGatePVFStatusReadFailed  = "read_failed"
	currentDimensionGatePVFStatusParseFailed = "parse_failed"
)

var currentDimensionGateEntranceOffValues = map[string]bool{
	"off": true, "0": true, "false": true, "no": true, "legacy": true, "disable": true, "disabled": true,
}

func currentDimensionGateEntranceEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(currentDimensionGateEntranceEnv)))
	if value == "" {
		return true
	}
	return !currentDimensionGateEntranceOffValues[value]
}

// currentDimensionGateEntryLimit 是一次发送要用的默认档 + 出处证据。
type currentDimensionGateEntryLimit struct {
	enterCount int
	extraCount int
	source     string
	pvfPath    string
	pvfStatus  string
	tagsFound  int
}

func clampCurrentDimensionGateCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

// buildCurrentDimensionGateEntranceInfoBody 按厂商 `Build()` 编码固定 8 字节包体：
// `u32 LE remaining_count + u32 LE extra_count`，两值都做 `Math.Max(0, ·)`。
func buildCurrentDimensionGateEntranceInfoBody(remainingCount, extraCount int) []byte {
	var writer packetWriter
	writer.writeUint32(uint32(clampCurrentDimensionGateCount(remainingCount)))
	writer.writeUint32(uint32(clampCurrentDimensionGateCount(extraCount)))
	return writer.bytes()
}

// parseCurrentDimensionGateEntryLimit 复刻厂商
// `DimensionGateEntryLimitConfigProvider.Parse/ReadSingleInt`：按 tag 取**第一个整数**，
// 取不到就用 fallback（5 / 0），两个结果都再做一次 `Math.Max(0, ·)`。
func parseCurrentDimensionGateEntryLimit(text string) (currentDimensionGateEntryLimit, error) {
	limit := currentDimensionGateEntryLimit{
		enterCount: currentDimensionGateVendorFallbackEnterCount,
		extraCount: currentDimensionGateVendorFallbackExtraCount,
		source:     currentDimensionGateEntryLimitSourceFallback,
		pvfPath:    currentDimensionGateConfigPath,
		pvfStatus:  currentDimensionGatePVFStatusReadOK,
	}
	if strings.TrimSpace(text) == "" {
		return limit, fmt.Errorf("%s is empty", currentDimensionGateConfigPath)
	}
	document, err := dnfpvf.Parse(currentDimensionGateConfigPath, text)
	if err != nil {
		limit.pvfStatus = currentDimensionGatePVFStatusParseFailed
		return limit, fmt.Errorf("parse %s: %w", currentDimensionGateConfigPath, err)
	}
	if value, found := document.Int(currentDimensionGateEnterCountTag); found {
		limit.enterCount = clampCurrentDimensionGateCount(int(value))
		limit.tagsFound++
	}
	if value, found := document.Int(currentDimensionGateExtraEnterCountTag); found {
		limit.extraCount = clampCurrentDimensionGateCount(int(value))
		limit.tagsFound++
	}
	switch limit.tagsFound {
	case 2:
		limit.source = currentDimensionGateEntryLimitSourceVendorPVF
	case 1:
		limit.source = currentDimensionGateEntryLimitSourceVendorPVFPartial
	default:
		limit.source = currentDimensionGateEntryLimitSourceFallback
	}
	return limit, nil
}

// currentDimensionGateEntryLimitForSession 真档 PVF 优先：能读到
// `etc/dimensiongate.etc` 就用真值，读不到就按厂商 fallback 5 / 0。
// 这里不缓存：PVF archive 在进程内已经打开（initialEquipmentArchive），单次查表 + 444 字节
// 解码的代价可以忽略，换来的是"改了 PVF 立刻生效"和测试之间不会串档。
func (s *Service) currentDimensionGateEntryLimitForSession() currentDimensionGateEntryLimit {
	limit := currentDimensionGateEntryLimit{
		enterCount: currentDimensionGateVendorFallbackEnterCount,
		extraCount: currentDimensionGateVendorFallbackExtraCount,
		source:     currentDimensionGateEntryLimitSourceFallback,
	}
	if s == nil {
		limit.pvfStatus = currentDimensionGatePVFStatusOpenFailed
		return limit
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil || archive == nil {
		limit.pvfStatus = currentDimensionGatePVFStatusOpenFailed
		return limit
	}
	text, readPath, err := readInitialPVFText(archive, currentDimensionGateConfigPath)
	if err != nil {
		limit.pvfStatus = currentDimensionGatePVFStatusReadFailed
		return limit
	}
	parsed, parseErr := parseCurrentDimensionGateEntryLimit(text)
	parsed.pvfPath = readPath
	if parseErr != nil {
		parsed.source = currentDimensionGateEntryLimitSourceFallback
		parsed.pvfStatus = currentDimensionGatePVFStatusParseFailed
		return parsed
	}
	return parsed
}

// sendCurrentDimensionGateEntranceInfo 下发 NOTI 0x0326（厂商十进制 806）
// `DIMENSION_GATE_ENTRANCE_INFO`。
//
// 接线位置：登录/进城 init 包序列里，紧挨着既有的 op1303 兄弟包
// （`sendCurrentRaidDailyAvailability`，见 initial_town_player_state.go）；同一处
// `gameSession.townMu` 临界区、同一个路由 stage 闸门，落在 op1340 冒险团模型 /
// mode0-mode1 / 定型的 op24 城镇提交**之前**，也就是城镇 UI 打开前。
//
// 与厂商注册顺序的对应关系：厂商把它注册在 `InitPacketBuilderRegistry.cs:105-107`，
// 即 `WeddingInfo` 之后、`StrikerSupportTagCharacter` 之前。我方目前**并不下发**
// `WeddingInfo`(0x03D0) / `StrikerSupportTagCharacter` 这两条，登录进城序列是一条合并
// 路由，唯一已经按厂商顺序钉过的锚点就是同窗口的 op1303，所以挨着它发，语义上仍在
// "init 窗口内、城镇提交之前"。
//
// 一键回退：`DNFBRIDGE_DIMENSION_GATE_ENTRANCE=off`（或 0/false/no/legacy/disable/disabled）
// 时走 skipped 分支，**逐字节回到"不发"**（改动前行为）。
func (s *Service) sendCurrentDimensionGateEntranceInfo(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	// The current EXE registers dimension entry state at class0/812,
	// handler 0x01049E00: u32/u32/u8 (9 bytes). Legacy 806/8 is a
	// different feature. Do not emit or renumber the default-only legacy
	// state until the shared durable counters and final byte are verified.
	if s.options.currentClientVersion == "official-24.9" {
		s.logGameEvent(session, "game-dimension-gate-entrance-info-skipped",
			"char_id", session.selectedCharacterID,
			"source", source,
			"msg_id", currentDimensionGateEntranceMsgID,
			"reason", "current_client_requires_verified_812_and_shared_persisted_counts",
			"native_msg_id", 812,
			"native_body_len", 9)
		return nil
	}
	if !currentDimensionGateEntranceEnabled() {
		s.logGameEvent(session, "game-dimension-gate-entrance-info-skipped",
			"char_id", session.selectedCharacterID,
			"source", source,
			"msg_id", currentDimensionGateEntranceMsgID,
			"switch", currentDimensionGateEntranceEnv,
			"reason", "rollback_switch_off")
		return nil
	}
	limit := s.currentDimensionGateEntryLimitForSession()
	body := buildCurrentDimensionGateEntranceInfoBody(limit.enterCount, limit.extraCount)
	s.logGameEvent(session, "game-dimension-gate-entrance-info-sent",
		"char_id", session.selectedCharacterID,
		"msg_id", currentDimensionGateEntranceMsgID,
		"classification", 0,
		"body_len", len(body),
		"layout", "u32_le_remaining_count_u32_le_extra_count",
		"remaining_count", limit.enterCount,
		"extra_count", limit.extraCount,
		"source", source,
		"body_source", limit.source,
		"pvf_path", limit.pvfPath,
		"pvf_status", limit.pvfStatus,
		"pvf_tags_found", limit.tagsFound,
		"vendor_fallback", "5/0",
		"entry_limit_source_note", "default_tier_only_no_character_dimensiongate_records_table_in_this_repo")
	return s.sendGameUpperRawClass(session, currentDimensionGateEntranceMsgID, body, 0)
}
