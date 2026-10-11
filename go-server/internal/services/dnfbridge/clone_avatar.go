package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是「稀有克隆装扮显示被克隆时装外观」这个功能的 dnfbridge 侧入口。
//
// 机制（全部照抄厂商/同族参考实现，不发明协议形状）：
//  1. 克隆装扮自己的 PVF 模板带 `[item category] clear avatar`，本身就是**透明件**
//     —— 这就是操作者看到"克隆装扮不显示外观"的直接原因；
//  2. "我克隆的是哪一件"不在 PVF 里，而是**穿上那一刻**写进穿戴行持久 Extra 的
//     一个 ItemID：厂商 `Game/Inventory/Models/AvatarDetail.cs:19`
//     `AvatarDetail.ClearAvatarId` + `Game/Inventory/Services/InventoryMoveService.cs:950-1005`
//     `SyncAvatarClearAvatarId`。写入点在装备域
//     `internal/modules/dnf/equip/owner.go` 的 `withCloneAvatarAppearance`；
//  3. 真正把这件克隆件"显示成"被克隆件的地方只有三处外观投影：
//     mode0 当前场景对象外观表（本包 buildCurrentActorMode0AppearanceSummaryFromEquipment，
//     厂商 `Game/Inventory/Builders/Noti2InventoryProjectionBuilder.cs:250-257`
//     `ResolveAppearanceItemId`）、角色名册/场景对象装备摘要
//     （本包 equipmentRosterSummary）、以及 mode1 选中角色装备创建行
//     （本包 currentMode1EquipmentObjectRows，厂商
//     `Network/Builders/Inventory/ItemListProtocolWriter.cs:299-307`）。
//
//  4. 外观记录（msg_id=2 的 NOTI2 外观列表）里还有第二个字段：厂商
//     `Game/Characters/CharacterAppearanceEntry.cs:68-80` 的 `FromBytes` 明写
//     `LinkItemId` 在 **+14**（State 之后的第一个 u32），`:29-30` 的 `DisplayItemId`
//     才是"这一格画哪件模板"；克隆件两个字段都要写：
//     `Noti2InventoryProjectionBuilder.cs:249-257` `ResolveAppearanceDisplayItemId`
//     返回 `ClearAvatarId`，`:259-272` `ResolveAppearanceLinkItemId` 返回 `core.ItemId`
//     （= 克隆件自己）——"显示被克隆件，并链接回真正拥有的克隆件"。
//     本包两处载体（userinfo subtype0 外观表、名册/mode0 外观摘要）原先只写显示件、
//     LinkItemId 恒 0，由 `cloneAvatarRowLinkItemID` 补齐（开关见下）。
//
// 开关：DNFBRIDGE_CLONE_AVATAR_APPEARANCE，默认 = 修复档（on）。
// 回退值 off/0/false/no/legacy 同时关掉写入与投影，逐字节回到改动前，不需要重编译。
//
// 开关：DNFBRIDGE_CLONE_AVATAR_ROW_CLEAR_ID，默认 = 修复档（on），只管外观记录里
// LinkItemId 那一个 u32；回退值 off/0/false/no/legacy 时该字段回到改动前的 0。
// 它同时受 DNFBRIDGE_CLONE_AVATAR_APPEARANCE 闸门约束：后者关掉时本字段也恒 0。

const (
	// cloneAvatarAppearanceEnv 是这条功能的单变量开关。
	cloneAvatarAppearanceEnv = "DNFBRIDGE_CLONE_AVATAR_APPEARANCE"

	// cloneAvatarRowClearIDEnv 只管外观记录（msg_id=2 NOTI2 外观列表）里的
	// LinkItemId u32：克隆件把"自己"写进去。非克隆行一个字节都不变。
	cloneAvatarRowClearIDEnv = "DNFBRIDGE_CLONE_AVATAR_ROW_CLEAR_ID"

	cloneAvatarAppearanceOn     = "on"
	cloneAvatarAppearanceOff    = "off"
	cloneAvatarAppearanceLegacy = "legacy"

	// currentCloneAvatarCategory 是 PVF 里标记"这是个克隆装扮、自身不画外观"的
	// `[item category]` 值。厂商同名字符串见 90原神开源0.1.8
	// `dnfbridge/clone_avatar.go:9`（同族参考实现），真档取证见本目录
	// clone_avatar_test.go 的 real-PVF 用例。
	currentCloneAvatarCategory = "clear avatar"
)

// cloneAvatarAppearanceSetting 解析 DNFBRIDGE_CLONE_AVATAR_APPEARANCE。
//
// **默认 on**（修复档）：未设置/空 = 修复行为。只有显式写 off/0/false/no/legacy
// 才回到改动前。bad=true 表示取值非法（已回退到 on），调用方在启动日志里告警。
//
// 取值方式与同包既有的 itemSlotFamilyGuardSetting（item_slot_family_guard.go:153）
// 一致：**每次调用读一次 env**，因此回退只需要改环境变量 + 重启，不需要重编译，
// 而且不经过 optionsFromConfig 的测试/探针构造路径同样受这个开关约束。
func cloneAvatarAppearanceSetting() (enabled bool, raw string, bad bool) {
	raw = strings.TrimSpace(os.Getenv(cloneAvatarAppearanceEnv))
	switch strings.ToLower(raw) {
	case "", cloneAvatarAppearanceOn, "1", "true", "yes", "current", "fix", "fixed":
		return true, raw, false
	case cloneAvatarAppearanceOff, "0", "false", "no", cloneAvatarAppearanceLegacy:
		return false, raw, false
	default:
		return true, raw, true
	}
}

// cloneAvatarAppearanceEnabled 是写入点与投影点共用的唯一判据。
func cloneAvatarAppearanceEnabled() bool {
	enabled, _, _ := cloneAvatarAppearanceSetting()
	return enabled
}

// cloneAvatarAppearanceMode 是启动日志里的档位串（on = 修复档 / off = 回退档）。
func cloneAvatarAppearanceMode() string {
	if cloneAvatarAppearanceEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// cloneAvatarRowClearIDSetting 解析 DNFBRIDGE_CLONE_AVATAR_ROW_CLEAR_ID。
//
// **默认 on**（修复档）：未设置/空 = 外观记录的 LinkItemId 写克隆件自己。
// 只有显式写 off/0/false/no/legacy 才回到改动前（该 u32 恒 0）。
// bad=true 表示取值非法（已回退到 on），调用方在启动日志里告警。
// 解析规则与 cloneAvatarAppearanceSetting 完全一致（同样每次调用读一次 env）。
func cloneAvatarRowClearIDSetting() (enabled bool, raw string, bad bool) {
	raw = strings.TrimSpace(os.Getenv(cloneAvatarRowClearIDEnv))
	switch strings.ToLower(raw) {
	case "", cloneAvatarAppearanceOn, "1", "true", "yes", "current", "fix", "fixed":
		return true, raw, false
	case cloneAvatarAppearanceOff, "0", "false", "no", cloneAvatarAppearanceLegacy:
		return false, raw, false
	default:
		return true, raw, true
	}
}

// cloneAvatarRowClearIDEnabled 是外观记录 LinkItemId 投影的唯一判据。
func cloneAvatarRowClearIDEnabled() bool {
	enabled, _, _ := cloneAvatarRowClearIDSetting()
	return enabled
}

// cloneAvatarRowClearIDMode 是启动日志里的档位串（on = 修复档 / off = 回退档）。
func cloneAvatarRowClearIDMode() string {
	if cloneAvatarRowClearIDEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// cloneAvatarRowLinkItemID 返回外观记录（msg_id=2 NOTI2 外观列表）里 LinkItemId
// 该写什么：这条穿戴行是"有克隆来源的克隆装扮"时返回**克隆件自己的 ItemID**，
// 其余任何情况返回 0（= 改动前的字节）。
//
// 语义与偏移全部来自厂商，不发明形状：
//   - 偏移：`Game/Characters/CharacterAppearanceEntry.cs:68-80` `FromBytes` ——
//     slot@+0、DisplayItemId@+1、ExpansionLen@+5、ExpansionData@+9、State@+13、
//     **LinkItemId@+14**、EnchantValue@+18、Flag20@+22；`:58-59` 的注释把它称为
//     "NOTI2外观列表中的关联物品ID"。
//   - 取值：`Game/Inventory/Builders/Noti2InventoryProjectionBuilder.cs:259-272`
//     `ResolveAppearanceLinkItemId` —— 装扮且 `ClearAvatarId != 0` 时返回 `core.ItemId`
//     （= 克隆件本身），否则 0。与 `:249-257` 的 DisplayItemId（返回 ClearAvatarId）
//     成对使用：显示被克隆件，链接回真正拥有的克隆件。
//
// 本包的两处载体都以这个返回值写 LinkItemId：
//   - userinfo subtype0 外观表（userinfo_selected_subtype0.go 的
//     currentSubtype0AppearanceEntries → writeCSharpAppearanceEntry）；
//   - 名册/场景对象 mode0 外观摘要（initial_equipment_roster_projection.go 的
//     equipmentRosterSummary → CharacterRosterEquipSummary.OptionalIDOrExpire →
//     character_roster_entries.go:187 / character_scene_object_tail.go:202）。
//
// 回退档恒为 0；DNFBRIDGE_CLONE_AVATAR_APPEARANCE 关掉时 cloneAvatarAppearanceLinkedItemID
// 也恒 0，因此本字段同样恒 0 —— 两个开关任一个取回退值，下行字节都逐字节回到改动前。
func cloneAvatarRowLinkItemID(equipped dnfrepo.EquipmentEntry) uint32 {
	if !cloneAvatarRowClearIDEnabled() {
		return 0
	}
	if cloneAvatarAppearanceLinkedItemID(equipped) == 0 {
		return 0
	}
	if equipped.ItemID <= 0 || equipped.ItemID > int64(^uint32(0)) {
		return 0
	}
	return uint32(equipped.ItemID)
}

// currentCloneAvatarClassifier 是注入 op19 装备搬运的 avatarClassifier。
//
// **回退档必须返回 nil**：装备域拿到 nil 就一个字节都不写，所以
// DNFBRIDGE_CLONE_AVATAR_APPEARANCE=off 之后不会再产生新的克隆外观键，
// 配合 cloneAvatarAppearanceLinkedItemID 的投影闸门，整条链路回到改动前。
func (s *Service) currentCloneAvatarClassifier() alignedcmd.AvatarClassifier {
	if s == nil || !cloneAvatarAppearanceEnabled() {
		return nil
	}
	return s.currentClassifyAvatarItem
}

// currentClassifyAvatarItem 同时回答"是不是装扮"和"是不是克隆装扮"，两个答案
// 都来自**活动运行时 PVF**，不接受物品名或固定 item id 表作为权威。
//
// 判据与同族参考实现 90原神开源0.1.8 `dnfbridge/clone_avatar.go:14-39` 逐条一致：
// 物品必须是 equipment 类，其 `[equipment type]` 必须映射到 actor 外观槽
// （currentEquipmentPlacementRuleForPVFType），克隆性另外要求该文档里存在
// `[item category] clear avatar`。
func (s *Service) currentClassifyAvatarItem(itemID uint32) (bool, bool) {
	if s == nil || itemID == 0 {
		return false, false
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return false, false
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil || definition.Kind != dungeonDropItemEquipment {
		return false, false
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		return false, false
	}
	rule, ok := currentEquipmentPlacementRuleForPVFType(definition.EquipmentType)
	if !ok || rule.class != currentEquipmentPlacementClassAvatar {
		return false, false
	}
	return true, currentPVFDocumentHasText(doc, "item category", currentCloneAvatarCategory)
}

// currentPVFDocumentHasText 在**所有**同名 section 里找一段文本。
//
// 必须自己遍历 doc.Sections 而不能用 doc.Text/doc.Texts：internal/modules/dnf/pvf
// 的 Section(name)（types.go:67-84）命中第一个同名 section 就返回，
// 而真实 equ 文档里 `[item category]` 会重复出现（真档克隆件同时有
// `clear avatar` 和别的类别），只读第一段就会漏判。
// 同族参考实现：90原神开源0.1.8 `dnfbridge/clone_avatar.go:41-61`
// （含 TestCurrentPVFDocumentHasRepeatedCloneAvatarCategory 夹具）。
func currentPVFDocumentHasText(doc *dnfpvf.Document, sectionName, want string) bool {
	if doc == nil {
		return false
	}
	want = normalizeCloneAvatarPVFText(want)
	for _, section := range doc.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), strings.TrimSpace(sectionName)) ||
			section.Start < 0 || section.End > len(doc.Tokens) || section.Start > section.End {
			continue
		}
		for _, token := range doc.Tokens[section.Start:section.End] {
			if token.Kind != dnfpvf.TokenString && token.Kind != dnfpvf.TokenIdent {
				continue
			}
			if normalizeCloneAvatarPVFText(token.Value) == want {
				return true
			}
		}
	}
	return false
}

// normalizeCloneAvatarPVFText 去掉 PVF 文本值的包裹符与空白后小写化。
func normalizeCloneAvatarPVFText(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(value), "`'\"")))
}

// cloneAvatarAppearanceLinkedItemID 返回"这条穿戴行应该显示成哪件道具"，
// 0 表示这不是一件有来源的克隆装扮。**回退档恒为 0**，因此三个投影点都会退回
// 各自的原始 ItemID，即使库里已经留下了键也不会改变下行字节。
func cloneAvatarAppearanceLinkedItemID(equipped dnfrepo.EquipmentEntry) uint32 {
	if !cloneAvatarAppearanceEnabled() {
		return 0
	}
	return sceneInventoryExtraUint32(equipped.Extra, dnfrepo.CloneAvatarAppearanceExtraKey)
}

// currentMode1EquipmentLinkedItemID 是 mode1 装备创建行的"链接外观"字段。
//
// 语义见厂商 `Game/Inventory/Builders/Noti2InventoryProjectionBuilder.cs:259-272`
// `ResolveAppearanceLinkItemId`：一件有克隆来源的装扮，行自身的 itemID 仍是克隆件，
// 被克隆件写在链接字段里。同族参考实现 90原神开源0.1.8
// `dnfbridge/userinfo_current_equipment_projection.go:129` 把这个键放在别名表首位。
//
// 回退档下 cloneAvatarAppearanceLinkedItemID 恒 0，别名表与改动前完全一致。
func currentMode1EquipmentLinkedItemID(equipped dnfrepo.EquipmentEntry) uint32 {
	if linked := cloneAvatarAppearanceLinkedItemID(equipped); linked != 0 {
		return linked
	}
	return sceneInventoryExtraUint32(equipped.Extra, "mode1_linked_item_id", "mode1_clone_or_linked_id", "clone_or_linked_id")
}
