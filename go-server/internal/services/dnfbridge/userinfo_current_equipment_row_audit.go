package dnfbridge

import (
	"os"
	"strconv"
	"strings"
)

// 本文件是「登录后武器耐久显示 0」这轮调查的**取证与开关落点**。它一个下行字节都不改
// （默认档与回退档的下行逐字节相同），只做两件事：
//
//	A. 把 mode1 create 行的**逐行事实**打成一条可 grep 的事件
//	   （槽位 / 类型 / 实际下发的耐久 / 是否带两段 raw 块 / 行宽 / 实例），
//	   让"下一次登录到底是哪一档、武器行到底发了什么"从 packet_log.txt 就能判定。
//	B. 给"给 type>11 行也补两段 raw 块"这个**假设**留一个显式实验开关
//	   （默认关；打开即与当前客户端解析错位的风险自负，见下）。
//
// ── 为什么默认档不改行字节（现场 + 客户端反汇编，逐条列证据） ─────────────
//
//  1. 登录行与进图行**同源同字节**：两者都走
//     `buildCurrentUserInfoMode1WithEquipmentInContext`（登录
//     `initial_town_player_state.go:167` 走 `buildCurrentSelectedUserInfoMode1Body...`；
//     进图 `scene_post_start_map.go:381` 走
//     `buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(..., true)`），
//     行本身由同一个 `writeCurrentMode1EquipmentCreateRow`
//     （`userinfo_current_equipment_wire.go:11`）写出。现场 `packet_log.txt` 里
//     13:35:08（登录）、13:36:28 / 13:41:45（进图）、13:52:34（登录）四次的
//     `row_summary` 中武器行都是 `12:101040493:type=12:durability=48:...:raw=0:create=1`。
//     ⇒ 进图能显示正确耐久的那一行，本身就是 raw=0；"客户端靠 raw 块渲染耐久"
//     这一条被操作者自己的复现步骤直接否掉。
//
//  2. 客户端自己就不给 type>0x0b 的行读 raw 块（90CN `DNF.exe`，静态反汇编，
//     未启动客户端、未挂调试器）：
//     · 每行字段读序在 `sub_1D77560`（RVA 0x1977560）：byte 槽位 → u32 itemID →
//     u32 实例 → byte extAttr → **u16 耐久（RVA 0x19776AB，落在行 +10）** →
//     u32 链接/封印 → u32 aux → byte aux → byte bind → u16 marker →
//     `sub_1D6E020`(u8 count + count*8) → u32 → `sub_225CCA0`(u8 count + count*4)
//     → `sub_1E636E0`(u16) → `sub_1C61F40`(索引态) → 3+2+5 字节 →
//     `sub_3457C50`(u32 len + data) ×2 → byte → `sub_3457C50` ×1。
//     · 两段可选 raw 块在 `sub_225C960`/`sub_225C9B0`（RVA 0x1E5C960/0x1E5C9B0）里：
//     只有当 `sub_229ACD0(itemID)` 为真时才调用 `sub_3457C50` 去读那段块；而
//     `sub_229ACD0`（RVA 0x1E9ACD0）就是 `itemTemplate->[0x444] <= 0x0b`
//     （`cmp dword ptr [eax+0x444], 0xb` / `setbe al`）。
//     · 厂商 A21 参考实现同一取舍：`EquipmentType.cs:21` `Weapon = 12`，
//     `IsCostumeBarSlot`（同文件:134-138）= 0..11，`ItemListProtocolWriter.cs:41`
//     只在 `IsCostumeBarSlot(slot)` 时写 avatar/jewel 块。
//     ⇒ 类型 12（武器）走的就是**不读 raw 块**的那条分支；今天服务端不写这两段
//     8 字节是**对的**。给武器行硬塞 8 字节会让客户端从武器行开始逐行错位
//     （槽 12 是 raw=1 区段之后的**第一行**，其后 13..32 全靠它对齐）。
//
//  3. 耐久字段本身在两条载体上都是 48 且偏移正确：mode1 create 行 +10（u16，
//     客户端 RVA 0x19776AB 读的就是它）；list 3 的 0x77 行 +0x0B（现场
//     `SEND msg_id=14` 行 `0c00 6dc10506 fec99a3b 0f 3000 ...` 里 `0x0030`）。
//     数据库里该行 `raw[0x0B:0x0D]=48`、`durability=48`、`max_durability=48`。
//
//  4. 因此本轮的结论是：**不是投影行的字段缺口**。登录与进图的差别在客户端
//     初始化窗口里自己的那次"复述穿戴态"op19（现场 13:52:36.251Z
//     `src=(0,10,IV=0) dst=(3,12,IV=0x0605C16D)`，服务端判 `noopGate=login_wear_reassert_noop`
//     `changed=false` 并补发两条逐槽行）。那条链路的文件（`login_equipment_container*.go`、
//     `equip_login_wear*`、`game_aligned.go`）**不在本轮可改范围**，故本轮只交付
//     "让下一次登录可判定"的取证 + 回归钉子，并把结论与建议交给上游。
//
// ── 开关表（都在消费点自读 env，不进 options.go） ─────────────────────────
//
//	DNFBRIDGE_MODE1_EQUIPMENT_ROW_AUDIT=on（默认，含未设置）
//	    每次 mode1 装备投影额外打一条
//	    `game-upper-selected-userinfo-mode1-equipment-row-audit`，含武器行的
//	    slot/type/durability_sent/raw_blocks/row_bytes/instance 与整表逐行摘要。
//	off（及 0/false/no/legacy/disable/disabled/none）
//	    一键回退：不打这条事件。**下行字节两档完全相同**（本开关不碰 wire）。
//
//	DNFBRIDGE_MODE1_EQUIPMENT_RAW_BLOCKS=evidence（默认，含未设置）
//	    按客户端 `sub_229ACD0` 的判据（type <= 0x0b 才读那两段 raw 块）投影。
//	all_types（实验档，**不是**修复档）
//	    给 type>11 的行也补两段 raw 块（空块时 +8 字节）。这是"武器耐久 0 是缺
//	    raw 块"这一假设的**唯一**验证手段；打开前请先读本文件第 2 条：客户端
//	    自己的判据是 type<=0x0b，打开会使武器行与其后所有行错位。
const (
	// mode1EquipmentRowAuditEnv 是"mode1 装备投影是否打逐行取证事件"的开关名。
	mode1EquipmentRowAuditEnv = "DNFBRIDGE_MODE1_EQUIPMENT_ROW_AUDIT"
	// mode1EquipmentRawBlocksEnv 是"哪些行带两段 raw 块"的开关名。
	mode1EquipmentRawBlocksEnv = "DNFBRIDGE_MODE1_EQUIPMENT_RAW_BLOCKS"

	mode1EquipmentRowAuditOn  = "on"
	mode1EquipmentRowAuditOff = "off"

	mode1EquipmentRawBlocksEvidence = "evidence"
	mode1EquipmentRawBlocksAllTypes = "all_types"
)

// mode1EquipmentRowAuditEnabled 回答"要不要打逐行取证事件"。
// 只有**显式**写 off 及其别名才回退；其余取值（含未设置、写错的字串）都打。
func mode1EquipmentRowAuditEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(mode1EquipmentRowAuditEnv))) {
	case mode1EquipmentRowAuditOff, "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// mode1EquipmentRowAuditSwitch 只用于事件/诊断。
func mode1EquipmentRowAuditSwitch() string {
	if mode1EquipmentRowAuditEnabled() {
		return mode1EquipmentRowAuditOn
	}
	return mode1EquipmentRowAuditOff
}

// mode1EquipmentRawBlocksForAllTypes 回答"是否打开'type>11 也补 raw 块'实验档"。
// 只有**显式**写 all_types（及其别名）才打开；默认与写错的值都是客户端验证过的档。
func mode1EquipmentRawBlocksForAllTypes() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(mode1EquipmentRawBlocksEnv))) {
	case mode1EquipmentRawBlocksAllTypes, "all", "all-types", "force", "experiment":
		return true
	default:
		return false
	}
}

// mode1EquipmentRawBlocksSwitch 只用于事件/诊断。
func mode1EquipmentRawBlocksSwitch() string {
	if mode1EquipmentRawBlocksForAllTypes() {
		return mode1EquipmentRawBlocksAllTypes
	}
	return mode1EquipmentRawBlocksEvidence
}

// currentMode1EquipmentRowAudit 把每一行投影成紧凑的 key=value 片段（只有 ASCII，
// 不含空格，便于 grep/awk），字段顺序与现场调查的关注点一致：
// slot / type / durability_sent（= 写进 create 行 +10 的那个 u16）/ raw_blocks /
// row_bytes（该行真实下行字节数）/ instance（create 行 +5）/ create / verified。
func currentMode1EquipmentRowAudit(rows []currentMode1EquipmentObjectRow) string {
	if len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		typePart := "unknown"
		if row.equipmentTypeKnown {
			typePart = strconv.FormatUint(row.equipmentType, 10)
		}
		var writer packetWriter
		writeCurrentMode1EquipmentCreateRow(&writer, row)
		parts = append(parts, "slot="+strconv.Itoa(int(row.slot))+
			",type="+typePart+
			",durability_sent="+strconv.Itoa(int(row.durability))+
			",raw_blocks="+boolDigit(row.readsRawBlocks)+
			",row_bytes="+strconv.Itoa(len(writer.bytes()))+
			",instance="+strconv.FormatUint(uint64(row.instance), 10)+
			",create="+boolDigit(row.createEnabled)+
			",verified="+boolDigit(currentMode1EquipmentCreateRowHasVerifiedState(row)))
	}
	return strings.Join(parts, ";")
}

// mode1EquipmentWeaponAuditRow 找到审计里最关心的那一条：装备槽（type>0x0b）里
// 第一个"有耐久语义"的行——现场就是槽 12 的武器。找不到就返回 nil。
func mode1EquipmentWeaponAuditRow(rows []currentMode1EquipmentObjectRow) (currentMode1EquipmentObjectRow, bool) {
	for _, row := range rows {
		if row.equipmentTypeKnown && row.equipmentType > 0x0b && row.durabilityKnown && row.durability != 0 {
			return row, true
		}
	}
	return currentMode1EquipmentObjectRow{}, false
}

// mode1EquipmentWeaponAuditFields 返回武器行那四个"下一次登录要判定"的字段。
// 没有可判定的武器行时返回显式的 none，避免日志里出现空值歧义。
func mode1EquipmentWeaponAuditFields(rows []currentMode1EquipmentObjectRow) []any {
	row, ok := mode1EquipmentWeaponAuditRow(rows)
	if !ok {
		return []any{"weapon_audit", "none"}
	}
	var writer packetWriter
	writeCurrentMode1EquipmentCreateRow(&writer, row)
	return []any{
		"weapon_audit", "present",
		"weapon_slot", strconv.Itoa(int(row.slot)),
		"weapon_type", strconv.FormatUint(row.equipmentType, 10),
		"weapon_durability_sent", strconv.Itoa(int(row.durability)),
		"weapon_raw_blocks", boolDigit(row.readsRawBlocks),
		"weapon_row_bytes", strconv.Itoa(len(writer.bytes())),
		"weapon_instance", strconv.FormatUint(uint64(row.instance), 10),
		// 客户端证据（90CN DNF.exe 静态反汇编，见本文件头部）：
		"client_durability_read", "row_plus_10_u16_sub_1D77560_0x19776AB",
		"client_raw_block_gate", "item_template_plus_0x444_le_0x0b_sub_229ACD0_via_sub_225C960",
		"raw_blocks_experiment", mode1EquipmentRawBlocksSwitch(),
	}
}
