package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/avatarcompound"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func absoluteAvatarGameplayModule() gameplayModuleDefinition {
	h := func(s *Service, session *gameSession, r gameplayRequest) error {
		return s.handleCurrentAbsoluteAvatar(session, r.Body)
	}
	return gameplayModuleDefinition{Name: "avatar-absolute", LegacyHandlers: map[uint16]gameplayHandler{uint16(dnfenum.CmdPacketBindPlus): h}, UpperHandlers: map[uint16]gameplayHandler{uint16(dnfenum.CmdPacketBindPlus): h}}
}

func (s *Service) handleCurrentAbsoluteAvatar(session *gameSession, body []byte) error {
	if s == nil || session == nil {
		return errCurrentAvatarCompoundUnavailable
	}
	fail := func(reason string, err error) error {
		s.logGameEvent(session, "梦想装扮合成失败", "reason", reason, "error", err)
		return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketBindPlus), []byte{0, 0x16}, dnfproto.DefaultChannelClassification)
	}
	r, err := parseCurrentAbsoluteAvatarRequest(body)
	if err != nil {
		return fail("请求无效", err)
	}
	repos, ok := s.repositoryGroup()
	if !ok || session.selectedCharacterID == 0 {
		return fail("角色资产不可用", errCurrentAvatarCompoundUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	id := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	ch, found, err := repos.Character.Load(ctx, id)
	if err != nil || !found {
		return fail("角色不可用", errCurrentAvatarCompoundUnavailable)
	}
	job, ok := characterJobByte(ch)
	if !ok {
		return fail("职业无效", errCurrentAvatarCompoundUnavailable)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail("PVF不可用", err)
	}
	inv, found, err := repos.Inventory.Load(ctx, id)
	if err != nil || !found {
		return fail("库存不可用", errCurrentAvatarCompoundUnavailable)
	}
	cube, def, err := currentAvatarCompoundCube(inv, catalog, r.ConsumeSlot)
	if err != nil {
		return fail("合成器无效", err)
	}
	if currentNPCShopItemLocked(cube) || (!cube.ExpireAt.IsZero() && !cube.ExpireAt.After(time.Now())) {
		return fail("合成器锁定", errCurrentAvatarCompoundSource)
	}
	rule, err := loadCurrentAbsoluteAvatarRule(catalog.source, def, job, r.Target)
	if err != nil {
		return fail("PVF配置或产出无效", err)
	}
	if len(r.Materials) != rule.MaterialCount {
		return fail("材料数量不符", fmt.Errorf("PVF要求%d件，实际%d件", rule.MaterialCount, len(r.Materials)))
	}
	for _, m := range r.Materials {
		materialStack, material, _, err := currentAvatarCompoundMaterial(inv, catalog, m.Slot, m.ItemID)
		if err != nil {
			return fail("材料无效", err)
		}
		if !materialStack.ExpireAt.IsZero() && !materialStack.ExpireAt.After(time.Now()) {
			return fail("材料已过期", errCurrentAvatarCompoundSource)
		}
		text, readErr := catalog.source.ReadText(material.PVFPath)
		if readErr != nil {
			return fail("材料PVF不可用", readErr)
		}
		materialDoc, parseErr := dnfpvf.Parse(material.PVFPath, text)
		if parseErr != nil {
			return fail("材料PVF无效", parseErr)
		}
		if !currentAbsoluteAvatarAllowsJob(materialDoc, job) {
			return fail("材料职业不符", errCurrentAvatarCompoundSource)
		}
		if material.Grade != rule.MaterialGrade {
			return fail("材料品质不符", fmt.Errorf("物品%d品质%d，PVF要求%d", m.ItemID, material.Grade, rule.MaterialGrade))
		}
	}
	resultDef, err := catalog.ResolveItem(r.Target)
	if err != nil || resultDef.EquipmentType != currentAvatarCompoundPartEquipmentTypes[rule.Part] {
		return fail("产出部位无效", errCurrentAvatarCompoundSource)
	}
	grant, err := s.currentAvatarCompoundGrantStack(catalog, r.Target, r.Ability)
	if err != nil {
		return fail("产出配置无效", err)
	}
	slot := r.Materials[0].Slot
	entry := currentItemListEntryFromStack(currentExeInventoryAvatarListType, slot, grant)
	grant.RawEntry = append([]byte(nil), entry.data[:]...)
	owner, err := avatarcompound.NewOwner(repos)
	if err != nil {
		return fail("事务不可用", err)
	}
	err = owner.CompoundAvatars(ctx, avatarcompound.Command{AccountID: s.accountIDForSession(session), CharacterID: id, UpdatedAt: time.Now().UTC(), Project: func(a *avatarcompound.Assets) (avatarcompound.Changes, error) {
		if a.Inventory == nil {
			return avatarcompound.Changes{}, errCurrentAvatarCompoundUnavailable
		}
		slots := a.Inventory.Slots
		key := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, r.ConsumeSlot)
		current, ok := slots[key]
		if !ok || current.ItemID != cube.ItemID || current.Count < 1 || currentNPCShopItemLocked(current) {
			return avatarcompound.Changes{}, errCurrentAvatarCompoundSource
		}
		for _, m := range r.Materials {
			key := currentCeraShopInventorySlotKey(currentExeInventoryAvatarListType, m.Slot)
			v, ok := slots[key]
			if !ok || v.ItemID != int64(m.ItemID) || v.Count != 1 || currentNPCShopItemLocked(v) {
				return avatarcompound.Changes{}, errCurrentAvatarCompoundSource
			}
		}
		for _, m := range r.Materials {
			delete(slots, currentCeraShopInventorySlotKey(currentExeInventoryAvatarListType, m.Slot))
		}
		if current.Count == 1 {
			delete(slots, key)
		} else {
			current.Count--
			row := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, r.ConsumeSlot, current)
			current.RawEntry = append([]byte(nil), row.data[:]...)
			slots[key] = current
		}
		slots[currentCeraShopInventorySlotKey(currentExeInventoryAvatarListType, slot)] = grant
		return avatarcompound.Changes{Inventory: true}, nil
	}})
	if err != nil {
		return fail("事务拒绝", err)
	}
	s.logGameEvent(session, "梦想装扮合成已提交", "cube_item", def.ItemID, "consume_slot", r.ConsumeSlot, "pvf_key", rule.Key, "material_count", rule.MaterialCount, "material_grade", rule.MaterialGrade, "materials", r.Materials, "result_item", r.Target, "result_slot", slot, "ability", r.Ability)
	if err = s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketBindPlus), buildCurrentAbsoluteAvatarSuccessBody(r, slot), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	// 1119读者已经删除八条装扮并创建产出/结果弹窗；仅刷新实际合成器槽。
	// 不重建装扮页，保留原生结果对象的身份和生命周期。
	if err = s.sendSelectedIncrementalItemSlotRefreshes(session, "梦想装扮合成", []alignedcmd.ItemSlotRefresh{{ListType: dnfrepo.MainInventoryListType, SlotIndex: r.ConsumeSlot}}); err != nil {
		return err
	}
	s.logGameEvent(session, "梦想装扮合成结果已通知", "cube_item", def.ItemID, "result_item", r.Target, "result_slot", slot)
	return nil
}
