package dnfbridge

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) alignedMailboxItemResolverForCommand(opcode dnfenum.CmdPacket) (alignedcmd.MailboxItemResolver, error) {
	needsPrice := opcode == dnfenum.CmdPacketMailboxSend || opcode == dnfenum.CmdPacketMultiMailboxSend
	if !needsPrice && opcode != dnfenum.CmdPacketMailboxExtractItem {
		return nil, nil
	}
	// 只有领取（op95）才会把附件落成实例：装备/时装要在这一刻拿到 quality_seed
	// （实例身份）。发送（op94/op315）不落地实例，就不做无意义的种子生成。
	claiming := opcode == dnfenum.CmdPacketMailboxExtractItem
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	return func(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
		definition, resolveErr := catalog.ResolveItem(itemID)
		if resolveErr != nil {
			return alignedcmd.MailboxItemResolution{}, resolveErr
		}
		resolution, err := mailboxItemResolutionFromPVF(definition, needsPrice)
		if err != nil {
			return resolution, err
		}
		if claiming {
			// 见 currentMailAttachmentQualitySeedExtra：缺了它，领出来的装备
			// 在 op19 穿上时会因为"raw 行重建失败"被静默拒绝（重登脱落）。
			projected, seedErr := currentMailAttachmentQualitySeedExtra(resolution.DefaultExtra, definition)
			if seedErr != nil {
				return alignedcmd.MailboxItemResolution{}, fmt.Errorf("mailbox claim item=%d quality seed: %w", itemID, seedErr)
			}
			resolution.DefaultExtra = projected
		}
		if listType, slotStart, slotEnd, socketData, isAvatar := currentMailboxAvatarResolution(catalog, definition); isAvatar {
			resolution.TargetListType = listType
			resolution.SlotStart = slotStart
			resolution.SlotEnd = slotEnd
			resolution.StackLimit = 1
			resolution.AvatarSocketData = socketData
		}
		if claiming && s.epicPieceGrantEnabled() {
			// 史诗碎片（`etc/epicpieceinfo.etc [piece list]` 的成员，真档 966 条）：
			// 标上 EpicPieceID 之后 mail 模块不写任何背包行，改记账号级图鉴账本。
			// 只在领取期、且两个开关都在 on 档时置位；否则为零 ⇒ 附件照旧堆进
			// 材料页，逐字节等于改动前。见 epic_piece_grant.go 文件头（含现场
			// 封包 hex 与真档依据）。
			if pieceID, isPiece := epicPieceCreditTarget(s, itemID); isPiece {
				resolution.EpicPieceID = pieceID
			}
		}
		if claiming && s.cubeWarehouseRoutingEnabled() {
			// 小晶块/灵魂一律进账号晶块仓库的固定格（354..365），不进主背包材料页。
			// 判据是 dnfrepo.AccountWarehouseSlotForItem（唯一映射）；回退档或映射
			// 未命中时字段为零 ⇒ 附件照旧入包，逐字节等于改动前。
			// 现场与证据见 cube_warehouse_routing.go 文件头。
			if slot, isCube := dnfrepo.AccountWarehouseSlotForItem(int64(itemID)); isCube {
				resolution.AccountWarehouseSlot = slot
			}
		}
		return resolution, nil
	}, nil
}

// currentMailboxAvatarResolution 处理装扮类附件：目标容器是装扮栏(list 1)，
// 皮肤/光环还要带上它们按运行时 PVF 声明的创建期徽章孔向量。
// 附件本身只带 itemID（GM 面板邮件就是这么发的），所以这两件事都必须在领取端
// 补齐，否则玩家收到的时装既不在装扮栏、也没有徽章孔。
func currentMailboxAvatarResolution(
	catalog *pvfDungeonDropCatalog,
	definition dungeonDropItemDefinition,
) (byte, int16, int16, []byte, bool) {
	if catalog == nil || catalog.source == nil || definition.Kind != dungeonDropItemEquipment {
		return 0, 0, 0, nil, false
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		return 0, 0, 0, nil, false
	}
	equipmentType, found := document.Text("equipment type")
	if !found {
		return 0, 0, 0, nil, false
	}
	rule, ok := currentEquipmentPlacementRuleForPVFType(equipmentType)
	if !ok || rule.class != currentEquipmentPlacementClassAvatar {
		return 0, 0, 0, nil, false
	}
	const (
		mailboxAvatarSlotStart int16 = 0
		mailboxAvatarSlotEnd   int16 = 500
	)
	var socketData []byte
	if socketTypes := currentAvatarCreationSocketTypes(catalog.source, definition.PVFPath, rule); len(socketTypes) > 0 {
		// 皮肤([skin avatar])与光环([aurora avatar])按运行时 PVF 声明天生带孔
		// （皮肤是 [avatar emblem socket num] 2，光环是 [emblem socket default]）；
		// 普通时装部位不预开孔。孔位向量由装扮栏 list 1 的可选 blob 承载，所以这
		// 里只交给领取路径写进堆栈 Extra，不能烘进固定行。
		var data [currentAvatarSocketBytes]byte
		currentSetAvatarSocketTypes(&data, socketTypes)
		socketData = append([]byte(nil), data[:]...)
	}
	return 1, mailboxAvatarSlotStart, mailboxAvatarSlotEnd, socketData, true
}

func mailboxItemResolutionFromPVF(definition dungeonDropItemDefinition, needsPrice bool) (alignedcmd.MailboxItemResolution, error) {
	// A listed, successfully parsed PVF item remains authoritative even when
	// its document omits [price]. The current composer accepts those items and
	// displays only the fixed attachment postage, so an absent price contributes
	// zero value instead of invalidating the durable attachment.
	if needsPrice && definition.Price < 0 {
		return alignedcmd.MailboxItemResolution{}, fmt.Errorf("mailbox item %d has invalid negative runtime-PVF price", definition.ItemID)
	}
	stackLimit := definition.StackLimit
	policy := currentPVFItemPlacementPolicy(definition)
	if definition.Kind == dungeonDropItemEquipment {
		stackLimit = 1
	}
	resolution := alignedcmd.MailboxItemResolution{
		Price:           definition.Price,
		PVFPath:         definition.PVFPath,
		Kind:            string(definition.Kind),
		EquipmentType:   definition.EquipmentType,
		SlotStart:       definition.SlotStart,
		SlotEnd:         definition.SlotEnd,
		StackLimit:      stackLimit,
		PreferQuickSlot: policy.PreferQuickSlots,
		AllowQuickSlots: policy.AllowQuickSlots,
	}
	// 附件行本身可能没有实例到期时间（GM 面板发的邮件就是这样）。当前客户端把
	// "到期时间为 0"渲染成"已超过使用期限"，所以这里按运行时 PVF 先算好创建期
	// 到期投影，领取时由 mail 模块补进堆栈（附件已经带了到期时间时不覆盖）。
	//
	// 投影必须先过 currentPVFItemDefinitionForGrantAt：一整批"完成类型"券在运行时
	// PVF 里只声明了相对 [usable period]、没有绝对 [expiration date]
	// （Lv1~90主线任务完成券 10164630 = stackable/10164001/10164630.stk、
	//  Lv1~90主线/成就任务完成券 10301410 = stackable/10301001/10301410.stk，
	// 两者都是 [stackable type] `[etc]` + [usable period] 1），
	// 只看 definition.ExpirationDate 会把它们当成"永久"，投影整个落空。
	// 该 helper 是仓库既有的发放期口径：相对期限在这里（实例创建时刻）解析一次，
	// 绝对日期原样返回，所以两条声明都不会漏。
	grantAt := time.Now().UTC()
	grantDefinition, grantErr := currentPVFItemDefinitionForGrantAt(definition, grantAt)
	if grantErr != nil {
		return alignedcmd.MailboxItemResolution{}, fmt.Errorf("mailbox claim item=%d usable period: %w", definition.ItemID, grantErr)
	}
	if grantDefinition.Kind == dungeonDropItemStackable && !grantDefinition.ExpirationDate.IsZero() {
		projected, _ := applyCurrentPVFItemExpirationAt(
			dnfrepo.ItemStack{ItemID: int64(grantDefinition.ItemID), Count: 1},
			grantDefinition,
			grantAt,
		)
		resolution.ExpireAt = projected.ExpireAt
		if len(projected.Extra) > 0 {
			resolution.DefaultExtra = projected.Extra
		}
	}
	// 到期投影只负责"有到期日"的道具（`applyCurrentPVFItemExpirationAt` 在没有
	// [expiration date] 时直接返回），所以身份信息必须单独补：官方发放路径
	// （点券套餐/商城/盒子）都会给堆栈写 item_kind/pvf_path/stackable_type，而 GM 面板
	// 邮件没有附件 Extra。宠物更名卡的改名校验（pet.isCurrentPetRenameCard）正是按
	// pvf_path + stackable_type 判定的——缺了它，领出来的卡"看得见、改不了名"。
	resolution.DefaultExtra = mergeMailboxItemIdentityExtra(resolution.DefaultExtra, definition)
	// 装备/时装类附件的耐久证据同样要在领取端补齐：GM 面板邮件没有附件 Extra，
	// 于是领出来的装备既显示 0 耐久、也修不了（修理路径要求 durability +
	// max_durability，缺证据就失败关闭）。真源是运行时 PVF 的 [durability]。
	// 堆叠物没有耐久语义，helper 内部按 Kind 硬门禁，绝不写。
	// 交付端在盘里旧邮件上也做同一次修补（reconcileCurrentMailAttachmentExpirations）。
	resolution.DefaultExtra = mergeCurrentMailAttachmentDurabilityExtra(resolution.DefaultExtra, definition)
	// 宠物背包的合法槽位不在主背包里：[creature] 消耗品（宠物更名卡）与 [feed]
	// 属于 list 7 的宠物消耗品段，[creature] 装备（宠物本体）属于 list 7 的
	// 0..139。玩家期望（2026-09-24 现场确认）与 A21 参考实现一致：更名卡就该待在
	// 宠物栏消耗品段；改名请求带的也是这一段里的槽位（见 pet/rename.go）。
	//
	// 判定不再各写一套：容器/分段一律问 currentPVFItemTargetContainer（唯一真源）。
	container := currentPVFItemTargetContainer(definition)
	switch {
	case container.PetBody:
		resolution.TargetListType = container.ListType
		resolution.SlotStart = container.SlotStart
		resolution.SlotEnd = container.SlotEnd
		resolution.StackLimit = 1
		return resolution, nil
	case container.PetConsumable:
		resolution.TargetListType = container.ListType
		resolution.SlotStart = container.SlotStart
		resolution.SlotEnd = container.SlotEnd
		return resolution, nil
	case container.GuildMedal, container.GuardianGem:
		// 公会勋章 / 公会守护珠的容器是 wire list 38（同一个 98 格容器的两个
		// 49 格分页：勋章 0..48、守护珠 49..97）。现场报障「勋章获得时直接进
		// 装备栏」「守护珠获得时直接进消耗品栏」都发生在领取期：解析出来的
		// domain 默认是主背包页。这里与宠物段同样处理——容器/区间由
		// currentPVFItemTargetContainer 给出，邮件模块只按 (list, slot) 落行。
		resolution.TargetListType = container.ListType
		resolution.SlotStart = container.SlotStart
		resolution.SlotEnd = container.SlotEnd
		if container.GuildMedal {
			resolution.StackLimit = 1
		}
		return resolution, nil
	}
	return resolution, nil
}

// mergeMailboxItemIdentityExtra 把运行时 PVF 解析出的道具身份并入领取投影的
// DefaultExtra（缺键才补，绝不覆盖附件自带的值）。
func mergeMailboxItemIdentityExtra(
	base map[string]string,
	definition dungeonDropItemDefinition,
) map[string]string {
	identity := make(map[string]string, 4)
	if definition.Kind != "" {
		identity["item_kind"] = string(definition.Kind)
	}
	if pvfPath := strings.TrimSpace(definition.PVFPath); pvfPath != "" {
		identity["pvf_path"] = pvfPath
	}
	// Persist the same positive PVF cap as dungeon/shop instance constructors.
	// The claim allocator's StackLimit alone does not survive account-cargo moves.
	if definition.Kind == dungeonDropItemStackable && definition.StackLimit > 0 {
		identity["stack_limit"] = strconv.FormatInt(definition.StackLimit, 10)
	}
	if stackableType := strings.TrimSpace(definition.StackableType); stackableType != "" {
		identity["stackable_type"] = stackableType
	}
	if definition.Kind == dungeonDropItemEquipment {
		if equipmentType := strings.TrimSpace(definition.EquipmentType); equipmentType != "" {
			identity["equipment_type"] = equipmentType
		}
	}
	if len(identity) == 0 {
		return base
	}
	if base == nil {
		return identity
	}
	for key, value := range identity {
		if _, exists := base[key]; !exists {
			base[key] = value
		}
	}
	return base
}
