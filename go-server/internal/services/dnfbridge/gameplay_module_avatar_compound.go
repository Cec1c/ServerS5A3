package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/avatarcompound"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// avatarCompoundGameplayModule 承接当前客户端 op99「装扮合成」（时装合成）。
//
// 这个 opcode 原先只落在 avatartitle 模块的只读预检分支里，那条分支固定返回
// ResponseAllowed=false，因此客户端点了"合成"之后永远等不到任何回包，界面就停在
// 等待状态。这里把它提升为真正的 owner：任何分支（解析失败、校验失败、事务失败、
// 成功）都必须回一个 class1/op99 的包。
func avatarCompoundGameplayModule() gameplayModuleDefinition {
	opcode := uint16(dnfenum.CmdPacketCompoundAvatar)
	return gameplayModuleDefinition{
		Name: "avatar-compound",
		LegacyHandlers: map[uint16]gameplayHandler{
			opcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentAvatarCompound(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			opcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentAvatarCompound(session, request.Body)
			},
		},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{
			opcode: stripLegacyAvatarCompoundTransportTrailer,
		},
	}
}

var (
	errCurrentAvatarCompoundUnavailable = errors.New("dnf current avatar compound source is unavailable")
	errCurrentAvatarCompoundSource      = errors.New("dnf current avatar compound material is invalid")
)

// op99 的失败分支名。
//
// 客户端在 op99 上只有一个已证实的失败码（currentAvatarCompoundFailureCode=0x16，
// 同族既有实现的所有拒绝分支都回同一个 0x16，现场抓包回包体是 `00 16 00`）。
// 因此"回到客户端的文案"目前无法按分支区分；服务端能做且必须做对的是
// **分支本身**：不能让服务端配置/内部问题伪装成"合成材料不足"。
// 这些常量就是这个区分：它们进日志、进用例断言，避免再出现
// "所有失败都当成材料不足"这种排查黑洞。
const (
	currentAvatarCompoundBranchRequestInvalid        = "request_invalid"
	currentAvatarCompoundBranchUnavailable           = "server_unavailable"
	currentAvatarCompoundBranchConfigUnavailable     = "compound_config_unavailable"
	currentAvatarCompoundBranchMaterialSlotInvalid   = "material_slot_invalid"
	currentAvatarCompoundBranchMaterialItemMismatch  = "material_item_mismatch"
	currentAvatarCompoundBranchMaterialNotAvatar     = "material_not_avatar"
	currentAvatarCompoundBranchMaterialPartMismatch  = "material_part_mismatch"
	currentAvatarCompoundBranchCompounderMissing     = "compounder_slot_invalid"
	currentAvatarCompoundBranchCompounderNotUsable   = "compounder_not_a_consumable"
	currentAvatarCompoundBranchCompounderUndeclared  = "compounder_not_declared"
	currentAvatarCompoundBranchRequestedOutsidePool  = "requested_item_outside_pool"
	currentAvatarCompoundBranchRequestedPartMismatch = "requested_item_part_mismatch"
	currentAvatarCompoundBranchRequestedNotAvatar    = "requested_item_not_an_avatar"
	currentAvatarCompoundBranchBonusItemInvalid      = "bonus_item_invalid"
	currentAvatarCompoundBranchCommitRejected        = "commit_rejected"
	currentAvatarCompoundBranchActionPoolMismatch    = "compounder_action_pool_mismatch"
)

// currentAvatarCompoundRejection 把一次可预期的拒绝归到一个稳定分支名。
type currentAvatarCompoundRejection struct {
	Branch string
	Err    error
}

func (r currentAvatarCompoundRejection) Error() string {
	if r.Err == nil {
		return r.Branch
	}
	return r.Branch + ": " + r.Err.Error()
}

func (r currentAvatarCompoundRejection) Unwrap() error { return r.Err }

// rejectCurrentAvatarCompound 给一次拒绝贴上分支名。
func rejectCurrentAvatarCompound(branch string, err error) error {
	return currentAvatarCompoundRejection{Branch: branch, Err: err}
}

// currentAvatarCompoundRejectionBranch 取回分支名；没有贴分支时返回空串
// （调用方会退回它自己给出的粗粒度 reason）。
func currentAvatarCompoundRejectionBranch(err error) string {
	var rejection currentAvatarCompoundRejection
	if errors.As(err, &rejection) {
		return rejection.Branch
	}
	return ""
}

// currentAvatarCompoundRoll 是额外稀有装扮判定用的随机源（0..limit-1）。
// 测试会替换它来固定命中/不命中两条分支。
var currentAvatarCompoundRoll = func(limit int) int {
	if limit <= 0 {
		return 0
	}
	return rand.IntN(limit)
}

// currentAvatarCompoundPlan 是提交前算好的合成计划：两条材料、一个合成器、
// 结果外观与写入槽位。事务内只会复核这份计划观察到的物品，不会重新决定产出。
type currentAvatarCompoundPlan struct {
	Part              string
	VariantPath       string
	ItemID1           uint32
	ItemID2           uint32
	CubeItemID        uint32
	ResultItemIDs     []uint32
	GrantSlots        []int16
	AbilityNo         uint16
	BonusRate         int64
	BonusHits         bool
	MaterialGrades    [2]int64
	PVFActionCube     bool
	MaterialRole      string
	MaterialRateBonus int64
}

// currentAvatarCompoundCommit 是提交结果，用于拼装成功回包与刷新列表。
type currentAvatarCompoundCommit struct {
	Rows             []currentAvatarCompoundResultRow
	ChangedListTypes []byte
}

func (s *Service) handleCurrentAvatarCompound(session *gameSession, body []byte) error {
	if s == nil || session == nil {
		// 没有会话就没有可回包的对象；正常分流不会走到这里。
		return errCurrentAvatarCompoundUnavailable
	}
	request, err := parseCurrentAvatarCompoundRequest(body)
	if err != nil {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundBranchRequestInvalid, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundBranchUnavailable, errCurrentAvatarCompoundUnavailable)
	}
	if session.selectedCharacterID == 0 {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundBranchUnavailable, errCurrentAvatarCompoundUnavailable)
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	record, found, err := repos.Character.Load(ctx, characterID)
	if err != nil || !found {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundBranchUnavailable, errCurrentAvatarCompoundUnavailable)
	}
	job, ok := characterJobByte(record)
	if !ok {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundBranchUnavailable, errCurrentAvatarCompoundUnavailable)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundBranchConfigUnavailable, err)
	}
	config, err := loadCurrentAvatarCompoundConfig(catalog.source, job)
	if err != nil {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundBranchConfigUnavailable, err)
	}
	plan, err := s.prepareCurrentAvatarCompound(ctx, session, catalog, config, request)
	if err != nil {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundRejectionBranch(err), err)
	}
	result, err := s.commitCurrentAvatarCompound(ctx, session, catalog, request, plan)
	if err != nil {
		return s.sendCurrentAvatarCompoundFailure(session, currentAvatarCompoundBranchCommitRejected, err)
	}
	if err := s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketCompoundAvatar),
		buildCurrentAvatarCompoundSuccessBody(request, result.Rows),
		dnfproto.DefaultChannelClassification,
	); err != nil {
		return err
	}
	for _, listType := range result.ChangedListTypes {
		listBody, _, _, listOK := s.buildCurrentItemListBodyForSession(ctx, session, listType)
		if !listOK {
			s.logGameEvent(session, "game-avatar-compound-refresh-skipped",
				"list_type", listType, "reason", "item_list_unavailable")
			continue
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), listBody); err != nil {
			return err
		}
	}
	s.logGameEvent(session, "game-avatar-compound-committed",
		"consume_slot", request.ConsumeSlot,
		"slot1", request.Slot1,
		"slot2", request.Slot2,
		"requested_item", request.RequestedItemID,
		"ability_no", request.AbilityNo,
		"part", plan.Part,
		"variant", plan.VariantPath,
		"cube_item", plan.CubeItemID,
		"pvf_action_cube", plan.PVFActionCube,
		"material_role", plan.MaterialRole,
		"material_rate_bonus", plan.MaterialRateBonus,
		"material_grades", fmt.Sprintf("%v", plan.MaterialGrades),
		"bonus_rate", plan.BonusRate,
		"bonus_hit", plan.BonusHits,
		"result_items", fmt.Sprintf("%v", plan.ResultItemIDs),
		"result_slots", fmt.Sprintf("%v", plan.GrantSlots),
		"ack_body", "sibling_op99_success_shape_consumed_rows_plus_two_result_rows",
		"inventory_refresh", "current_exe_op13_avatar_and_main_lists")
	return nil
}

// sendCurrentAvatarCompoundFailure 交付显式失败回包，保证客户端一定会离开等待状态。
//
// 回包体固定为 `00 16 00`：op99 只有这一个已证实的失败码（见分支常量注释），
// 所以真正要保证的是 reason/branch 记对——排查时必须能从日志看出
// 是材料问题、合成器问题，还是服务端配置问题。
func (s *Service) sendCurrentAvatarCompoundFailure(session *gameSession, reason string, err error) error {
	if reason == "" {
		reason = "unclassified"
	}
	s.logGameEvent(session, "game-avatar-compound-failed",
		"reason", reason,
		"error", err,
		"ack_body", "sibling_op99_failure_shape_00_16_00")
	return s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketCompoundAvatar),
		buildCurrentAvatarCompoundFailureBody(),
		dnfproto.DefaultChannelClassification,
	)
}

// prepareCurrentAvatarCompound 只读取角色背包与运行时 PVF，校验两条材料、
// 合成器与玩家选定外观，并在提交前定下产出与写入槽位。
func (s *Service) prepareCurrentAvatarCompound(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	config currentAvatarCompoundConfig,
	request currentAvatarCompoundRequest,
) (currentAvatarCompoundPlan, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || catalog == nil || catalog.source == nil {
		return currentAvatarCompoundPlan{}, errCurrentAvatarCompoundUnavailable
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return currentAvatarCompoundPlan{}, errCurrentAvatarCompoundUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	inventory, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		return currentAvatarCompoundPlan{}, errCurrentAvatarCompoundUnavailable
	}

	material1, definition1, rule1, err := currentAvatarCompoundMaterial(inventory, catalog, request.Slot1, request.ItemID1)
	if err != nil {
		return currentAvatarCompoundPlan{}, err
	}
	material2, definition2, rule2, err := currentAvatarCompoundMaterial(inventory, catalog, request.Slot2, request.ItemID2)
	if err != nil {
		return currentAvatarCompoundPlan{}, err
	}
	part1, ok := currentAvatarCompoundPartForEquipmentType(rule1.pvfType)
	if !ok {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchMaterialNotAvatar,
			fmt.Errorf("%w: slot=%d type=%q", errCurrentAvatarCompoundSource, request.Slot1, rule1.pvfType))
	}
	part2, ok := currentAvatarCompoundPartForEquipmentType(rule2.pvfType)
	if !ok || part1 != part2 {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchMaterialPartMismatch,
			fmt.Errorf("%w: slot1=%d type=%q slot2=%d type=%q", errCurrentAvatarCompoundSource,
				request.Slot1, rule1.pvfType, request.Slot2, rule2.pvfType))
	}

	_, cubeDefinition, err := currentAvatarCompoundCube(inventory, catalog, request.ConsumeSlot)
	if err != nil {
		return currentAvatarCompoundPlan{}, err
	}
	declaredCube := config.acceptsMaterial(cubeDefinition.ItemID)
	pvfActionCube := false
	if !declaredCube {
		pvfActionCube, err = currentAvatarCompoundHasMasterAction(catalog.source, cubeDefinition)
		if err != nil {
			return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(currentAvatarCompoundBranchConfigUnavailable, err)
		}
	}
	if !declaredCube && !pvfActionCube {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchCompounderUndeclared,
			fmt.Errorf("%w: cube item=%d is not a declared compounder (declared=%v)",
				errCurrentAvatarCompoundSource, cubeDefinition.ItemID, config.declaredMaterialIDs()))
	}

	variant, part, ok := config.variantForItem(request.RequestedItemID)
	if !ok {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchRequestedOutsidePool,
			fmt.Errorf("%w: requested item=%d is not in any compound pool (%v)",
				errCurrentAvatarCompoundSource, request.RequestedItemID, config.usablePaths()))
	}
	if part != part1 {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchRequestedPartMismatch,
			fmt.Errorf("%w: requested item part=%s materials part=%s", errCurrentAvatarCompoundSource, part, part1))
	}
	if pvfActionCube && !variant.masterActionAllowsTarget(part, request.RequestedItemID) {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(currentAvatarCompoundBranchActionPoolMismatch,
			fmt.Errorf("%w: 动作类钻石%d的目标%d不在支持master的经典高级池%s", errCurrentAvatarCompoundSource, cubeDefinition.ItemID, request.RequestedItemID, variant.Path))
	}
	resultDefinition, err := catalog.ResolveItem(request.RequestedItemID)
	if err != nil || resultDefinition.Kind != dungeonDropItemEquipment {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchRequestedNotAvatar,
			fmt.Errorf("%w: requested item=%d", errCurrentAvatarCompoundSource, request.RequestedItemID))
	}
	resultRule, ok := currentEquipmentPlacementRuleForPVFType(resultDefinition.EquipmentType)
	if !ok || resultRule.class != currentEquipmentPlacementClassAvatar {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchRequestedNotAvatar,
			fmt.Errorf("%w: requested item=%d type=%q", errCurrentAvatarCompoundSource, request.RequestedItemID, resultDefinition.EquipmentType))
	}

	// 动作类活动钻石已通过自身PVF及所选经典池校验，其概率角色取该池的master。
	// 扣料仍保留实际活动道具编号，不替换成配置表中的常规钻石编号。
	rateCubeID := cubeDefinition.ItemID
	if pvfActionCube {
		rateCubeID = variant.MasterMaterialID
	}
	materialBonus, materialRole, err := config.materialRateBonus(catalog.source, variant, rateCubeID)
	if err != nil {
		return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(currentAvatarCompoundBranchConfigUnavailable, err)
	}
	plan := currentAvatarCompoundPlan{
		Part:              part,
		VariantPath:       variant.Path,
		ItemID1:           uint32(material1.ItemID),
		ItemID2:           uint32(material2.ItemID),
		CubeItemID:        cubeDefinition.ItemID,
		ResultItemIDs:     []uint32{request.RequestedItemID},
		GrantSlots:        []int16{request.Slot1},
		AbilityNo:         request.AbilityNo,
		MaterialGrades:    [2]int64{definition1.Grade, definition2.Grade},
		PVFActionCube:     pvfActionCube,
		MaterialRole:      materialRole,
		MaterialRateBonus: materialBonus,
	}
	plan.BonusRate = variant.bonusRateWithMaterial(part, definition1.Grade, definition2.Grade, materialBonus)
	if currentAvatarCompoundBonusHits(plan.BonusRate) {
		bonus, ok := currentRollCurrentAvatarCompoundBonus(variant.Parts[part].Bonus, currentAvatarCompoundRoll)
		if ok {
			bonusDefinition, err := catalog.ResolveItem(bonus.ItemID)
			if err != nil || bonusDefinition.Kind != dungeonDropItemEquipment {
				return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
					currentAvatarCompoundBranchBonusItemInvalid,
					fmt.Errorf("%w: bonus item=%d", errCurrentAvatarCompoundSource, bonus.ItemID))
			}
			if bonusRule, ok := currentEquipmentPlacementRuleForPVFType(bonusDefinition.EquipmentType); !ok || bonusRule.class != currentEquipmentPlacementClassAvatar {
				return currentAvatarCompoundPlan{}, rejectCurrentAvatarCompound(
					currentAvatarCompoundBranchBonusItemInvalid,
					fmt.Errorf("%w: bonus item=%d type=%q", errCurrentAvatarCompoundSource, bonus.ItemID, bonusDefinition.EquipmentType))
			}
			plan.ResultItemIDs = append(plan.ResultItemIDs, bonus.ItemID)
			plan.GrantSlots = append(plan.GrantSlots, request.Slot2)
			plan.BonusHits = true
		}
	}
	return plan, nil
}

func currentAvatarCompoundBonusHits(rate int64) bool {
	if rate <= 0 {
		return false
	}
	roll := currentAvatarCompoundRoll(currentAvatarCompoundRateScale)
	return roll >= 0 && int64(roll) < rate
}

// commitCurrentAvatarCompound 在单个账号-角色资产事务里扣除两条材料与一个合成器，
// 写入结果装扮。事务内复核的是计划观察到的物品，任何不一致都整体回滚（失败不消耗）。
func (s *Service) commitCurrentAvatarCompound(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	request currentAvatarCompoundRequest,
	plan currentAvatarCompoundPlan,
) (currentAvatarCompoundCommit, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || catalog == nil || catalog.source == nil {
		return currentAvatarCompoundCommit{}, errCurrentAvatarCompoundUnavailable
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return currentAvatarCompoundCommit{}, errCurrentAvatarCompoundUnavailable
	}
	owner, err := avatarcompound.NewOwner(repos)
	if err != nil {
		return currentAvatarCompoundCommit{}, err
	}
	accountID := s.accountIDForSession(session)
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)

	var commit currentAvatarCompoundCommit
	err = owner.CompoundAvatars(ctx, avatarcompound.Command{
		AccountID:   accountID,
		CharacterID: characterID,
		UpdatedAt:   time.Now().UTC(),
		Project: func(assets *avatarcompound.Assets) (avatarcompound.Changes, error) {
			inventory := assets.Inventory
			if inventory == nil || inventory.Slots == nil {
				return avatarcompound.Changes{}, errCurrentAvatarCompoundUnavailable
			}
			key1 := currentCeraShopInventorySlotKey(currentExeInventoryAvatarListType, request.Slot1)
			key2 := currentCeraShopInventorySlotKey(currentExeInventoryAvatarListType, request.Slot2)
			stack1, ok := inventory.Slots[key1]
			if !ok || stack1.ItemID != int64(plan.ItemID1) || stack1.Count != 1 || currentNPCShopItemLocked(stack1) {
				return avatarcompound.Changes{}, fmt.Errorf(
					"%w: slot1=%d item=%d", errCurrentAvatarCompoundSource, request.Slot1, plan.ItemID1)
			}
			stack2, ok := inventory.Slots[key2]
			if !ok || stack2.ItemID != int64(plan.ItemID2) || stack2.Count != 1 || currentNPCShopItemLocked(stack2) {
				return avatarcompound.Changes{}, fmt.Errorf(
					"%w: slot2=%d item=%d", errCurrentAvatarCompoundSource, request.Slot2, plan.ItemID2)
			}
			cubeKey := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, request.ConsumeSlot)
			cube, ok := inventory.Slots[cubeKey]
			if !ok || cube.ItemID != int64(plan.CubeItemID) || cube.Count < 1 {
				return avatarcompound.Changes{}, fmt.Errorf(
					"%w: cube slot=%d item=%d", errCurrentAvatarCompoundSource, request.ConsumeSlot, plan.CubeItemID)
			}

			delete(inventory.Slots, key1)
			delete(inventory.Slots, key2)
			if cube.Count == 1 {
				delete(inventory.Slots, cubeKey)
			} else {
				cube.Count--
				entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, request.ConsumeSlot, cube)
				cube.RawEntry = append([]byte(nil), entry.data[:]...)
				inventory.Slots[cubeKey] = cube
			}

			rows := make([]currentAvatarCompoundResultRow, 0, len(plan.ResultItemIDs))
			for index, itemID := range plan.ResultItemIDs {
				if index >= len(plan.GrantSlots) {
					return avatarcompound.Changes{}, fmt.Errorf(
						"%w: result index=%d has no slot", errCurrentAvatarCompoundSource, index)
				}
				slot := plan.GrantSlots[index]
				stack, err := s.currentAvatarCompoundGrantStack(catalog, itemID, plan.AbilityNo)
				if err != nil {
					return avatarcompound.Changes{}, err
				}
				socket := currentAvatarSocketData(stack.Extra)
				entry := currentItemListEntryFromStack(currentExeInventoryAvatarListType, slot, stack)
				stack.RawEntry = append([]byte(nil), entry.data[:]...)
				inventory.Slots[currentCeraShopInventorySlotKey(currentExeInventoryAvatarListType, slot)] = stack
				rows = append(rows, currentAvatarCompoundResultRow{
					Slot:      slot,
					ItemID:    itemID,
					AbilityNo: plan.AbilityNo,
					Socket:    socket[:],
				})
			}
			commit.Rows = rows
			commit.ChangedListTypes = []byte{currentExeInventoryAvatarListType, dnfrepo.MainInventoryListType}
			return avatarcompound.Changes{Inventory: true}, nil
		},
	})
	if err != nil {
		return currentAvatarCompoundCommit{}, err
	}
	if len(commit.Rows) == 0 {
		return currentAvatarCompoundCommit{}, errCurrentAvatarCompoundUnavailable
	}
	return commit, nil
}

// currentAvatarCompoundGrantStack 构造一件合成结果装扮的持久化形态。
// 字段与商店购买装扮的授予路径（grantCurrentCeraShopAvatar）保持一致：
// ext_data0 承载玩家选定的装扮属性序号，Count=1，永久装扮没有倒计时。
func (s *Service) currentAvatarCompoundGrantStack(
	catalog *pvfDungeonDropCatalog,
	itemID uint32,
	abilityNo uint16,
) (dnfrepo.ItemStack, error) {
	if catalog == nil || catalog.source == nil {
		return dnfrepo.ItemStack{}, errCurrentAvatarCompoundUnavailable
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil || definition.Kind != dungeonDropItemEquipment {
		return dnfrepo.ItemStack{}, fmt.Errorf("%w: result item=%d", errCurrentAvatarCompoundSource, itemID)
	}
	rule, ok := currentEquipmentPlacementRuleForPVFType(definition.EquipmentType)
	if !ok || rule.class != currentEquipmentPlacementClassAvatar {
		return dnfrepo.ItemStack{}, fmt.Errorf(
			"%w: result item=%d type=%q", errCurrentAvatarCompoundSource, itemID, definition.EquipmentType)
	}
	stack := dnfrepo.ItemStack{
		ItemID: int64(definition.ItemID),
		Count:  1,
		Extra: map[string]string{
			"source":          "avatar_compound",
			"item_kind":       "avatar",
			"pvf_path":        definition.PVFPath,
			"equipment_type":  rule.pvfType,
			"ext_data0":       strconv.FormatUint(uint64(abilityNo), 10),
			"amount_or_count": "0",
		},
	}
	if !definition.ExpirationDate.IsZero() {
		stack, _ = applyCurrentPVFItemExpirationAt(stack, definition, time.Now().UTC())
	}
	return stack, nil
}

func currentAvatarCompoundMaterial(
	inventory dnfrepo.InventoryRecord,
	catalog *pvfDungeonDropCatalog,
	slot int16,
	expectedItemID uint32,
) (dnfrepo.ItemStack, dungeonDropItemDefinition, currentEquipmentPlacementRule, error) {
	stack, ok := inventory.Slots[currentCeraShopInventorySlotKey(currentExeInventoryAvatarListType, slot)]
	if !ok || stack.ItemID <= 0 || stack.Count != 1 {
		return dnfrepo.ItemStack{}, dungeonDropItemDefinition{}, currentEquipmentPlacementRule{},
			rejectCurrentAvatarCompound(currentAvatarCompoundBranchMaterialSlotInvalid,
				fmt.Errorf("%w: slot=%d stack=%+v", errCurrentAvatarCompoundSource, slot, stack))
	}
	if expectedItemID != 0 && stack.ItemID != int64(expectedItemID) {
		return dnfrepo.ItemStack{}, dungeonDropItemDefinition{}, currentEquipmentPlacementRule{},
			rejectCurrentAvatarCompound(currentAvatarCompoundBranchMaterialItemMismatch,
				fmt.Errorf("%w: slot=%d want=%d got=%d", errCurrentAvatarCompoundSource, slot, expectedItemID, stack.ItemID))
	}
	definition, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil || definition.Kind != dungeonDropItemEquipment {
		return dnfrepo.ItemStack{}, dungeonDropItemDefinition{}, currentEquipmentPlacementRule{},
			rejectCurrentAvatarCompound(currentAvatarCompoundBranchMaterialNotAvatar,
				fmt.Errorf("%w: slot=%d item=%d is not an equipment item", errCurrentAvatarCompoundSource, slot, stack.ItemID))
	}
	rule, ok := currentEquipmentPlacementRuleForPVFType(definition.EquipmentType)
	if !ok || rule.class != currentEquipmentPlacementClassAvatar {
		return dnfrepo.ItemStack{}, dungeonDropItemDefinition{}, currentEquipmentPlacementRule{},
			rejectCurrentAvatarCompound(currentAvatarCompoundBranchMaterialNotAvatar,
				fmt.Errorf("%w: slot=%d item=%d type=%q is not an avatar", errCurrentAvatarCompoundSource, slot, stack.ItemID, definition.EquipmentType))
	}
	return stack, definition, rule, nil
}

func currentAvatarCompoundCube(
	inventory dnfrepo.InventoryRecord,
	catalog *pvfDungeonDropCatalog,
	slot int16,
) (dnfrepo.ItemStack, dungeonDropItemDefinition, error) {
	stack, ok := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)]
	if !ok || stack.ItemID <= 0 || stack.Count < 1 {
		return dnfrepo.ItemStack{}, dungeonDropItemDefinition{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchCompounderMissing,
			fmt.Errorf("%w: cube slot=%d stack=%+v", errCurrentAvatarCompoundSource, slot, stack))
	}
	definition, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil || definition.Kind != dungeonDropItemStackable {
		return dnfrepo.ItemStack{}, dungeonDropItemDefinition{}, rejectCurrentAvatarCompound(
			currentAvatarCompoundBranchCompounderNotUsable,
			fmt.Errorf("%w: cube slot=%d item=%d is not a stackable consumable", errCurrentAvatarCompoundSource, slot, stack.ItemID))
	}
	return stack, definition, nil
}

// currentAvatarCompoundPartForEquipmentType 把 PVF `[equipment type]` 令牌反查回
// 合成表的部位名（[breast avatar] → neck，[waist avatar] → belt）。
func currentAvatarCompoundPartForEquipmentType(pvfType string) (string, bool) {
	normalized := normalizeEquipmentPlacementPVFType(pvfType)
	for _, part := range currentAvatarCompoundPartOrder {
		if currentAvatarCompoundPartEquipmentTypes[part] == normalized {
			return part, true
		}
	}
	return "", false
}
