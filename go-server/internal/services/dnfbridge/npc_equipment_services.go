package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfsvc "longheng.io/server/internal/modules/dnf/equipmentservice"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"sort"
	"strconv"
	"strings"
)

const currentEquipmentTransferOpcode = uint16(dnfenum.CmdPacketCargoTransportItem)

type currentEquipmentTransferRequest struct {
	StoneSlot, TargetSlot int16
	Mode                  byte
	Recipient             uint32
}

func decodeCurrentEquipmentTransferRequest(body []byte) (currentEquipmentTransferRequest, error) {
	if len(body) != 9 {
		return currentEquipmentTransferRequest{}, fmt.Errorf("装备跨界正文长%d，预期9", len(body))
	}
	r := currentEquipmentTransferRequest{int16(binary.LittleEndian.Uint16(body)), int16(binary.LittleEndian.Uint16(body[2:])), body[4], binary.LittleEndian.Uint32(body[5:])}
	if r.Mode != 0 && r.Mode != 2 {
		return r, fmt.Errorf("装备跨界模式%d不支持", r.Mode)
	}
	return r, nil
}
func equipmentTransferGameplayModule() gameplayModuleDefinition {
	op := currentEquipmentTransferOpcode
	return gameplayModuleDefinition{Name: "equipment-transfer", LegacyHandlers: map[uint16]gameplayHandler{op: func(s *Service, g *gameSession, r gameplayRequest) error {
		return s.handleCurrentEquipmentTransfer(g, r.Body)
	}}, UpperHandlers: map[uint16]gameplayHandler{op: defaultClassGameplayHandler("装备跨界分类拒绝", "557请求必须为class1", (*Service).handleCurrentEquipmentTransfer)}}
}

// 此适配器只补本业务所需的PVF视图，不扩大共享掉落目录的解析和缓存。
type currentNPCEquipmentCatalog struct{ items *pvfDungeonDropCatalog }

func (c currentNPCEquipmentCatalog) Resolve(id uint32) (dnfsvc.Definition, error) {
	if c.items == nil || c.items.source == nil {
		return dnfsvc.Definition{}, dnfsvc.ErrRules
	}
	item, e := c.items.ResolveItem(id)
	if e != nil {
		return dnfsvc.Definition{}, e
	}
	text, e := c.items.source.ReadText(item.PVFPath)
	if e != nil {
		return dnfsvc.Definition{}, e
	}
	doc, e := dnfpvf.Parse(item.PVFPath, text)
	if e != nil {
		return dnfsvc.Definition{}, e
	}
	d := dnfsvc.Definition{ItemID: id, PVFPath: item.PVFPath, AttachType: item.AttachType, Equipment: item.Kind == dungeonDropItemEquipment, EquipmentType: item.EquipmentType, Policy: currentPVFItemPlacementPolicy(item)}
	d.MinimumLevel, _ = doc.Int("minimum level")
	d.Rarity, _ = doc.Int("rarity")
	d.RandomOption, _ = doc.Int("random option")
	_, d.HasGrowthType = doc.Section("growth type")
	_, d.PeriodEquipment = doc.Section("period equipment")
	d.PeriodEquipment = d.PeriodEquipment || item.UsablePeriodDays > 0 || !item.ExpirationDate.IsZero()
	_, d.ResealOneShot = doc.Section("reseal oneshot")
	if v := doc.Ints("reseal lv"); len(v) >= 2 {
		d.ResealLevelMin, d.ResealLevelMax = v[0], v[1]
	}
	d.CargoTransportStoneType, d.CargoTransportStone = doc.Int("cargo transport stone")
	if kind, found := doc.Text("action type"); found && normalizeDungeonDropStackableType(kind) == "[cargo transport stone]" {
		if values := doc.Ints("action type"); len(values) > 0 {
			d.CargoTransportStone = true
			d.CargoTransportStoneType = values[0]
		}
	}
	if d.Equipment && d.Rarity == 4 {
		book, err := loadCurrentEpicPieceCatalog(c.items.source)
		if err != nil {
			return d, err
		}
		d.EpicPieceID = book.pieceByOutput[id]
	}
	return d, nil
}

func currentNPCEquipmentEncoder(items *pvfDungeonDropCatalog) func(byte, int16, dnfrepo.ItemStack) dnfrepo.ItemStack {
	return func(list byte, slot int16, stack dnfrepo.ItemStack) dnfrepo.ItemStack {
		if len(stack.RawEntry) == currentItemListEntryWireSize {
			stack.RawEntry = append([]byte(nil), stack.RawEntry...)
			binary.LittleEndian.PutUint16(stack.RawEntry, uint16(slot))
			if d, e := items.ResolveItem(uint32(stack.ItemID)); e == nil && d.Kind == dungeonDropItemStackable {
				binary.LittleEndian.PutUint32(stack.RawEntry[6:10], uint32(stack.Count))
			}
			return stack
		}
		entry := currentItemListEntryFromStack(list, slot, stack)
		stack.RawEntry = append([]byte(nil), entry.data[:]...)
		return stack
	}
}
func currentNPCEquipmentEntry(list byte, slot int16, s dnfrepo.ItemStack) currentItemListEntry {
	if len(s.RawEntry) == currentItemListEntryWireSize {
		var e currentItemListEntry
		copy(e.data[:], s.RawEntry)
		binary.LittleEndian.PutUint16(e.data[:2], uint16(slot))
		return e
	}
	return currentItemListEntryFromStack(list, slot, s)
}
func (s *Service) currentNPCEquipmentRecipient(ctx context.Context, repos dnfrepo.Group, accountID, sourceID string, index uint32) (string, error) {
	if repos.Character == nil {
		return "", dnfsvc.ErrOwner
	}
	rows, e := repos.Character.ListByAccount(ctx, accountID, defaultCharacterSlots)
	if e != nil {
		return "", e
	}
	roster := make([]dnfrepo.CharacterRecord, 0, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r.AccountID) == accountID && r.Slot >= 0 && r.Slot < defaultCharacterSlots && r.Stats["delete_flag"] == 0 {
			roster = append(roster, r)
		}
	}
	sort.SliceStable(roster, func(i, j int) bool {
		if roster[i].Slot == roster[j].Slot {
			return roster[i].CharacterID < roster[j].CharacterID
		}
		return roster[i].Slot < roster[j].Slot
	})
	if uint64(index) >= uint64(len(roster)) || roster[index].CharacterID == sourceID {
		return "", fmt.Errorf("同账号跨界接收选择无效：%d", index)
	}
	return roster[index].CharacterID, nil
}
func currentEquipmentTransferSuccessBody(r dnfsvc.Result) []byte {
	b := make([]byte, 9)
	binary.LittleEndian.PutUint16(b, uint16(r.MaterialSlot))
	binary.LittleEndian.PutUint16(b[2:], uint16(r.TargetSlot))
	if r.Mode == 0 {
		binary.LittleEndian.PutUint16(b[4:], uint16(r.MaterialRemaining))
		binary.LittleEndian.PutUint16(b[6:], uint16(r.CargoSlot))
	}
	b[8] = r.Mode
	return b
}
func (s *Service) handleCurrentEquipmentTransfer(session *gameSession, body []byte) error {
	mode := byte(0)
	if len(body) >= 5 {
		mode = body[4]
	}
	r, e := decodeCurrentEquipmentTransferRequest(body)
	if e != nil {
		return s.rejectCurrentEquipmentTransfer(session, mode, e)
	}
	if session == nil || session.selectedCharacterID == 0 {
		return s.rejectCurrentEquipmentTransfer(session, mode, dnfsvc.ErrOwner)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok {
		return s.rejectCurrentEquipmentTransfer(session, mode, dnfsvc.ErrOwner)
	}
	owner, e := dnfsvc.NewOwner(repos)
	if e != nil {
		return s.rejectCurrentEquipmentTransfer(session, mode, e)
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		return s.rejectCurrentEquipmentTransfer(session, mode, e)
	}
	config, e := dnfsvc.LoadTransferConfig(items.source)
	if e != nil {
		return s.rejectCurrentEquipmentTransfer(session, mode, e)
	}
	c := dnfsvc.Command{AccountID: strings.TrimSpace(s.accountIDForSession(session)), CharacterID: strconv.FormatUint(uint64(session.selectedCharacterID), 10), TargetSlot: r.TargetSlot, MaterialSlot: r.StoneSlot, Mode: r.Mode, Now: s.gameplayNow(), Encode: currentNPCEquipmentEncoder(items)}
	if r.Mode == 2 {
		c.RecipientID, e = s.currentNPCEquipmentRecipient(ctx, repos, c.AccountID, c.CharacterID, r.Recipient)
		if e != nil {
			return s.rejectCurrentEquipmentTransfer(session, mode, e)
		}
	}
	result, e := owner.Transfer(ctx, currentNPCEquipmentCatalog{items}, config, c)
	if e != nil {
		return s.rejectCurrentEquipmentTransfer(session, mode, e)
	}
	s.logGameEvent(session, "装备跨界资产已提交", "mode", r.Mode, "target_slot", r.TargetSlot, "recipient", result.RecipientID)
	// 当前EXE 0x2126160先读四个u16及模式。模式0需要原有对象完成本地搬运，
	// 所以成功回执在增量刷新前；模式2不删除来源，由随后权威行清除。
	if e = s.sendGameUpperSuccess(session, currentEquipmentTransferOpcode, currentEquipmentTransferSuccessBody(result)); e != nil {
		return e
	}
	if e = s.sendCurrentNPCEquipmentChanges(session, result); e != nil {
		return e
	}
	if result.EpicPieceID != 0 {
		return s.sendCurrentNPCTransferEpicSnapshot(session, c.AccountID)
	}
	return nil
}
func (s *Service) sendCurrentNPCEquipmentChanges(session *gameSession, r dnfsvc.Result) error {
	for _, list := range []byte{0, 12} {
		entries := []currentItemListEntry{}
		for _, v := range r.Changed {
			if v.List != list {
				continue
			}
			if v.Stack.ItemID > 0 && v.Stack.Count > 0 {
				entries = append(entries, currentNPCEquipmentEntry(list, v.Index, v.Stack))
			} else {
				var entry currentItemListEntry
				entry.patchCore(v.Index, math.MaxUint32, 0)
				entries = append(entries, entry)
			}
		}
		if len(entries) == 0 {
			continue
		}
		if e := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentItemUpdateBody(list, entries), 0); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) currentRequestUsesEquipmentReseal(session *gameSession, r currentResetItemAttrRequest) (bool, error) {
	if r.Mode != 0 {
		if r.Mode != 1 {
			return false, fmt.Errorf("装备封装模式%d不支持", r.Mode)
		}
		return true, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	i, found, e := repos.Inventory.Load(ctx, strconv.FormatUint(uint64(session.selectedCharacterID), 10))
	if e != nil {
		return false, e
	}
	if !found {
		return false, nil
	}
	m := i.Slots[fmt.Sprintf("0:%d", r.MaterialSlot)]
	if m.ItemID <= 0 || m.ItemID > math.MaxUint32 {
		return false, nil
	}
	_, knownOneShot := dnfsvc.WaxKind(dnfsvc.Definition{ItemID: uint32(m.ItemID)})
	if m.ItemID == 14 || knownOneShot {
		return true, nil
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		return false, nil
	}
	d, e := (currentNPCEquipmentCatalog{items}).Resolve(uint32(m.ItemID))
	if e != nil {
		return false, nil
	}
	_, wax := dnfsvc.WaxKind(d)
	return wax, nil
}
func (s *Service) handleCurrentEquipmentReseal(session *gameSession, r currentResetItemAttrRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok {
		return s.rejectCurrentEquipmentReseal(session, r, dnfsvc.ErrOwner)
	}
	owner, e := dnfsvc.NewOwner(repos)
	if e != nil {
		return s.rejectCurrentEquipmentReseal(session, r, e)
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		return s.rejectCurrentEquipmentReseal(session, r, e)
	}
	config, e := dnfsvc.LoadResealConfig(items.source)
	if e != nil {
		return s.rejectCurrentEquipmentReseal(session, r, e)
	}
	c := dnfsvc.Command{AccountID: strings.TrimSpace(s.accountIDForSession(session)), CharacterID: strconv.FormatUint(uint64(session.selectedCharacterID), 10), TargetSlot: r.TargetSlot, TargetItemID: uint32(r.TargetItemID), MaterialSlot: r.MaterialSlot, Mode: byte(r.Mode), Now: s.gameplayNow(), Encode: currentNPCEquipmentEncoder(items)}
	result, e := owner.Reseal(ctx, currentNPCEquipmentCatalog{items}, config, c)
	if e != nil {
		return s.rejectCurrentEquipmentReseal(session, r, e)
	}
	s.logGameEvent(session, "装备封装资产已提交", "mode", r.Mode, "target_slot", r.TargetSlot)
	if e = s.sendCurrentNPCEquipmentChanges(session, result); e != nil {
		return e
	}
	// 现用0x1fc7660会按槽修改主包对象。灵魂模式用0xffff跳过这次材料对象操作；
	// 真实目标与账号灵魂行已由上方op14更新，避免误删目标或同号的其它物品。
	b := make([]byte, 8)
	binary.LittleEndian.PutUint16(b, uint16(result.MaterialSlot))
	binary.LittleEndian.PutUint32(b[2:], uint32(result.MaterialRemaining))
	binary.LittleEndian.PutUint16(b[6:], 1)
	return s.sendGameUpperSuccess(session, currentResetItemAttrOpcode, b)
}

func (s *Service) rejectCurrentEquipmentTransfer(session *gameSession, mode byte, err error) error {
	s.logGameEvent(session, "装备跨界校验拒绝", "mode", mode, "err", err)
	if mode == 2 {
		return nil
	}
	return s.sendGameUpperFailure(session, currentEquipmentTransferOpcode, dnfsvc.FailureCode(err))
}
func (s *Service) rejectCurrentEquipmentReseal(session *gameSession, r currentResetItemAttrRequest, err error) error {
	s.logGameEvent(session, "装备封装校验拒绝", "mode", r.Mode, "target_slot", r.TargetSlot, "err", err)
	if r.Mode == 1 {
		return nil
	}
	code := dnfsvc.FailureCode(err)
	if code == 7 {
		code = 0x11
	}
	return s.sendGameUpperFailure(session, currentResetItemAttrOpcode, code)
}

// 仅服务于NPC跨界后的扣除显示，不改变领取、拾取、礼包或全局推送默认档。
// 全量从已提交账号账本读取；空表也发送，因此最后一种碎片归零时可清除旧显示。
func (s *Service) sendCurrentNPCTransferEpicSnapshot(session *gameSession, accountID string) error {
	if session == nil || !s.epicPieceBook().enabled() {
		return nil
	}
	push := s.epicPiecePush()
	if push == epicPiecePushOff || push == epicPiecePushVendorEmpty {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Account == nil {
		return dnfsvc.ErrOwner
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	account, found, err := repos.Account.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || account.AccountID != accountID {
		return dnfsvc.ErrOwner
	}
	balances := dnfrepo.AccountEpicPieceBalances(account)
	body, err := buildCurrentEpicPieceBalanceSyncBody(balances, true)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "跨界后史诗碎片余额刷新", "rows", len(balances))
	return s.sendGameUpperRawClass(session, currentEpicPieceBalanceSyncOpcode, body, 0)
}
