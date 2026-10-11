package skillswitch

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

// 本文件把 op1428 的记录按角色落进**既有 settings 家族**
// （`dnf_settings` / `dnf_setting_values`，仓储 `dnfrepo.SettingsRepository`），
// 与 op331 技能指令（`character:<id>:skill_commands`）、op447 角色选项
// （`character:<id>:character_option`）同族、同事务边界、同 fire-and-forget 契约。
// **不新增表、不写裸 SQL**：所有读写都经仓储接口。
//
// # 落库字节形（两种，由 `DNFBRIDGE_SKILL_SWITCH_RECORD_BODY` 决定）
//
//	v4（默认 = 修复档）: `slot_<n>` 的值 = **4 字节包体**的 hex（8 个字符），
//	                     取证键 `record_<8hex>` / `sentinel_<8hex>`；
//	v6（一键回退 = 改动前）: `slot_<n>` 的值 = 6 字节（2 字节导线前缀 + 4 字节包体）
//	                     的 hex（12 个字符），取证键 12 hex。
//
// **旧行不需要迁移**：读取一律 dual-read（两种长度都认，按 record.go 里那条固定映射
// 去掉/补回那 2 个非包体字节）。v4 档只有**再次收到同一条记录**时才会把旧行改写成
// 4 字节规范形，并在落库日志上打 `layout_upgraded=true`（见 Apply 与
// `recordRowUpgraded`）。旧取证键一律保留、不改、不删。
const (
	// scopeSuffix 拼在 `character:<id>` 之后。
	scopeSuffix = ":skill_switch_inventory"

	// SourceCurrentEXE 是落库时写入的 `source` 值（与 op447 的
	// `current_exe_op447` 同风格，便于线上 grep 归属）。
	SourceCurrentEXE = "current_exe_op1428"

	// MaxForensicRecords 是 `record_<hex>` **取证键**的条数上界，不是对客户端
	// 槽位数的断言，也**不**限制可登记的状态：到顶后仍继续写 `slot_*`（状态键），
	// 只是不再新增取证键（见 Apply 的 forensic_skipped）。
	//
	// 为什么状态键不需要上界：`slot_*` 的键来自 `Record.Slot`，是 u8，
	// 天然 ≤256 个键，而且「同一技能重复登记只覆盖同一个键」保证幂等。
	MaxForensicRecords = 128
)

// settings 家族的 entry_key 前缀。
const (
	entrySlotPrefix     = "slot_"
	entryRecordPrefix   = "record_"
	entrySentinelPrefix = "sentinel_"
	entrySlotCount      = "slot_count"
	entryRecordCount    = "record_count"
	entrySource         = "source"
	entryUpdatedAt      = "updated_at"
)

// Scope 是这条状态唯一的 owner 作用域。
func Scope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + scopeSuffix
}

func slotEntryKey(slot byte) string {
	return entrySlotPrefix + strconv.Itoa(int(slot))
}

// isRecordEntryKey 认两种取证键长度：v4 的 8 hex（4 字节包体）与改动前的
// 12 hex（6 字节行）。旧键**不删不改**，只是继续被计入 forensic_count。
func isRecordEntryKey(key string) bool {
	if !strings.HasPrefix(key, entryRecordPrefix) {
		return false
	}
	raw := strings.TrimPrefix(key, entryRecordPrefix)
	if len(raw) != RecordBodySize*2 && len(raw) != RecordWireSize*2 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// parseStoredRow 读一行已落库的 `slot_*` 值，返回**归一化到 layout** 的记录。
//
// dual-read（两种长度都认，且都只按固定映射读，不猜字段）：
//
//	8 hex（4 字节）  → 规范 v4 包体，直接读；
//	12 hex（6 字节） → 改动前的行（2 字节导线前缀 + 4 字节包体），按映射去掉前缀。
//
// 归一化的方向由**当前档位**决定：v6 档下会把 4 字节行补回一个 `0x0000` 前缀
// （否则 v4 档写过的行在回退后会整条读不出、状态会"消失"）；
// v4 档下 6 字节行按去前缀读成 4 字节形。任一方向都只动那 2 个**非字段**字节。
func parseStoredRow(raw []byte, layout Layout) (Record, bool) {
	switch len(raw) {
	case RecordBodySize:
		record, err := ParseRecordIn(LayoutV4, raw)
		if err != nil {
			return Record{}, false
		}
		return record.InLayout(layout), true
	case RecordWireSize:
		record, err := ParseRecordIn(LayoutV6, raw)
		if err != nil {
			return Record{}, false
		}
		return record.InLayout(layout), true
	default:
		return Record{}, false
	}
}

// recordRowUpgraded 判断这次落库是不是"把同一条记录的旧 6 字节行改写成 v4 规范形"。
// 只有**字段完全相同、字节形不同**才算升级（它是可 grep 的迁移信号，不是内容变化）。
func recordRowUpgraded(previousHex string, record Record) bool {
	previousHex = strings.TrimSpace(previousHex)
	if previousHex == "" || previousHex == record.Hex() {
		return false
	}
	raw, err := hex.DecodeString(previousHex)
	if err != nil || len(raw) == len(record.Bytes()) {
		return false
	}
	previous, ok := parseStoredRow(raw, record.Layout)
	if !ok {
		return false
	}
	return previous.SameFields(record)
}

// State 是一个角色已落库的换装栏登记状态。
type State struct {
	CharacterID string
	// Records 是按 `Slot` 升序排列的可重投影记录（来自 `slot_*` 键），
	// 已按 `Load` 的 layout 参数归一化。
	Records []Record
	// ForensicCount 是 `record_*` / `sentinel_*` 取证键的条数（两种长度都算）。
	ForensicCount int
	// UpdatedAt 是这一行设置最后一次写入的时间（= 这些行**最后一次被保存**的时刻，
	// 上行重下发日志里的 `loadout_saved_at` 就是它）。
	UpdatedAt time.Time
	// Source 是落库时写进 `source` 键的来源标记（`SourceCurrentEXE` = op1428 现场）。
	// 它让"这份换装是从哪来的"在重下发日志里可 grep；缺失时为空串。
	Source string
	// Restored 是这次读取里**按客户端自己上一次的绑定声明补回来**的行
	// （`DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE`，按 Slot 升序）。
	// 空表示这一份状态没有需要还原的行（绝大多数角色都是空）。
	// 它**只影响读取视图**：DB 里的 `slot_*` 行一个字节都没动。
	Restored []Record
}

// Frames 返回投影时要按序下发的包体（每条记录一包，**原样回放**它落库的字节形：
// v4 档 = 4 字节包体，v6 档 = 改动前的 6 字节行）。
func (s State) Frames() [][]byte {
	frames := make([][]byte, 0, len(s.Records))
	for _, record := range s.Records {
		frames = append(frames, record.Bytes())
	}
	return frames
}

// Load 读取一个角色已落库的换装栏状态；没有记录时 found=false。
//
// `layout` 是**归一化目标**：记录按 dual-read 读出（4 或 6 字节行都认，见 ParseStoredRow），
// 再统一成该布局的字节形，于是 `Frames()` 的字节形只由档位决定。
//
// settings 仓储为 nil（内存/裁剪过的测试装配）时返回 found=false 而不是错误：
// 投影路径必须能在没有该仓储时继续工作（与 `skillcmd.LoadSkillCommands` 同口径）。
func Load(ctx context.Context, repos dnfrepo.Group, characterID string, layout Layout) (State, bool, error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || repos.Settings == nil {
		return State{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, found, err := repos.Settings.Load(ctx, Scope(characterID))
	if err != nil || !found {
		return State{}, false, err
	}
	state := State{
		CharacterID: characterID,
		UpdatedAt:   record.UpdatedAt,
		Source:      strings.TrimSpace(record.Values[entrySource]),
	}
	slots := make([]int, 0, len(record.Values))
	bySlot := make(map[int]Record, len(record.Values))
	for key, value := range record.Values {
		switch {
		case isRecordEntryKey(key):
			state.ForensicCount++
		case strings.HasPrefix(key, entrySlotPrefix):
			slot, err := strconv.Atoi(strings.TrimPrefix(key, entrySlotPrefix))
			if err != nil || slot < 0 || slot > 0xFF {
				continue
			}
			raw, err := hex.DecodeString(strings.TrimSpace(value))
			if err != nil {
				continue
			}
			parsed, ok := parseStoredRow(raw, layout)
			if !ok {
				continue
			}
			slots = append(slots, slot)
			bySlot[slot] = parsed
		}
	}
	sort.Ints(slots)
	// 绑定还原（`DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE`，默认 = 修复档，**只读**）：
	// `Value == Slot` 的行不含绑定，而 `record_*` 里存着客户端自己上一次为这个槽位
	// 声明过的绑定 ⇒ 在读取视图里补回来（见 registration.go 那一档的说明）。
	//
	// 只在**登记窗口档**生效：`union` 是"整档回到改动前"的字面量，它必须逐字节
	// 复现改动前的下行帧（那台二进制没有本档）。
	if CurrentBindingRestore() == BindingRestoreRestore && CurrentRegistrationMode() != RegistrationUnion {
		state.Restored = restoreDegenerateBindings(record.Values, layout, bySlot)
	}
	state.Records = make([]Record, 0, len(slots))
	for _, slot := range slots {
		state.Records = append(state.Records, bySlot[slot])
	}
	return state, true, nil
}

// restoreDegenerateBindings 把**不含绑定信息**的状态行按客户端自己上一次的绑定声明补回来。
//
// 输入 `bySlot` 是 `Load` 已经从 `slot_*` 读出的行（会被就地改写；调用方随后用它
// 构造 `State.Records`）。候选只来自 `record_<hex>` 取证键里**携带绑定**的那些
// （`Record.BindingBearing()`），同槽位有多条时按 hex 升序取第一条（确定性）。
//
// 返回被补回来的行（按 Slot 升序），供日志判读；没有可补的行时返回空切片。
func restoreDegenerateBindings(values map[string]string, layout Layout, bySlot map[int]Record) []Record {
	candidates := make(map[int][]Record)
	for key := range values {
		if !isRecordEntryKey(key) {
			continue
		}
		raw, err := hex.DecodeString(strings.TrimPrefix(key, entryRecordPrefix))
		if err != nil {
			continue
		}
		stored, ok := parseStoredRow(raw, layout)
		if !ok || !stored.BindingBearing() {
			continue
		}
		candidates[int(stored.Slot)] = append(candidates[int(stored.Slot)], stored)
	}
	if len(candidates) == 0 {
		return nil
	}
	slots := make([]int, 0, len(candidates))
	for slot := range candidates {
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	restored := make([]Record, 0, len(slots))
	for _, slot := range slots {
		current, present := bySlot[slot]
		if !present || current.BindingBearing() {
			// 没有这一行（被删掉的槽位**不复活**）或这一行本来就带绑定（绝不覆盖）。
			continue
		}
		options := candidates[slot]
		sort.Slice(options, func(i, j int) bool { return options[i].Hex() < options[j].Hex() })
		bySlot[slot] = options[0]
		restored = append(restored, options[0])
	}
	return restored
}

// Owner 拥有一次 op1428 记录的校验 + 落库。
type Owner struct {
	characters   dnfrepo.CharacterRepository
	settings     dnfrepo.SettingsRepository
	catalog      *dnfskill.Table
	registration string
	arm          string
	bind         string
	delete       string
}

// OwnerOptions 注入权威 PVF 技能目录（与 skillcmd.OwnerOptions 同形）。
type OwnerOptions struct {
	Catalog *dnfskill.Table
	// Registration 是**登记窗口**档位（`NormalizeRegistrationMode` 的取值，见
	// registration.go）。空值 ⇒ 自读 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION`，
	// 该 env 未设 / 未知取值 ⇒ `RegistrationWindow` = 修复档（house idiom：
	// 默认档就是修复档，未知取值不许把它悄悄关掉）。
	Registration string
	// RegistrationArm 是**记账起点**档位（`NormalizeRegistrationArm` 的取值，见
	// registration.go 的 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`）。空值 ⇒ 自读该 env，
	// 未设 / 未知取值 ⇒ `RegistrationArmRecord` = 修复档。
	RegistrationArm string
	// RegistrationBind 是**绑定保护**档位（`NormalizeRegistrationBind` 的取值，见
	// registration.go 的 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`）。空值 ⇒ 自读该 env，
	// 未设 / 未知取值 ⇒ `RegistrationBindKeep` = 修复档。
	RegistrationBind string
	// RegistrationDelete 是**删除语义**档位（`NormalizeRegistrationDelete` 的取值，见
	// registration.go 的 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）。空值 ⇒ 自读该 env，
	// 未设 / 未知取值 ⇒ `RegistrationDeleteKeep` = 修复档（窗口提交不删行）。
	RegistrationDelete string
}

// NewOwner 需要角色仓储（取 job 做 PVF 门禁）与 settings 仓储（落库）。
func NewOwner(repos dnfrepo.Group, options ...OwnerOptions) (*Owner, error) {
	if repos.Character == nil || repos.Settings == nil {
		return nil, ErrStoreUnavailable
	}
	owner := &Owner{characters: repos.Character, settings: repos.Settings}
	if len(options) > 0 {
		owner.catalog = options[0].Catalog
		owner.registration = options[0].Registration
		owner.arm = options[0].RegistrationArm
		owner.bind = options[0].RegistrationBind
		owner.delete = options[0].RegistrationDelete
	}
	return owner, nil
}

// registrationMode 返回归一化后的登记窗口档位。
//
// options 没显式给档位时**自读 env**（于是运维一个环境变量就能回退，不需要改
// options.go，也不需要调用方重新装配）；env 也没设 ⇒ 修复档。
func (o *Owner) registrationMode() string {
	if o == nil {
		return RegistrationWindow
	}
	if strings.TrimSpace(o.registration) == "" {
		return CurrentRegistrationMode()
	}
	return NormalizeRegistrationMode(o.registration)
}

// registrationArm 返回归一化后的**记账起点**档位（口径同 `registrationMode`）。
func (o *Owner) registrationArm() string {
	if o == nil {
		return RegistrationArmRecord
	}
	if strings.TrimSpace(o.arm) == "" {
		return CurrentRegistrationArm()
	}
	return NormalizeRegistrationArm(o.arm)
}

// registrationDelete 返回归一化后的**删除语义**档位（口径同 `registrationMode`，见
// registration.go 的 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）。
func (o *Owner) registrationDelete() string {
	if o == nil {
		return RegistrationDeleteKeep
	}
	if strings.TrimSpace(o.delete) == "" {
		return CurrentRegistrationDelete()
	}
	return NormalizeRegistrationDelete(o.delete)
}

// registrationBind 返回归一化后的**绑定保护**档位（口径同 `registrationMode`，见
// registration.go 的 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`）。
func (o *Owner) registrationBind() string {
	if o == nil {
		return RegistrationBindKeep
	}
	if strings.TrimSpace(o.bind) == "" {
		return CurrentRegistrationBind()
	}
	return NormalizeRegistrationBind(o.bind)
}

// SkillGate* 是技能号门禁的判定来源，随结果一起落日志，便于线上区分
// 「PVF 真的查过」与「目录没加载所以放行」。
const (
	SkillGateJobScopedPVF  = "job_scoped_pvf"
	SkillGateCatalogAbsent = "catalog_unavailable"
	SkillGateJobUnresolved = "job_unresolved"
	SkillGateNotApplicable = "not_applicable"
)

// ApplyResult 描述一次 op1428 记录的落库结果。
type ApplyResult struct {
	CharacterID string
	Record      Record
	// Sentinel 为真表示这条是 ModeClear 枚举哨兵，只留取证键、不改状态键。
	Sentinel bool
	// SkillID 是这条记录声明的技能号（哨兵形态为 0）。
	SkillID uint16
	// SkillGate 是技能号门禁的判定来源（SkillGate* 之一）。
	SkillGate string
	// SlotCount / ForensicCount 是落库后的键数。
	SlotCount     int
	ForensicCount int
	// ForensicSkipped 为真表示取证键已达 MaxForensicRecords，本条未新增取证键
	// （状态键仍然照写）。
	ForensicSkipped bool
	// Replayed 为真表示这个 (Slot) 上原本就是同一条记录（幂等重放）。
	Replayed bool
	// LayoutUpgraded 为真表示这个槽上原本是同一条记录的**旧 6 字节行**，
	// 本次被改写成当前档位的规范字节形（字段完全相同，只有字节形变了）。
	// 这是一次性、可 grep 的迁移信号，**不是**内容变化；映射见 record.go。
	LayoutUpgraded bool
	// RegistrationMode 是本次落库时的登记窗口档位（registration.go）。
	RegistrationMode string
	// RegistrationArm 是本次落库时的**记账起点**档位（`NormalizeRegistrationArm`，
	// 见 registration.go 的 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`）。
	// `union` 档恒空（该档整段跳过登记记账）。
	RegistrationArm string
	// RegistrationBind 是本次落库时的**绑定保护**档位（`NormalizeRegistrationBind`，
	// 见 registration.go 的 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`）。
	// `union` 档恒空（该档整段跳过登记记账）。
	RegistrationBind string
	// RegistrationDelete 是本次落库时的**删除语义**档位（`NormalizeRegistrationDelete`，
	// 见 registration.go 的 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）：默认档
	// `keep` 下这次提交**不删任何行**，本可被删掉的那些行进 `RegistrationRetained`。
	RegistrationDelete string
	// BindingBearing 为真表示这一条记录**携带绑定信息**（`Record.BindingBearing`：
	// `Value != Slot`）——现场 `ModeRegister` 是，`ModeConfirm` 不是。
	BindingBearing bool
	// BindingPreserved 为真表示这一条记录不含绑定信息、而该槽位上已落库的行携带绑定，
	// 于是那一行被**原样保留**（默认档 `keep` 的行为）。`replace` 档恒 false。
	BindingPreserved bool
	// PreservedHex 是被保留下来的那一行的字节形（`BindingPreserved` 为真时才有值）。
	PreservedHex string
	// RegistrationWindowCount 是这条记录写完之后、当前窗口里的槽位数
	// （`union` 档恒 0：该档不做任何窗口记账）。
	RegistrationWindowCount int
	// RegistrationPendingCount 是这条记录写完之后、"arm 之前那一段 burst"
	// （`pending_*`）里的槽位数。未 arm 时才非 0；它是**纯记账键**（不进 `slot_*`
	// 集合、不进下行帧、不参与 slot_count/record_count），而提交一律以
	// `window_armed` 为前提 ⇒ 从不发哨兵的客户端行为与改动前逐字节相同。
	RegistrationPendingCount int
	// RegistrationCommitted 为真表示**本条是 `ModeClear` 哨兵，且它关闭的窗口里有
	// 记录** ⇒ 状态键集合已被换成窗口集合（移除真的生效）。空窗口永不提交，
	// 所以"哨兵"与"提交"不是同一件事。
	RegistrationCommitted bool
	// RegistrationTrigger 是这次提交的触发点（RegistrationTrigger* 之一）。未提交时为空。
	RegistrationTrigger string
	// RegistrationConfirmOnly 为真表示这次提交的窗口里**一条携带绑定的记录都没有**
	// （整段登记只声明了槽位集合），于是它只删无绑定的行。
	RegistrationConfirmOnly bool
	// RegistrationProtected 是这次提交**因为这一段登记没声明绑定而保留下来**的绑定行
	// （按 Slot 升序；`replace` 档恒 nil）。
	RegistrationProtected []Record
	// RegistrationRetained 是这次提交里**按旧语义（`delete=replace`）本来会被删掉、
	// 而被默认档保留下来的行**（按 Slot 升序；`replace` 档恒 nil）。
	RegistrationRetained []Record
	// RegistrationKept / RegistrationDropped 是这次提交**保留**与**删除**的行
	// （按 Slot 升序、按档位归一化）。未提交时都是 nil。
	RegistrationKept    []Record
	RegistrationDropped []Record
	UpdatedAt           time.Time
}

// Apply 校验并落库一条 op1428 记录。
//
// 拒绝语义（**具名、可 grep**）：
//
//   - 该记录声明的技能号在角色 job 的 PVF 技能目录里不存在 ⇒ `ErrSkillUnknown`
//     （包装后的错误里带 job 与 skill，见下），**整条不落库**；
//   - settings 仓储缺失 ⇒ `ErrStoreUnavailable`；
//   - 角色不存在 / 账号不匹配 ⇒ `ErrCharacterNotFound` / `ErrCharacterOwner`。
//
// 放行语义（fail-open，全部记在 SkillGate 里，绝不静默）：
//
//   - PVF 技能目录没加载（`catalog == nil`）⇒ `SkillGateCatalogAbsent`，仍然落库。
//     理由：目录缺失是**服务端装配问题**，不是"这条记录引用了不存在的技能"；
//     把它当成未知技能会把一次装配故障升级成"玩家登记全丢"，正是本改动要修的症状。
//   - 角色 job 列解析不出 u8 ⇒ `SkillGateJobUnresolved`，仍然落库。同上。
func (o *Owner) Apply(ctx context.Context, characterID string, accountID string, record Record) (ApplyResult, error) {
	if o == nil || o.characters == nil || o.settings == nil {
		return ApplyResult{}, ErrStoreUnavailable
	}
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return ApplyResult{}, ErrCharacterRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	character, found, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return ApplyResult{}, err
	}
	if !found {
		return ApplyResult{}, ErrCharacterNotFound
	}
	if wanted := strings.TrimSpace(accountID); wanted != "" && wanted != strings.TrimSpace(character.AccountID) {
		return ApplyResult{}, ErrCharacterOwner
	}

	result := ApplyResult{CharacterID: characterID, Record: record, Sentinel: record.Sentinel()}
	switch {
	case record.Sentinel():
		result.SkillGate = SkillGateNotApplicable
	case o.catalog == nil:
		result.SkillGate = SkillGateCatalogAbsent
	default:
		skillID, ok := record.SkillID()
		if !ok {
			result.SkillGate = SkillGateNotApplicable
			break
		}
		result.SkillID = skillID
		job, jobOK := characterJobByte(character.Job)
		if !jobOK {
			result.SkillGate = SkillGateJobUnresolved
			break
		}
		if _, present := o.catalog.Find(job, skillID); !present {
			return ApplyResult{}, fmt.Errorf("%w: job=%d skill=%d record=%s",
				ErrSkillUnknown, job, skillID, record.Hex())
		}
		result.SkillGate = SkillGateJobScopedPVF
	}

	stored, exists, err := o.settings.Load(ctx, Scope(characterID))
	if err != nil {
		return ApplyResult{}, err
	}
	if !exists {
		stored = dnfrepo.SettingsRecord{Scope: Scope(characterID), Values: map[string]string{}}
	}
	stored = dnfrepo.CloneSettings(stored)
	if stored.Values == nil {
		stored.Values = map[string]string{}
	}

	// 状态键：ModeClear 哨兵只写取证键，绝不碰 slot_*（理由见 ModeClear 注释）。
	//
	// 绑定保护（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`，默认 = 修复档）：
	// 一条**不含绑定信息**的记录（`Value == Slot`，现场 `ModeConfirm`）不许改掉同槽位上
	// 已落库的**携带绑定**的行。现场实证（2026-09-28T17:12:45Z，char 6）：那 9 条
	// `000011031100`/`00000e030e00`… 把 `slot_17=11003b00`（绑定 59）、
	// `slot_14=0e003e00`（绑定 62）… 覆写成 `(N,N)`，换装栏的 value 从此不再指向
	// list 1 里那 11 件换装物品。本档把那一行原样留下（记录里如实写
	// `binding_preserved=true`），仍然照常记取证键、照常进窗口（所以"移除"不受影响）。
	//
	// 只看**登记窗口档**（`union` 是"一键回退到改动前"的字面量，它连
	// `registration_bind` 都写回退字面量 `replace`，行为必须逐字节一致）。
	mode := o.registrationMode()
	bindTier := o.registrationBind()
	deleteTier := o.registrationDelete()
	protectBindings := mode != RegistrationUnion && bindTier == RegistrationBindKeep
	binding := record.BindingBearing()
	result.BindingBearing = binding
	if !record.Sentinel() {
		key := slotEntryKey(record.Slot)
		previous := stored.Values[key]
		result.Replayed = previous == record.Hex()
		result.LayoutUpgraded = recordRowUpgraded(previous, record)
		if !binding && protectBindings {
			if preserved, ok := preservedBindingRow(previous, record.Layout); ok {
				result.BindingPreserved = true
				result.PreservedHex = preserved.Hex()
			}
		}
		if !result.BindingPreserved {
			stored.Values[key] = record.Hex()
		}
	}
	forensicKey := entryRecordPrefix + record.Hex()
	if record.Sentinel() {
		forensicKey = entrySentinelPrefix + record.Hex()
	}
	if _, present := stored.Values[forensicKey]; present || countForensicEntries(stored.Values) < MaxForensicRecords {
		stored.Values[forensicKey] = record.Describe()
	} else {
		result.ForensicSkipped = true
	}

	// 登记窗口（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION`，默认 = 修复档，见 registration.go）。
	//
	// 顺序刻意放在 `countEntries` **之前**：哨兵提交会删行，`slot_count` 必须按
	// 提交后的集合算。`union`（一键回退）档整段跳过 —— 不写 `window_*`、
	// 不提交、不删，逐字节等于改动前（上面那段绑定保护同样跳过）。
	if mode != RegistrationUnion {
		result.RegistrationMode = mode
		result.RegistrationArm = o.registrationArm()
		result.RegistrationBind = bindTier
		result.RegistrationDelete = deleteTier
		if record.Sentinel() {
			// 哨兵 = 分隔符。**第一次**见到哨兵只是把记账打开（`window_armed=1`），
			// 那次提交面对的是空窗口 ⇒ 一个 `slot_*` 都不动。
			// 于是"客户端不发哨兵"的字符连一个键都不会新增。
			armedBefore := windowArmed(stored.Values)
			armRegistrationWindow(stored.Values)
			// **连续两个分隔符之间没有记录** ⇒ 这是"这条哨兵后面没有登记内容"的形状
			// （现场 09:08:27.525 / 09:08:28.058 那两条连着来的包）。这时把 arm 之前
			// 那一段 burst 丢掉：它属于**上一个**已经关掉的窗口，不是这一次的登记。
			// 这条规则只会**少提交**，永远不会多删一行 —— "零记录窗口不能清空换装表"
			// 的性质因此不受影响。
			if armedBefore && len(windowSlots(stored.Values)) == 0 {
				clearPendingBurst(stored.Values)
			}
			kept, dropped, protected, retained, confirmOnly, committed :=
				commitRegistrationWindow(stored.Values, record.Layout, result.RegistrationBind, deleteTier)
			result.RegistrationCommitted = committed
			result.RegistrationConfirmOnly = confirmOnly
			result.RegistrationKept = kept
			result.RegistrationDropped = dropped
			result.RegistrationProtected = protected
			result.RegistrationRetained = retained
			result.RegistrationWindowCount = len(kept)
			if committed {
				result.RegistrationTrigger = RegistrationTriggerSentinel
			}
		} else if windowArmed(stored.Values) {
			result.RegistrationWindowCount = openRegistrationWindow(stored.Values, record.Slot, binding)
		} else if result.RegistrationArm == RegistrationArmRecord {
			// **arm 之前那一段 burst**（现场 15:35 char 3 的形状：8 条记录 + 收尾哨兵，
			// 而该 scope 那一刻 `window_armed` 还不存在）。记账起点档位是默认档时才记；
			// 回退字面量 `sentinel` 下连这些键都不写（= 线上当前这一版）。
			result.RegistrationPendingCount = openPendingBurst(stored.Values, record.Slot, binding)
		}
	}

	slotCount, forensicCount := countEntries(stored.Values)
	stored.Values[entrySlotCount] = strconv.Itoa(slotCount)
	stored.Values[entryRecordCount] = strconv.Itoa(forensicCount)
	stored.Values[entrySource] = SourceCurrentEXE
	stored.UpdatedAt = time.Now().UTC()
	stored.Values[entryUpdatedAt] = stored.UpdatedAt.Format(time.RFC3339Nano)
	if err := o.settings.Save(ctx, stored); err != nil {
		return ApplyResult{}, err
	}
	result.SlotCount = slotCount
	result.ForensicCount = forensicCount
	result.UpdatedAt = stored.UpdatedAt
	return result, nil
}

// preservedBindingRow 判断同一个槽位上**已落库的那一行**是否携带绑定信息
// （`Value != Slot`）；是则把它解回来，供"不含绑定信息的记录不许覆盖它"这条规则使用。
//
// 读法与 `Load` 同一条路径（`parseStoredRow` 的 dual-read：4 与 6 字节行都认），
// 解析不出来（空值 / 长度不对 / 非法 hex）一律返回 false —— 那种情况下按今天的行为
// 覆盖，不额外发明任何保护。
func preservedBindingRow(previous string, layout Layout) (Record, bool) {
	previous = strings.TrimSpace(previous)
	if previous == "" {
		return Record{}, false
	}
	raw, err := hex.DecodeString(previous)
	if err != nil {
		return Record{}, false
	}
	record, ok := parseStoredRow(raw, layout)
	if !ok || !record.BindingBearing() {
		return Record{}, false
	}
	return record, true
}

func countForensicEntries(values map[string]string) int {
	count := 0
	for key := range values {
		if isRecordEntryKey(key) || strings.HasPrefix(key, entrySentinelPrefix) {
			count++
		}
	}
	return count
}

func countEntries(values map[string]string) (int, int) {
	slots, forensic := 0, 0
	for key := range values {
		switch {
		case strings.HasPrefix(key, entrySlotPrefix):
			if _, err := strconv.Atoi(strings.TrimPrefix(key, entrySlotPrefix)); err == nil {
				slots++
			}
		case isRecordEntryKey(key), strings.HasPrefix(key, entrySentinelPrefix):
			forensic++
		}
	}
	return slots, forensic
}

// characterJobByte mirrors the bridge's own projection of the character job
// column: `dnf_characters.job` is a numeric string, and only a value that fits
// one byte addresses a PVF job list.
func characterJobByte(job string) (byte, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(job), 10, 64)
	if err != nil || value < 0 || value > 0xFF {
		return 0, false
	}
	return byte(value), true
}
