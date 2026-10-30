package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「Buff 强化 / 技能换装」换装栏的**跨选择角色重下发**
// （`DNFBRIDGE_SKILL_SWITCH_RESEND`）。
//
// # 这一档修的是什么（操作者第三次报障：选择角色后换装还是会消失）
//
// 换装状态**已经落库、且从不被覆盖**（HK `192.0.2.11` 只读 SQL，
// 2026-09-28T13:55Z：`dnf_setting_values` 里 `character:6:skill_switch_inventory`
// 有 14 条 `slot_*` 状态行 + `slot_count=14` + `source=current_exe_op1428` +
// `updated_at=2026-09-28T13:52:44.958764974Z`）。所以缺陷**不是保存**，
// 而是**我们从不在选角/进场景边界上把这份状态重新发给客户端**。
//
// 为什么"从不发"是结构性的（这才是本档要修的那个自家缺陷）：
//
//   - 旧路径把**上行的 4/6 字节记录**当成下行帧逐条回放
//     （`sendSkillSwitchInventoryProjection` 的 `state.Frames()` 循环）；
//   - 而客户端 1428 的**入站解析器**读的是 `u8 count + count×(u32,u32)`，
//     判长式 `len == 1+8*count` 只取 1,9,17,…（DNF.exe RVA 0x565660，活进程逐字节
//     实测，见 `_dist/buff-bar-breakpoint-measurement.md` §1）；
//   - 4 与 6 都落不进那个集合 ⇒ 默认档 `shape_guard` 把**每一帧**都判成
//     `body_shape_not_client_s2c_map` 拦下 ⇒ `sent_count=0`，
//     **默认档对换装栏的贡献恒等于零字节**。
//   - 现场实证（`packet_log.txt`，char 6 = `test78`，conn `game-000021`，
//     2026-09-28T13:52:48.901971Z / 13:52:49.007244Z 两条边界）：
//     `slot_count=14 sent_count=0 blocked_count=14` —— 十四行状态一行都没出去。
//
// 根因是**两个闸门被混用**：`len == 1+8*count` 是**下行帧**的形状闸门（它是对的），
// 却被套在了**上行记录**上；上行记录的闸门是 `len ∈ {4,6}`（`skillswitch.ParseRecordIn`
// 已经在入口处执行）。本档把两者分开：
//
//	skillSwitchUplinkRecordBodyValid(len)  —— 上行记录体（4 = v4 规范包体，6 = 改动前的行）
//	skillSwitchDownlinkFrameValid(frame)   —— 下行帧（必须恰好 1+8*count）
//
// 两条判据各自被表驱动测试钉住，且**同一条字节不会同时被两边接受**
// （4/6 永远不是 1+8c；见 TestSkillSwitchInventoryGuardSeparation）。
//
// 于是默认档的行为变成：上行记录仍然**全部**被下行闸门拦下（这一点逐字节不变，
// 因为它们确实不是合法下行帧——放它们出去会把客户端入站流读乱，读原语没有边界检查），
// 但**另外**按测量到的下行形状构造**一帧**合法状态帧，把落库的换装行重发给客户端。
//
// # 开关（house idiom：自读 env、默认 = 修复档、回退字面量逐字节复现今天）
//
// `DNFBRIDGE_SKILL_SWITCH_RESEND`
//
//	未设 / slots / on / fix / resend / 1 / true / 其它未知取值（**默认 = 修复档**）
//	    → 在既有重投影边界上多发**一帧** `u8 count + count×(u32,u32)`，
//	      pair 取自**落库的换装行**（见下"pair 取值"）。空换装 ⇒ 零字节。
//	shape_guard / off / today / legacy / rollback / 0 / disable
//	    → **与 2026-09-28 之前逐字节相同**：本文件一行代码都不执行、
//	      不发、不记（回退字面量刻意与今天那个档位同名，因为"今天"就是
//	      `shape_guard` 把所有帧拦成 0 字节的那个行为）。
//
// 只读档口三层，缺一不可（都成立才会发）：
//
//	① 本开关不是回退字面量；
//	② `DNFBRIDGE_SKILL_SWITCH_INVENTORY` 仍是默认档 `shape_guard`
//	   —— `project` / `before_transition_only` / `record` / `reference_drop`
//	   都是**取证/回退档**，它们必须逐字节保持改动前，所以本档不叠加在它们上面；
//	③ 该角色确实有落库的 `slot_*` 行（没有 ⇒ 零字节 + 既有 skipped 事件）。
//
// 本档**不改 `options.go`**（该文件此刻由兄弟子代理并发修改）：开关直接
// `os.Getenv` 自读，先例见 `dungeon_tournament_config.go:17-20`、
// `item_expiration_projection.go:391`、`initial_town_login_owner.go:457`。
//
// # pair 取值（**这是本档唯一未证实的一步，必须与结论分开读**）
//
// 每一条落库行贡献**一组** pair，key = 行的 `Slot`，value = **同一个 `Slot`**
// （恒等对，`slot → slot`）。取值来源与理由：
//
//   - 落库行是客户端**自己**在「Buff 强化」页登记时推上来的（`current_exe_op1428`），
//     所以 pair 的 key 集合 = 该角色真实的已登记槽位集合，**不是**硬编码常量；
//   - `ModeConfirm` 形态（现场 9/9：`Slot == Value`）本来就编码成恒等对，
//     本档对它们**逐字节忠实**；
//   - **刻意不使用 `Value` 字段**：现场 `Value` 取值集合是 `{47, 54..64}`
//     （`47` 与 `dnf_skill_layouts.slot_index` 的 `54..65` 段），正是上一轮
//     1.39 GB 全内存扫描 0 命中的那组 family-2 值；仓内既有测试
//     （`skill_switch_inventory_candidate_pairs_test.go:85-87`）把它钉成
//     "这些值永远不许发出去"。所以本档只重发**槽位集合**这一半信息。
//
// ⚠️ **未证实**（不要在别处引用成结论）：
//
//  1. pair 的语义（`(slot,slot)` 是否就是 manager 329 那张 `map<u32,u32>` 的期望内容）
//     至今没有客户端侧证据；
//  2. 上一轮的候选 A 实验帧（硬编码 `(12,12)…(26,26)`）**写进了**那张 map 但
//     栏位仍空（`_dist/buff-bar-breakpoint-measurement.md` §3/§6）。本档与它的差别是
//     **内容来源**（落库的真实槽位集合，含 3/13/20、不含 26）、**时机**（选角/进场景
//     的既有重投影边界）、**可判读性**（每次都带 `loadout_rows`/`saved_at`/`resent_at`）；
//     但"恒等对能不能让栏位显示出来"这一点，本档**没有**新增证据，
//     它把这一 bit 交给下一次实机判定（判据见 `_dist/buff-swap-lost-on-select.md`）。
//     槽位集合若正确、栏位仍空 ⇒ 该栏不是由这张 map 驱动的，或 pair 的 value
//     必须携带绑定槽（family-2 的值域）—— 那需要新证据才允许改默认值；
//  3. 客户端**不会**因为多收这一帧而崩：它的形状按构造满足解析器
//     （`count` 之后恰好 `count` 组 `u32,u32`），与既有实验帧同一安全论证
//     （`skill_switch_inventory_candidate_pairs.go` 顶部"安全性"一节）。
const skillSwitchLoadoutResendEnv = "DNFBRIDGE_SKILL_SWITCH_RESEND"

// skillSwitchLoadoutPairsEnv 是**下行 pair 取值**的开关
// （`record` = 修复档 / `identity` = 一键回退字面量）。
//
// # 它修的是什么（操作者第四次报障：选择角色后**刚选择的**装备没有登记）
//
// 上行一条记录是 `u8 Slot | u8 Mode | u16 Value`（4 字节真包体，见
// skillswitch/record.go 顶部的帧长算术）。上一版把每一行重新编码成**恒等对**
// `(Slot, Slot)`（`pair_value_source=slot_field_not_record_value`），于是 `Value`
// ——也就是"操作者刚选的那个槽位"——**从来没有离开过服务端**。现场实证：
//
//	char 6 = `test78` 的落库行是 `slot_12=0c003600`（Slot 12 / Mode 0 / Value 54）、
//	`slot_14=0e003e00`（Value 62）… 全部 11 条 Mode 0 行的 `Value ∈ {54..64}`
//	（技能 → 快捷栏槽位的绑定，见 record.go 的 ModeRegister）；
//	而 2026-09-28T14:27:22.468Z 那条重下发帧的 body 是
//	`0e03000000030000000c0000000c0000000d000000...` ⇒ pair = `(3,3)(12,12)(13,13)…`
//	——**只重发了一半信息，而且是错的那一半**。
//
// 所以本档默认把每行的 `(Slot, Value)` **原样回显**（客户端自己推上来的字段，
// 不是发明出来的数据）：Mode 0 行发 `(12,54)`，Mode 3 行 `Slot == Value` ⇒
// 与恒等对逐字节相同。客户端 1428 入站解析器读 `u8 count + count×(u32,u32)`
// 写进那张 `std::map<u32,u32>`，回显它自己发过的键值对是**最保守**的内容。
//
// 为什么上一版禁止发这个值域（`family2_slot_to_source_slot` 的 1.39 GB 全内存
// 扫描 0 命中）在本档**不构成反证**：那份扫描证明的是"客户端内存里没有这组 pair"，
// 而扫描当时客户端**从来没收到过合法内容**（正是这个缺陷），所以它无法区分
// "编码不对"与"数据本来就不在"（`_dist/buff-swap-lost-on-select.md` §5.3 未证实项 3
// 已经写明这一点）。回显客户端自己的字段与那个被证伪的**硬编码映射**是两回事。
//
// # 仍未确证（不要在别处引用成结论）
//
//  1. `(Slot, Value)` 是不是那张 map 的期望内容，仍然**没有客户端侧证据**；
//     本档只保证"发出去的字节 = 客户端自己发上来的字段"，比恒等对更忠实。
//  2. 若框内仍然为空 ⇒ 该栏不由这张 map 驱动（或 `count` 语义不对），
//     那时用 `identity` 一个字面量即可回到上一版的字节形。
const skillSwitchLoadoutPairsEnv = "DNFBRIDGE_SKILL_SWITCH_RESEND_PAIRS"

const (
	// skillSwitchLoadoutResendSlots 是**默认档（修复档）**：按落库槽位集合重发一帧。
	skillSwitchLoadoutResendSlots = "slots"
	// skillSwitchLoadoutResendShapeGuard 是**一键回退字面量**：与改动前逐字节相同
	// （本文件不执行任何代码路径，不发、不记）。
	skillSwitchLoadoutResendShapeGuard = "shape_guard"
)

const (
	// skillSwitchLoadoutPairsRecord 是**默认档（修复档）**：
	// pair = 该行自己的 `(Slot, Value)`（忠实回显客户端上行字段）。
	skillSwitchLoadoutPairsRecord = "record"
	// skillSwitchLoadoutPairsIdentity 是**一键回退字面量**：
	// pair = `(Slot, Slot)`，逐字节等于上一版线上那 15 帧。
	skillSwitchLoadoutPairsIdentity = "identity"
)

// 重下发事件名（具名 + 自成判据：字符、行数、来源、存于何时、发于何时、逐对 hex）。
const (
	// skillSwitchLoadoutResendEvent：合法状态帧已下发。
	skillSwitchLoadoutResendEvent = "game-skill-switch-inventory-loadout-resend"
	// skillSwitchLoadoutResendBlockedEvent：构造/自校验失败 ⇒ **零字节**。
	// 正常路径不会出现；出现即为 bug（与候选实验的 blocked 同口径）。
	skillSwitchLoadoutResendBlockedEvent = "game-skill-switch-inventory-loadout-resend-blocked"
)

// errSkillSwitchLoadoutResendCount 表示落库行数落在客户端 `u8 count` 之外
// （0 或 > 255）：构造不出来就一个字节都不发。
var errSkillSwitchLoadoutResendCount = errors.New("skill switch loadout row count does not fit the client u8 count")

// errSkillSwitchLoadoutResendShape 表示构造出来的帧没有通过下行形状闸门
// （`len == 1+8*count`）。这是"自家构造写错"的自校验，不是客户端形状问题。
var errSkillSwitchLoadoutResendShape = errors.New("skill switch loadout downlink frame failed the 1+8*count gate")

// normalizeSkillSwitchLoadoutResendMode 归一化 `DNFBRIDGE_SKILL_SWITCH_RESEND`。
//
// 口径与 `skillswitch.NormalizeLayout` / `readDungeonTournamentRewardAckMode` 一致：
// **只有回退字面量才回退**，未设置 / 空 / 未知取值一律落到修复档 `slots`。
func normalizeSkillSwitchLoadoutResendMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case skillSwitchLoadoutResendShapeGuard, "off", "today", "legacy", "rollback", "0", "disable":
		return skillSwitchLoadoutResendShapeGuard
	default:
		return skillSwitchLoadoutResendSlots
	}
}

// currentSkillSwitchLoadoutResendMode 直接自读 env（本档刻意不进 options.go）。
func currentSkillSwitchLoadoutResendMode() string {
	return normalizeSkillSwitchLoadoutResendMode(os.Getenv(skillSwitchLoadoutResendEnv))
}

// skillSwitchLoadoutResendMode 返回当前进程的档位；nil Service 恒回退档（什么都不做）。
func (s *Service) skillSwitchLoadoutResendMode() string {
	if s == nil {
		return skillSwitchLoadoutResendShapeGuard
	}
	return currentSkillSwitchLoadoutResendMode()
}

// skillSwitchLoadoutResendActive 报告这一次重投影要不要发那帧合法状态帧。
//
// 三个条件（缺一不发）：开关非回退档、`DNFBRIDGE_SKILL_SWITCH_INVENTORY` 是
// 默认档 `shape_guard`、服务非 nil。取证/回退档不叠加本档，见文件顶部开关表。
func (s *Service) skillSwitchLoadoutResendActive() bool {
	if s == nil {
		return false
	}
	if s.skillSwitchLoadoutResendMode() == skillSwitchLoadoutResendShapeGuard {
		return false
	}
	return s.skillSwitchInventoryMode() == skillSwitchInventoryShapeGuardTier
}

// normalizeSkillSwitchLoadoutPairsMode 归一化 `DNFBRIDGE_SKILL_SWITCH_RESEND_PAIRS`。
//
// 口径同上：**只有回退字面量才回退**，未设置 / 空 / 未知取值一律修复档 `record`。
func normalizeSkillSwitchLoadoutPairsMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case skillSwitchLoadoutPairsIdentity, "slot", "slot_slot", "off", "today", "legacy", "rollback", "0", "disable":
		return skillSwitchLoadoutPairsIdentity
	default:
		return skillSwitchLoadoutPairsRecord
	}
}

// currentSkillSwitchLoadoutPairsMode 直接自读 env（本档刻意不进 options.go）。
func currentSkillSwitchLoadoutPairsMode() string {
	return normalizeSkillSwitchLoadoutPairsMode(os.Getenv(skillSwitchLoadoutPairsEnv))
}

// skillSwitchLoadoutPairsMode 返回当前进程的 pair 档位；nil Service 恒回退档。
func (s *Service) skillSwitchLoadoutPairsMode() string {
	if s == nil {
		return skillSwitchLoadoutPairsIdentity
	}
	return currentSkillSwitchLoadoutPairsMode()
}

// ── 两个闸门（**这就是被混用的那两个**）─────────────────────────────────────
//
// `skillSwitchUplinkRecordBodyValid` 是**上行记录体**的判据：
// v4 规范包体 4 字节、或现行 13 字节分帧交来的 6 字节（2 非包体字节 + 4 真包体）。
// 它由 `skillswitch.ParseRecordIn` 在入口处执行（长度不符 ⇒ `ErrRecordShape`）。
//
// `skillSwitchDownlinkFrameValid` 是**下行帧**的判据：`u8 count + count×(u32,u32)`，
// 即 `len == 1+8*count`（客户端入站解析器实测形状）。
//
// ⚠️ 4 与 6 **永远**不可能是 `1+8c`（该集合只有 1,9,17,…）。这正是"默认档
// 结构性 0 下行"的算术根因，也是本档必须**构造**一帧、而不能把上行记录
// 放行给下行闸门的原因（放行 = 客户端把 `0x0c` 当 count=12 去读 96 字节，
// 读原语无边界检查、会从缓冲区重填 ⇒ 入站流读乱）。
func skillSwitchUplinkRecordBodyValid(bodyLen int) bool {
	return bodyLen == skillswitch.RecordBodySize || bodyLen == skillswitch.RecordWireSize
}

func skillSwitchDownlinkFrameValid(frame []byte) bool {
	// 单一真源：与默认档那道形状闸门是同一个判定式（纯函数，表驱动测试在
	// TestSkillSwitchInventoryFrameShapeGate 与 TestSkillSwitchInventoryGuardSeparation）。
	return skillSwitchInventoryFrameShapeValid(frame)
}

// skillSwitchLoadoutPair 是一组下行 pair（客户端读作两个 u32 LE）。
type skillSwitchLoadoutPair struct {
	Slot  uint32
	Value uint32
}

// skillSwitchLoadoutPairs 把落库的换装行映射成下行 pair。
//
// `mode == skillSwitchLoadoutPairsRecord`（默认 = 修复档）：pair = 该行自己的
// `(Slot, Value)` —— **忠实回显客户端上行字段**（Mode 0 行 `(12,54)`；
// Mode 3 行 `Slot == Value` ⇒ 与恒等对逐字节相同）。
// `mode == skillSwitchLoadoutPairsIdentity`（一键回退字面量）：pair = `(Slot, Slot)`，
// 逐字节等于上一版线上那 15 帧。
//
// 顺序：`skillswitch.Load` 已按 `Slot` 升序返回记录，这里原样保持 ⇒ 同一份落库
// 状态在任何一次重下发里都产生**逐字节相同**的帧（可对拍、可 grep）。
// 行数落在 `u8 count` 之外 ⇒ 返回错误（调用方零字节 + blocked 事件）。
func skillSwitchLoadoutPairs(records []skillswitch.Record, mode string) ([]skillSwitchLoadoutPair, error) {
	if len(records) == 0 || len(records) > 0xFF {
		return nil, errSkillSwitchLoadoutResendCount
	}
	useRecordValue := normalizeSkillSwitchLoadoutPairsMode(mode) != skillSwitchLoadoutPairsIdentity
	pairs := make([]skillSwitchLoadoutPair, 0, len(records))
	for _, record := range records {
		slot := uint32(record.Slot)
		value := slot
		if useRecordValue {
			value = uint32(record.Value)
		}
		pairs = append(pairs, skillSwitchLoadoutPair{Slot: slot, Value: value})
	}
	return pairs, nil
}

// buildSkillSwitchLoadoutBody 构造客户端 1428 **入站形状**的包体：
// `u8 count` + `count × (u32 key LE, u32 value LE)`。
//
// 与候选实验的 `buildSkillSwitchCandidatePairsBody` **同形但刻意不共用**：
// 那一个属于已退休的实验（它自己的表、自己的测试钉着），本档是产品路径，
// 两者的取值来源必须能分别演进（避免"改实验动了产品"这类事故）。
func buildSkillSwitchLoadoutBody(records []skillswitch.Record, mode string) ([]byte, error) {
	pairs, err := skillSwitchLoadoutPairs(records, mode)
	if err != nil {
		return nil, err
	}
	body := make([]byte, 1+8*len(pairs))
	body[0] = byte(len(pairs))
	offset := 1
	for _, pair := range pairs {
		binary.LittleEndian.PutUint32(body[offset:offset+4], pair.Slot)
		binary.LittleEndian.PutUint32(body[offset+4:offset+8], pair.Value)
		offset += 8
	}
	if !skillSwitchDownlinkFrameValid(body) {
		return nil, errSkillSwitchLoadoutResendShape
	}
	return body, nil
}

// skillSwitchLoadoutPairsText 是给日志用的确定性 pair 列表
// （与候选实验的 `pairs=(12,12)(14,14)…` 同格式，便于线上逐对比对）。
// 修复档下打印的是**客户端上行字段**，于是"重发的 pair 是否等于操作者选的值"
// 在 `packet_log.txt` 上一眼可判。
func skillSwitchLoadoutPairsText(records []skillswitch.Record, mode string) string {
	useRecordValue := normalizeSkillSwitchLoadoutPairsMode(mode) != skillSwitchLoadoutPairsIdentity
	parts := make([]string, 0, len(records))
	for _, record := range records {
		value := int(record.Slot)
		if useRecordValue {
			value = int(record.Value)
		}
		parts = append(parts, "("+strconv.Itoa(int(record.Slot))+","+strconv.Itoa(value)+")")
	}
	return strings.Join(parts, "")
}

// skillSwitchRestoredBindingsText 把"这次读取里被补回来的绑定"渲染成
// `slot_<n>=<hex>`（升序，与 `written_entries` 同形）；空列表写成 `-`。
func skillSwitchRestoredBindingsText(records []skillswitch.Record) string {
	if len(records) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, "slot_"+strconv.Itoa(int(record.Slot))+"="+record.Hex())
	}
	return strings.Join(parts, ",")
}

// skillSwitchLoadoutPairsCarryFamily2Values 报告这一帧里有没有
// **family-2 值域**（`{47} ∪ {54..64}`，即 Mode 0/ ModeUnread 行的 `Value`）。
// 修复档下这是有意的忠实回显，日志里如实写出来（不是一个安全承诺）。
func skillSwitchLoadoutPairsCarryFamily2Values(records []skillswitch.Record, mode string) bool {
	if normalizeSkillSwitchLoadoutPairsMode(mode) == skillSwitchLoadoutPairsIdentity {
		return false
	}
	for _, record := range records {
		if record.Sentinel() {
			continue
		}
		value := record.Value
		if value == 47 || (value >= 54 && value <= 64) {
			return true
		}
	}
	return false
}

// sendSkillSwitchLoadoutResend 在既有重投影边界上重发**一帧**合法状态帧。
//
// 语义与失败契约（与 `sendSkillSwitchCandidatePairs` 同口径）：
//   - 开关回退档 / 非默认 tier / 没有落库行：直接返回，**零字节**（调用方已在
//     空换装时记过 `...-skipped reason=no_persisted_skill_switch_inventory`）；
//   - 构造或自校验失败：只记 `...-loadout-resend-blocked`，**零字节**；
//   - 只有真正的写线失败才返回错误（调用方本来就在处理写线错误）。
//
// 事件字段自成判据（下一次实机只看这一行就能判读）：
//
//	char_id / scope          —— 哪个角色、哪个存储作用域
//	source                   —— 哪条边界（选角/进城/副本返回/穿脱装备/重新登录）
//	loadout_rows             —— 落库换装行数（= pair 数 = count）
//	loadout_source           —— 这些行**是谁写的**（`current_exe_op1428`）
//	loadout_saved_at         —— 这些行**最后一次保存**的时刻（仓储 updated_at）
//	resent_at                —— **本次重下发**的时刻
//	pairs / body_hex         —— 逐对与逐字节（线上可对拍）
//	resend_switch            —— 本档当前取值（含回退字面量名）
//	supersedes_candidate_pairs —— 候选实验帧是否也在同一边界发过（本帧在它之后发，
//	                              所以客户端那张 map 上**本帧胜出**）
func (s *Service) sendSkillSwitchLoadoutResend(
	session *gameSession,
	source string,
	characterID string,
	state skillswitch.State,
) error {
	if s == nil || session == nil || len(state.Records) == 0 {
		return nil
	}
	if !s.skillSwitchLoadoutResendActive() {
		return nil
	}
	pairsMode := s.skillSwitchLoadoutPairsMode()
	body, err := buildSkillSwitchLoadoutBody(state.Records, pairsMode)
	if err != nil {
		s.logPacketEvent(skillSwitchLoadoutResendBlockedEvent,
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"scope", skillswitch.Scope(characterID),
			"source", source,
			"loadout_rows", len(state.Records),
			"resend_switch", s.skillSwitchLoadoutResendMode(),
			"pairs_switch", pairsMode,
			"reason", "loadout_frame_build_failed",
			"build_error", err)
		return nil
	}
	if err := s.sendGameUpperRawClass(session, currentSkillSwitchInventoryOpcode, body, 0); err != nil {
		return err
	}
	resentAt := time.Now().UTC()
	candidateFrameSent := false
	if pairs, ok := skillSwitchCandidatePairSets[s.skillSwitchCandidatePairsMode()]; ok && len(pairs) > 0 {
		candidateFrameSent = true
	}
	// pair 档位如实写进日志（两个档位各自可 grep），`pairs` 字段就是线上判读的依据：
	// 修复档下它必须**逐对等于操作者选的值**（`(12,54)(14,62)…`）。
	pairMapping := "slot_identity"
	pairValueSource := "slot_field_not_record_value"
	pairEvidence := "key_set_from_persisted_op1428_rows_value_reuses_slot"
	if pairsMode != skillSwitchLoadoutPairsIdentity {
		pairMapping = "slot_records_value"
		pairValueSource = "record_value_field"
		pairEvidence = "key_and_value_echoed_from_persisted_op1428_record_fields"
	}
	s.logPacketEvent(skillSwitchLoadoutResendEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"scope", skillswitch.Scope(characterID),
		"source", source,
		"msg_id", currentSkillSwitchInventoryOpcode,
		"classification", 0,
		"loadout_rows", len(state.Records),
		"pair_count", len(state.Records),
		"count", int(body[0]),
		"body_len", len(body),
		"shape_guard", "pass",
		"downlink_shape_gate", "len_eq_1_plus_8_times_count",
		"uplink_record_gate", "len_in_4_or_6",
		"pair_mapping", pairMapping,
		"pair_value_source", pairValueSource,
		"pairs_switch", pairsMode,
		"pairs_switch_default", skillSwitchLoadoutPairsRecord,
		"pairs_rollback_literal", skillSwitchLoadoutPairsIdentity,
		// 如实报告：修复档**会**把客户端自己的 Value（含 47/54..64）回显出去。
		// 这不是安全承诺，是一个可 grep 的事实（回退档恒 false）。
		"family2_values_ever_sent", skillSwitchLoadoutPairsCarryFamily2Values(state.Records, pairsMode),
		"pairs", skillSwitchLoadoutPairsText(state.Records, pairsMode),
		"body_hex", hex.EncodeToString(body),
		// 绑定还原（`DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE`，默认 = 修复档，**只读**）：
		// 这一帧里有多少对是"客户端自己上一次声明过、而当前行里已经没有的绑定"。
		// `restored_bindings=-` 表示这一份状态没有需要还原的行。
		"binding_restore", skillswitch.CurrentBindingRestore(),
		"binding_restore_default", skillswitch.BindingRestoreRestore,
		"binding_restore_rollback_literal", skillswitch.BindingRestoreOff,
		"restored_count", len(state.Restored),
		"restored_bindings", skillSwitchRestoredBindingsText(state.Restored),
		// 来源与时刻：这三项就是"哪来的、什么时候存的、什么时候重发的"。
		"loadout_source", state.Source,
		"loadout_saved_at", state.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"resent_at", resentAt.Format(time.RFC3339Nano),
		"record_body_layout", s.skillSwitchInventoryRecordBodyLayout().String(),
		"body_source", "built_from_persisted_skill_switch_rows",
		"resend_switch", s.skillSwitchLoadoutResendMode(),
		"resend_switch_default", skillSwitchLoadoutResendSlots,
		"resend_rollback_literal", skillSwitchLoadoutResendShapeGuard,
		"supersedes_candidate_pairs", candidateFrameSent,
		// 证据状态：**未证实**。pair 语义没有客户端证据；修复档只保证
		// "发出去的字节 = 客户端自己发上来的字段"（比恒等对多带 `Value` 这一半）。
		"proof_state", "unproven_pair_semantics",
		"pair_evidence", pairEvidence,
		"downlink_count_semantics", "unverified")
	// 只读诊断（`DNFBRIDGE_SKILL_SWITCH_PAIR_RESOLUTION`，默认 `resolve`）：把这一帧
	// 每一组 pair 的 value 在 list 0（主背包）/ list 1（换装栏·装扮容器）里各查得到什么
	// 写进日志。**不改任何字节** —— 帧在上面已经发完了，这里只多发一条 EVENT。
	s.logSkillSwitchPairResolution(session, characterID, state.Records, pairsMode)
	return nil
}
