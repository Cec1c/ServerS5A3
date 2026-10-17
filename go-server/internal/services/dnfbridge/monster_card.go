package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentMonsterCardUpgradeOpcode uint16 = 603
const currentMonsterCardCompoundOpcode uint16 = 414

// 当前EXE窗口登记的1/op603消费者0x1244DA0：成功标志外再读bool、ID、级数。
func buildCurrentMonsterCardUpgradeResult(result dnfinventory.MonsterCardResult) []byte {
	body := make([]byte, 6)
	if result.Upgraded {
		body[0] = 1
	}
	binary.LittleEndian.PutUint32(body[1:5], uint32(result.ItemID))
	body[5] = result.Level
	return body
}

// 当前EXE1/op414消费者0x12406B0按计数循环读来源槽，不能套用旧两张卡固定包。
func buildCurrentMonsterCardCompoundResult(cmd dnfinventory.MonsterCardCommand, result dnfinventory.MonsterCardResult) []byte {
	body := make([]byte, 15+len(cmd.InputSlots)*2)
	binary.LittleEndian.PutUint16(body[0:2], uint16(cmd.BinderSlot))
	body[2] = byte(len(cmd.InputSlots))
	offset := 3
	for _, slot := range cmd.InputSlots {
		binary.LittleEndian.PutUint16(body[offset:offset+2], uint16(slot))
		offset += 2
	}
	body[offset] = 1
	offset++
	binary.LittleEndian.PutUint16(body[offset:offset+2], uint16(result.ResultSlot))
	offset += 2
	binary.LittleEndian.PutUint32(body[offset:offset+4], uint32(result.ItemID))
	offset += 4
	binary.LittleEndian.PutUint32(body[offset:offset+4], 1)
	offset += 4
	if result.Upgraded {
		body[offset] = 1
	}
	return body
}

func currentMonsterCardChangedEntries(result dnfinventory.MonsterCardResult) []currentItemListEntry {
	entries := make([]currentItemListEntry, 0, len(result.ChangedSlots))
	for _, slot := range result.ChangedSlots {
		stack, exists := result.Inventory.Slots[fmt.Sprintf("0:%d", slot)]
		var entry currentItemListEntry
		if exists && stack.Count > 0 {
			entry = currentItemListEntryFromStack(0, slot, stack)
		} else {
			entry.patchCore(slot, math.MaxUint32, 0)
		}
		entries = append(entries, entry)
	}
	return entries
}

func (s *Service) handleCurrentMonsterCardCommand(session *gameSession, typ uint16, body []byte) (bool, error) {
	if typ != currentMonsterCardUpgradeOpcode && typ != currentMonsterCardCompoundOpcode {
		return false, nil
	}
	if session == nil {
		return true, fmt.Errorf("卡片请求缺少会话")
	}
	reject := func(reason string, code byte) (bool, error) {
		s.logGameEvent(session, "卡片操作拒绝", "op", typ, "原因", reason)
		return true, s.sendGameUpperFailure(session, typ, code)
	}
	if session.selectedCharacterID == 0 {
		return reject("尚未选择角色", 0x13)
	}
	var cmd dnfinventory.MonsterCardCommand
	var err error
	if typ == currentMonsterCardUpgradeOpcode {
		cmd, err = dnfinventory.DecodeMonsterCardUpgrade(body)
	} else {
		cmd, err = dnfinventory.DecodeMonsterCardCompound(body)
	}
	if err != nil {
		return reject(err.Error(), 0x13)
	}
	cmd.CharacterID = session.selectedCharacterID
	repos, ok := s.repositoryGroup()
	if !ok {
		return reject("角色仓储不可用", 0x13)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	// 采用服务端选角身份，且核对账号归属，客户端不能指定其他角色。
	if repos.Character == nil {
		return reject("角色仓储缺失", 0x13)
	}
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(cmd.CharacterID)))
	if err != nil {
		return reject(err.Error(), 0x13)
	}
	if !found || character.AccountID != s.accountIDForSession(session) {
		return reject("角色不属于当前账号", 0x13)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject(err.Error(), 0x13)
	}
	if catalog == nil {
		return reject("真实PVF目录缺失", 0x13)
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, "etc/monstercardcontents.etc")
	if err != nil {
		return reject(err.Error(), 0x13)
	}
	rules, err := parseCurrentMonsterCardRules(doc)
	if err != nil {
		return reject(err.Error(), 0x13)
	}
	owner, err := dnfinventory.NewOwner(repos)
	if err != nil {
		return reject(err.Error(), 0x13)
	}
	resolve := func(id int64) (dnfinventory.MonsterCardDefinition, error) {
		return resolveCurrentMonsterCard(catalog, id)
	}
	var result dnfinventory.MonsterCardResult
	if typ == currentMonsterCardUpgradeOpcode {
		result, err = owner.UpgradeMonsterCard(ctx, cmd, resolve, rules)
	} else {
		result, err = owner.CompoundMonsterCards(ctx, cmd, resolve, rules)
	}
	if err != nil {
		return reject(err.Error(), 0x13)
	}
	if !result.Applied {
		return reject(result.Reason, result.ErrorCode)
	}
	// 合成消费者还需克隆原来源卡，成功ACK必须先于权威库存行。
	ack := buildCurrentMonsterCardUpgradeResult(result)
	if typ == currentMonsterCardCompoundOpcode {
		ack = buildCurrentMonsterCardCompoundResult(cmd, result)
	}
	if err = s.sendGameUpperSuccess(session, typ, ack); err != nil {
		return true, err
	}
	if err = s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, currentMonsterCardChangedEntries(result)), 0); err != nil {
		return true, err
	}
	if typ == currentMonsterCardUpgradeOpcode && result.Upgraded {
		// 同ID级数变化后仅重建主背包，恢复新卡对象的属性和再次升级上限。
		if err = s.sendCurrentContainerListTypesWithRefresh(session, "卡片升级后主背包刷新", true, []byte{0}); err != nil {
			return true, err
		}
	}
	s.logGameEvent(session, "卡片操作已提交", "op", typ, "角色", cmd.CharacterID, "金币费用", 0, "升级成功", result.Upgraded, "概率百分比", result.ChancePercent, "卡片ID", result.ItemID, "升级次数", result.Level, "结果格", result.ResultSlot, "材料卡槽", cmd.InputSlots, "合成器槽", cmd.BinderSlot, "目标卡槽", cmd.TargetSlot, "升级材料槽", cmd.MaterialSlot)
	return true, nil
}
