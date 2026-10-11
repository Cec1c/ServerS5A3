package gmtool

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// J7：账号晶块 6 格读/写（上游 `gm_extended.go:379-505` GMAccountCubes / GMSetAccountCube）。
//
// 数据源与槽位口径（与上游逐段同一份 PVF 语义）：
//
//	上游 :399-410  商品表 crystalCubeIDs[6]（premium_pvf.go:59，来自
//	               etc/premiumlist_new.etc + etc/cerashop.etc + stackable/stackable.lst）
//	               -> 槽位 = dnfrepo.CrystalWarehouseFirstSlot(354) + selection = 354..359
//	               -> key  = AccountSharedInventorySlotKey(slot) = "0:354".."0:359"
//	本仓          同一份解析在本进程已有唯一实现（dnfbridge/premium_pvf.go:126-153
//	               parseCurrentCrystalContractCubes：按 stackable/stackable.lst 里
//	               material/cubepiece_{black,white,red,blue,clear,gold}.stk 六个路径取 id）。
//	               gmtool 不重写这段解析，改为经 Deps.CrystalCubeItemIDs 注入同一份结果
//	               （装配点 dnfbridge/gmtool_service.go），避免第二套"商品表"实现。
//
// 与上游 `gm_extended.go:379-505` 的逐段对拍：
//
//	:380-394  解析角色；角色不存在 → 拒绝
//	:395-398  读账号共享背包（本仓 group.AccountInventory.Load(accountID)）
//	:399-402  商品表不可用 -> 明确失败
//	:403-408  **任一格 itemID<=0 都返回 ErrGMUnavailable，绝不填 0**
//	:409-416  逐格输出（含空格），count 只在"栈内物品就是该格物品且 count>0"时给出
//	:423-426  selection 必须 0..5、count 必须 0..MaxUint32（越界 → 拒绝，零写入）
//	:427-429  角色在线 -> 拒绝（本仓 J0 守卫；默认档不检查）
//	:441-445  **账号级**在线 -> 拒绝（本仓 requireAccountOffline，同一探针原语）
//	:446-453  商品表缺项 -> 明确失败（不填 0）
//	:454-461  物品目录解析失败 -> 硬错（本仓用 gmtool 既有物品索引 + PVF 文档实读）
//	:462-463  slot/key
//	:464      AccountItems.WithinAccountCharacterItems（账号共享槽的既有事务边界）
//	:465-476  事务内 Load/Clone；记录不存在时新建空记录
//	:477-478  count==0 -> delete(record.Slots, key)（整格删除，含 Extra）
//	:479-483  栈内是别的物品 -> **整栈替换**（旧 Extra 与 RawEntry 全丢）
//	:484-493  Extra 写 item_kind / pvf_path / stackable_type 恒写，stack_limit 仅 >0 时写
//	:494-495  currentItemListEntryFromStack 重编码 0x77 行写回 stack.RawEntry
//	:498-499  UpdatedAt + accounts.Save（单一写入点）
//	:504      提交后重读并返回整份 6 格快照
//
// 本仓与上游的**有意差异**（都是收紧，且拒绝时零写入）：
//
//	D3 请求体必须显式给出 count。上游 DTO 的 Count 缺省是 Go 零值 0，而 0 的语义是
//	   **整格删除** —— 少写一个字段就会销毁操作者没打算动的晶块。这里用指针区分
//	   "没给"与"给了 0"，缺 count 直接拒绝。显式 count=0 仍然是删除（与上游同义）。
//	D4 `Deps.EncodeStackRow` 未注入时不做 RawEntry 重编码（其余字段照写），并在
//	   审计事件里如实标注 raw_entry_encoded=false。装配点一定会注入（它包的就是
//	   本进程唯一的行编码器），此分支只是"拿不到能力就不要凭空造字节"的兜底。
const accountCubeSelectionCount = 6

var (
	// errCrystalCubeCatalogUnavailable：商品表能力没接上（Deps 未注入或读取失败）。
	errCrystalCubeCatalogUnavailable = errors.New("读取晶块商品表失败")
	// errCrystalCubeCatalogIncomplete：商品表存在但该格没有物品 id。
	// 对应上游 :407 的 ErrGMUnavailable —— **绝不填 0**。
	errCrystalCubeCatalogIncomplete = errors.New("晶块商品表缺项")
	// errAccountInventoryUnavailable：账号共享背包能力没接上。
	errAccountInventoryUnavailable = errors.New("账号共享背包能力未就绪")
	// errAccountMissingOnCharacter：角色记录没有归属账号，无法定位账号共享背包。
	errAccountMissingOnCharacter = errors.New("角色记录缺少归属账号，无法定位账号共享背包")
)

// accountCubeStackDefinition 是写晶块时需要的 PVF 证据（上游 :454-461 的
// catalog.ResolveItem 结果里真正被 :488-493 用到的那四项）。
type accountCubeStackDefinition struct {
	ItemID        uint32
	Kind          string
	PVFPath       string
	StackableType string
	StackLimit    int64
}

// accountCubeSetRequest 是设置请求体。
//
// 刻意**不接受** itemId：物品 id 由服务端的商品表决定，客户端不能指定
// （与 dnfbridge/premium_pvf.go:123-125 的"never trusts or hard-codes a
// request-side item ID"同一立场）。
type accountCubeSetRequest struct {
	Selection *int   `json:"selection"`
	Count     *int64 `json:"count"`
}

// crystalCubeItemIDs 取六个原生槽位的物品 id。
func (s *Server) crystalCubeItemIDs() ([accountCubeSelectionCount]int64, error) {
	var ids [accountCubeSelectionCount]int64
	if s == nil || s.deps.CrystalCubeItemIDs == nil {
		return ids, fmt.Errorf("%w: 面板未接入运行时 PVF 的晶块商品表", errCrystalCubeCatalogUnavailable)
	}
	loaded, err := s.deps.CrystalCubeItemIDs()
	if err != nil {
		return ids, fmt.Errorf("%w: %v", errCrystalCubeCatalogUnavailable, err)
	}
	return loaded, nil
}

// accountCubeDefinition 解析一格晶块要写进 ItemStack.Extra 的 PVF 证据。
//
// 与发放链路同源：物品索引（items_index.go，stackable.lst + equipment.lst）
// 给 kind/pvf_path，再按该路径实读文档取 `[stackable type]`（原文，含方括号，
// 与 dnfbridge 的 Extra 口径一致：dungeon_drop_catalog.go:261）与 `[stack limit]`。
// 解析不出来就明确失败——**不允许**写一个只有 ItemID 的栈让客户端渲染成别的样子。
func (s *Server) accountCubeDefinition(itemID int64) (accountCubeStackDefinition, error) {
	definition := accountCubeStackDefinition{ItemID: uint32(itemID)}
	if itemID <= 0 || itemID > math.MaxUint32 {
		return accountCubeStackDefinition{}, fmt.Errorf("%w: 物品 %d 超出 uint32", errCrystalCubeCatalogIncomplete, itemID)
	}
	entry, found := s.items.lookup(uint32(itemID))
	if !found {
		return accountCubeStackDefinition{}, fmt.Errorf(
			"%w: 运行时 PVF 物品索引里没有晶块物品 %d（stackable.lst/equipment.lst 与商品表不一致）",
			errCrystalCubeCatalogIncomplete, itemID)
	}
	document, err := s.grantPVFItemDocument(entry)
	if err != nil {
		return accountCubeStackDefinition{}, fmt.Errorf("读取晶块物品 %d 的 PVF 文档失败: %w", itemID, err)
	}
	definition.Kind = strings.TrimSpace(entry.Kind)
	definition.PVFPath = strings.TrimSpace(entry.PVFPath)
	if raw, ok := document.Text("stackable type"); ok {
		definition.StackableType = strings.TrimSpace(raw)
	}
	if limit, ok := document.Int("stack limit"); ok && limit > 0 {
		definition.StackLimit = limit
	}
	return definition, nil
}

// ---------------------------------------------------------------------------
// 读：GET /api/characters/{id}/account-cubes
// ---------------------------------------------------------------------------

func (s *Server) handleAccountCubes(w http.ResponseWriter, r *http.Request) {
	ctx, group, _, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	payload, err := s.accountCubeSnapshot(ctx, group, character)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, payload)
}

// accountCubeSnapshot 永远输出 6 行（含空格），与上游 :404-416 一致。
func (s *Server) accountCubeSnapshot(
	ctx context.Context,
	group dnfrepo.Group,
	character dnfrepo.CharacterRecord,
) (map[string]any, error) {
	itemIDs, err := s.crystalCubeItemIDs()
	if err != nil {
		return nil, err
	}
	if group.AccountInventory == nil {
		return nil, errAccountInventoryUnavailable
	}
	accountID := strings.TrimSpace(character.AccountID)
	if accountID == "" {
		return nil, errAccountMissingOnCharacter
	}
	inventory, _, err := group.AccountInventory.Load(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("读取账号共享背包失败: %w", err)
	}
	cubes := make([]map[string]any, 0, accountCubeSelectionCount)
	for selection := 0; selection < accountCubeSelectionCount; selection++ {
		itemID := itemIDs[selection]
		if itemID <= 0 || itemID > math.MaxUint32 {
			// 上游 :407：ErrGMUnavailable。缺项就是缺项，不填 0、不猜。
			return nil, fmt.Errorf("%w: 第 %d 格的物品 id 在运行时 PVF 里是 %d",
				errCrystalCubeCatalogIncomplete, selection, itemID)
		}
		slot := dnfrepo.CrystalWarehouseFirstSlot + int16(selection)
		stack := inventory.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)]
		count := int64(0)
		if stack.ItemID == itemID && stack.Count > 0 {
			count = stack.Count
		}
		cubes = append(cubes, map[string]any{
			"selection": selection,
			"slot":      int(slot),
			"itemId":    itemID,
			"name":      s.items.name(uint32(itemID)),
			"count":     count,
		})
	}
	return map[string]any{
		"characterId": panelIDOfCharacter(character),
		"accountId":   accountID,
		"cubes":       cubes,
	}, nil
}

// ---------------------------------------------------------------------------
// 写：POST /api/characters/{id}/account-cubes
// ---------------------------------------------------------------------------

func (s *Server) handleSetAccountCube(w http.ResponseWriter, r *http.Request) {
	// 顺序与上游 gm_extended.go:423-464 一致：
	// 解析角色 → 校验请求体 → 角色级守卫 → 账号级守卫 → 商品表 → 物品目录 → 事务。
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeBody[accountCubeSetRequest](w, r)
	if !ok {
		return
	}
	selection, count, err := accountCubeRequestValues(body)
	if err != nil {
		s.logEvent("gm-account-cube-rejected",
			"panel_id", panelID,
			"character_id", character.CharacterID,
			"account_id", character.AccountID,
			"reason", accountCubeRejectionReason(err),
			"result", "rejected",
		)
		writeFailure(w, err.Error())
		return
	}

	// J0 守卫：先角色级（上游 :427），再账号级（上游 :441）。
	if !s.requireOffline(w, "account-cube-set", panelID) {
		return
	}
	if !s.requireAccountOffline(ctx, w, "account-cube-set", group, character.AccountID, panelID) {
		return
	}

	itemIDs, err := s.crystalCubeItemIDs()
	if err != nil {
		s.logRejectedAccountCube(panelID, character, selection, count, 0, 0, "crystal_catalog_unavailable", err)
		writeFailure(w, err.Error())
		return
	}
	itemID := itemIDs[selection]
	if itemID <= 0 || itemID > math.MaxUint32 {
		err := fmt.Errorf("%w: 第 %d 格的物品 id 在运行时 PVF 里是 %d", errCrystalCubeCatalogIncomplete, selection, itemID)
		s.logRejectedAccountCube(panelID, character, selection, count, 0, 0, "crystal_catalog_incomplete", err)
		writeFailure(w, err.Error()+"（未做任何写入）。")
		return
	}
	definition, err := s.accountCubeDefinition(itemID)
	if err != nil {
		s.logRejectedAccountCube(panelID, character, selection, count, 0, 0, "item_definition_unavailable", err)
		writeFailure(w, "解析晶块物品失败: "+err.Error()+"（未做任何写入）。")
		return
	}

	if group.AccountInventory == nil || group.AccountItems == nil {
		s.logRejectedAccountCube(panelID, character, selection, count, 0, 0, "account_items_transaction_unavailable", errAccountInventoryUnavailable)
		writeFailure(w, "设置晶块失败: 账号共享背包事务能力未就绪")
		return
	}
	accountID := strings.TrimSpace(character.AccountID)
	if accountID == "" {
		s.logRejectedAccountCube(panelID, character, selection, count, 0, 0, "character_account_missing", errAccountMissingOnCharacter)
		writeFailure(w, errAccountMissingOnCharacter.Error())
		return
	}

	slot := dnfrepo.CrystalWarehouseFirstSlot + int16(selection)
	key := dnfrepo.AccountSharedInventorySlotKey(slot)
	var (
		oldItemID int64
		oldCount  int64
		encoded   bool
	)
	// 单事务、单一写入点，复用账号共享槽既有的事务边界（上游 :464）。
	err = group.AccountItems.WithinAccountCharacterItems(ctx, accountID, character.CharacterID, func(
		accounts dnfrepo.AccountInventoryRepository,
		_ dnfrepo.InventoryRepository,
	) error {
		record, exists, loadErr := accounts.Load(ctx, accountID)
		if loadErr != nil {
			return loadErr
		}
		if !exists {
			record = dnfrepo.AccountInventoryRecord{AccountID: accountID, Slots: make(map[string]dnfrepo.ItemStack)}
		} else {
			record = dnfrepo.CloneAccountInventory(record)
		}
		if record.Slots == nil {
			record.Slots = make(map[string]dnfrepo.ItemStack)
		}
		record.AccountID = accountID

		previous := record.Slots[key]
		oldItemID, oldCount = previous.ItemID, previous.Count
		if count == 0 {
			// 上游 :477-478：整格删除（含 Extra）。
			delete(record.Slots, key)
		} else {
			stack := previous
			if stack.ItemID != itemID {
				// 上游 :481-483：栈内是别的物品 -> 整栈替换，旧元数据与 RawEntry 全丢。
				stack = dnfrepo.ItemStack{ItemID: itemID, Extra: make(map[string]string)}
			}
			if stack.Extra == nil {
				stack.Extra = make(map[string]string)
			}
			stack.ItemID, stack.Count = itemID, count
			// 上游 :488-493：这三项恒写，stack_limit 仅 >0 时写。
			stack.Extra["item_kind"] = definition.Kind
			stack.Extra["pvf_path"] = definition.PVFPath
			stack.Extra["stackable_type"] = definition.StackableType
			if definition.StackLimit > 0 {
				stack.Extra["stack_limit"] = strconv.FormatInt(definition.StackLimit, 10)
			}
			// 上游 :494-495：用本进程唯一的行编码器把 0x77 行重编码写回 RawEntry。
			if s.deps.EncodeStackRow != nil {
				stack.RawEntry = s.deps.EncodeStackRow(dnfrepo.MainInventoryListType, slot, stack)
				encoded = len(stack.RawEntry) > 0
			}
			record.Slots[key] = stack
		}
		record.UpdatedAt = time.Now().UTC()
		// 上游 :499：整条账号共享背包一次 Save。
		return accounts.Save(ctx, record)
	})
	if err != nil {
		s.logEvent("gm-account-cube-failed",
			"panel_id", panelID,
			"character_id", character.CharacterID,
			"account_id", accountID,
			"selection", selection,
			"slot", int(slot),
			"item_id", itemID,
			"old_item_id", oldItemID,
			"old_count", oldCount,
			"count", count,
			"reason", "write_failed",
			"error", err.Error(),
			"result", "failed",
		)
		writeFailure(w, "设置晶块失败: "+err.Error())
		return
	}

	s.logEvent("gm-account-cube-set",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", accountID,
		"selection", selection,
		"slot", int(slot),
		"item_id", itemID,
		"old_item_id", oldItemID,
		"old_count", oldCount,
		"count", count,
		"raw_entry_encoded", encoded,
		"result", "applied",
	)

	// 上游 :504：提交后重读，返回整份 6 格快照。
	payload, snapshotErr := s.accountCubeSnapshot(ctx, group, character)
	if snapshotErr != nil {
		writeFailure(w, "晶块已写入，但重读快照失败: "+snapshotErr.Error())
		return
	}
	payload["selection"] = selection
	payload["slot"] = int(slot)
	payload["itemId"] = itemID
	payload["count"] = count
	writeOK(w, payload)
}

// accountCubeRequestValues 校验请求体；任何不合法都在触碰仓储之前返回。
func accountCubeRequestValues(body accountCubeSetRequest) (int, int64, error) {
	if body.Selection == nil {
		return 0, 0, errors.New("缺少 selection（0..5，对应黑/白/红/蓝/无色/金，未做任何写入）。")
	}
	selection := *body.Selection
	if selection < 0 || selection >= accountCubeSelectionCount {
		return 0, 0, fmt.Errorf("selection 必须在 0..%d 之间，当前 %d（未做任何写入）。",
			accountCubeSelectionCount-1, selection)
	}
	if body.Count == nil {
		// 见文件头 D3：count 缺省在上游等于 0，而 0 是"整格删除"，必须显式给出。
		return 0, 0, errors.New("缺少 count（0 表示整格删除，必须显式给出，未做任何写入）。")
	}
	count := *body.Count
	if count < 0 || uint64(count) > math.MaxUint32 {
		return 0, 0, fmt.Errorf("count 必须在 0..%d 之间，当前 %d（未做任何写入）。", uint64(math.MaxUint32), count)
	}
	return selection, count, nil
}

// accountCubeRejectionReason 给请求体拒绝归类，便于审计事件聚合。
func accountCubeRejectionReason(err error) string {
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "缺少 selection"):
		return "missing_selection"
	case strings.HasPrefix(message, "selection 必须在"):
		return "selection_out_of_range"
	case strings.HasPrefix(message, "缺少 count"):
		return "missing_count"
	default:
		return "count_out_of_range"
	}
}

func (s *Server) logRejectedAccountCube(
	panelID int,
	character dnfrepo.CharacterRecord,
	selection int,
	count int64,
	oldItemID, oldCount int64,
	cause string,
	err error,
) {
	fields := []any{
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"selection", selection,
		"slot", int(dnfrepo.CrystalWarehouseFirstSlot + int16(selection)),
		"old_item_id", oldItemID,
		"old_count", oldCount,
		"count", count,
		"reason", cause,
		"result", "rejected",
	}
	if err != nil {
		fields = append(fields, "error", err.Error())
	}
	s.logEvent("gm-account-cube-rejected", fields...)
}
