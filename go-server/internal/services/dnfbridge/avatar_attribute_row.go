package dnfbridge

import (
	"encoding/binary"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 时装「选定属性」在 0x77 行里的位置 —— 现场根因与三条独立证据（2026-09-25）。
//
// 现场（香港 packet_log.txt，conn_id=game-000006 = test2 / char 3 / dnf:10001）：
// 客户端在 op518 里已经按**该件装扮自己 `[avatar select ability]` 的下标**把玩家选的
// 属性发上来了（例：头发 415560044 option=1），服务端也逐项存对了（持久化 extra
// `ext_data0=1`，真档回放逐项一致），但**发回客户端的 0x77 行把这个字节写在了 +0x0A**，
// 而客户端读的是 **+0x0B 的 u16（与耐久同一个字）**：于是每件装扮都显示下标 0
// —— 对头发/帽子就是「智力」。操作者原话「选了精神属性的头发时装，发给我的还是智力」
// 正是"这个字恒为 0"的表现。
//
// `+0x0B` 有三条互相独立的证据：
//
//  1. 参考实现 `ServerS4A21/.../Inventory/Models/ItemCore.cs`：装扮的选定属性就叫
//     `AbilityNo`，而它是 `get => Durability; set => Durability = value;`；同一份文件里
//     `AttrOffset = 9`、`DurabilityOffset = 10`、`SealFlagOffset = 12`，装扮创建
//     （`InventoryCreateService.ApplyAvatarDefaults`）只写 `AbilityNo`，`Attr` 保持 0
//     ⇒ **属性与耐久同字**，而 +0x0A 是强化字节（`Upgrade => Attr & 0x1F`）。
//
//  2. 本仓库自己的 0x77 映射：`currentItemListEquipmentExtData` 从 `raw[0x0A]` 取强化/
//     ext 字节，`currentItemListEquipmentDurability` 从 `raw[0x0B:0x0D]` 取耐久字；
//     `equip/owner.go buildCurrentEquipmentRawEntryFromMoveEvidence` 同样把耐久写进
//     `raw[0x0B:0x0D]`、把 packed 写进 `raw[0x0A]`。
//
//  3. PVF 的 43 字节 create-equipment 摘要（`buildCurrentTitleEquipmentRawEntry`：
//     slot@+0(u8)、itemID@+1）整体 +1 就是 0x77 行（slot 变成 u16）⇒ 摘要里
//     reinforce@+9 / durability@+10 在 0x77 行就是 **+0x0A / +0x0B**。
//
// 所以：装扮行的 +0x0A 一律写 0（装扮没有强化语义），玩家选的属性写进 +0x0B 的 u16。
// 持久化（extra `ext_data0` / `raw_entry_hex`）一个字节都不改 —— 只改这一处**行投影**。
const (
	currentItemListExtDataByteOffset = 0x0A
	currentItemListDurabilityOffset  = 0x0B
)

// currentItemListStackIsAvatar 判断一个背包堆叠是不是装扮行。
//
// 判据与参考实现一致（由 PVF `[equipment type]` 解析出的 item_kind=avatar），
// 再加一条"装扮栏 list 1 里的行必然是装扮"的容器事实；两条都不臆造数据。
func currentItemListStackIsAvatar(listType byte, stack dnfrepo.ItemStack) bool {
	if listType == currentExeInventoryAvatarListType {
		return true
	}
	return currentItemListStackExtraIsAvatar(stack.Extra)
}

// currentItemListStackExtraIsAvatar 是 extra 侧判据：发放端（点券礼包 op518、
// 商城 op64、装扮合成 op99、邮件领取）都会写 `item_kind=avatar` 与
// `equipment_type=[... avatar]`，所以这两个键足够识别装扮，不会误伤普通装备。
func currentItemListStackExtraIsAvatar(extra map[string]string) bool {
	if extra == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(extra["item_kind"]), "avatar") {
		return true
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(extra["equipment_type"])), "avatar")
}

// currentItemListAvatarAbility 解析装扮的"选定属性"下标：
//   - 首选持久化 extra（发放/合成路径都写 `ext_data0`）；
//   - 旧行没有该键时退回 0x77 行的 +0x0A（本修复前服务端自己写在那里）；
//   - 再退回耐久字（参考实现里两者本就是同一个字）。
func currentItemListAvatarAbility(extra map[string]string, raw []byte) uint16 {
	if value, ok := currentItemListExtraUint16OK(extra, "ext_data0", "ext0", "ability_no", "avatar_ability_no"); ok {
		return value
	}
	if len(raw) > currentItemListExtDataByteOffset {
		return uint16(raw[currentItemListExtDataByteOffset])
	}
	return 0
}

// currentItemListExtraUint16OK 与 sceneInventoryExtraUint16 同解析规则，但把
// "键存在且为合法 u16（含 0）"与"键缺失"分开：装扮属性 0 是合法值（下标 0），
// 不能与"没有证据"混为一谈，否则旧行的 0 会被误读成缺省。
func currentItemListExtraUint16OK(extra map[string]string, keys ...string) (uint16, bool) {
	value, ok := sceneInventoryExtraUint(extra, keys...)
	if !ok || value > 65535 {
		return 0, false
	}
	return uint16(value), true
}

// currentItemListEquipmentAvatarAbility 是穿戴态（装备表行）的同一份读取。
func currentItemListEquipmentAvatarAbility(equipped dnfrepo.EquipmentEntry) uint16 {
	if value, ok := currentItemListExtraUint16OK(equipped.Extra, "ext_data0", "ext0", "ability_no", "avatar_ability_no"); ok {
		return value
	}
	raw := equipped.RawEntry
	if currentEquipmentEntryHasCurrentRaw77(equipped) {
		if len(raw) >= currentItemListDurabilityOffset+2 {
			return binary.LittleEndian.Uint16(raw[currentItemListDurabilityOffset : currentItemListDurabilityOffset+2])
		}
		return 0
	}
	// 旧摘要行（43 字节 / 82 字节布局）里属性字就在 +10。
	if len(raw) >= 12 {
		return binary.LittleEndian.Uint16(raw[10:12])
	}
	return 0
}

// applyCurrentItemListAvatarAbilityRow 把装扮行写成人客户端真正读的形状：
// +0x0A = 0（装扮没有强化字节），+0x0B:0x0D = 属性下标。
func applyCurrentItemListAvatarAbilityRow(entry *currentItemListEntry, ability uint16) {
	if entry == nil {
		return
	}
	entry.setByte(currentItemListExtDataByteOffset, 0)
	binary.LittleEndian.PutUint16(entry.data[currentItemListDurabilityOffset:currentItemListDurabilityOffset+2], ability)
}

// applyCurrentEquipmentUpdateAvatarAbilityRow 是 op14 更新行（0x54）的同一处投影：
// 该行前 0x0D 字节与 0x77 行同序（slot/itemID/instance/强化/耐久字/seal）。
func applyCurrentEquipmentUpdateAvatarAbilityRow(entry *currentEquipmentUpdateEntry, ability uint16) {
	if entry == nil {
		return
	}
	entry.data[currentItemListExtDataByteOffset] = 0
	binary.LittleEndian.PutUint16(entry.data[currentItemListDurabilityOffset:currentItemListDurabilityOffset+2], ability)
}
