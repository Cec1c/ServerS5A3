package dnfbridge

// op25 equipment-design-drawing owner. Wire evidence: current-client-contracts/
// resume02 and recipe-row-proof03 (v97 EXE bb42898c...531). Decode only the
// proven 7B source/u8 mode/u16 count and 9B form with tail 00 01. Item-ID
// mode is decoded but rejected: its drawing ownership/consumption is unproven.
// Slot mode consumes count drawings from that unlocked main-bag row, following
// A21 TryConsumeFromSlot(RequestedCount); PVF itself does not encode this policy.
//
// Support only explicit material-output without rates, failure/upgrade/maintain,
// expert-job, need-material, bead/category or skill semantics. Itemmaking permits
// only a strict known-category + level 1 pair: recipe-capability-proof08 proves
// the client query 0x27A38E0 has floor 1. Higher levels need an authoritative
// category experience ledger. No crafting experience or gain notice is invented.
// Floor admission never waives the drawing's ordinary minimum level. Check it
// against the character snapshot in the same transaction; dated drawings remain
// rejected until their use/expiration rule is proved (independent09 P5).
// All explicit success/creation rates, including 100, remain unproven.
// Materials/gold/output quantities and item classifications come from PVF.
// Fixed warehouse items use AccountWarehouseSlotForItem and account assets only.
// All mutations share Owner.Compound's character/account settlement transaction.
//
// Success is 01 00 00, followed by existing op13 full container refresh. The
// zero reward count parses and reaches the client's fixed cleanup; GUI behavior
// still needs native acceptance. 119B rows share the proven ctor/size/core offsets
// with op13/op14; equipment +6 is an instance value, not always a stack count.
// Failure uses sendGameUpperFailure(op25, 0); field-driven failure text/code
// semantics remain unproven. ACK follows commit; send failure never re-commits.
// Upper/class1 and native Legacy COMMAND route share this owner. ROOT captured
// QA06 packet_log.txt:1320-1321 at 13:01:48Z: cmd1/type25, body 450000000001000001
// (slot69/count1/tail0001). No normalizer or generic dispatch fallback is needed.
import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// currentEquipmentRecipeCompoundOpcode 是 op25 ENUM_CMDPACKET_COMPOUND_ITEM。
	currentEquipmentRecipeCompoundOpcode = uint16(dnfenum.CmdPacketCompoundItem)
	// currentEquipmentRecipeCompoundRequestSize 是 0x1a9b510 证明的 7 字节请求体。
	currentEquipmentRecipeCompoundRequestSize = 7
	// currentEquipmentRecipeCompoundExtendedRequestSize 是 0x33c2840 证明的 9 字节请求体。
	currentEquipmentRecipeCompoundExtendedRequestSize = 9
	// 两种已证的 source 解释方式（u8@4）。
	currentEquipmentRecipeCompoundSourceIsSlot   = 0
	currentEquipmentRecipeCompoundSourceIsItemID = 1
	// 9 字节型尾部已证的两字节。
	currentEquipmentRecipeCompoundExtendedTailLow  = 0x00
	currentEquipmentRecipeCompoundExtendedTailHigh = 0x01
	// currentEquipmentRecipeCompoundFailureCode 是失败体的通用占位码：失败文案由
	// 客户端帧字段决定，包体码值语义未闭合，因此只用 0，不冒充任何业务码。
	currentEquipmentRecipeCompoundFailureCode = 0
	// currentEquipmentRecipeCompoundMaxSlot 是槽位型 source 的 i16 上限。
	currentEquipmentRecipeCompoundMaxSlot = int64(32767)
)

var (
	errCurrentEquipmentRecipeCompoundUnavailable   = errors.New("dnf current equipment recipe compound owner is unavailable")
	errCurrentEquipmentRecipeCompoundSourceInvalid = errors.New("dnf current equipment recipe compound source item is invalid")
	errCurrentEquipmentRecipeCompoundUnsupported   = errors.New("dnf current equipment recipe compound recipe is not supported")
	errCurrentEquipmentRecipeCompoundMaterials     = errors.New("dnf current equipment recipe compound materials are insufficient")
	errCurrentEquipmentRecipeCompoundOverflow      = errors.New("dnf current equipment recipe compound amount overflows")
)

// equipmentRecipeCompoundGameplayModule owns both proved COMMAND routes for op25.
func equipmentRecipeCompoundGameplayModule() gameplayModuleDefinition {
	opcode := currentEquipmentRecipeCompoundOpcode
	return gameplayModuleDefinition{
		Name: "equipment-recipe-compound",
		LegacyHandlers: map[uint16]gameplayHandler{
			opcode: func(s *Service, session *gameSession, request gameplayRequest) error {
				return s.handleCurrentEquipmentRecipeCompound(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			opcode: defaultClassGameplayHandler(
				"game-equipment-recipe-compound-blocked",
				"current_exe_op25_command_class_mismatch",
				(*Service).handleCurrentEquipmentRecipeCompound,
			),
		},
	}
}

// currentEquipmentRecipeCompoundRequest 是已证请求字段的解码结果。SourceSlot 只在
// 按槽位时有效（按物品 id 时为 -1）。
type currentEquipmentRecipeCompoundRequest struct {
	SourceIsItemID bool
	SourceSlot     int16
	SourceItemID   int64
	Count          int64
}

func parseCurrentEquipmentRecipeCompoundRequest(body []byte) (currentEquipmentRecipeCompoundRequest, error) {
	if len(body) != currentEquipmentRecipeCompoundRequestSize &&
		len(body) != currentEquipmentRecipeCompoundExtendedRequestSize {
		return currentEquipmentRecipeCompoundRequest{}, fmt.Errorf(
			"equipment recipe compound body=%d want=%d or %d",
			len(body), currentEquipmentRecipeCompoundRequestSize, currentEquipmentRecipeCompoundExtendedRequestSize)
	}
	if len(body) == currentEquipmentRecipeCompoundExtendedRequestSize &&
		(body[currentEquipmentRecipeCompoundRequestSize] != currentEquipmentRecipeCompoundExtendedTailLow ||
			body[currentEquipmentRecipeCompoundRequestSize+1] != currentEquipmentRecipeCompoundExtendedTailHigh) {
		return currentEquipmentRecipeCompoundRequest{}, errors.New("equipment recipe compound 9-byte tail is not 00 01")
	}
	raw := binary.LittleEndian.Uint32(body[0:4])
	mode := body[4]
	count := int64(binary.LittleEndian.Uint16(body[5:7]))
	if count <= 0 {
		return currentEquipmentRecipeCompoundRequest{}, errors.New("equipment recipe compound count must be non-zero")
	}
	switch mode {
	case currentEquipmentRecipeCompoundSourceIsSlot:
		if int64(raw) <= 0 || int64(raw) > currentEquipmentRecipeCompoundMaxSlot {
			return currentEquipmentRecipeCompoundRequest{}, fmt.Errorf("equipment recipe compound slot=%d is not a main inventory slot", raw)
		}
		return currentEquipmentRecipeCompoundRequest{SourceSlot: int16(raw), SourceItemID: -1, Count: count}, nil
	case currentEquipmentRecipeCompoundSourceIsItemID:
		if raw == 0 {
			return currentEquipmentRecipeCompoundRequest{}, errors.New("equipment recipe compound item id 0 is invalid")
		}
		return currentEquipmentRecipeCompoundRequest{SourceIsItemID: true, SourceSlot: -1, SourceItemID: int64(raw), Count: count}, nil
	default:
		return currentEquipmentRecipeCompoundRequest{}, fmt.Errorf("equipment recipe compound source mode=%d is not 0 or 1", mode)
	}
}

// currentEquipmentRecipeCompoundRejectReason 把一条已解析的设计图映射成"拒绝原因"。
// 返回空串表示落在最小确定性 material-output 支持范围内。判据按"未证即拒绝"排列，
// 每个分支都能在交接说明里对应到一条未证语义。
func currentEquipmentRecipeCompoundRejectReason(entry equipmentRecipeEntry, source dnfpvf.Source) string {
	switch {
	case entry.Kind != equipmentRecipeKindMaterialOutput:
		// unsupported / int-data-encoded / in-place-upgrade 都在这里被挡住：
		// 它们要么没有已证的材料/产出模型，要么是另一种操作种类。
		return "kind_" + string(entry.Kind)
	case len(entry.Materials) == 0 || len(entry.Outputs) == 0:
		return "recipe_payload_missing"
	case len(entry.FailOutputs) > 0:
		return "fail_output_present"
	case entry.ActionType != "" || entry.CustomUpgradeType != "":
		return "upgrade_present"
	case entry.MaintainAbility != "" || entry.MaintainSocketStat:
		return "maintain_present"
	case entry.ExpertJobOnly:
		return "expert_job_only"
	case len(entry.ExtraMaterials) > 0:
		return "need_material_semantics_unproven"
	case len(entry.BeadItems) > 0:
		return "bead_item_semantics_unproven"
	case len(entry.ItemCategories) > 0:
		return "item_category_upgrade_table_unproven"
	case len(entry.NeedSkillInts) > 0:
		return "need_skill_semantics_unproven"
	}
	if reason := currentEquipmentRecipeCompoundItemmakingRejectReason(entry, source); reason != "" {
		return reason
	}
	switch {
	case entry.SuccessRateFound:
		return "success_rate_unit_unproven"
	case entry.CreationRateFound:
		return "creation_rate_unit_unproven"
	}
	return ""
}

// The catalog intentionally projects only the first text/integer of minimum.
// Re-read its resolved PVF document here, inside the settlement callback, so an
// empty, duplicate or excess-field section cannot masquerade as the proven pair.
// No minimum is still legal; a declared minimum must match the catalog fields.
func currentEquipmentRecipeCompoundItemmakingRejectReason(entry equipmentRecipeEntry, source dnfpvf.Source) string {
	declared := entry.MinimumItemmakingFound || entry.MinimumItemmakingField != "" || entry.MinimumItemmakingLevel != 0
	if source == nil {
		if declared {
			return "itemmaking_level_shape_invalid"
		}
		return ""
	}
	text, err := source.ReadText(entry.PVFPath)
	if err != nil {
		return "itemmaking_document_unavailable"
	}
	document, err := dnfpvf.Parse(entry.PVFPath, text)
	if err != nil {
		return "itemmaking_level_shape_invalid"
	}
	tokens, found, err := currentEquipmentRecipeCompoundQualificationTokens(document, "minimum itemmaking level")
	if err != nil {
		return "itemmaking_level_shape_invalid"
	}
	if !found {
		if declared {
			return "itemmaking_level_shape_invalid"
		}
		return currentEquipmentRecipeCompoundDocumentRejectReason(document)
	}
	if len(tokens) != 2 || tokens[0].Kind != dnfpvf.TokenString || tokens[1].Kind != dnfpvf.TokenInt ||
		!entry.MinimumItemmakingFound || tokens[0].Value != entry.MinimumItemmakingField || tokens[1].Int != entry.MinimumItemmakingLevel || tokens[1].Int <= 0 {
		return "itemmaking_level_shape_invalid"
	}
	// Exact enums read by 0x3B452DF into template +0x9F0; no aliases/default.
	switch tokens[0].Value {
	case "weapon", "cloth", "leather", "larmor", "harmor", "plate", "amulet", "wrist", "ring", "support", "magic stone":
	default:
		return "itemmaking_category_unproven"
	}
	if entry.MinimumItemmakingLevel != 1 {
		return "itemmaking_level_semantics_unproven"
	}
	return currentEquipmentRecipeCompoundDocumentRejectReason(document)
}

// Raw guards apply to drawings with and without a making minimum. Catalog
// first-value/empty-vector projections must not erase an explicit unknown gate.
func currentEquipmentRecipeCompoundDocumentRejectReason(document *dnfpvf.Document) string {
	// Empty rate sections are also explicit unknown semantics. Do not let
	// catalog Found=false erase them when admitting the new floor subset.
	for _, rate := range []struct{ section, reason string }{
		{"success rate", "success_rate_unit_unproven"},
		{"creation rate", "creation_rate_unit_unproven"},
	} {
		if _, present, err := equipmentRecipeSectionTokens(document, rate.section); err != nil || present {
			return rate.reason
		}
		if _, present, err := equipmentRecipeSectionTokens(document, "/"+rate.section); err != nil || present {
			return rate.reason
		}
	}
	// A genuinely empty vector has one opening and its immediate empty closing
	// section. Symbols, orphan/missing closers and duplicate vectors are unknown.
	needSkillOpen, needSkillClose := -1, -1
	for i, section := range document.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		// The catalog omits symbols and takes first scalar values. Presence of
		// these unproved operations is therefore the boundary, even if empty,
		// malformed, duplicated, or represented by an orphan closing section.
		switch strings.TrimPrefix(name, "/") {
		case "need material":
			return "need_material_semantics_unproven"
		case "bead item":
			return "bead_item_semantics_unproven"
		case "fail output":
			return "fail_output_present"
		case "item category":
			return "item_category_upgrade_table_unproven"
		case "action type", "custom upgrade type":
			return "upgrade_present"
		case "maintain ability", "maintain socket stat":
			return "maintain_present"
		case "expertjob only":
			return "expert_job_only"
		case "int data":
			return "int_data_semantics_unproven"
		}
		switch name {
		case "need skill":
			if needSkillOpen >= 0 || section.Start != section.End {
				return "need_skill_shape_invalid"
			}
			needSkillOpen = i
		case "/need skill":
			if needSkillClose >= 0 || section.Start != section.End {
				return "need_skill_shape_invalid"
			}
			needSkillClose = i
		case "expiration date", "/expiration date":
			// Presence, not a guessed comparison with today's date, is the gate.
			return "source_expiration_semantics_unproven"
		}
	}
	if (needSkillOpen >= 0 || needSkillClose >= 0) && (needSkillOpen < 0 || needSkillClose != needSkillOpen+1) {
		return "need_skill_shape_invalid"
	}
	level, present, err := currentEquipmentRecipeCompoundQualificationTokens(document, "minimum level")
	if err != nil || (present && (len(level) != 1 || level[0].Kind != dnfpvf.TokenInt || level[0].Int <= 0)) {
		return "source_minimum_level_shape_invalid"
	}
	jobs, present, err := currentEquipmentRecipeCompoundQualificationTokens(document, "usable job")
	if err != nil || (present && (len(jobs) != 1 || jobs[0].Kind != dnfpvf.TokenString || jobs[0].Value != "[all]")) {
		return "source_usable_job_semantics_unproven"
	}
	return ""
}

// Scalar qualification fields need one opening; their optional closing tag
// must be unique, empty and immediately after that opening. The generic PVF
// parser treats closing tags as sections, so their payload needs checking too.
func currentEquipmentRecipeCompoundQualificationTokens(document *dnfpvf.Document, section string) ([]dnfpvf.Token, bool, error) {
	tokens, found, err := equipmentRecipePayloadTokens(document, section)
	if err != nil {
		return nil, found, err
	}
	closed := false
	for i, candidate := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(candidate.Name), "/"+section) {
			continue
		}
		if !found || closed || candidate.Start != candidate.End || i == 0 || !strings.EqualFold(strings.TrimSpace(document.Sections[i-1].Name), section) {
			return nil, found, fmt.Errorf("qualification section=%s has invalid closing tag", section)
		}
		closed = true
	}
	return tokens, found, nil
}

// Ordinary character level is an existing authoritative source-use condition,
// distinct from the making-level floor. No premium/job or crafting bonus is
// inferred. This helper runs before any mutation, on the owner-loaded snapshot.
func currentEquipmentRecipeCompoundSourceQualification(entry equipmentRecipeEntry, source dnfpvf.Source, character *dnfrepo.CharacterRecord) error {
	if source == nil || character == nil {
		return errCurrentEquipmentRecipeCompoundUnavailable
	}
	text, err := source.ReadText(entry.PVFPath)
	if err != nil {
		return fmt.Errorf("%w: source document: %v", errCurrentEquipmentRecipeCompoundUnsupported, err)
	}
	document, err := dnfpvf.Parse(entry.PVFPath, text)
	if err != nil {
		return fmt.Errorf("%w: source document: %v", errCurrentEquipmentRecipeCompoundUnsupported, err)
	}
	if reason := currentEquipmentRecipeCompoundDocumentRejectReason(document); reason != "" {
		return fmt.Errorf("%w: reason=%s", errCurrentEquipmentRecipeCompoundUnsupported, reason)
	}
	level, present, err := currentEquipmentRecipeCompoundQualificationTokens(document, "minimum level")
	if err != nil || (present && int64(character.Level) < level[0].Int) {
		return fmt.Errorf("%w: source_minimum_level_not_met character_level=%d", errCurrentEquipmentRecipeCompoundSourceInvalid, character.Level)
	}
	return nil
}

// currentEquipmentRecipeCompoundOutputs 把 [output item] ×count 合并、解析成真实 PVF 定义。
type currentEquipmentRecipeCompoundOutput struct {
	Definition dungeonDropItemDefinition
	Count      uint32
}

func currentEquipmentRecipeCompoundMaterials(entry equipmentRecipeEntry, count int64) ([]dnfexpertjob.RecipeEntry, int64, error) {
	if count <= 0 {
		return nil, 0, errCurrentEquipmentRecipeCompoundSourceInvalid
	}
	merged := make(map[int64]int64, len(entry.Materials))
	for _, material := range entry.Materials {
		if material.ItemID <= 0 || material.Count <= 0 {
			return nil, 0, fmt.Errorf("%w: material pair=(%d,%d)", errCurrentEquipmentRecipeCompoundUnsupported, material.ItemID, material.Count)
		}
		if material.Count > math.MaxInt64/count {
			return nil, 0, fmt.Errorf("%w: material item=%d count=%d x%d", errCurrentEquipmentRecipeCompoundOverflow, material.ItemID, material.Count, count)
		}
		total := material.Count * count
		if merged[material.ItemID] > math.MaxInt64-total {
			return nil, 0, fmt.Errorf("%w: material item=%d total", errCurrentEquipmentRecipeCompoundOverflow, material.ItemID)
		}
		merged[material.ItemID] += total
	}
	itemIDs := make([]int64, 0, len(merged))
	for itemID := range merged {
		itemIDs = append(itemIDs, itemID)
	}
	sort.Slice(itemIDs, func(left, right int) bool { return itemIDs[left] < itemIDs[right] })
	materials := make([]dnfexpertjob.RecipeEntry, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		materials = append(materials, dnfexpertjob.RecipeEntry{ItemID: itemID, Count: merged[itemID]})
	}
	goldCost := int64(0)
	if entry.GoldCostFound {
		if entry.GoldCost < 0 {
			return nil, 0, fmt.Errorf("%w: gold=%d", errCurrentEquipmentRecipeCompoundUnsupported, entry.GoldCost)
		}
		if entry.GoldCost > 0 {
			if entry.GoldCost > math.MaxInt64/count {
				return nil, 0, fmt.Errorf("%w: gold=%d x%d", errCurrentEquipmentRecipeCompoundOverflow, entry.GoldCost, count)
			}
			goldCost = entry.GoldCost * count
		}
	}
	return materials, goldCost, nil
}

func currentEquipmentRecipeCompoundOutputPlan(catalog *pvfDungeonDropCatalog, entry equipmentRecipeEntry, count int64) ([]currentEquipmentRecipeCompoundOutput, error) {
	if count <= 0 {
		return nil, errCurrentEquipmentRecipeCompoundSourceInvalid
	}
	if catalog == nil {
		return nil, errCurrentEquipmentRecipeCompoundUnavailable
	}
	merged := make(map[int64]int64, len(entry.Outputs))
	for _, output := range entry.Outputs {
		if output.ItemID <= 0 || output.ItemID > math.MaxUint32 || output.Count <= 0 {
			return nil, fmt.Errorf("%w: output pair=(%d,%d)", errCurrentEquipmentRecipeCompoundUnsupported, output.ItemID, output.Count)
		}
		if output.Count > math.MaxInt64/count {
			return nil, fmt.Errorf("%w: output item=%d count=%d x%d", errCurrentEquipmentRecipeCompoundOverflow, output.ItemID, output.Count, count)
		}
		total := output.Count * count
		if merged[output.ItemID] > math.MaxInt64-total {
			return nil, fmt.Errorf("%w: output item=%d total", errCurrentEquipmentRecipeCompoundOverflow, output.ItemID)
		}
		merged[output.ItemID] += total
	}
	itemIDs := make([]int64, 0, len(merged))
	for itemID := range merged {
		itemIDs = append(itemIDs, itemID)
	}
	sort.Slice(itemIDs, func(left, right int) bool { return itemIDs[left] < itemIDs[right] })
	plan := make([]currentEquipmentRecipeCompoundOutput, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		total := merged[itemID]
		if total <= 0 || total > math.MaxUint32 {
			return nil, fmt.Errorf("%w: output item=%d total=%d", errCurrentEquipmentRecipeCompoundOverflow, itemID, total)
		}
		definition, err := catalog.ResolveItem(uint32(itemID))
		if err != nil {
			return nil, fmt.Errorf("%w: output item=%d: %v", errCurrentEquipmentRecipeCompoundUnsupported, itemID, err)
		}
		if definition.Kind != dungeonDropItemStackable && definition.Kind != dungeonDropItemEquipment {
			return nil, fmt.Errorf("%w: output item=%d kind=%s", errCurrentEquipmentRecipeCompoundUnsupported, itemID, definition.Kind)
		}
		plan = append(plan, currentEquipmentRecipeCompoundOutput{Definition: definition, Count: uint32(total)})
	}
	return plan, nil
}

// currentEquipmentRecipeCompoundSource 是一条已校验的设计图来源行。
type currentEquipmentRecipeCompoundSource struct {
	Key    string
	Slot   int16
	ItemID int64
	Count  int64
}

// Source is one unlocked, positive main-bag row, excluding account warehouse
// slots and warehouse-owned item identities. ID-source semantics are unproven.
func currentEquipmentRecipeCompoundSourceStack(inventory *dnfrepo.InventoryRecord, request currentEquipmentRecipeCompoundRequest) (currentEquipmentRecipeCompoundSource, error) {
	if request.SourceIsItemID || request.Count <= 0 || request.SourceSlot <= 0 || dnfrepo.IsAccountSharedInventorySlot(0, request.SourceSlot) {
		return currentEquipmentRecipeCompoundSource{}, errCurrentEquipmentRecipeCompoundSourceInvalid
	}
	if inventory == nil {
		return currentEquipmentRecipeCompoundSource{}, errCurrentEquipmentRecipeCompoundUnavailable
	}
	key := currentCeraShopInventorySlotKey(0, request.SourceSlot)
	stack, found := inventory.Slots[key]
	_, shared := dnfrepo.AccountWarehouseSlotForItem(stack.ItemID)
	if !found || shared || stack.ItemID <= 0 || stack.Count <= 0 || currentNPCShopItemLocked(stack) {
		return currentEquipmentRecipeCompoundSource{}, errCurrentEquipmentRecipeCompoundSourceInvalid
	}
	return currentEquipmentRecipeCompoundSource{Key: key, Slot: request.SourceSlot, ItemID: stack.ItemID, Count: stack.Count}, nil
}

// currentEquipmentRecipeCompoundConsumeSource 从已定位的那一行扣掉 count 张，并为
// 剩余行重刷当前物品行（与 consumeCurrentExpertJobRecipe 的落库口径一致）。
func currentEquipmentRecipeCompoundConsumeSource(inventory *dnfrepo.InventoryRecord, source currentEquipmentRecipeCompoundSource, count int64) error {
	if inventory == nil || source.Key == "" || count <= 0 {
		return errCurrentEquipmentRecipeCompoundSourceInvalid
	}
	stack, found := inventory.Slots[source.Key]
	if !found || stack.ItemID != source.ItemID || stack.Count < count {
		return fmt.Errorf("%w: slot=%d item=%d count=%d need=%d",
			errCurrentEquipmentRecipeCompoundSourceInvalid, source.Slot, source.ItemID, stack.Count, count)
	}
	stack.Count -= count
	if stack.Count == 0 {
		delete(inventory.Slots, source.Key)
		return nil
	}
	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, source.Slot, stack)
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
	inventory.Slots[source.Key] = stack
	return nil
}

func buildCurrentEquipmentRecipeCompoundSuccessBody() []byte {
	// 已证 op25 成功体的最小完整形状：success(1) + u8 changedCount=0 + u8 rewardCount=0。
	// 界面状态由随后的 op13 容器刷新拉回权威值（见文件头）。
	return []byte{1, 0, 0}
}

// Consume through the existing recipe projector, but expose only each item's
// durable owner. Historical character copies of warehouse items are never money.
// Copy the resulting rows back to their owners only after all balances pass.
func (s *Service) consumeCurrentRecipeMaterials(assets *dnfexpertjob.Assets, materials []dnfexpertjob.RecipeEntry, gold int64) (bool, error) {
	view := dnfrepo.CloneInventory(*assets.Inventory)
	for key, stack := range view.Slots {
		list, slot, ok := parseSceneInventorySlotKey(key)
		_, sharedItem := dnfrepo.AccountWarehouseSlotForItem(stack.ItemID)
		if sharedItem || (ok && dnfrepo.IsAccountSharedInventorySlot(list, slot)) {
			delete(view.Slots, key)
		}
	}
	accountDirty := false
	for _, material := range materials {
		if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
			key := dnfrepo.AccountSharedInventorySlotKey(fixed)
			stack, found := assets.AccountInventory.Slots[key]
			if !found || stack.ItemID != material.ItemID || stack.Count < material.Count || currentNPCShopItemLocked(stack) {
				return false, errCurrentExpertJobInsufficientMaterials
			}
			view.Slots[key] = stack
			accountDirty = true
		}
		// The reused consumer adds persisted counts in int64. Reject corrupt
		// balances before that addition can wrap, without editing its other users.
		available := int64(0)
		for key, stack := range view.Slots {
			list, slot, ok := parseSceneInventorySlotKey(key)
			if !ok || list != 0 || slot <= 0 || stack.ItemID != material.ItemID || stack.Count <= 0 || currentNPCShopItemLocked(stack) {
				continue
			}
			if available > math.MaxInt64-stack.Count {
				return false, errCurrentEquipmentRecipeCompoundOverflow
			}
			available += stack.Count
		}
	}
	if err := s.consumeCurrentExpertJobRecipe(assets.Character, &view, materials, gold); err != nil {
		return false, err
	}
	for _, material := range materials {
		if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
			key := dnfrepo.AccountSharedInventorySlotKey(fixed)
			if stack, exists := view.Slots[key]; exists {
				assets.AccountInventory.Slots[key] = stack
			} else {
				delete(assets.AccountInventory.Slots, key)
			}
		}
	}
	for key, stack := range assets.Inventory.Slots {
		list, slot, ok := parseSceneInventorySlotKey(key)
		_, shared := dnfrepo.AccountWarehouseSlotForItem(stack.ItemID)
		if shared || (ok && dnfrepo.IsAccountSharedInventorySlot(list, slot)) {
			continue
		}
		if updated, exists := view.Slots[key]; exists {
			assets.Inventory.Slots[key] = updated
		} else {
			delete(assets.Inventory.Slots, key)
		}
	}
	return accountDirty, nil
}

// currentEquipmentRecipeCompoundCatalog 只读地构造一次目录。它不做缓存：目录构造
// 只读一份 stackable.lst，单次制作请求的代价可以接受（真实档实测见交付说明），
// 且不需要给 Service 增加字段。
func (s *Service) currentEquipmentRecipeCompoundCatalog() (*pvfEquipmentRecipeCatalog, error) {
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	if items == nil || items.source == nil {
		return nil, errEquipmentRecipePVFSourceRequired
	}
	return newPVFEquipmentRecipeCatalog(items.source)
}

type currentEquipmentRecipeCompoundResult struct {
	RecipeItemID int64
	SourceSlot   int16
	Materials    int
	GoldSpent    int64
	Outputs      int
	GrantedSlots []int16
}

func (s *Service) handleCurrentEquipmentRecipeCompound(session *gameSession, body []byte) error {
	opcode := currentEquipmentRecipeCompoundOpcode
	request, err := parseCurrentEquipmentRecipeCompoundRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-equipment-recipe-compound-rejected",
			"msg_id", opcode,
			"body_len", len(body),
			"reason", err)
		return s.sendGameUpperFailure(session, opcode, currentEquipmentRecipeCompoundFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := s.commitCurrentEquipmentRecipeCompound(ctx, session, request)
	if err != nil {
		s.logGameEvent(session, "game-equipment-recipe-compound-rejected",
			"msg_id", opcode,
			"source_is_item_id", request.SourceIsItemID,
			"source_item_id", request.SourceItemID,
			"source_slot", request.SourceSlot,
			"count", request.Count,
			"error", err)
		return s.sendGameUpperFailure(session, opcode, currentEquipmentRecipeCompoundFailureCode)
	}
	if err := s.sendGameUpperRawClass(session, opcode, buildCurrentEquipmentRecipeCompoundSuccessBody(), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	if err := s.sendSelectedCurrentContainerListsRefresh(session, "equipment_recipe_compound_after_ack"); err != nil {
		return err
	}
	slots := make([]string, 0, len(result.GrantedSlots))
	for _, slot := range result.GrantedSlots {
		slots = append(slots, strconv.FormatInt(int64(slot), 10))
	}
	s.logGameEvent(session, "game-equipment-recipe-compound-committed",
		"msg_id", opcode,
		"recipe", result.RecipeItemID,
		"source_slot", result.SourceSlot,
		"count", request.Count,
		"materials", result.Materials,
		"gold", result.GoldSpent,
		"outputs", result.Outputs,
		"granted_slots", strings.Join(slots, ","))
	return nil
}

// commitCurrentEquipmentRecipeCompound 在 dnfexpertjob.Owner.Compound 的克隆事务里
// 完成"校验 → 消耗 → 发放"。任何一步报错都会让 Project 返回错误，事务不提交，
// 设计图/材料/金币/产出全部保持原状。
func (s *Service) commitCurrentEquipmentRecipeCompound(ctx context.Context, session *gameSession, request currentEquipmentRecipeCompoundRequest) (currentEquipmentRecipeCompoundResult, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return currentEquipmentRecipeCompoundResult{}, errCurrentEquipmentRecipeCompoundUnavailable
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return currentEquipmentRecipeCompoundResult{}, err
	}
	if items == nil || items.source == nil {
		return currentEquipmentRecipeCompoundResult{}, errEquipmentRecipePVFSourceRequired
	}
	recipes, err := newPVFEquipmentRecipeCatalog(items.source)
	if err != nil {
		return currentEquipmentRecipeCompoundResult{}, err
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return currentEquipmentRecipeCompoundResult{}, errCurrentEquipmentRecipeCompoundUnavailable
	}
	owner, err := dnfexpertjob.NewOwner(repositories)
	if err != nil {
		return currentEquipmentRecipeCompoundResult{}, err
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return currentEquipmentRecipeCompoundResult{}, errCurrentEquipmentRecipeCompoundUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	now := s.gameplayNow()

	var result currentEquipmentRecipeCompoundResult
	err = owner.Compound(ctx, dnfexpertjob.Command{
		AccountID:   accountID,
		CharacterID: characterID,
		UpdatedAt:   now,
		Project: func(assets *dnfexpertjob.Assets) (dnfexpertjob.Changes, error) {
			if assets == nil || assets.Character == nil || assets.Inventory == nil || assets.AccountInventory == nil {
				return dnfexpertjob.Changes{}, errCurrentEquipmentRecipeCompoundUnavailable
			}
			source, err := currentEquipmentRecipeCompoundSourceStack(assets.Inventory, request)
			if err != nil {
				return dnfexpertjob.Changes{}, err
			}
			entry, found, err := recipes.Recipe(source.ItemID)
			if err != nil {
				return dnfexpertjob.Changes{}, err
			}
			if !found {
				return dnfexpertjob.Changes{}, fmt.Errorf("%w: item=%d is not a [recipe] design drawing", errCurrentEquipmentRecipeCompoundUnsupported, source.ItemID)
			}
			if reason := currentEquipmentRecipeCompoundRejectReason(entry, recipes.source); reason != "" {
				return dnfexpertjob.Changes{}, fmt.Errorf("%w: item=%d reason=%s", errCurrentEquipmentRecipeCompoundUnsupported, source.ItemID, reason)
			}
			if err := currentEquipmentRecipeCompoundSourceQualification(entry, recipes.source, assets.Character); err != nil {
				return dnfexpertjob.Changes{}, err
			}
			if source.Count < request.Count {
				return dnfexpertjob.Changes{}, fmt.Errorf("%w: item=%d slot=%d count=%d need=%d",
					errCurrentEquipmentRecipeCompoundSourceInvalid, source.ItemID, source.Slot, source.Count, request.Count)
			}
			materials, goldCost, err := currentEquipmentRecipeCompoundMaterials(entry, request.Count)
			if err != nil {
				return dnfexpertjob.Changes{}, err
			}
			outputs, err := currentEquipmentRecipeCompoundOutputPlan(items, entry, request.Count)
			if err != nil {
				return dnfexpertjob.Changes{}, err
			}
			// 以上全部是只读判定；下面才开始写入，且都在同一个克隆事务里。
			if err := currentEquipmentRecipeCompoundConsumeSource(assets.Inventory, source, request.Count); err != nil {
				return dnfexpertjob.Changes{}, err
			}
			accountDirty, err := s.consumeCurrentRecipeMaterials(assets, materials, goldCost)
			if err != nil {
				if errors.Is(err, errCurrentExpertJobInsufficientMaterials) {
					return dnfexpertjob.Changes{}, fmt.Errorf("%w: %v", errCurrentEquipmentRecipeCompoundMaterials, err)
				}
				return dnfexpertjob.Changes{}, err
			}
			granted := make([]int16, 0, len(outputs))
			for _, output := range outputs {
				if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(int64(output.Definition.ItemID)); shared {
					// Never fall back to a character copy of an account-owned item.
					stack := assets.AccountInventory.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)]
					if output.Definition.Kind != dungeonDropItemStackable || stack.Count < 0 || stack.Count > math.MaxUint32-int64(output.Count) || currentNPCShopItemLocked(stack) {
						return dnfexpertjob.Changes{}, errCurrentEquipmentRecipeCompoundUnsupported
					}
					added, grantErr := s.mergeCurrentAccountWarehouseStack(assets.AccountInventory, output.Definition, fixed, int64(output.Count), nil)
					if grantErr != nil || added != int64(output.Count) {
						return dnfexpertjob.Changes{}, errDungeonPickupInventoryFull
					}
					accountDirty = true
					granted = append(granted, fixed)
					continue
				}
				slots, grantErr := grantCurrentCeraShopProductAt(assets.Inventory, output.Definition, output.Count, now)
				if grantErr != nil {
					return dnfexpertjob.Changes{}, grantErr
				}
				for _, slot := range slots {
					granted = append(granted, int16(slot))
				}
			}
			result = currentEquipmentRecipeCompoundResult{
				RecipeItemID: source.ItemID,
				SourceSlot:   source.Slot,
				Materials:    len(materials),
				GoldSpent:    goldCost,
				Outputs:      len(outputs),
				GrantedSlots: granted,
			}
			return dnfexpertjob.Changes{Character: goldCost > 0, Inventory: true, AccountInventory: accountDirty}, nil
		},
	})
	if err != nil {
		return currentEquipmentRecipeCompoundResult{}, err
	}
	return result, nil
}
