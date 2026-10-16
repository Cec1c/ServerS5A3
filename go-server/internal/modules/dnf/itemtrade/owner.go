// Package itemtrade owns the durable, two-character inventory exchange used
// by the current client's town item-trade flow. Wire/session state remains in
// dnfbridge; this package validates offers and commits both inventories.
package itemtrade

import (
	"context"
	"errors"
	"fmt"
	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrRepositoryUnavailable = errors.New("item trade repository is unavailable")
	ErrInvalidParticipants   = errors.New("item trade participants are invalid")
	ErrInvalidOffer          = errors.New("item trade offer is invalid")
	ErrItemUnavailable       = errors.New("item trade item is unavailable")
	ErrItemNotTradeable      = errors.New("item is not tradeable")
	ErrInventoryFull         = errors.New("item trade destination inventory is full")
	ErrGoldUnavailable       = errors.New("item trade gold is unavailable")
	ErrGoldOverflow          = errors.New("item trade gold would overflow")
)

const (
	mainListType   byte = 0
	avatarListType byte = 1

	equipmentFirst  int16 = 9
	equipmentLast   int16 = 64
	consumableFirst int16 = 65
	consumableLast  int16 = 120
	materialFirst   int16 = 121
	materialLast    int16 = 176
	questFirst      int16 = 177
	questLast       int16 = 232
	professionFirst int16 = 233
	professionLast  int16 = 288
	emblemFirst     int16 = 289
	emblemLast      int16 = 344
	specialFirst    int16 = 345
	specialLast     int16 = 353

	avatarFirst int16 = 0
	avatarLast  int16 = 500
)

type Offer struct {
	TradeSlot    uint16
	SourceList   byte
	SourceSlot   int16
	Count        int64
	ExpectedItem int64
}

type Participant struct {
	CharacterID string
	AccountID   string
	Offers      []Offer
	Gold        int64
}

type Transfer struct {
	FromCharacterID string
	ToCharacterID   string
	TradeSlot       uint16
	DestinationList byte
	DestinationSlot int16
	Stack           dnfrepo.ItemStack
}

type Result struct {
	Inventories map[string]dnfrepo.InventoryRecord
	Characters  map[string]dnfrepo.CharacterRecord
	Received    map[string][]Transfer
}

type Owner struct {
	repositories        dnfrepo.Group
	now                 func() time.Time
	destinationResolver DestinationResolver
}

// DestinationResolver 由运行时 PVF 提供收件分类；未知旧调用仍使用原有来源段。
type DestinationResolver func(Offer, dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error)

func NewOwner(repositories dnfrepo.Group, resolvers ...DestinationResolver) (*Owner, error) {
	if repositories.CharacterTrade == nil {
		return nil, ErrRepositoryUnavailable
	}
	owner := &Owner{repositories: repositories, now: time.Now}
	if len(resolvers) > 0 {
		owner.destinationResolver = resolvers[0]
	}
	return owner, nil
}

func (o *Owner) Exchange(ctx context.Context, first Participant, second Participant) (Result, error) {
	if o == nil || o.repositories.CharacterTrade == nil {
		return Result{}, ErrRepositoryUnavailable
	}
	first.CharacterID = strings.TrimSpace(first.CharacterID)
	second.CharacterID = strings.TrimSpace(second.CharacterID)
	if first.CharacterID == "" || second.CharacterID == "" || first.CharacterID == second.CharacterID {
		return Result{}, ErrInvalidParticipants
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	participants := []Participant{first, second}
	if first.Gold < 0 || second.Gold < 0 {
		return Result{}, ErrInvalidOffer
	}
	sort.Slice(participants, func(i, j int) bool {
		return participants[i].CharacterID < participants[j].CharacterID
	})
	result := Result{}
	err := o.withTradeRepositories(ctx, participants,
		func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository, shared *crystalTradeState) error {
			loaded := make(map[string]dnfrepo.InventoryRecord, 2)
			for _, participant := range participants {
				record, found, err := inventories.Load(ctx, participant.CharacterID)
				if err != nil {
					return err
				}
				if !found {
					record = dnfrepo.InventoryRecord{CharacterID: participant.CharacterID}
				}
				if record.Slots == nil {
					record.Slots = make(map[string]dnfrepo.ItemStack)
				}
				if err := dnfrepo.BindInventoryContainerState(ctx, o.repositories.Settings, &record); err != nil {
					return err
				}
				loaded[participant.CharacterID] = record
			}
			loadedCharacters := make(map[string]dnfrepo.CharacterRecord, 2)
			if participants[0].Gold > 0 || participants[1].Gold > 0 {
				for _, participant := range participants {
					record, found, err := characters.Load(ctx, participant.CharacterID)
					if err != nil {
						return err
					}
					if !found {
						return fmt.Errorf("character %s: %w", participant.CharacterID, ErrGoldUnavailable)
					}
					if record.Stats == nil {
						record.Stats = make(map[string]int64)
					}
					loadedCharacters[participant.CharacterID] = record
				}
				for index, participant := range participants {
					recipient := participants[1-index]
					record := loadedCharacters[participant.CharacterID]
					balance := record.Stats["gold"]
					if balance < participant.Gold {
						return fmt.Errorf("character %s balance=%d offer=%d: %w", participant.CharacterID, balance, participant.Gold, ErrGoldUnavailable)
					}
					balance -= participant.Gold
					if recipient.Gold > math.MaxInt64-balance {
						return fmt.Errorf("character %s balance=%d incoming=%d: %w", participant.CharacterID, balance, recipient.Gold, ErrGoldOverflow)
					}
					record.Stats["gold"] = balance + recipient.Gold
					loadedCharacters[participant.CharacterID] = record
				}
			}

			staged := make(map[string][]stagedOffer, 2)
			for _, participant := range participants {
				rows, err := validateOffersWithCrystals(shared.view(participant.CharacterID, loaded[participant.CharacterID]), participant.Offers, o.clock(), shared != nil)
				if err != nil {
					return fmt.Errorf("character %s: %w", participant.CharacterID, err)
				}
				staged[participant.CharacterID] = rows
			}

			// Remove every outgoing quantity before assigning incoming slots. This
			// lets a full inventory complete a one-for-one exchange without a
			// transient capacity failure.
			for _, participant := range participants {
				record := loaded[participant.CharacterID]
				for _, row := range staged[participant.CharacterID] {
					if handled, err := shared.remove(participant.CharacterID, row); handled || err != nil {
						if err != nil {
							return err
						}
						continue
					}
					stack := record.Slots[row.sourceKey]
					if row.offer.Count == stack.Count {
						delete(record.Slots, row.sourceKey)
					} else {
						stack.Count -= row.offer.Count
						record.Slots[row.sourceKey] = stack
					}
				}
				loaded[participant.CharacterID] = record
			}

			received := make(map[string][]Transfer, 2)
			for index, participant := range participants {
				recipient := participants[1-index]
				record := loaded[recipient.CharacterID]
				for _, row := range staged[participant.CharacterID] {
					if transfer, handled, err := shared.receive(participant.CharacterID, recipient.CharacterID, row); handled || err != nil {
						if err != nil {
							return err
						}
						received[recipient.CharacterID] = append(received[recipient.CharacterID], transfer)
						continue
					}
					stack := cloneStack(row.stack)
					stack.Count = row.offer.Count
					policy, err := o.destinationSlotPolicy(row.offer, stack)
					if err != nil {
						return err
					}
					policy = policy.ForInventory(&record)
					// 堆叠品先并入同身份的既有行（同一页段内优先），只有装不下的
					// 剩余部分才另开空格。旧代码无条件 allocateDestinationSlot() 开新格，
					// 于是"交易收下同一件材料"每次都多一行（仓库里 2:22 ×8480 与
					// 2:23 ×25 就是这么并存的）。判据是仓库层的 dnfrepo.StackContract
					// 与 dnfrepo.CanMergeItemStack，与发放路径同口径。
					remaining, mergedSlot, mergedStack, merged := mergeTradeStack(
						record.Slots, policy, stack)
					if merged {
						received[recipient.CharacterID] = append(received[recipient.CharacterID], Transfer{
							FromCharacterID: participant.CharacterID,
							ToCharacterID:   recipient.CharacterID,
							TradeSlot:       row.offer.TradeSlot,
							DestinationList: row.offer.SourceList,
							DestinationSlot: mergedSlot,
							Stack:           mergedStack,
						})
					}
					if remaining <= 0 {
						continue
					}
					destinationSlot, err := allocateDestinationByPolicy(record.Slots, policy)
					if err != nil {
						return fmt.Errorf("character %s receiving trade slot %d: %w", recipient.CharacterID, row.offer.TradeSlot, err)
					}
					stack.Count = remaining
					destinationKey := slotKey(row.offer.SourceList, destinationSlot)
					record.Slots[destinationKey] = stack
					received[recipient.CharacterID] = append(received[recipient.CharacterID], Transfer{
						FromCharacterID: participant.CharacterID,
						ToCharacterID:   recipient.CharacterID,
						TradeSlot:       row.offer.TradeSlot,
						DestinationList: row.offer.SourceList,
						DestinationSlot: destinationSlot,
						Stack:           cloneStack(stack),
					})
				}
				loaded[recipient.CharacterID] = record
			}

			for _, participant := range participants {
				record := loaded[participant.CharacterID]
				record.UpdatedAt = o.clock().UTC()
				persisted := record
				persisted.ContainerState = nil
				if err := inventories.Save(ctx, persisted); err != nil {
					return err
				}
				loaded[participant.CharacterID] = record
			}
			for _, participant := range participants {
				record, changed := loadedCharacters[participant.CharacterID]
				if !changed {
					continue
				}
				record.UpdatedAt = o.clock().UTC()
				if err := characters.Save(ctx, record); err != nil {
					return err
				}
				loadedCharacters[participant.CharacterID] = record
			}
			result = Result{Inventories: loaded, Characters: loadedCharacters, Received: received}
			return nil
		},
	)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

type stagedOffer struct {
	offer     Offer
	sourceKey string
	stack     dnfrepo.ItemStack
}

func validateOffers(record dnfrepo.InventoryRecord, offers []Offer, now time.Time) ([]stagedOffer, error) {
	return validateOffersWithCrystals(record, offers, now, false)
}

func validateOffersWithCrystals(record dnfrepo.InventoryRecord, offers []Offer, now time.Time, shared bool) ([]stagedOffer, error) {
	rows := make([]stagedOffer, 0, len(offers))
	tradeSlots := make(map[uint16]struct{}, len(offers))
	sourceCounts := make(map[string]int64, len(offers))
	ordered := append([]Offer(nil), offers...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].TradeSlot < ordered[j].TradeSlot })
	for _, offer := range ordered {
		if _, duplicate := tradeSlots[offer.TradeSlot]; duplicate || offer.Count <= 0 || !supportedSourceList(offer.SourceList) || offer.SourceSlot < 0 {
			return nil, ErrInvalidOffer
		}
		if dnfrepo.IsAccountSharedInventorySlot(offer.SourceList, offer.SourceSlot) && !shared {
			return nil, ErrItemNotTradeable
		}
		tradeSlots[offer.TradeSlot] = struct{}{}
		key := slotKey(offer.SourceList, offer.SourceSlot)
		stack, found := record.Slots[key]
		if !found || stack.ItemID <= 0 || stack.Count <= 0 || (offer.ExpectedItem > 0 && stack.ItemID != offer.ExpectedItem) {
			return nil, ErrItemUnavailable
		}
		if dnfrepo.IsAccountSharedInventorySlot(offer.SourceList, offer.SourceSlot) {
			if slot, ok := TradableCrystalSlot(stack.ItemID); !ok || slot != offer.SourceSlot {
				return nil, ErrItemNotTradeable
			}
		}
		if stack.Bind || (!stack.ExpireAt.IsZero() && !stack.ExpireAt.After(now)) {
			return nil, ErrItemNotTradeable
		}
		if offer.Count > stack.Count-sourceCounts[key] {
			return nil, ErrItemUnavailable
		}
		sourceCounts[key] += offer.Count
		rows = append(rows, stagedOffer{offer: offer, sourceKey: key, stack: cloneStack(stack)})
	}
	return rows, nil
}

func allocateDestinationSlot(slots map[string]dnfrepo.ItemStack, listType byte, preferred int16) (int16, error) {
	if !supportedSourceList(listType) {
		return 0, ErrInvalidOffer
	}
	start, end, ok := destinationSlotRange(listType, preferred)
	if !ok {
		return 0, ErrInvalidOffer
	}
	return allocateDestinationByPolicy(slots, dnfplacement.SlotPolicy{ListType: listType, Start: start, End: end})
}

func allocateDestinationByPolicy(slots map[string]dnfrepo.ItemStack, policy dnfplacement.SlotPolicy) (int16, error) {
	for _, slot := range policy.EmptySlots() {
		if _, occupied := slots[slotKey(policy.ListType, slot)]; !occupied {
			return slot, nil
		}
	}
	return 0, ErrInventoryFull
}

func (o *Owner) destinationSlotPolicy(offer Offer, stack dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error) {
	if o.destinationResolver != nil {
		policy, err := o.destinationResolver(offer, stack)
		if err != nil {
			return policy, err
		}
		if policy.ListType != offer.SourceList || len(policy.EmptySlots()) == 0 {
			return policy, ErrInvalidOffer
		}
		return policy, nil
	}
	start, end, ok := destinationSlotRange(offer.SourceList, offer.SourceSlot)
	if !ok {
		return dnfplacement.SlotPolicy{}, ErrInvalidOffer
	}
	return dnfplacement.SlotPolicy{ListType: offer.SourceList, Start: start, End: end}, nil
}

// destinationSlotRange keeps an incoming item in the same current-client bag
// page as its source. The source index is not a portable destination: copying
// a sender's expanded high slot into a recipient with only the base capacity
// persists the item but leaves it outside the recipient's visible cells.
func destinationSlotRange(listType byte, sourceSlot int16) (int16, int16, bool) {
	if listType == avatarListType {
		if sourceSlot < avatarFirst || sourceSlot > avatarLast {
			return 0, 0, false
		}
		return avatarFirst, avatarLast, true
	}
	for _, page := range [...]struct{ first, last int16 }{
		{3, 8},
		{equipmentFirst, equipmentLast},
		{consumableFirst, consumableLast},
		{materialFirst, materialLast},
		{questFirst, questLast},
		{professionFirst, professionLast},
		{emblemFirst, emblemLast},
		{specialFirst, specialLast},
	} {
		if sourceSlot >= page.first && sourceSlot <= page.last {
			return page.first, page.last, true
		}
	}
	return 0, 0, false
}

func supportedSourceList(listType byte) bool {
	return listType == mainListType || listType == avatarListType
}

// mergeTradeStack 按共同选格规则，把交易收下的 stack 并入收件人同身份的既有行，
// 返回还没落位的剩余量与"并入的那一行"（槽位 + 合并后的行；没并上就是 ok=false）。
//
// 为什么必须并：交易以前无条件 allocateDestinationSlot() 另开一格，于是收下同一件
// 材料会越收越多行（线上库角色 2 的 2:22 ×8480 与 2:23 ×25 就是这么并存的）。
// 判据照抄发放路径与 op19 的口径：
//   - 两边都得是可堆叠的同一件东西：dnfrepo.StackContract 给"有没有堆叠资格 + 上限"，
//     StackContract 的 pvf_path/limit 必须逐字相同；
//   - 身份与到期时间：dnfrepo.CanMergeItemStack（缺省即兼容，记账键不参与）；
//   - 上限：limit > 0 严格封顶，limit == 0（PVF 没写 [stack limit]）按无上限处理。
//
// 装备/时装/已达上限而 Contract 不成立的行一律跳过，剩余部分仍走原来的空格逻辑，
// 所以"收下装备"与"背包满"两条老行为一个字都没变。
func mergeTradeStack(slots map[string]dnfrepo.ItemStack, policy dnfplacement.SlotPolicy, incoming dnfrepo.ItemStack) (int64, int16, dnfrepo.ItemStack, bool) {
	if incoming.Count <= 0 {
		return incoming.Count, 0, dnfrepo.ItemStack{}, false
	}
	path, limit, ok := dnfrepo.StackContract(incoming)
	if !ok || path == "" {
		return incoming.Count, 0, dnfrepo.ItemStack{}, false
	}
	remaining := incoming.Count
	mergedSlot := int16(-1)
	mergedStack := dnfrepo.ItemStack{}
	for _, slot := range policy.MergeSlots() {
		if remaining <= 0 {
			break
		}
		key := slotKey(policy.ListType, slot)
		existing, found := slots[key]
		if !found || existing.Count <= 0 {
			continue
		}
		existingPath, existingLimit, existingOK := dnfrepo.StackContract(existing)
		if !existingOK || existingPath != path || existingLimit != limit {
			continue
		}
		if !dnfrepo.CanMergeItemStack(existing, incoming) {
			continue
		}
		room := remaining
		if limit > 0 && existing.Count+room > limit {
			room = limit - existing.Count
		}
		if room <= 0 {
			continue
		}
		existing = cloneStack(existing)
		existing.Count += room
		slots[key] = existing
		remaining -= room
		if mergedSlot < 0 {
			mergedSlot = slot
			mergedStack = cloneStack(existing)
		}
	}
	if mergedSlot < 0 {
		return remaining, 0, dnfrepo.ItemStack{}, false
	}
	return remaining, mergedSlot, mergedStack, true
}

func validDestinationSlot(listType byte, slot int16) bool {
	_, _, ok := destinationSlotRange(listType, slot)
	return ok
}

func slotKey(listType byte, slot int16) string {
	return strconv.Itoa(int(listType)) + ":" + strconv.FormatInt(int64(slot), 10)
}

func cloneStack(stack dnfrepo.ItemStack) dnfrepo.ItemStack {
	stack.RawEntry = append([]byte(nil), stack.RawEntry...)
	if stack.Extra != nil {
		extra := make(map[string]string, len(stack.Extra))
		for key, value := range stack.Extra {
			extra[key] = value
		}
		stack.Extra = extra
	}
	return stack
}

func (o *Owner) clock() time.Time {
	if o != nil && o.now != nil {
		return o.now()
	}
	return time.Now()
}
