package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfcompound "longheng.io/server/internal/modules/dnf/equipmentcompound"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentEquipmentRegenerationOpcode = uint16(dnfenum.CmdPacketRegenerationRandomOption)

// 当前原生407请求是6字节，日志134931..134954；消费者0x18844a0
// 成功时读取一个u16结果槽并立即查主背包，所以库存行必须先于成功ACK。
// 接收准入位是客户端的独立条件，本代码不修改EXE或代发其他命令的动画。
func equipmentRegenerationGameplayModule() gameplayModuleDefinition {
	op := currentEquipmentRegenerationOpcode
	return gameplayModuleDefinition{
		Name: "equipment-regeneration",
		LegacyHandlers: map[uint16]gameplayHandler{op: func(s *Service, session *gameSession, request gameplayRequest) error {
			return s.handleCurrentEquipmentRegeneration(session, request.Body)
		}},
		UpperHandlers: map[uint16]gameplayHandler{op: defaultClassGameplayHandler("装备合成分类拒绝", "407请求必须为class1", (*Service).handleCurrentEquipmentRegeneration)},
	}
}

func parseCurrentEquipmentRegenerationRequest(body []byte) (dnfcompound.Request, error) {
	if len(body) != 6 {
		return dnfcompound.Request{}, fmt.Errorf("装备合成请求长%d，预期6", len(body))
	}
	req := dnfcompound.Request{SourceSlot: int16(binary.LittleEndian.Uint16(body[0:2])), Mode: binary.LittleEndian.Uint16(body[2:4]), Part: binary.LittleEndian.Uint16(body[4:6])}
	if req.SourceSlot < 3 || req.Mode != 0 || req.Part < 1 || req.Part > 4 {
		return dnfcompound.Request{}, fmt.Errorf("装备合成来源槽、模式或90部位组无效")
	}
	return req, nil
}

// 启动加载阶段完成全目录建池；监听开放后只读取这份已发布的缓存。
func (s *Service) preloadEquipmentRegenerationCatalog(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	items, err := s.currentPVFItemCatalog()
	if err != nil || items == nil {
		return fmt.Errorf("装备合成真实物品目录不可用：%v", err)
	}
	items.mu.Lock()
	defer items.mu.Unlock()
	if items.equipmentCompoundCatalog != nil {
		return nil
	}
	catalog, err := dnfcompound.NewCatalog(items.source)
	if err != nil {
		return fmt.Errorf("启动加载装备合成规则：%w", err)
	}
	stats, err := catalog.Preload(ctx)
	if err != nil {
		return fmt.Errorf("启动加载装备合成目录：%w", err)
	}
	// 只有完整目录成功后才发布，取消或读取失败不会留下可使用的半份缓存。
	items.equipmentCompoundCatalog = catalog
	s.logPacketEvent("装备合成目录启动加载完成", "引用数", stats.References, "解析数", stats.Parsed,
		"非本玩法元数据", stats.UnsupportedMetadata, "不可读数", stats.Unreadable, "候选数", stats.Eligible, "耗时毫秒", time.Since(started).Milliseconds())
	return nil
}

func (s *Service) currentEquipmentRegenerationCatalog() (*pvfDungeonDropCatalog, *dnfcompound.Catalog, error) {
	items, loaded, err := s.currentPVFItemCatalogIfLoaded()
	if err != nil || !loaded || items == nil {
		return nil, nil, fmt.Errorf("装备合成目录未在启动阶段加载：%w", dnfcompound.ErrCandidates)
	}
	items.mu.Lock()
	catalog := items.equipmentCompoundCatalog
	items.mu.Unlock()
	if catalog == nil {
		return nil, nil, fmt.Errorf("装备合成目录未在启动阶段加载：%w", dnfcompound.ErrCandidates)
	}
	return items, catalog, nil
}

func buildCurrentEquipmentRegenerationSuccessBody(slot int16) []byte {
	body := []byte{1, 0, 0}
	binary.LittleEndian.PutUint16(body[1:3], uint16(slot))
	return body
}

func currentEquipmentRegenerationEntries(result dnfcompound.Result) []currentItemListEntry {
	entries := make([]currentItemListEntry, 0, len(result.ChangedSlots))
	for _, slot := range result.ChangedSlots {
		items := result.Inventory.Slots
		if dnfrepo.IsAccountSharedInventorySlot(0, slot) {
			items = result.AccountInventory.Slots
		}
		stack, found := items[currentDungeonPickupMainSlotKey(slot)]
		var entry currentItemListEntry
		if found && stack.Count > 0 {
			entry = currentItemListEntryFromStack(0, slot, stack)
		} else {
			entry.patchCore(slot, math.MaxUint32, 0)
		}
		entries = append(entries, entry)
	}
	return entries
}

func currentBuildEquipmentRegenerationStack(items *pvfDungeonDropCatalog, slot int16, candidate dnfcompound.Definition, source dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
	definition, err := items.ResolveItem(uint32(candidate.ItemID))
	if err != nil {
		return dnfrepo.ItemStack{}, err
	}
	policy := currentPVFItemPlacementPolicy(definition)
	if definition.Kind != dungeonDropItemEquipment || definition.PVFPath != candidate.PVFPath || policy.ListType != 0 || policy.Start != 9 || policy.End != 64 {
		return dnfrepo.ItemStack{}, dnfcompound.ErrSource
	}
	stack, err := buildCurrentDungeonPickupStack(slot, definition, 1)
	if err != nil {
		return dnfrepo.ItemStack{}, err
	}
	// 90只允许封装来源，新产物同样保持封装；旧强化/附魔等实例不继承。
	if !currentEquipmentRegenerationSourceSealed(source) {
		return dnfrepo.ItemStack{}, dnfcompound.ErrSource
	}
	stack.Bind = source.Bind
	stack.Extra["source"] = "equipment_compound"
	stack.Extra["seal_flag"] = "1"
	stack.RawEntry[0x0d] = 1
	return stack, nil
}

func currentEquipmentRegenerationSourceSealed(source dnfrepo.ItemStack) bool {
	for _, key := range []string{"seal_removed_by_first_equip", "trade_locked_by_first_equip"} {
		if value := strings.TrimSpace(source.Extra[key]); value != "" && value != "0" && !strings.EqualFold(value, "false") {
			return false
		}
	}
	for _, key := range []string{"seal_flag", "seal"} {
		if value, found := source.Extra[key]; found {
			flag, err := strconv.ParseUint(strings.TrimSpace(value), 10, 8)
			return err == nil && flag == 1
		}
	}
	// Bind字段并非封装证据；无明确标记时只认当前119字节库存行。
	return len(source.RawEntry) == currentItemListEntryWireSize && source.RawEntry[0x0d] == 1
}

func (s *Service) handleCurrentEquipmentRegeneration(session *gameSession, body []byte) error {
	op := currentEquipmentRegenerationOpcode
	reject := func(reason error) error {
		s.logGameEvent(session, "装备合成拒绝", "op", op, "原因", reason)
		return s.sendGameUpperFailure(session, op, 17)
	}
	request, err := parseCurrentEquipmentRegenerationRequest(body)
	if err != nil {
		return reject(err)
	}
	if session == nil || session.selectedCharacterID == 0 {
		return reject(dnfcompound.ErrOwner)
	}
	items, catalog, err := s.currentEquipmentRegenerationCatalog()
	if err != nil {
		return reject(err)
	}
	policies := map[int64]itemplacement.SlotPolicy{}
	for _, id := range catalog.Rules.MaterialIDs() {
		definition, err := items.ResolveItem(uint32(id))
		if err != nil || definition.Kind != dungeonDropItemStackable {
			return reject(dnfcompound.ErrRules)
		}
		policies[id] = currentPVFItemPlacementPolicy(definition)
	}
	group, available := s.repositoryGroup()
	if !available {
		return reject(dnfcompound.ErrOwner)
	}
	owner, err := dnfcompound.NewOwner(group)
	if err != nil {
		return reject(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.Compound(ctx, dnfcompound.Command{
		AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID)), Request: request, UpdatedAt: s.gameplayNow(), Catalog: catalog,
		EquipmentPolicy: itemplacement.SlotPolicy{ListType: 0, Start: 9, End: 64, AllowQuickSlots: true}, MaterialPolicies: policies,
		SourceSealed: currentEquipmentRegenerationSourceSealed,
		BuildStack: func(slot int16, definition dnfcompound.Definition, source dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			return currentBuildEquipmentRegenerationStack(items, slot, definition, source)
		},
		EncodeStack: func(slot int16, stack dnfrepo.ItemStack) dnfrepo.ItemStack {
			entry := currentItemListEntryFromStack(0, slot, stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			return stack
		},
	})
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(session, "装备合成已提交", "op", op, "角色", session.selectedCharacterID, "来源ID", result.Source.ItemID, "来源等级", result.Source.MinimumLevel, "来源格", result.SourceSlot, "产出ID", result.Output.ItemID, "产出等级", result.Output.MinimumLevel, "结果格", result.ResultSlot, "部位组", request.Part, "圣物", result.Output.Legacy(), "等级权重", result.LevelWeight, "等级总权重", result.TotalLevelWeight, "物品权重", result.SelectedWeight, "金币费用", 0)
	if result.ResultSlot == result.SourceSlot {
		var empty currentItemListEntry
		empty.patchCore(result.SourceSlot, math.MaxUint32, 0)
		if err = s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(0, []currentItemListEntry{empty}), 0); err != nil {
			return err
		}
	}
	if err = s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(0, currentEquipmentRegenerationEntries(result)), 0); err != nil {
		return err
	}
	if err = s.sendGameUpperRawClass(session, op, buildCurrentEquipmentRegenerationSuccessBody(result.ResultSlot), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	s.logGameEvent(session, "装备合成结果已发送", "op", op, "角色", session.selectedCharacterID, "结果格", result.ResultSlot)
	return nil
}
