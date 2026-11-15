package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/npcshop"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const secretShopLedgerPrefix = "secret_shop_"

func secretShopStockKey(i int) string { return secretShopLedgerPrefix + "stock_" + strconv.Itoa(i) }

func currentSecretShopQuote(shop *currentSecretShop) [2]int64 {
	h := sha256.New()
	var row [25]byte
	for _, v := range shop.Rows {
		c := v.Candidate
		binary.LittleEndian.PutUint32(row[:], c.ItemID)
		row[4] = c.CostMode
		binary.LittleEndian.PutUint64(row[5:], uint64(c.Price))
		binary.LittleEndian.PutUint32(row[13:], c.CostItemID)
		binary.LittleEndian.PutUint32(row[17:], c.Count)
		binary.LittleEndian.PutUint32(row[21:], uint32(len(shop.Rows)))
		h.Write(row[:])
	}
	b := h.Sum(nil)
	return [2]int64{int64(binary.LittleEndian.Uint64(b[:8])), int64(binary.LittleEndian.Uint64(b[8:16]))}
}

func (s *Service) restoreCurrentSecretShopStock(session *gameSession, shop *currentSecretShop) error {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return errCurrentSecretShop
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	c, found, err := repos.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return err
	}
	if !found || c.AccountID != s.accountIDForSession(session) {
		return errCurrentSecretShop
	}
	st := c.Stats
	n := shop.Encounter.Nonce
	if st[secretShopLedgerPrefix+"initialized"] != 1 || st[secretShopLedgerPrefix+"nonce_0"] != n[0] || st[secretShopLedgerPrefix+"nonce_1"] != n[1] {
		return nil
	}
	q := currentSecretShopQuote(shop)
	if st[secretShopLedgerPrefix+"quote_0"] != q[0] || st[secretShopLedgerPrefix+"quote_1"] != q[1] {
		return errCurrentSecretShop
	}
	for i := range shop.Rows {
		v := st[secretShopStockKey(i)]
		if v < 0 || v > int64(shop.Rows[i].Candidate.Count) {
			return errCurrentSecretShop
		}
		shop.Rows[i].Remaining = uint32(v)
	}
	return nil
}

const secretShopReceiptWords = (int(repo.SoulWarehouseLastSlot) + 64) / 64

func secretShopReceiptMaskKey(i int) string {
	return secretShopLedgerPrefix + "updated_slots_" + strconv.Itoa(i)
}
func currentSecretShopReplayUpdates(a *npcshop.Assets, st map[string]int64, gold int64) []currentItemListEntry {
	updates := []currentItemListEntry{currentGoldWalletItemListEntry(gold)}
	for slot := int16(1); slot <= repo.SoulWarehouseLastSlot; slot++ {
		mask := uint64(st[secretShopReceiptMaskKey(int(slot)/64)])
		if mask&(uint64(1)<<uint(slot%64)) == 0 {
			continue
		}
		key := currentCeraShopInventorySlotKey(0, slot)
		slots := a.Inventory.Slots
		if repo.IsAccountSharedInventorySlot(0, slot) {
			slots = a.AccountInventory.Slots
		}
		if stack, ok := slots[key]; ok && stack.Count > 0 {
			updates = append(updates, currentItemListEntryFromStack(0, slot, stack))
		} else {
			var empty currentItemListEntry
			empty.patchCore(slot, math.MaxUint32, 0)
			updates = append(updates, empty)
		}
	}
	sortCurrentItemListEntries(updates)
	return updates
}

// The fixed-size ledger and asset mutation share the established account-first
// owner. There is no network I/O in the transaction and no per-run key growth.
// A committed-but-unacknowledged wire request is recognized by connection,
// monotonic wire sequence and exact request body on retry.
func (s *Service) commitCurrentSecretShopBuy(ctx context.Context, session *gameSession, shop *currentSecretShop, index int, wireCount uint32, sequence *uint64) (currentNPCShopBuyResult, uint32, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return currentNPCShopBuyResult{}, 0, errCurrentSecretShop
	}
	if shop == nil || shop.Encounter == nil || index < 0 || index >= len(shop.Rows) || len(shop.Rows) > 8 {
		return currentNPCShopBuyResult{}, 0, errCurrentSecretShop
	}
	row := shop.Rows[index]
	c := row.Candidate
	count, err := normalizeCurrentNPCShopBuyCount(row.Definition, wireCount)
	if err != nil || count > currentNPCShopMaxBuyCount {
		return currentNPCShopBuyResult{}, 0, errCurrentSecretShop
	}
	definition, err := currentPVFItemDefinitionForGrantAt(row.Definition, s.gameplayNow())
	if err != nil {
		return currentNPCShopBuyResult{}, 0, err
	}
	if c.Price <= 0 || c.Price > math.MaxInt32 || int64(count) > math.MaxInt64/c.Price {
		return currentNPCShopBuyResult{}, 0, errCurrentSecretShop
	}
	cost := c.Price * int64(count)
	accountID, characterID, _, owner, err := s.currentNPCShopMutationOwner(session)
	if err != nil {
		return currentNPCShopBuyResult{}, 0, err
	}
	hash := sha256.Sum256([]byte(session.connID))
	conn := int64(binary.LittleEndian.Uint64(hash[:8]))
	var result currentNPCShopBuyResult
	var remaining uint32
	err = owner.Mutate(ctx, npcshop.Command{AccountID: accountID, CharacterID: characterID, UpdatedAt: s.gameplayNow(), Project: func(a *npcshop.Assets) (npcshop.Changes, error) {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return npcshop.Changes{}, errCurrentSecretShop
		}
		st := a.Character.Stats
		if st == nil {
			st = map[string]int64{}
			a.Character.Stats = st
		}
		gold := st["gold"]
		if gold < 0 || gold > math.MaxInt32 {
			return npcshop.Changes{}, errCurrentNPCShopGoldOverflow
		}
		nonce := shop.Encounter.Nonce
		sameRun := st[secretShopLedgerPrefix+"nonce_0"] == nonce[0] && st[secretShopLedgerPrefix+"nonce_1"] == nonce[1] && st[secretShopLedgerPrefix+"initialized"] == 1
		quote := currentSecretShopQuote(shop)
		if sameRun && (st[secretShopLedgerPrefix+"quote_0"] != quote[0] || st[secretShopLedgerPrefix+"quote_1"] != quote[1]) {
			return npcshop.Changes{}, errCurrentSecretShop
		}
		if sequence != nil {
			if *sequence > math.MaxInt64 {
				return npcshop.Changes{}, errCurrentSecretShop
			}
			if st[secretShopLedgerPrefix+"seq_set"] == 1 && st[secretShopLedgerPrefix+"conn"] == conn {
				last := st[secretShopLedgerPrefix+"seq"]
				if int64(*sequence) < last {
					return npcshop.Changes{}, errCurrentSecretShop
				}
				if int64(*sequence) == last {
					if !sameRun || st[secretShopLedgerPrefix+"item"] != int64(c.ItemID) || st[secretShopLedgerPrefix+"wire_count"] != int64(wireCount) {
						return npcshop.Changes{}, errCurrentSecretShop
					}
					remaining = uint32(st[secretShopStockKey(index)])
					var entry currentItemListEntry
					for i := 0; i < 4; i++ {
						binary.LittleEndian.PutUint32(entry.data[i*4:], uint32(st[secretShopLedgerPrefix+"ack_"+strconv.Itoa(i)]))
					}
					result = currentNPCShopBuyResult{GoldAfter: gold, Item: entry, ItemID: c.ItemID, Count: count, CostItemID: c.CostItemID, CostItemNewCount: st[secretShopLedgerPrefix+"cost_remaining"], Updates: currentSecretShopReplayUpdates(a, st, gold)}
					return npcshop.Changes{}, nil
				}
			}
		}
		if !sameRun {
			st[secretShopLedgerPrefix+"initialized"] = 1
			st[secretShopLedgerPrefix+"nonce_0"] = nonce[0]
			st[secretShopLedgerPrefix+"nonce_1"] = nonce[1]
			st[secretShopLedgerPrefix+"quote_0"] = quote[0]
			st[secretShopLedgerPrefix+"quote_1"] = quote[1]
			for i := 0; i < 8; i++ {
				stock := uint32(0)
				if i < len(shop.Rows) {
					stock = shop.Rows[i].Candidate.Count
				}
				st[secretShopStockKey(i)] = int64(stock)
			}
		}
		stock := st[secretShopStockKey(index)]
		if stock < int64(count) || stock < 0 || stock > math.MaxInt32 {
			return npcshop.Changes{}, errCurrentSecretShop
		}
		changes := npcshop.Changes{Character: true}
		updates := []currentItemListEntry{}
		costRemaining := int64(0)
		if c.CostMode == 0 {
			if gold < cost {
				return npcshop.Changes{}, errCurrentNPCShopGoldInsufficient
			}
			gold -= cost
		} else if c.CostMode == 1 {
			var dirtyAccount, dirtyInventory bool
			costRemaining, updates, dirtyAccount, dirtyInventory, err = s.debitCurrentSecretShopMaterial(a, c.CostItemID, cost, s.gameplayNow())
			if err != nil {
				return npcshop.Changes{}, err
			}
			changes.AccountInventory = dirtyAccount
			changes.Inventory = dirtyInventory
			if int64(c.CostItemID) == repo.ColorlessCubeItemID {
				changes.ColorlessSpent = cost
			}
		} else {
			return npcshop.Changes{}, errCurrentSecretShop
		}
		slots, accountDirty, inventoryDirty, grantErr := s.grantCurrentNPCShopProductToContainers(a.AccountInventory, a.Inventory, definition, count)
		if grantErr != nil {
			return npcshop.Changes{}, grantErr
		}
		if len(slots) == 0 {
			return npcshop.Changes{}, errCurrentSecretShop
		}
		changes.AccountInventory = changes.AccountInventory || accountDirty
		changes.Inventory = changes.Inventory || inventoryDirty
		list := currentNPCShopProductList(definition)
		var item currentItemListEntry
		for i, slot := range slots {
			key := currentCeraShopInventorySlotKey(list, slot)
			container := a.Inventory.Slots
			if repo.IsAccountSharedInventorySlot(list, slot) {
				container = a.AccountInventory.Slots
			}
			stack, ok := container[key]
			if !ok || stack.ItemID != int64(c.ItemID) || stack.Count <= 0 {
				return npcshop.Changes{}, errCurrentSecretShop
			}
			if stack.Extra == nil {
				stack.Extra = map[string]string{}
			}
			stack.Extra["last_grant_source"] = "secret_shop"
			if err := s.initializePrivateGrowthGrant(&stack); err != nil {
				return npcshop.Changes{}, err
			}
			entry := currentItemListEntryFromStack(list, slot, stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			container[key] = stack
			if i == 0 {
				item = entry
			}
			updates = append(updates, entry)
		}
		st["gold"] = gold
		remaining = uint32(stock - int64(count))
		st[secretShopStockKey(index)] = int64(remaining)
		updates = append(updates, currentGoldWalletItemListEntry(gold))
		sortCurrentItemListEntries(updates)
		// Unlike the ordinary NPC-buy ACK, CMD296 sets the slot's final value.
		// Keep the actual merged stack count / equipment instance value.
		result = currentNPCShopBuyResult{GoldAfter: gold, Slot: slots[0], ItemID: c.ItemID, Count: count, CostItemID: c.CostItemID, CostItemNewCount: costRemaining, Item: item, Updates: updates}
		if sequence != nil {
			st[secretShopLedgerPrefix+"seq_set"] = 1
			st[secretShopLedgerPrefix+"conn"] = conn
			st[secretShopLedgerPrefix+"seq"] = int64(*sequence)
			st[secretShopLedgerPrefix+"item"] = int64(c.ItemID)
			st[secretShopLedgerPrefix+"wire_count"] = int64(wireCount)
			st[secretShopLedgerPrefix+"cost_remaining"] = costRemaining
			var masks [secretShopReceiptWords]uint64
			for _, entry := range updates {
				slot := int16(binary.LittleEndian.Uint16(entry.data[:2]))
				if slot < 0 || slot > repo.SoulWarehouseLastSlot {
					return npcshop.Changes{}, errCurrentSecretShop
				}
				masks[int(slot)/64] |= uint64(1) << uint(slot%64)
			}
			for i, mask := range masks {
				st[secretShopReceiptMaskKey(i)] = int64(mask)
			}
			for i := 0; i < 4; i++ {
				st[secretShopLedgerPrefix+"ack_"+strconv.Itoa(i)] = int64(binary.LittleEndian.Uint32(result.Item.data[i*4:]))
			}
		}
		return changes, nil
	}})
	if err != nil {
		return currentNPCShopBuyResult{}, 0, err
	}
	return result, remaining, nil
}

// Physical material costs span eligible main-bag stacks or their fixed shared
// warehouse cell. Locked and expired stacks never count toward the payment.
func (s *Service) debitCurrentSecretShopMaterial(a *npcshop.Assets, id uint32, amount int64, now time.Time) (int64, []currentItemListEntry, bool, bool, error) {
	if amount <= 0 || id == 0 {
		return 0, nil, false, false, errCurrentSecretShop
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return 0, nil, false, false, err
	}
	d, err := items.ResolveItem(id)
	if err != nil {
		return 0, nil, false, false, err
	}
	available := func(stack repo.ItemStack) bool {
		expire := currentItemListStackExpire(stack)
		return stack.ItemID == int64(id) && stack.Count > 0 && !currentNPCShopItemLocked(stack) && (expire == 0 || now.Unix() < int64(expire)) && (d.ExpirationDate.IsZero() || now.Before(d.ExpirationDate))
	}
	type payment struct {
		slot   int16
		key    string
		stack  repo.ItemStack
		shared bool
	}
	var payments []payment
	var total int64
	if slot, shared := currentDisjointWarehouseFixedSlots[id]; shared {
		key := repo.AccountSharedInventorySlotKey(slot)
		stack := a.AccountInventory.Slots[key]
		if available(stack) {
			payments = append(payments, payment{slot, key, stack, true})
			total = stack.Count
		}
	} else {
		for slot := int16(0); slot <= repo.SoulWarehouseLastSlot; slot++ {
			if repo.IsAccountSharedInventorySlot(repo.MainInventoryListType, slot) {
				continue
			}
			key := currentCeraShopInventorySlotKey(repo.MainInventoryListType, slot)
			stack := a.Inventory.Slots[key]
			if available(stack) {
				if stack.Count > math.MaxInt32-total {
					return 0, nil, false, false, errCurrentSecretShop
				}
				payments = append(payments, payment{slot, key, stack, false})
				total += stack.Count
			}
		}
	}
	if total < amount {
		return 0, nil, false, false, fmt.Errorf("%w: item=%d need=%d have=%d", errCurrentNPCShopMaterialInsufficient, id, amount, total)
	}
	left := amount
	var updates []currentItemListEntry
	ad, idirty := false, false
	for _, p := range payments {
		if left == 0 {
			break
		}
		n := p.stack.Count
		if n > left {
			n = left
		}
		p.stack.Count -= n
		left -= n
		container := a.Inventory.Slots
		if p.shared {
			container = a.AccountInventory.Slots
			ad = true
		} else {
			idirty = true
		}
		var entry currentItemListEntry
		if p.stack.Count == 0 && !p.shared {
			delete(container, p.key)
			entry.patchCore(p.slot, math.MaxUint32, 0)
		} else {
			entry = currentItemListEntryFromStack(repo.MainInventoryListType, p.slot, p.stack)
			p.stack.RawEntry = append([]byte(nil), entry.data[:]...)
			container[p.key] = p.stack
		}
		updates = append(updates, entry)
	}
	return total - amount, updates, ad, idirty, nil
}
