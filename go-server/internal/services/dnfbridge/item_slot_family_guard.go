package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是操作者问题清单第 48 条（「物品栏可随意拖动至任意类别」）的**一层**闸门：
// op19（ENUM_CMDPACKET_MOVE_ITEMSPACE）把道具拖到**同一个多分页容器内的另一页**时，
// 若该道具的 PVF 家族根本不属于那一页，就记事件（`log` 档）或拒绝并回失败 ACK（`enforce` 档）。
//
// # 一、厂商依据（本版源码，逐行确证）
//
// `S4A21-完整包-20260923\源码\服务端\Server\DfoServer\`
//
//   - `Game\Inventory\Services\ItemSlotBoundService.cs:52-106` `TryGetSlotRange`：家族 → 容器 + 槽段
//
//     KindEquipment          → Main       9..64    （随 mainExpandStage 收缩：64/56/48）
//     KindConsumable         → Main       65..120
//     KindMaterial           → Main       121..176
//     KindQuest              → Main       177..232
//     KindExpertJobMaterial  → Main       233..288
//     KindAvatarEmblem       → Main       289..351
//     KindUnknown            → Main       3..8     （快捷槽）
//     KindAvatar             → Avatar     0..209
//     KindCreature           → Pet        0..139
//     KindCreatureEquipment  → Pet        140..188
//     KindCreatureConsumable → Pet        189..239
//     KindGuildMedal         → GuildMedal 0..48
//     KindGuardianGem        → GuildMedal 49..97
//
//   - 入口 `:108-123` `IsValidSlotForKind(itemKind, listType, slotIndex, mainExpandStageKey)`；
//     移动路径的调用点 `Game\Inventory\Services\InventoryMoveService.cs:593-598`
//     （不匹配 ⇒ `InventoryMoveServiceError.InvalidDestinationSlot`）。
//
//   - 失败包形状 `Network\Builders\Inventory\MoveItemSpaceAckBuilder.cs`：
//     成功 = u8 0x01 | u8 srcList | i16 srcSlot | i32 moveValue | u8 dstList | i16 dstSlot（11 字节），
//     与本仓 `internal/modules/dnf/inventory/body.go:87 buildMoveItemspaceSuccessAck` **逐字节同形**；
//     失败 = u8 0x00 | u8 errCode | u8 srcList | u8 dstList（4 字节；errCode 0x02 = InvalidOperation），
//     厂商调用点 `Network\Handlers\InventoryHandler.Move.cs:100`（CLIENT_ECHO 分支）与 `:111`（通用失败分支）。
//
//   - 仓库/账号金库有独立校验（`InventoryInsertService.ValidatePersonalCargoInsert` /
//     `ValidateAccountCargoInsert`，调用点 `InventoryMoveService.cs:561-575`）——**本文件不碰**。
//
// # 二、我们自己的分页口径（真源 = item_target_container.go，不新造第二套）
//
// 家族判定**复用发放落点的唯一真源** `currentPVFItemTargetContainer`
// （`item_target_container.go:98-179`），它读的正是运行时 PVF 家族在落库时写下的
// `item_kind` + `stackable_type` / `equipment_type`（`dungeon_drop_pickup.go:692-709`、
// `ItemStackIdentityExtraKeys`）。本文件只把它的结果当作"这件道具应该在哪一页"，
// 与厂商表逐段核对（**差异一律以我仓现状为准，不用厂商数字改玩家可见布局**）：
//
//	家族                        厂商                  本仓（判据真源）        差异
//	[material] 族               Main 121..176         Main 121..176           同
//	[material expert job]       Main 233..288         Main 233..288           同
//	[quest] 族                  Main 177..232         Main 177..232           同
//	[avatar emblem]             Main 289..351         Main 289..344           ⚠ 我仓尾端 344
//	其它 stackable（[waste]…）  Main 65..120          Main 65..120            同
//	[feed]/[creature] 堆叠品    Pet 189..239          list 7 189..238         ⚠ 我仓尾端 238
//	artifact *（宠物装备）      Pet 140..188          list 7 140..188         同
//	[creature]（宠物本体）      Pet 0..139            list 7 0..139           同
//	avatar（装扮部位）          Avatar 0..209         list 1 0..500           ⚠ 我仓 0..500
//	[flag]（公会勋章）          GuildMedal 0..48      list 38 0..48           同（wire 容器 38）
//	[flag gem]（守护珠）        GuildMedal 49..97     list 38 49..97          同
//	装备（其它）                Main 9..64            Main 9..64              同
//	快捷格                      Main 3..8             Main 3..8               同
//
// 我仓 289..344 与 list 7 189..238 是**当前客户端已验证**的口径
// （`inventory.sortSegment` 引 NoPack sub_232A9F0 接受 189..238；
// `item_target_container.go` 的发放全量取证）。凡两侧段不同的，只要目标槽落在
// "两侧都认同的公共段"就按**我仓段**判；落在差异尾段（345..351）则按
// "目标槽不属于任何已知页"退化为不校验（见下），不会因为厂商数字而误拒。
//
// mainExpandStage：厂商用它把装备/消耗品/材料/任务/副职五段按背包扩容档收缩
// （`GetExpandedMainEnd`，满档 24；档位 0/8/16/24）。**本仓没有这个维度的等价物**
// （`ItemSlotBoundService.MainExpandStage*` 在本仓没有任何对应存储、请求字段或投影），
// 所以按题目"取不到就不要猜"的要求**不校验该维度**：只按满档段判，扩容不足时
// 末段之外的目标槽会落进"不属于任何已知页 ⇒ 不校验"这条退化路径，不会误拒。
//
// # 三、这一层只做什么（不做什么）
//
// 只判**同容器**（src list == dst list）、且目标是**主背包 list 0 / 勋章容器 list 38 的已知分页**
// 的拖拽：家族期望页 ≠ 目标页 ⇒ 命中。其余一律不管：
//
//   - 跨容器移动（背包↔仓库↔装扮↔宠物↔账号金库↔交易↔穿戴端点）——那是别的 owner 的校验，本文件不碰；
//   - 目标槽不在任何已知分页（快捷格 3..8、0..2、账号共享水晶/灵魂仓库 354..365、
//     我仓 `[material*4]` 的 345..359 扩展段、装扮徽章尾段 345..351…）⇒ 不校验；
//   - 家族取不到（`item_kind` 缺失或不是 stackable/equipment；`stackable_type`/`equipment_type` 缺失）⇒ 不校验；
//   - 源格没有该道具（客户端空发、反向拆分、源格是账号共享行）⇒ 不校验；
//   - 家族期望容器不是目标容器（例如背包里错位的装扮/宠物/勋章/宠物消耗品）⇒ 不校验。
//
// 也就是说：**只拦"明显不匹配"，任何"取不到"都退化为不拦。**
//
// 只读取证（本地 `dnf_local-20260806-112541.sql` 的 `dnf_inventory_item_extra`，
// 全部 376 个 (容器, 槽) 行按同一张表复算）：主背包 list 0 的 136 行里，
// 徽章 47 行落在 289..344、消耗品 22 行落在 65..120、装备 20 行落在 9..64、
// 材料 14 行落在 121..176、任务品 5 行落在 177..232（全部✅与表一致）；
// **31 行根本没有家族键**（`stackable_type`/`equipment_type` 缺失）⇒ 全部走"不校验"；
// 2 行确实错位（slot 158 `[enchant waste]` 在材料页、slot 69 `[creature]` 更名卡在消耗品页，
// 后者正是 2026-09-24「商城更名卡落到消耗品页」那条已知根因的现场残留）。
// ⇒ 表与现场一致；退化路径不是可选项而是必须项（约 23% 的行取不到家族）。
//
// # 四、开关（一键回退）
//
//	DNFBRIDGE_ITEM_SLOT_FAMILY_GUARD
//	  - 未设置 / 空 / 非法值（**默认 log**）：**只记事件、不拦**。包、写库、既有日志全部不变，
//	    只多一条命中事件 `item-slot-family-guard-mismatch`。默认取 log 的理由：拒绝有误伤风险
//	    （见第五节），而只记录零风险、且能立刻拿到"会不会误拒"的现场数据。
//	  - `log`：同上（显式写法）。
//	  - `enforce`：命中即**不路由到 owner**（不写库、不发成功 ACK、不刷新容器），
//	    只回厂商那 4 字节失败体（class1 / msg_id 19，与本仓成功 ACK 同一传输与分类）。
//	  - `off` / `0` / `false` / `legacy`：**逐字节回到今天**——不校验、不回包、零新增日志，
//	    连"多读一次背包"都没有（在解码请求体之前就返回）。
//
// # 五、已知风险（enforce 档）
//
//  1. **失败包形状未经本客户端实测**：本仓从未回过 4 字节 op19 失败体。可信度依据是
//     (a) 我方成功 ACK 与厂商成功体逐字节同形（同一个 dispatcher 先消费 +0x00 成功标志，
//     见 `inventory/body.go:88-89` 注释）；(b) 厂商同一代客户端就是这么回失败的；
//     (c) 本仓 op44/op251 的失败体（首字节 0x00）已在线上验证能被本客户端解释
//     （`use_stackable_rejection.go`）。仍属"厂商形状 + 同族先例"，**不是抓包确证**。
//  2. **客户端本地预测的回滚未知**：厂商失败时也只回这 4 字节、不补容器刷新
//     （`InventoryHandler.Move.cs:94-113` 失败分支直接 return），本文件同样不补刷新
//     （补刷新会替换客户端刚建出的物品对象，本仓已有该类事故记录）。
//     若本客户端不回滚本地拖拽，enforce 档下会出现"看上去移动了、服务端没动"的短暂不一致，
//     重登或下一次容器刷新即恢复。
//  3. **误伤面**：家族键来自落库时的 PVF 快照；若某条发放路径写错家族，玩家在**正确**分页内
//     的拖拽也会被拒。加上"客户端是否用 op19 实现某种自动归类移动"未取证，
//     所以默认必须先跑 `log` 档观察，确认无 `reason=family_page_mismatch` 的合法移动后再切 `enforce`。
const (
	itemSlotFamilyGuardEnv     = "DNFBRIDGE_ITEM_SLOT_FAMILY_GUARD"
	itemSlotFamilyGuardLog     = "log"
	itemSlotFamilyGuardEnforce = "enforce"
	itemSlotFamilyGuardOff     = "off"

	// 厂商 MoveItemSpaceAckBuilder.InvalidOperationErrorCode。
	currentItemSlotFamilyMoveErrorInvalidOperation byte = 0x02

	itemSlotFamilyGuardMismatchEvent = "item-slot-family-guard-mismatch"
)

// itemSlotFamilyGuardSetting 解析 DNFBRIDGE_ITEM_SLOT_FAMILY_GUARD。
// 默认 log（只记事件不拦）；只有显式 `enforce` 才真拒绝，显式 off/0/false/legacy 才逐字节回退。
// bad=true 表示取值非法（已回退到 log），调用方在启动日志里告警。
func itemSlotFamilyGuardSetting() (mode string, raw string, bad bool) {
	raw = strings.TrimSpace(os.Getenv(itemSlotFamilyGuardEnv))
	switch strings.ToLower(raw) {
	case "", itemSlotFamilyGuardLog:
		return itemSlotFamilyGuardLog, raw, false
	case itemSlotFamilyGuardEnforce:
		return itemSlotFamilyGuardEnforce, raw, false
	case itemSlotFamilyGuardOff, "0", "false", "legacy":
		return itemSlotFamilyGuardOff, raw, false
	default:
		return itemSlotFamilyGuardLog, raw, true
	}
}

// itemSlotFamilyGuardMode 是启动日志与事件日志里的档位串。
func itemSlotFamilyGuardMode() string {
	mode, _, _ := itemSlotFamilyGuardSetting()
	return mode
}

// itemSlotFamilyPage 是一页的物理槽段（list + 闭区间）。
type itemSlotFamilyPage struct {
	listType byte
	start    int16
	end      int16
}

// currentItemSlotFamilyGuardPages 是**本仓与客户端对齐过的多分页容器**的页表。
// 段值全部引用既有常量（真源仍是 item_target_container.go / guardian_gem_protocol.go），
// 这里不写任何字面量，避免第二套口径漂移。
var currentItemSlotFamilyGuardPages = []itemSlotFamilyPage{
	{dnfrepo.MainInventoryListType, currentPVFMainEquipmentSlotStart, currentPVFMainEquipmentSlotEnd},
	{dnfrepo.MainInventoryListType, currentPVFMainConsumableStart, currentPVFMainConsumableEnd},
	{dnfrepo.MainInventoryListType, currentPVFMainMaterialStart, currentPVFMainMaterialEnd},
	{dnfrepo.MainInventoryListType, currentPVFMainQuestStart, currentPVFMainQuestEnd},
	{dnfrepo.MainInventoryListType, currentPVFMainExpertJobStart, currentPVFMainExpertJobEnd},
	{dnfrepo.MainInventoryListType, currentPVFMainEmblemStart, currentPVFMainEmblemEnd},
	{currentGuildMedalInventoryListType, currentGuildMedalPageSlotStart, currentGuildMedalPageSlotEnd},
	{currentGuildMedalInventoryListType, currentGuardianGemPageSlotStart, currentGuardianGemPageSlotEnd},
}

// currentItemSlotFamilyGuardPageFor 返回槽位所在的已知页。第二个返回值为 false 表示
// "这个槽不属于任何已知分页"（快捷格、账号共享仓、扩展段、越界槽…）——此时**不校验**。
func currentItemSlotFamilyGuardPageFor(listType byte, slot int16) (itemSlotFamilyPage, bool) {
	for _, page := range currentItemSlotFamilyGuardPages {
		if page.listType == listType && slot >= page.start && slot <= page.end {
			return page, true
		}
	}
	return itemSlotFamilyPage{}, false
}

// itemSlotFamilyGuardDecision 是一次命中的证据（只用于日志）。
type itemSlotFamilyGuardDecision struct {
	ItemID       int64
	ItemKind     string
	Family       string
	Reason       string
	Destination  itemSlotFamilyPage
	ExpectedList byte
	ExpectedFrom int16
	ExpectedTo   int16
}

// currentItemSlotFamilyGuardDefinition 从落库堆叠的 Extra 还原"发放落点判定"需要的
// PVF 家族视图（item_target_container.go 的输入形状）。第二个返回值为 false =
// 家族取不到 ⇒ 本维度不校验（现场约 23% 的行属于这一类）。
func currentItemSlotFamilyGuardDefinition(stack dnfrepo.ItemStack) (dungeonDropItemDefinition, string, bool) {
	kind := strings.ToLower(strings.TrimSpace(stack.Extra["item_kind"]))
	itemID := stack.ItemID
	if itemID <= 0 || itemID > int64(^uint32(0)) {
		return dungeonDropItemDefinition{}, "", false
	}
	definition := dungeonDropItemDefinition{
		ItemID:  uint32(itemID),
		PVFPath: strings.TrimSpace(stack.Extra["pvf_path"]),
	}
	switch dungeonDropItemKind(kind) {
	case dungeonDropItemStackable:
		family := normalizeDungeonDropStackableType(stack.Extra["stackable_type"])
		if family == "" {
			return dungeonDropItemDefinition{}, "", false
		}
		definition.Kind = dungeonDropItemStackable
		definition.StackableType = family
		return definition, family, true
	case dungeonDropItemEquipment:
		family := strings.ToLower(strings.TrimSpace(stack.Extra["equipment_type"]))
		if family == "" {
			return dungeonDropItemDefinition{}, "", false
		}
		definition.Kind = dungeonDropItemEquipment
		definition.EquipmentType = family
		return definition, family, true
	default:
		return dungeonDropItemDefinition{}, "", false
	}
}

// currentItemSlotFamilyGuardMismatch 是这一层闸门的**唯一判定**：给定源格上的堆叠与目标 (list, slot)，
// 返回"是否明显不匹配"。第二个返回值为 false = 不校验（取不到、跨容器、目标不在已知页、同页）。
func currentItemSlotFamilyGuardMismatch(
	stack dnfrepo.ItemStack,
	destinationListType byte,
	destinationSlot int16,
) (itemSlotFamilyGuardDecision, bool) {
	destinationPage, known := currentItemSlotFamilyGuardPageFor(destinationListType, destinationSlot)
	if !known {
		// 目标槽不属于任何已知分页（快捷格 / 账号共享仓 / 扩展段 / 越界）：不校验这个维度。
		return itemSlotFamilyGuardDecision{}, false
	}
	definition, family, ok := currentItemSlotFamilyGuardDefinition(stack)
	if !ok {
		return itemSlotFamilyGuardDecision{}, false
	}
	expected := currentPVFItemTargetContainer(definition)
	if expected.ListType != destinationListType {
		// 期望容器 ≠ 目标容器：这是"跨容器"或"这件道具本来就放错了容器"，
		// 不属于本层（单层：同容器内的分页），交给既有 owner 校验。
		return itemSlotFamilyGuardDecision{}, false
	}
	expectedPage, known := currentItemSlotFamilyGuardPageFor(expected.ListType, expected.SlotStart)
	if !known || expectedPage.start != expected.SlotStart || expectedPage.end != expected.SlotEnd {
		// 家族期望段本身不是我们与客户端对齐过的某一页（例如我仓 [material*4] 的 345..359
		// 扩展段，或厂商 289..351 多出来的 345..351）：取不到就不猜，直接不校验。
		return itemSlotFamilyGuardDecision{}, false
	}
	if expectedPage == destinationPage {
		return itemSlotFamilyGuardDecision{}, false
	}
	return itemSlotFamilyGuardDecision{
		ItemID:       stack.ItemID,
		ItemKind:     string(definition.Kind),
		Family:       family,
		Reason:       "family_page_mismatch",
		Destination:  destinationPage,
		ExpectedList: expected.ListType,
		ExpectedFrom: expected.SlotStart,
		ExpectedTo:   expected.SlotEnd,
	}, true
}

// buildCurrentItemSlotFamilyGuardRejectionBody = 厂商 MoveItemSpaceAckBuilder.BuildError：
// u8 0x00 | u8 errCode(0x02) | u8 srcList | u8 dstList（4 字节）。
func buildCurrentItemSlotFamilyGuardRejectionBody(sourceListType, destinationListType byte) []byte {
	return []byte{0x00, currentItemSlotFamilyMoveErrorInvalidOperation, sourceListType, destinationListType}
}

// currentItemSlotFamilyGuardMoveCandidate 做**零副作用**的候选筛选：只有"同容器、
// 目标是已知分页"的 op19 才需要读一次背包。非候选一律不读库、不记日志。
func currentItemSlotFamilyGuardMoveCandidate(request dnfinventory.MoveItemspaceRequest) bool {
	if request.SourceListType != request.DestinationListType {
		return false
	}
	_, known := currentItemSlotFamilyGuardPageFor(request.DestinationListType, request.DestinationSlotIndex)
	return known
}

// applyItemSlotFamilyGuard 是 op19 上的闸门入口（在 game_aligned.go 的 op19 分支最前面调用）。
//
// 返回 (blocked, err)：
//   - off 档：恒 (false, nil)，不解析请求体、不读库、不写日志；
//   - log 档：命中时写一条事件后恒 (false, nil)，包与写库与改动前逐字节相同；
//   - enforce 档：命中时回 4 字节失败体并返回 (true, nil)，调用方必须直接结束这条命令
//     （不路由 owner、不发成功 ACK、不做任何刷新）。
func (s *Service) applyItemSlotFamilyGuard(
	session *gameSession,
	repos dnfrepo.Group,
	body []byte,
) (bool, error) {
	mode, _, _ := itemSlotFamilyGuardSetting()
	if mode == itemSlotFamilyGuardOff {
		return false, nil
	}
	if s == nil || session == nil {
		return false, nil
	}
	// Log mode has no gameplay effect. With no packet-event sink, loading the
	// entire inventory here only duplicates the owner's transactional read.
	// Enforce mode still performs its check regardless of logging state.
	if mode == itemSlotFamilyGuardLog {
		s.mu.Lock()
		logging := s.packetLog != nil
		s.mu.Unlock()
		if !logging {
			return false, nil
		}
	}
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return false, nil
	}
	if !currentItemSlotFamilyGuardMoveCandidate(request) {
		return false, nil
	}
	if repos.Inventory == nil || session.selectedCharacterID == 0 {
		return false, nil
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	record, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		// 读不到就"不校验"：**不打日志**，保证 log 档的契约是"一次命中一条事件"。
		// 这种情况下 owner 自己会给出它那条具名拒绝原因。
		return false, nil
	}
	stack, ok := record.Slots[currentCeraShopInventorySlotKey(request.SourceListType, request.SourceSlotIndex)]
	if !ok || stack.ItemID <= 0 {
		// 源格没有该道具（客户端空发 / 反向拆分）：本层不判，交给 owner 的既有路径。
		return false, nil
	}
	decision, mismatch := currentItemSlotFamilyGuardMismatch(stack, request.DestinationListType, request.DestinationSlotIndex)
	if !mismatch {
		return false, nil
	}
	s.logPacketEvent(itemSlotFamilyGuardMismatchEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"selected_character_id", session.selectedCharacterID,
		"mode", mode,
		"item_kind", decision.ItemKind,
		"item_family", decision.Family,
		"item_id", decision.ItemID,
		"src_list", request.SourceListType,
		"src_slot", request.SourceSlotIndex,
		"dst_list", request.DestinationListType,
		"dst_slot", request.DestinationSlotIndex,
		"expected_list", decision.ExpectedList,
		"expected_slots", fmt.Sprintf("%d-%d", decision.ExpectedFrom, decision.ExpectedTo),
		"dst_page_slots", fmt.Sprintf("%d-%d", decision.Destination.start, decision.Destination.end),
		"reason", decision.Reason)
	if mode != itemSlotFamilyGuardEnforce {
		return false, nil
	}
	if err := s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketMoveItemspace),
		buildCurrentItemSlotFamilyGuardRejectionBody(request.SourceListType, request.DestinationListType),
		dnfproto.DefaultChannelClassification,
	); err != nil {
		return true, err
	}
	return true, nil
}
