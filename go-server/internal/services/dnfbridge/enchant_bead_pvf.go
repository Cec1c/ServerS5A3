package dnfbridge

import (
	"errors"
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// alignedEnchantBeadResolverForCommand keeps PVF loading request-driven. Only
// the real current enchant-by-bead command needs this dependency; other
// aligned commands do not open or scan the item catalog. The resolver mirrors
// the 86JP InventoryEnchantStore metadata chain: bead [monster card id] /
// [enchant index] / [bead limited usable item] (legacy [target item id]), card [string data] equipment-type
// whitelist and [enchant table]/[enchant index] upgrade-count table, and the
// target equipment's [equipment type].
func (s *Service) alignedEnchantBeadResolverForCommand(opcode dnfenum.CmdPacket) (alignedcmd.EnchantBeadResolver, error) {
	if opcode != dnfenum.CmdPacketEnchantByBead {
		return nil, nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errDungeonDropSourceRequired
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	return func(beadItemID int64, targetItemID int64) (alignedcmd.EnchantBeadResolution, error) {
		return resolveCurrentEnchantBeadMetadata(catalog, source, beadItemID, targetItemID)
	}, nil
}

// resolveCurrentEnchantBeadMetadata resolves one bead/target template pair
// through the active runtime PVF. Unresolvable or non-stackable beads and
// beads without an enchant card are reported as a zero CardItemID (the owner
// maps that to the client-visible invalid-bead code); only catalog/document
// read failures surface as errors so the mutation fails closed.
func resolveCurrentEnchantBeadMetadata(catalog *pvfDungeonDropCatalog, source dnfpvf.Source, beadItemID int64, targetItemID int64) (alignedcmd.EnchantBeadResolution, error) {
	if catalog == nil || source == nil {
		return alignedcmd.EnchantBeadResolution{}, errDungeonDropSourceRequired
	}
	if beadItemID <= 0 || beadItemID > int64(^uint32(0)) || targetItemID <= 0 || targetItemID > int64(^uint32(0)) {
		return alignedcmd.EnchantBeadResolution{}, nil
	}

	resolution := alignedcmd.EnchantBeadResolution{}
	beadDefinition, err := catalog.ResolveItem(uint32(beadItemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			return resolution, nil
		}
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("resolve enchant bead item=%d: %w", beadItemID, err)
	}
	if beadDefinition.Kind != dungeonDropItemStackable {
		return resolution, nil
	}
	beadDocument, err := parseDungeonCardPVFDocument(source, beadDefinition.PVFPath)
	if err != nil {
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("parse enchant bead item=%d path=%s: %w", beadItemID, beadDefinition.PVFPath, err)
	}
	resolution.TargetWhitelist = append([]int64(nil), beadDocument.Ints("bead limited usable item")...)
	if len(resolution.TargetWhitelist) == 0 {
		resolution.TargetWhitelist = append([]int64(nil), beadDocument.Ints("target item id")...)
	}
	if _, random := beadDocument.Int("random title enchant"); random {
		return resolveCurrentRandomTitleEnchantMetadata(catalog, source, beadDocument, targetItemID)
	}
	cardItemID, found := beadDocument.Int("monster card id")
	if !found {
		cardItemID, found = beadDocument.Int("enchant index")
	}
	if !found || cardItemID <= 0 || cardItemID > int64(^uint32(0)) {
		return resolution, nil
	}
	resolution.CardItemID = cardItemID

	cardDefinition, err := catalog.ResolveItem(uint32(cardItemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			resolution.CardItemID = 0
			return resolution, nil
		}
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("resolve enchant card item=%d: %w", cardItemID, err)
	}
	if cardDefinition.Kind != dungeonDropItemStackable {
		resolution.CardItemID = 0
		return resolution, nil
	}
	resolution.CardPVFPath = cardDefinition.PVFPath
	cardDocument, err := parseDungeonCardPVFDocument(source, cardDefinition.PVFPath)
	if err != nil {
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("parse enchant card item=%d path=%s: %w", cardItemID, cardDefinition.PVFPath, err)
	}
	resolution.AllowedEquipmentTypes = append([]string(nil), currentEnchantCardParts(cardDocument)...)
	resolution.UpgradeCounts = append([]int64(nil), cardDocument.Ints("enchant index")...)
	if tableTokens, ok := cardDocument.Section("enchant table"); ok {
		// 嵌套表逐个读取实际级数，附魔属性整数不属于升级级数。
		if stages := currentMonsterCardStages(cardDocument); len(stages) > 0 {
			resolution.UpgradeCounts = nil
			for _, stage := range stages {
				resolution.UpgradeCounts = append(resolution.UpgradeCounts, int64(stage))
			}
		} else {
			// 没有嵌套级数时保持旧平铺表的已有口径。
			for _, token := range tableTokens {
				if token.Kind == dnfpvf.TokenInt {
					resolution.UpgradeCounts = append(resolution.UpgradeCounts, token.Int)
				}
			}
		}
	}

	targetDefinition, err := catalog.ResolveItem(uint32(targetItemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			return resolution, nil
		}
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("resolve enchant target item=%d: %w", targetItemID, err)
	}
	resolution.TargetKind = string(targetDefinition.Kind)
	if targetDefinition.Kind != dungeonDropItemEquipment {
		return resolution, nil
	}
	targetDocument, err := parseDungeonCardPVFDocument(source, targetDefinition.PVFPath)
	if err != nil {
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("parse enchant target item=%d path=%s: %w", targetItemID, targetDefinition.PVFPath, err)
	}
	equipmentType, found := targetDocument.Text("equipment type")
	if !found {
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("enchant target item=%d path=%s missing [equipment type]", targetItemID, targetDefinition.PVFPath)
	}
	resolution.TargetEquipmentType = equipmentType
	return resolution, nil
}

// currentEnchantCardParts 取怪物卡片 [string data] 里的**可附魔部位表**。
//
// PVF 原文形状（运行时 /opt/dnf90/data/dnf/Script.pvf 实测）：
//
//	[string data]
//	`Interface/MonsterCard/MonsterCard_image.img` `[weapon]`
//	[/string data]
//
// 第一项是卡片图标路径，其余每一项都是一个部位（`[creature]` / `[weapon]` /
// `[coat]` / `[amulet] [wrist] [ring]` / `[title name]` …）。这就是"宝珠能打哪个
// 部位"的官方判据：目标装备自己的 `[equipment type]` 必须出现在这张表里。
//
// 全量取证（1808 个 `[stackable type] [enchant waste]` 宝珠，30 种部位表）：
// 77 个宝珠的卡片部位表恰好只有 `[creature]`，**没有任何一张卡片同时列出
// `[creature]` 与装备部位** ⇒ 同一颗宝珠不可能既能打宠物又能打装备。
//
// 这里跳过"图标项"用形状判断（`.img` / 路径分隔符）而不是死写 stringData[1:]：
// 图标缺省时旧写法会把真实的第一个部位当成图标丢掉，部位表因此变窄。
func currentEnchantCardParts(document *dnfpvf.Document) []string {
	values := document.Texts("string data")
	parts := make([]string, 0, len(values))
	for index, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if index == 0 && currentEnchantCardIconEntry(trimmed) {
			continue
		}
		parts = append(parts, trimmed)
	}
	return parts
}

// currentEnchantCardIconEntry 判断 [string data] 的第一项是不是卡片图标（图片路径），
// 而不是部位标识。
func currentEnchantCardIconEntry(value string) bool {
	lowered := strings.ToLower(value)
	return strings.Contains(lowered, ".img") || strings.ContainsAny(value, `/\`)
}

// resolveCurrentEnchantCardMetadata validates a raw monster card used through
// an enchanter player store. Unlike the bead command, the supplied stack is
// already the card, so no bead-to-card indirection or target-item whitelist is
// involved; the card's PVF equipment-type and upgrade-count tables remain
// authoritative.
func resolveCurrentEnchantCardMetadata(catalog *pvfDungeonDropCatalog, source dnfpvf.Source, cardItemID, targetItemID int64) (alignedcmd.EnchantBeadResolution, error) {
	resolution := alignedcmd.EnchantBeadResolution{CardItemID: cardItemID}
	if catalog == nil || source == nil {
		return alignedcmd.EnchantBeadResolution{}, errDungeonDropSourceRequired
	}
	if cardItemID <= 0 || cardItemID > int64(^uint32(0)) || targetItemID <= 0 || targetItemID > int64(^uint32(0)) {
		return alignedcmd.EnchantBeadResolution{}, nil
	}
	cardDefinition, err := catalog.ResolveItem(uint32(cardItemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			return alignedcmd.EnchantBeadResolution{}, nil
		}
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("resolve enchant card item=%d: %w", cardItemID, err)
	}
	if cardDefinition.Kind != dungeonDropItemStackable {
		return alignedcmd.EnchantBeadResolution{}, nil
	}
	resolution.CardPVFPath = cardDefinition.PVFPath
	cardDocument, err := parseDungeonCardPVFDocument(source, cardDefinition.PVFPath)
	if err != nil {
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("parse enchant card item=%d path=%s: %w", cardItemID, cardDefinition.PVFPath, err)
	}
	resolution.AllowedEquipmentTypes = append([]string(nil), currentEnchantCardParts(cardDocument)...)
	resolution.UpgradeCounts = append([]int64(nil), cardDocument.Ints("enchant index")...)
	if tableTokens, ok := cardDocument.Section("enchant table"); ok {
		// 嵌套表逐个读取实际级数，附魔属性整数不属于升级级数。
		if stages := currentMonsterCardStages(cardDocument); len(stages) > 0 {
			resolution.UpgradeCounts = nil
			for _, stage := range stages {
				resolution.UpgradeCounts = append(resolution.UpgradeCounts, int64(stage))
			}
		} else {
			// 没有嵌套级数时保持旧平铺表的已有口径。
			for _, token := range tableTokens {
				if token.Kind == dnfpvf.TokenInt {
					resolution.UpgradeCounts = append(resolution.UpgradeCounts, token.Int)
				}
			}
		}
	}
	targetDefinition, err := catalog.ResolveItem(uint32(targetItemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			return resolution, nil
		}
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("resolve enchant target item=%d: %w", targetItemID, err)
	}
	resolution.TargetKind = string(targetDefinition.Kind)
	if targetDefinition.Kind != dungeonDropItemEquipment {
		return resolution, nil
	}
	targetDocument, err := parseDungeonCardPVFDocument(source, targetDefinition.PVFPath)
	if err != nil {
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("parse enchant target item=%d path=%s: %w", targetItemID, targetDefinition.PVFPath, err)
	}
	equipmentType, found := targetDocument.Text("equipment type")
	if !found {
		return alignedcmd.EnchantBeadResolution{}, fmt.Errorf("enchant target item=%d path=%s missing [equipment type]", targetItemID, targetDefinition.PVFPath)
	}
	resolution.TargetEquipmentType = equipmentType
	return resolution, nil
}

// Random-title beads carry PVF config/row identifiers, not a monster-card item.
func resolveCurrentRandomTitleEnchantMetadata(catalog *pvfDungeonDropCatalog, source dnfpvf.Source, bead *dnfpvf.Document, targetItemID int64) (alignedcmd.EnchantBeadResolution, error) {
	r := alignedcmd.EnchantBeadResolution{}
	kind, _ := bead.Text("stackable type")
	config, ok := bead.Int("random title enchant")
	if !ok || config <= 0 || config > 0xffff || !strings.EqualFold(strings.TrimSpace(kind), "[auto enchant]") {
		return r, nil
	}
	if len(bead.Ints("monster card id")) > 0 || len(bead.Ints("enchant index")) > 0 {
		return r, nil
	}
	r.TargetWhitelist = append([]int64(nil), bead.Ints("bead limited usable item")...)
	if len(r.TargetWhitelist) == 0 {
		return alignedcmd.EnchantBeadResolution{}, nil
	}
	allowed := false
	for _, id := range r.TargetWhitelist {
		if id == targetItemID {
			allowed = true
		}
	}
	if !allowed {
		return alignedcmd.EnchantBeadResolution{}, nil
	}
	parts := bead.Texts("string data")
	hasTitle := false
	for _, part := range parts {
		if strings.EqualFold(strings.TrimSpace(part), "[title name]") {
			hasTitle = true
		}
	}
	if !hasTitle {
		return alignedcmd.EnchantBeadResolution{}, nil
	}
	target, err := catalog.ResolveItem(uint32(targetItemID))
	if err != nil {
		return r, err
	}
	if target.Kind != dungeonDropItemEquipment {
		return alignedcmd.EnchantBeadResolution{}, nil
	}
	document, err := parseDungeonCardPVFDocument(source, target.PVFPath)
	if err != nil {
		return r, err
	}
	targetType, _ := document.Text("equipment type")
	if !strings.EqualFold(strings.TrimSpace(targetType), "[title name]") {
		return alignedcmd.EnchantBeadResolution{}, nil
	}
	random, err := parseCurrentRandomTitleEnchantCatalog(source)
	if err != nil {
		return r, err
	}
	row, err := random.choose(uint16(config))
	if err != nil {
		return r, err
	}
	r.RandomTitleEnchantConfigID = uint16(config)
	r.RandomTitleEnchantRowID = row.ID
	r.TargetKind = "equipment"
	r.TargetEquipmentType = "[title name]"
	r.AllowedEquipmentTypes = []string{"[title name]"}
	return r, nil
}
