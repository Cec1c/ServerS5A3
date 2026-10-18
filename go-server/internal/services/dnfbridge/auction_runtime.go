package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	"longheng.io/server/internal/modules/dnf/dnfenum"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// Current NoPack class-0/op183 handler sub_1D80260 reads exactly
	// {u8 service_type,u8 state}. Type 0 controls the ordinary auction entry;
	// type 1 controls the independent gold-consignment entry. The
	// town UI uses its corresponding class-1 auction route.
	currentAuctionServiceNoticeMsgID   = uint16(183)
	currentAuctionServiceStateOpen     = byte(1)
	currentAuctionServiceTypeCount     = byte(2)
	currentAuctionPayTypeGold          = byte(0)
	currentAuctionPremiumType          = 29
	currentAuctionStandardListingLimit = 5
	currentAuctionPremiumListingLimit  = 30
	currentAuctionStandardDuration     = 24 * time.Hour
	currentAuctionPremiumDuration      = 48 * time.Hour
	currentAuctionRegisterRequestSize  = 37
	// currentAuctionPayTypeConsignment is the mode byte the live client writes
	// into op182 when the request comes from the 金币寄售 (gold consignment)
	// panel rather than the ordinary item auction. Live capture
	// 2026-09-21T09:22Z, packet_log.txt: op182 body `017DEB280000` =
	// mode 1 + item 2681725 (cash/chn_Trade_Gold/gold_1m.stk, 「1百万」) + one
	// reserved zero. Those requests used to fall through the pay==0 gate and
	// answer 153 「因不明原因操作失败」, which is the popup the panel showed.
	currentAuctionPayTypeConsignment = byte(1)
	// currentAuctionGoldAveragePriceRequestSize is that request's body length:
	// {u8 mode=1, u32 goldItemId, u8 reserved}. The item auction sends 8 bytes.
	currentAuctionGoldAveragePriceRequestSize = 6
	// currentAuctionPriceAbsent is what the client writes into a price total
	// the seller left empty. sub_3007EC0 normalizes both controls right before
	// the send: the opening bid becomes -1 when it is 0 or equal to the buyout,
	// and the buyout becomes -1 when it is 0.
	currentAuctionPriceAbsent = int64(-1)
)

// Client-visible auction failure codes, read straight off the auction panel's
// shared dispatcher sub_301E3D0: it computes `code - 7`, refuses anything above
// 0xE4 (so only 7..235 reach the table), indexes the 229-byte table at
// 0x301E940 and jumps through 0x301E8E0. Only 7, 8, 140..158, 214 and 235 have
// a branch; **every other code lands in the silent default at 0x301E8C2 and
// draws nothing at all**. The whole auction used to answer with a single code
// 4, which is below the table's floor - a refused bid or registration looked
// exactly like a dead button. The comments are the dstr text each code renders
// (see docs/client-dstr-zh.txt).
const (
	currentAuctionErrorGoldShort     = byte(144) // 所持金币不足， 将无法进行上架或竞拍
	currentAuctionErrorItemRejected  = byte(145) // 无法上架到拍卖行的物品
	currentAuctionErrorCountTooLarge = byte(146) // 无法上架超出持有数量的物品数
	currentAuctionErrorItemMissing   = byte(148) // 缺少物品
	currentAuctionErrorNoListing     = byte(150) // 拍卖行中不存在该物品
	currentAuctionErrorListingLimit  = byte(151) // 已超过上架数量上限， 需要拍卖行优惠券才能继续上架
	currentAuctionErrorPriceInvalid  = byte(152) // 最低竞拍价或一口价中输入了错误数值
	currentAuctionErrorUnknown       = byte(153) // 因不明原因操作失败
	currentAuctionErrorForbidden     = byte(154) // 您没有权限
	currentAuctionErrorOwnListing    = byte(155) // 无法竞拍自己上架的物品
	currentAuctionErrorPriceMismatch = byte(157) // 竞拍金额不符， 可能存在更高价格的竞拍者
	currentAuctionErrorItemLocked    = byte(214) // 在物品已锁定状态下， 您无法进行当前操作
	// currentAuctionFailureCode is what a refusal we cannot name more precisely
	// sends. It has to stay inside the table above; "因不明原因操作失败" is the
	// client's own wording for exactly that case.
	currentAuctionFailureCode = currentAuctionErrorUnknown
)

// currentAuctionBuyFailureCode maps a buyout refusal onto the code the auction
// panel can actually render.
func currentAuctionBuyFailureCode(err error) byte {
	switch {
	case errors.Is(err, dnfauction.ErrPriceMismatch):
		return currentAuctionErrorPriceInvalid
	case errors.Is(err, dnfauction.ErrBuyerIsSeller):
		return currentAuctionErrorOwnListing
	case errors.Is(err, dnfauction.ErrGoldUnavailable):
		return currentAuctionErrorGoldShort
	case errors.Is(err, dnfauction.ErrListingMissing), errors.Is(err, dnfauction.ErrListingExpired):
		return currentAuctionErrorNoListing
	case errors.Is(err, dnfauction.ErrItemUnavailable):
		return currentAuctionErrorItemMissing
	default:
		return currentAuctionErrorUnknown
	}
}

// currentAuctionBidFailureCode is the same map for the 竞拍 half. A bid that
// does not beat the standing one is the case 157
// (竞拍金额不符， 可能存在更高价格的竞拍者) exists for, and now that bids are
// actually recorded that text is true rather than an invented rival: the
// listing really may already hold a higher bid. A listing registered
// buyout-only has no bid to beat, so that refusal stays on 152
// (最低竞拍价或一口价中输入了错误数值).
func currentAuctionBidFailureCode(err error) byte {
	switch {
	case errors.Is(err, dnfauction.ErrBidTooLow):
		return currentAuctionErrorPriceMismatch
	case errors.Is(err, dnfauction.ErrBiddingNotAllowed):
		return currentAuctionErrorPriceInvalid
	case errors.Is(err, dnfauction.ErrBuyerIsSeller):
		return currentAuctionErrorOwnListing
	case errors.Is(err, dnfauction.ErrGoldUnavailable):
		return currentAuctionErrorGoldShort
	case errors.Is(err, dnfauction.ErrListingMissing), errors.Is(err, dnfauction.ErrListingExpired):
		return currentAuctionErrorNoListing
	case errors.Is(err, dnfauction.ErrItemUnavailable):
		return currentAuctionErrorItemMissing
	default:
		return currentAuctionErrorUnknown
	}
}

// currentAuctionAccountContainerByte is the op183 container byte the current
// client writes when the listed stack lives in the account-scoped crystal/soul
// warehouse (wire list-0 slots 354..365). Character-inventory registrations
// carry 0 instead.
const currentAuctionAccountContainerByte byte = 36

// currentAuctionRejectReason 把一个域层拒绝翻成记录里能读的原因。
//
// 与上面两个 *FailureCode 分开：错误码要落在客户端已有的文案表里，只能是一个
// 0..255 的档；而记录是给运营看的，越具体越好，也不受表长限制。
//
// 同一个错误在全部拍卖行操作里都翻译成同一个词（拍卖行记录里只有这一套
// reason 词汇）：这样"按 reason 统计最近有多少次因为金币不足被拒"一条查询就
// 能覆盖上架/竞拍/一口价三条路径，而不用记住三套写法。packet_log 里
// game-auction-buy-rejected 那条的 reason 是历史口径，保持不变。
func currentAuctionRejectReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, dnfauction.ErrGoldUnavailable):
		return "gold_unavailable"
	case errors.Is(err, dnfauction.ErrItemUnavailable):
		return "item_unavailable"
	case errors.Is(err, dnfauction.ErrPriceMismatch):
		return "price_mismatch"
	case errors.Is(err, dnfauction.ErrBuyerIsSeller):
		return "buyer_is_seller"
	case errors.Is(err, dnfauction.ErrBidTooLow):
		return "bid_too_low"
	case errors.Is(err, dnfauction.ErrBiddingNotAllowed):
		return "bidding_not_allowed"
	case errors.Is(err, dnfauction.ErrListingExpired):
		return "listing_expired"
	case errors.Is(err, dnfauction.ErrListingMissing):
		return "listing_missing"
	case errors.Is(err, dnfauction.ErrNotOwner):
		return "not_owner"
	case errors.Is(err, dnfauction.ErrListingLimit):
		return "listing_limit"
	case errors.Is(err, dnfauction.ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, dnfauction.ErrUnavailable):
		return "auction_unavailable"
	default:
		return "rejected"
	}
}

// currentAuctionUnitPriceOfTotal 把整单金额折算回单价（记录用）。
//
// 客户端每次都是按"整单总价"发金额（竞拍与一口价都一样），而行上存的是单价；
// 记录里两个数都留着，运营才能分辨"单价 75 买 2 个"和"单价 150 买 1 个"。
// 除不尽时向下取整，与 currentAuctionTotalPrice 的乘法互为反向。
func currentAuctionUnitPriceOfTotal(total, count int64) int64 {
	if total <= 0 || count <= 0 {
		return 0
	}
	return total / count
}

type currentAuctionRegisterRequest struct {
	PayType       byte
	ListType      byte
	Slot          int16
	ItemID        int64
	Count         int64
	StartPrice    int64
	InstantPrice  int64
	UnitPrice     int64
	DurationHours uint32
	// AccountOwned records that the client marked the account-scoped
	// crystal/soul warehouse as the container (op183 container byte 36). Those
	// entries are still wire list-0 slots 354..365, so ListType stays 0 and the
	// durable owner is the account inventory.
	AccountOwned bool
}

func decodeCurrentAuctionRegisterRequest(body []byte) (currentAuctionRegisterRequest, error) {
	// Current NoPack sender writes the container byte before the slot:
	// {u8 pay,u8 container,u16 slot,u32 item,i32 count,i32 start,i32 instant,
	//  i32 unit,u32 duration_hours,9 zero reserved bytes}. The active client
	// capture for slot 9/item 33608 is
	// 00 00 09 00 48 83 00 00 01 00 00 00 ...; omitting container shifts every
	// following field and rejects an otherwise valid registration at decode.
	//
	// The container byte is 0 for the character's own list 0 and 36 for the
	// account-scoped crystal/soul warehouse, which is how a seller lists
	// 无色小晶块 and the rest of the cube family: this server keeps those slots
	// account-owned, and the client therefore never sends them as container 0.
	// Any other value names a container this server cannot debit and stays
	// refused.
	if len(body) != currentAuctionRegisterRequestSize || body[0] != currentAuctionPayTypeGold {
		return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	rawSlot := binary.LittleEndian.Uint16(body[2:4])
	if rawSlot > 0x7fff {
		return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	accountOwned := false
	switch body[1] {
	case currentExeInventoryMainListType:
	case currentAuctionAccountContainerByte:
		if !dnfrepo.IsAccountSharedInventorySlot(
			dnfrepo.MainInventoryListType, int16(rawSlot)) {
			return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
		}
		accountOwned = true
	default:
		return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	request := currentAuctionRegisterRequest{
		PayType:       body[0],
		ListType:      currentExeInventoryMainListType,
		AccountOwned:  accountOwned,
		Slot:          int16(rawSlot),
		ItemID:        int64(binary.LittleEndian.Uint32(body[4:8])),
		Count:         int64(int32(binary.LittleEndian.Uint32(body[8:12]))),
		StartPrice:    int64(int32(binary.LittleEndian.Uint32(body[12:16]))),
		InstantPrice:  int64(int32(binary.LittleEndian.Uint32(body[16:20]))),
		UnitPrice:     int64(int32(binary.LittleEndian.Uint32(body[20:24]))),
		DurationHours: binary.LittleEndian.Uint32(body[24:28]),
	}
	for _, value := range body[28:] {
		if value != 0 {
			return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
		}
	}
	if request.Slot < 0 || request.ItemID <= 0 || request.Count <= 0 ||
		(request.DurationHours != 24 && request.DurationHours != 48) {
		return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	// 竞拍价 and 一口价 are independently optional in the client's own encoder.
	// sub_3007BC0 refuses exactly two shapes - both controls empty (text
	// 0xEAE2) and an opening bid above a set buyout (text 0xEAE4) - and lets a
	// bid-only registration through. sub_3007EC0 then sends the empty control
	// as -1 in its total slot, while the trailing per-item slot keeps the raw
	// buyout and so stays 0. A bid-only body is therefore
	// {start=<bid>, instant=-1, unit=0}, which the old "unit price must be
	// positive and equal to instant" gate rejected at decode - that is why
	// 只填竞拍价 could not list anything at all.
	//
	// The current client labels all three price controls "per item". For a
	// stackable registration it therefore sends instant_price == unit_price;
	// only the auction search/purchase projection multiplies by Count. Requiring
	// instant_price to contain that total accepted single items but rejected
	// every captured material stack before the transaction owner was reached.
	hasBuyout := request.InstantPrice != currentAuctionPriceAbsent
	if hasBuyout {
		if request.UnitPrice <= 0 || request.InstantPrice != request.UnitPrice ||
			request.UnitPrice > math.MaxInt32/request.Count {
			return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
		}
	} else if request.UnitPrice != 0 {
		return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	if request.StartPrice != currentAuctionPriceAbsent {
		if request.StartPrice < 0 || request.StartPrice > math.MaxInt32/request.Count ||
			(hasBuyout && request.StartPrice > request.InstantPrice) {
			return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
		}
	}
	if !hasBuyout && request.StartPrice <= 0 {
		return currentAuctionRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	return request, nil
}

func (s *Service) sendCurrentAuctionServicesOpen(session *gameSession) error {
	if s == nil || session == nil {
		return nil
	}
	for serviceType := byte(0); serviceType < currentAuctionServiceTypeCount; serviceType++ {
		bit := byte(1 << serviceType)
		if session.auctionServiceOpenMask&bit != 0 {
			continue
		}
		if err := s.sendGameUpperRawClass(
			session,
			currentAuctionServiceNoticeMsgID,
			buildAuctionServiceNotice(serviceType, currentAuctionServiceStateOpen),
			0,
		); err != nil {
			return err
		}
		session.auctionServiceOpenMask |= bit
	}
	return nil
}

// currentAuctionFailureNeedsPayFeature reports whether an auction failure ack
// must carry the trailing pay-feature byte. The class-1 auction handlers
// (op185 sub_1D17CA0, op188 sub_1D10910) special-case the security/level codes
// 0x69, 0x7B, 0x7E, 0x89, 0xCE, 0xD2 and 0xE8 and return without touching the
// cursor; for every other code they read one more byte and route on it - 0 to
// the gold panel (owner 7), 1 to its companion service (owner 8). Omit it and
// the client routes the refusal on whatever byte follows the packet.
func currentAuctionFailureNeedsPayFeature(opcode uint16, code byte) bool {
	switch code {
	case 0x69, 0x7B, 0x7E, 0x89, 0xCE, 0xD2, 0xE8:
		return false
	}
	switch opcode {
	case uint16(dnfenum.CmdPacketAuctionAskAveragePrice),
		uint16(dnfenum.CmdPacketAuctionRegistItem),
		uint16(dnfenum.CmdPacketAuctionRegistCancel),
		uint16(dnfenum.CmdPacketAuctionBidding),
		uint16(dnfenum.CmdPacketAuctionSearchByItemkey),
		uint16(dnfenum.CmdPacketAuctionMyRegistedItemInfo),
		uint16(dnfenum.CmdPacketAuctionMyBiddingInfo):
		return true
	default:
		return false
	}
}

func (s *Service) handleCurrentAuctionCommand(session *gameSession, opcode uint16, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	s.auctionMu.Lock()
	defer s.auctionMu.Unlock()
	defer s.scheduleCurrentAuctionMaintenanceLocked(30 * time.Second)
	// Settling won listings before anything else is what makes bidding work
	// without a scheduler: the sweep is seller- and bidder-agnostic, so any
	// auction request from anybody closes every listing whose duration ran out
	// under a standing bid. 86JP drives the same transition from a clock; this
	// tree has no auction clock, and an expired listing has to stop being
	// biddable and start being deliverable the moment somebody looks.
	s.settleCurrentAuctionWonListingsLocked(session)
	s.retryCurrentAuctionMailsLocked(session)

	if len(body) > 0 && body[0] == currentAuctionPayTypeConsignment {
		return s.handleCurrentGoldConsignLocked(session, opcode, body)
	}
	switch opcode {
	case uint16(dnfenum.CmdPacketAuctionRegistItem):
		return s.registerCurrentAuctionItemLocked(session, body)
	case uint16(dnfenum.CmdPacketAuctionRegistCancel):
		return s.cancelCurrentAuctionItemLocked(session, body)
	case uint16(dnfenum.CmdPacketAuctionBidding):
		return s.buyCurrentAuctionItemLocked(session, opcode, body, false)
	case uint16(dnfenum.CmdPacketAuctionBuyItemApiece):
		return s.buyCurrentAuctionItemLocked(session, opcode, body, true)
	case uint16(dnfenum.CmdPacketAuctionSearchByItemkey):
		s.rememberCurrentAuctionSearchLocked(session, opcode, body)
		return s.searchCurrentAuctionItemsLocked(session, body)
	case uint16(dnfenum.CmdPacketAuctionSearchByNoitemkey):
		s.rememberCurrentAuctionSearchLocked(session, opcode, body)
		return s.searchCurrentAuctionCategoryLocked(session, body)
	case uint16(dnfenum.CmdPacketAuctionAskAveragePrice):
		return s.sendCurrentAuctionAveragePriceLocked(session, body)
	case uint16(dnfenum.CmdPacketAuctionMyRegistedItemInfo):
		return s.sendCurrentAuctionOwnedItemsLocked(session, body)
	case uint16(dnfenum.CmdPacketAuctionMyBiddingInfo):
		return s.sendCurrentAuctionBiddingItemsLocked(session, body)
	default:
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
}

func (s *Service) currentAuctionOwner() (*dnfauction.Owner, error) {
	repositories, ok := s.repositoryGroup()
	if !ok {
		return nil, dnfauction.ErrUnavailable
	}
	return dnfauction.NewOwner(repositories)
}

// currentAuctionRegisterFailureCode names a registration refusal for the client
// by the stage that produced it. `decode` and `duration` are both "the seller
// typed a value this listing cannot carry", which is what 152 says; the item
// stages have their own texts; everything left is 因不明原因操作失败.
func currentAuctionRegisterFailureCode(stage string, cause error) byte {
	switch stage {
	case "decode", "duration":
		return currentAuctionErrorPriceInvalid
	case "source_mismatch":
		return currentAuctionErrorItemMissing
	case "item_locked":
		return currentAuctionErrorItemLocked
	case "item_policy":
		return currentAuctionErrorItemRejected
	case "count":
		return currentAuctionErrorCountTooLarge
	case "deposit_gold":
		return currentAuctionErrorGoldShort
	}
	switch {
	case errors.Is(cause, dnfauction.ErrListingLimit):
		return currentAuctionErrorListingLimit
	case errors.Is(cause, dnfauction.ErrGoldUnavailable):
		return currentAuctionErrorGoldShort
	case errors.Is(cause, dnfauction.ErrItemUnavailable):
		return currentAuctionErrorItemMissing
	default:
		return currentAuctionErrorUnknown
	}
}

// currentAuctionSourceSlots returns the slot map a registration draws from: the
// seller character's list 0, or the account-scoped crystal/soul warehouse when
// the client marked container 36.
func (s *Service) currentAuctionSourceSlots(
	ctx context.Context,
	repositories dnfrepo.Group,
	session *gameSession,
	request currentAuctionRegisterRequest,
) (map[string]dnfrepo.ItemStack, error) {
	if !request.AccountOwned {
		inventory, found, err := repositories.Inventory.Load(ctx, selectedCharacterIDString(session))
		if err != nil || !found {
			return nil, err
		}
		return inventory.Slots, nil
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return nil, dnfauction.ErrUnavailable
	}
	account, found, err := repositories.AccountInventory.Load(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !found {
		// No warehouse row yet is an empty warehouse, not a broken account: the
		// caller reports it as a missing item.
		return nil, nil
	}
	return account.Slots, nil
}

func (s *Service) registerCurrentAuctionItemLocked(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketAuctionRegistItem)
	// 金币寄售属于独立点券交易，本物品拍卖入口不接受 mode=1。
	if len(body) > 0 && body[0] == currentAuctionPayTypeConsignment {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, currentAuctionFailureCode, 1}, 1)
	}
	var request currentAuctionRegisterRequest
	reject := func(stage string, cause error) error {
		code := currentAuctionRegisterFailureCode(stage, cause)
		fields := []any{
			"stage", stage,
			"body_len", len(body),
			"char_id", selectedCharacterIDString(session),
			"slot", request.Slot,
			"item_id", request.ItemID,
			"count", request.Count,
			"duration_hours", request.DurationHours,
			"account_owned", request.AccountOwned,
			"client_code", code,
		}
		if cause != nil {
			fields = append(fields, "error", cause.Error())
		}
		s.logGameEvent(session, "game-auction-register-rejected", fields...)
		// 被拒绝的上架同样留一条记录（result=rejected + reason=阶段名）。
		// 上架失败以前只在 packet_log 里留下一行 stage=...，而 packet_log
		// 在高负载下会丢行；把它落到记录里，"他为什么挂不上"才有据可查。
		rejected := activityRejectionFields(stage)
		rejected["body_len"] = len(body)
		rejected["item_id"] = request.ItemID
		rejected["item_count"] = request.Count
		rejected["source_slot"] = int(request.Slot)
		rejected["unit_price"] = request.UnitPrice
		rejected["start_price"] = request.StartPrice
		rejected["buyout_price"] = request.InstantPrice
		rejected["duration_hours"] = request.DurationHours
		rejected["client_code"] = code
		s.recordAuction(session, "list", rejected)
		return s.sendGameUpperFailure(session, opcode, code)
	}
	var err error
	request, err = decodeCurrentAuctionRegisterRequest(body)
	if err != nil {
		return reject("decode", err)
	}
	const listType = byte(0)
	slot := request.Slot
	itemID := request.ItemID
	requestedCount := request.Count
	// UnitPrice is the price the listing actually sells at. With a 一口价 that
	// is the buyout; a bid-only listing has none, so its opening bid is the
	// asking price and BuyoutPrice records that there is no buyout at all.
	unitPrice := request.UnitPrice
	buyoutPrice := request.UnitPrice
	if request.InstantPrice == currentAuctionPriceAbsent {
		unitPrice = request.StartPrice
		buyoutPrice = currentAuctionPriceAbsent
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil || repositories.Character == nil ||
		(request.AccountOwned && repositories.AccountInventory == nil) {
		return reject("repository", dnfauction.ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := selectedCharacterIDString(session)
	sourceSlots, err := s.currentAuctionSourceSlots(ctx, repositories, session, request)
	if err != nil {
		return reject("load_inventory", err)
	}
	stack, found := sourceSlots[inventorySlotKey(listType, slot)]
	if !found || stack.ItemID != itemID || stack.Count <= 0 {
		return reject("source_mismatch", dnfauction.ErrItemUnavailable)
	}
	// A locked item is refused by the policy below too, but "无法上架到拍卖行的
	// 物品" would send the seller looking for a trade restriction that is not
	// there. The client has its own text for a lock, so name it.
	if currentDungeonDiscardStackLocked(stack) {
		return reject("item_locked", dnfauction.ErrItemUnavailable)
	}
	expectedBind, allowSealed, eligible := s.currentAuctionRegistrationPolicy(uint32(itemID), stack)
	if !eligible {
		return reject("item_policy", dnfauction.ErrItemUnavailable)
	}
	count := requestedCount
	if count > stack.Count {
		return reject("count", dnfauction.ErrItemUnavailable)
	}
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil || !found {
		return reject("load_character", err)
	}
	listingLimit, maximumDuration, feePercent := s.currentAuctionEntitlementLocked(ctx, session, repositories)
	duration := time.Duration(request.DurationHours) * time.Hour
	if duration > maximumDuration {
		return reject("duration", dnfauction.ErrInvalidRequest)
	}
	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		return reject("owner", err)
	}
	var result dnfauction.Result
	for attempts := 0; attempts < 8; attempts++ {
		listingID, allocateErr := owner.AllocateListingID(ctx)
		if allocateErr != nil {
			return reject("listing_sequence", allocateErr)
		}
		result, err = owner.Register(ctx, dnfauction.RegisterCommand{
			ListingID: listingID, SellerID: characterID, AccountID: strings.TrimSpace(s.accountIDForSession(session)),
			SellerName: character.Name, ListType: listType, SourceSlot: slot, ExpectedID: itemID,
			Count: count, UnitPrice: unitPrice, StartPrice: request.StartPrice, BuyoutPrice: buyoutPrice,
			Deposit: dnfauction.ListingDepositGold, FeePercent: feePercent,
			Duration: duration, ListingLimit: listingLimit,
			ExpectedBind: expectedBind, AllowSealed: allowSealed,
			SourceAccountOwned: request.AccountOwned,
		})
		if !errors.Is(err, dnfauction.ErrListingIDExists) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, dnfauction.ErrGoldUnavailable) {
			// A seller who cannot cover the registration deposit is a routine
			// refusal, not a fault; it must not read as a broken transaction.
			return reject("deposit_gold", err)
		}
		return reject("transaction", err)
	}
	s.logGameEvent(session, "game-auction-register-committed",
		"char_id", characterID,
		"listing_id", result.Listing.ID,
		"slot", slot,
		"item_id", itemID,
		"count", count,
		"unit_price", unitPrice,
		"start_price", request.StartPrice,
		"buyout_price", buyoutPrice,
		"deposit", dnfauction.ListingDepositGold,
		"fee_percent", feePercent,
		"gold_after", result.GoldAfter,
		"duration_hours", request.DurationHours)
	if err := s.sendGameUpperSuccess(session, opcode, buildCurrentAuctionRegisterSuccessBody(result.Listing.ID, false)); err != nil {
		return err
	}
	if err := s.sendCurrentAuctionInventorySlot(session, listType, slot, result.Inventory); err != nil {
		return err
	}
	// The deposit left the wallet inside the same transaction, so the gold row
	// has to be republished or the client keeps showing the pre-listing total.
	if err := s.sendGameUpperRawClass(session, 14, buildCurrentGoldStateBody(result.GoldAfter), 0); err != nil {
		return err
	}
	// 上架记录：一次成功的上架就是一次状态跃迁（物品离开背包、托管押金被扣、
	// 列表里多出一行），所以它只写在这条成功路径上，不写在任何扫描/重试入口。
	fields := map[string]any{
		"listing_id":     result.Listing.ID,
		"item_id":        itemID,
		"item_name":      s.currentAuctionItemDisplayName(itemID),
		"item_count":     count,
		"list_type":      int(listType),
		"source_slot":    int(slot),
		"unit_price":     unitPrice,
		"start_price":    request.StartPrice,
		"buyout_price":   buyoutPrice,
		"deposit_gold":   dnfauction.ListingDepositGold,
		"fee_percent":    feePercent,
		"duration_hours": request.DurationHours,
		"gold_after":     result.GoldAfter,
		"seller_id":      strings.TrimSpace(result.Listing.SellerID),
		"seller_name":    strings.TrimSpace(result.Listing.SellerName),
		"account_owned":  request.AccountOwned,
	}
	if !result.Listing.ExpiresAt.IsZero() {
		fields["expires_at"] = result.Listing.ExpiresAt.UTC().Format(time.RFC3339)
	}
	activityIdentityOverride(fields, strings.TrimSpace(s.accountIDForSession(session)), characterID)
	s.recordAuction(session, "list", fields)
	return nil
}

func buildCurrentAuctionRegisterSuccessBody(listingID uint64, robotSessionBound bool) []byte {
	var response packetWriter
	response.writeByte(currentAuctionPayTypeGold)
	// The stock client consumes only payType. Authenticated current90 robots get
	// a private trailing listing ID so the reconstructed market worker can later
	// buy/cancel its own listing without changing the public client contract.
	if robotSessionBound {
		response.writeUint64(listingID)
	}
	return response.bytes()
}

// currentAuctionEntitlementLocked resolves the three 拍卖行优惠券 effects this
// server can act on when a registration arrives: the active-listing cap, the
// selectable duration, and the sale commission. The coupon's own PVF explain
// text promises 减少使用拍卖行所需的手续费, 可同时上架30个物品 and
// 可选择拍卖期限48小时; the discounted rate itself is an operator setting,
// because that text names no number.
func (s *Service) currentAuctionEntitlementLocked(ctx context.Context, session *gameSession, repositories dnfrepo.Group) (int, time.Duration, int64) {
	if repositories.Account == nil {
		return currentAuctionStandardListingLimit, currentAuctionStandardDuration, dnfauction.SaleFeePercentFor(false)
	}
	account, found, err := repositories.Account.Load(ctx, strings.TrimSpace(s.accountIDForSession(session)))
	if err == nil && found && premium.ExpireAt(account, currentAuctionPremiumType) > time.Now().Unix() {
		return currentAuctionPremiumListingLimit, currentAuctionPremiumDuration, dnfauction.SaleFeePercentFor(true)
	}
	return currentAuctionStandardListingLimit, currentAuctionStandardDuration, dnfauction.SaleFeePercentFor(false)
}

func (s *Service) cancelCurrentAuctionItemLocked(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketAuctionRegistCancel)
	// 金币寄售 cancels on the same opcode and, on the item panel's own shape,
	// with a 9-byte mode-1 body ({u8 mode, i64 listingId}). INFERRED: listing
	// did not work before this change, so the gold cancel has never been
	// captured. See the gold protocol file.
	if len(body) == 9 &&
		body[0] == currentAuctionPayTypeConsignment {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, currentAuctionFailureCode, 1}, 1)
	}
	if len(body) != 9 || body[0] != currentAuctionPayTypeGold {
		rejected := activityRejectionFields("malformed_body")
		rejected["body_len"] = len(body)
		s.recordAuction(session, "cancel", rejected)
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		rejected := activityRejectionFields("auction_unavailable")
		rejected["body_len"] = len(body)
		s.recordAuction(session, "cancel", rejected)
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	listingID := binary.LittleEndian.Uint64(body[1:9])
	result, err := owner.Cancel(ctx, dnfauction.CancelCommand{
		SellerID: selectedCharacterIDString(session), AccountID: strings.TrimSpace(s.accountIDForSession(session)),
		ListingID: listingID,
	})
	if err != nil {
		code := currentAuctionErrorUnknown
		switch {
		case errors.Is(err, dnfauction.ErrListingMissing), errors.Is(err, dnfauction.ErrListingExpired):
			code = currentAuctionErrorNoListing
		case errors.Is(err, dnfauction.ErrNotOwner):
			code = currentAuctionErrorForbidden
		}
		s.logGameEvent(session, "game-auction-cancel-rejected",
			"char_id", selectedCharacterIDString(session), "listing_id", listingID,
			"client_code", code, "error", err.Error())
		rejected := activityRejectionFields(currentAuctionRejectReason(err))
		rejected["listing_id"] = listingID
		rejected["client_code"] = code
		s.recordAuction(session, "cancel", rejected)
		return s.sendGameUpperFailure(session, opcode, code)
	}
	// The ACK goes out before the return mail. That is the order every other
	// command uses - and what the gold consignment cancel already does: the
	// client's op184 handler pops its own toast the moment it consumes the ACK,
	// while the return letter, its mailbox alarm and the 60091 banner are
	// notifications that belong behind it. Keeping them in front of the ACK
	// would also leave the 60091 banner racing an outstanding op184 request,
	// which the class-1 dispatcher is free to consume as part of the pending
	// transition.
	if err := s.sendGameUpperSuccess(session, opcode, []byte{currentAuctionPayTypeGold}); err != nil {
		return err
	}
	if result.Delivery.ID != "" {
		if deliveryErr := s.deliverCurrentAuctionReturnMailLocked(ctx, owner, result.Delivery); deliveryErr != nil {
			// The item is already parked in the durable outbox, so a mail
			// failure defers the delivery instead of losing it: the next
			// pending-delivery sweep sends it.
			s.logGameEvent(session, "game-auction-return-mail-deferred",
				"delivery_id", result.Delivery.ID, "seller_id", result.Delivery.BuyerID,
				"listing_id", result.Delivery.ListingID, "item_id", result.Delivery.Item.ItemID,
				"error", deliveryErr)
		}
	}
	// 下架记录：Cancel 成功就是一次跃迁（挂单行被移除、返还邮件入队、站着的
	// 竞价被释放），所以只写在成功分支上。押金不退还这件事记录里要看得见：
	// 取消上架会没收押金（客户端自己的确认弹窗就这么写），而流拍会退还，
	// 两条路径的 deposit_gold 因此含义相反，字段必须都留着。
	fields := map[string]any{
		"listing_id":   result.Listing.ID,
		"item_id":      result.Listing.Item.ItemID,
		"item_name":    s.currentAuctionItemDisplayName(result.Listing.Item.ItemID),
		"item_count":   result.Listing.Item.Count,
		"list_type":    int(result.Listing.ListType),
		"source_slot":  int(result.Listing.SourceSlot),
		"unit_price":   result.Listing.UnitPrice,
		"start_price":  result.Listing.StartPrice,
		"buyout_price": result.Listing.BuyoutPrice,
		"deposit_gold": result.Listing.Deposit,
		"fee_percent":  result.Listing.FeePercent,
		"seller_id":    strings.TrimSpace(result.Listing.SellerID),
		"seller_name":  strings.TrimSpace(result.Listing.SellerName),
	}
	// 下架时站着的竞价会被释放（releaseListingBid），金额进 Payout 退款行。
	// 只有真的释放过才写这两栏：写一个空的 outbid_id 会让人以为存在一个
	// 空名字的竞拍者。
	if outbidID := strings.TrimSpace(result.Refund.SellerID); outbidID != "" {
		fields["outbid_id"] = outbidID
		fields["outbid_gold"] = result.Refund.Gold
	}
	if !result.Listing.ExpiresAt.IsZero() {
		fields["expires_at"] = result.Listing.ExpiresAt.UTC().Format(time.RFC3339)
	}
	activityIdentityOverride(fields,
		strings.TrimSpace(s.accountIDForSession(session)), selectedCharacterIDString(session))
	s.recordAuction(session, "cancel", fields)
	return nil
}

// rememberCurrentAuctionSearchLocked keeps the last search this session ran so
// a successful bid can replay it, and replayCurrentAuctionSearchLocked re-runs
// it. Both only ever run under auctionMu.
func (s *Service) rememberCurrentAuctionSearchLocked(session *gameSession, opcode uint16, body []byte) {
	if session == nil {
		return
	}
	session.auctionLastSearchOpcode = opcode
	session.auctionLastSearchBody = append([]byte(nil), body...)
}

// replayCurrentAuctionSearchLocked re-sends the list the player is looking at.
// The client's op185 success path erases the row it acted on before it draws
// the toast (sub_30071B0 removes the entry whose listing id matches the
// selection), which is right for a buyout and leaves a bid looking like the
// listing vanished. The response is the ordinary class-1 op186 ack: the
// dispatcher at 0x225C8A0 gates class-1 packets on one global transition flag,
// not on a per-request pending table, so an unsolicited one dispatches exactly
// like the player's own search.
func (s *Service) replayCurrentAuctionSearchLocked(session *gameSession) error {
	if session == nil || len(session.auctionLastSearchBody) == 0 {
		return nil
	}
	body := append([]byte(nil), session.auctionLastSearchBody...)
	switch session.auctionLastSearchOpcode {
	case uint16(dnfenum.CmdPacketAuctionSearchByItemkey):
		return s.searchCurrentAuctionItemsLocked(session, body)
	case uint16(dnfenum.CmdPacketAuctionSearchByNoitemkey):
		return s.searchCurrentAuctionCategoryLocked(session, body)
	default:
		return nil
	}
}

// currentAuctionAmountIsBid decides which half of the bidding opcode a request
// is. Anything short of the buyout total is a 竞拍; a listing with no buyout at
// all can only be bid on. A listing that cannot be read is left to the
// transactional path, which reports the real reason.
func (s *Service) currentAuctionAmountIsBid(ctx context.Context, owner *dnfauction.Owner, listingID uint64, money int64) bool {
	listing, err := owner.LoadListing(ctx, listingID)
	if err != nil {
		return false
	}
	if !listing.HasBuyout() {
		return true
	}
	return money < listing.BuyoutTotal()
}

// bidCurrentAuctionItemLocked records a 竞拍 and answers the same op185 ack a
// buyout does: the client's success branch only refreshes the panel and toasts
// 已完成竞拍, so a recorded bid and a completed buyout look alike to it. The
// bidder's gold left their wallet, so the wallet row has to be republished.
func (s *Service) bidCurrentAuctionItemLocked(ctx context.Context, session *gameSession, opcode uint16, owner *dnfauction.Owner, listingID uint64, expectedItemID, money int64) error {
	result, err := owner.Bid(ctx, dnfauction.BidCommand{
		BidderID: selectedCharacterIDString(session), AccountID: strings.TrimSpace(s.accountIDForSession(session)),
		ListingID: listingID, ExpectedItemID: expectedItemID, Money: money,
	})
	if err != nil {
		code := currentAuctionBidFailureCode(err)
		s.logGameEvent(session, "game-auction-bid-rejected",
			"char_id", selectedCharacterIDString(session), "listing_id", listingID,
			"item_id", expectedItemID, "money", money, "client_code", code, "error", err.Error())
		rejected := activityRejectionFields(currentAuctionRejectReason(err))
		rejected["listing_id"] = listingID
		rejected["item_id"] = expectedItemID
		rejected["bid_total"] = money
		rejected["client_code"] = code
		s.recordAuction(session, "bid", rejected)
		return s.sendGameUpperFailure(session, opcode, code)
	}
	// The outbid player's gold is already parked in the durable refund row, so
	// a failure here defers the payment instead of losing it.
	if result.Refund.ID != "" {
		if deliveryErr := s.settleCurrentAuctionPayoutLocked(ctx, owner, result.Refund); deliveryErr != nil {
			s.logGameEvent(session, "game-auction-bid-refund-deferred",
				"refund_id", result.Refund.ID, "bidder_id", result.Refund.SellerID,
				"listing_id", result.Refund.ListingID, "gold", result.Refund.Gold, "error", deliveryErr)
		}
	}
	s.logGameEvent(session, "game-auction-bid-accepted",
		"char_id", selectedCharacterIDString(session), "listing_id", listingID,
		"item_id", expectedItemID, "bid_total", money,
		"outbid_id", result.Refund.SellerID, "outbid_gold", result.Refund.Gold,
		"gold_after", result.GoldAfter)
	if err := s.sendGameUpperSuccess(session, opcode, []byte{currentAuctionPayTypeGold}); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, 14, buildCurrentGoldStateBody(result.GoldAfter), 0); err != nil {
		return err
	}
	// 出价记录：Bid 成功就是一次跃迁（新竞价写入挂单行、出价方金币进托管、
	// 被超越者的钱进退款行），所以只写在成功分支上——每个拍卖命令开头的那次
	// 结算扫描不会走到这里，重复执行同一条命令也只会因为 ErrBidTooLow 被拒。
	fields := map[string]any{
		"listing_id":   result.Listing.ID,
		"item_id":      result.Listing.Item.ItemID,
		"item_name":    s.currentAuctionItemDisplayName(result.Listing.Item.ItemID),
		"item_count":   result.Listing.Item.Count,
		"list_type":    int(result.Listing.ListType),
		"source_slot":  int(result.Listing.SourceSlot),
		"unit_price":   result.Listing.UnitPrice,
		"start_price":  result.Listing.StartPrice,
		"buyout_price": result.Listing.BuyoutPrice,
		"deposit_gold": result.Listing.Deposit,
		"fee_percent":  result.Listing.FeePercent,
		"bid_total":    money,
		// 行上存的是单价，客户端发的是整单总价，两个都留一份。
		"bid_unit_price": currentAuctionUnitPriceOfTotal(money, result.Listing.Item.Count),
		"gold_after":     result.GoldAfter,
		"seller_id":      strings.TrimSpace(result.Listing.SellerID),
		"seller_name":    strings.TrimSpace(result.Listing.SellerName),
		"bidder_id":      strings.TrimSpace(result.Listing.BidderID),
	}
	// 被超越的那位：钱已经从托管退回（钱包直充或邮件），金额一并记下。
	if outbidID := strings.TrimSpace(result.Refund.SellerID); outbidID != "" {
		fields["outbid_id"] = outbidID
		fields["outbid_gold"] = result.Refund.Gold
	}
	if !result.Listing.ExpiresAt.IsZero() {
		fields["expires_at"] = result.Listing.ExpiresAt.UTC().Format(time.RFC3339)
	}
	activityIdentityOverride(fields,
		strings.TrimSpace(s.accountIDForSession(session)), selectedCharacterIDString(session))
	s.recordAuction(session, "bid", fields)
	// The client drops the row it just bid on, so hand the list back with the
	// new current price on it. Without this the panel reads as empty until the
	// player searches again.
	return s.replayCurrentAuctionSearchLocked(session)
}

func (s *Service) buyCurrentAuctionItemLocked(session *gameSession, opcode uint16, body []byte, apiece bool) error {
	// 金币寄售 buys on op185 with a compact mode-1 body. INFERRED: it has never
	// been captured, because listing did not work before this change. The item
	// panel's op185 is 30 bytes and mode 0, so the two cannot collide, and the
	// gold decoder insists on its own 13/17-byte length as well.
	//
	// op334 is deliberately NOT diverted: the item body there carries no mode
	// byte at all (33 bytes beginning with the money field), so a gold variant
	// of op334, if one exists, is still unknown and must stay on the item path
	// until a capture shows its shape.
	if !apiece && len(body) > 0 && body[0] == currentAuctionPayTypeConsignment {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, currentAuctionFailureCode, 1}, 1)
	}
	var money int64
	var count int64 = 1
	var listingID uint64
	var expectedItemID int64
	if apiece {
		if len(body) != 33 {
			return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
		}
		money = int64(int32(binary.LittleEndian.Uint32(body[0:4])))
		count = int64(int32(binary.LittleEndian.Uint32(body[4:8])))
		listingID = binary.LittleEndian.Uint64(body[8:16])
		expectedItemID = int64(binary.LittleEndian.Uint32(body[29:33]))
	} else {
		if len(body) != 30 || body[0] != currentAuctionPayTypeGold {
			return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
		}
		money = int64(int32(binary.LittleEndian.Uint32(body[1:5])))
		listingID = binary.LittleEndian.Uint64(body[5:13])
		expectedItemID = int64(binary.LittleEndian.Uint32(body[26:30]))
	}
	if money <= 0 || (apiece && count <= 0) || listingID == 0 || expectedItemID <= 0 {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	// The bidding opcode carries one amount and no verb: the client asks for a
	// buyout by sending exactly the buyout total and for a 竞拍 by sending
	// anything less. 86JP's AuctionBidService.TryBid draws the line in the same
	// place from the other side (`bidAmount >= InstantPrice` is not a bid).
	// Only the whole-listing form can bid; op334 is per-piece buyout only.
	if !apiece && s.currentAuctionAmountIsBid(ctx, owner, listingID, money) {
		return s.bidCurrentAuctionItemLocked(ctx, session, opcode, owner, listingID, expectedItemID, money)
	}
	result, err := owner.Buy(ctx, dnfauction.BuyCommand{
		BuyerID: selectedCharacterIDString(session), AccountID: strings.TrimSpace(s.accountIDForSession(session)),
		ListingID: listingID, ExpectedItemID: expectedItemID, Count: count, Money: money, WholeListing: !apiece,
	})
	if err != nil {
		// Naming the refusal keeps a player who cannot pay apart from one whose
		// amount does not match and from a listing that is simply gone. A
		// sub-buyout amount never reaches here any more: it is routed to the
		// bid path above.
		event, reason := "game-auction-buy-rejected", "transaction"
		switch {
		case errors.Is(err, dnfauction.ErrBuyerIsSeller):
			reason = "buyer_is_seller"
		case errors.Is(err, dnfauction.ErrPriceMismatch):
			reason = "price_mismatch"
		case errors.Is(err, dnfauction.ErrGoldUnavailable):
			reason = "buyer_cannot_afford"
		case errors.Is(err, dnfauction.ErrListingExpired):
			reason = "listing_expired"
		case errors.Is(err, dnfauction.ErrListingMissing):
			reason = "listing_missing"
		}
		code := currentAuctionBuyFailureCode(err)
		s.logGameEvent(session, event,
			"char_id", selectedCharacterIDString(session),
			"listing_id", listingID,
			"item_id", expectedItemID,
			"count", count,
			"whole_listing", !apiece,
			"money", money,
			"reason", reason,
			"client_code", code,
			"error", err.Error())
		// 记录里的 reason 用统一词汇表（currentAuctionRejectReason），
		// 上面 logGameEvent 的那套是本条日志的历史口径，保持不动。
		rejected := activityRejectionFields(currentAuctionRejectReason(err))
		rejected["listing_id"] = listingID
		rejected["item_id"] = expectedItemID
		rejected["item_count"] = count
		rejected["sale_price"] = money
		rejected["whole_listing"] = !apiece
		rejected["client_code"] = code
		s.recordAuction(session, "buyout", rejected)
		return s.sendGameUpperFailure(session, opcode, code)
	}
	if result.Delivery.ID != "" {
		if deliveryErr := s.deliverCurrentAuctionBuyerMailLocked(ctx, owner, result.Delivery); deliveryErr != nil {
			s.logGameEvent(session, "game-auction-buyer-mail-deferred", "delivery_id", result.Delivery.ID, "buyer_id", result.Delivery.BuyerID, "item_id", result.Delivery.Item.ItemID, "count", result.Delivery.Item.Count, "error", deliveryErr)
		}
	}
	// 卖家收益的邮件是否真的发出去了，会写进收益记录：这一行是在成交那一刻
	// 写的（Payout 行由 Buy 当场新建），而信可能延后，记录不能默认它已经送达。
	payoutMailed := false
	if result.Payout.ID != "" {
		if deliveryErr := s.deliverCurrentAuctionPayoutMailLocked(ctx, owner, result.Payout); deliveryErr != nil {
			s.logGameEvent(session, "game-auction-payout-mail-deferred", "payout_id", result.Payout.ID, "seller_id", result.Payout.SellerID, "gold", result.Payout.Gold, "error", deliveryErr)
		} else {
			payoutMailed = true
		}
	}
	// A buyout over somebody else's standing bid releases that bid.
	if result.Refund.ID != "" {
		if deliveryErr := s.settleCurrentAuctionPayoutLocked(ctx, owner, result.Refund); deliveryErr != nil {
			s.logGameEvent(session, "game-auction-bid-refund-deferred", "refund_id", result.Refund.ID, "bidder_id", result.Refund.SellerID, "listing_id", result.Refund.ListingID, "gold", result.Refund.Gold, "error", deliveryErr)
		}
	}
	var response packetWriter
	if !apiece {
		response.writeByte(currentAuctionPayTypeGold)
	}
	if err := s.sendGameUpperSuccess(session, opcode, response.bytes()); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, 14, buildCurrentGoldStateBody(result.GoldAfter), 0); err != nil {
		return err
	}
	// 一口价成交记录：Buy 成功就是一次跃迁（挂单行被扣、买家金币进托管、
	// 物品进退货行、卖家收益进收益行）。它只写在这条成功路径上，重复发同一个
	// op 只会因为挂单已不存在被拒，不会走到这里。
	buyout := map[string]any{
		"listing_id":    result.Listing.ID,
		"item_id":       result.Listing.Item.ItemID,
		"item_name":     s.currentAuctionItemDisplayName(result.Listing.Item.ItemID),
		"item_count":    result.Listing.Item.Count,
		"list_type":     int(result.Listing.ListType),
		"source_slot":   int(result.Listing.SourceSlot),
		"unit_price":    result.Listing.UnitPrice,
		"start_price":   result.Listing.StartPrice,
		"buyout_price":  result.Listing.BuyoutPrice,
		"deposit_gold":  result.Listing.Deposit,
		"fee_percent":   result.Listing.FeePercent,
		"sale_price":    money,
		"gold_after":    result.GoldAfter,
		"buyout":        true,
		"whole_listing": !apiece,
		"seller_id":     strings.TrimSpace(result.Listing.SellerID),
		"seller_name":   strings.TrimSpace(result.Listing.SellerName),
		"buyer_id":      strings.TrimSpace(result.Delivery.BuyerID),
		"buyer_name":    s.activityCharacterName(session),
	}
	// 卖家实得与手续费来自 Payout 行上冻结的成交明细（HasStatement），
	// 拿不到就留空 —— 用 FeePercent 现算会与真实付款对不上（旧挂单费率不同）。
	if result.Payout.HasStatement {
		buyout["fee_gold"] = result.Payout.Statement.FeeGold
		buyout["seller_proceeds"] = result.Payout.Gold
	} else if result.Payout.ID != "" {
		buyout["seller_proceeds"] = result.Payout.Gold
	}
	if outbidID := strings.TrimSpace(result.Refund.SellerID); outbidID != "" {
		buyout["outbid_id"] = outbidID
		buyout["outbid_gold"] = result.Refund.Gold
	}
	if !result.Listing.ExpiresAt.IsZero() {
		buyout["expires_at"] = result.Listing.ExpiresAt.UTC().Format(time.RFC3339)
	}
	activityIdentityOverride(buyout,
		strings.TrimSpace(s.accountIDForSession(session)), selectedCharacterIDString(session))
	s.recordAuction(session, "buyout", buyout)
	// 卖家收益记录：Payout 行是 Buy 当场新建的（一行一单，重跑同一个扫描不会
	// 再产出同一行），所以"行新建的那一次"就是这里。延后重发的那些轮次
	// （retryCurrentAuctionMailsLocked）拿到的都是老行，一律不记。
	if result.Payout.ID != "" {
		s.recordAuction(session, "payout", auctionPayoutRecordFields(s, result.Payout, payoutMailed))
	}
	return nil
}

// auctionPayoutRecordFields 组装一条"卖家收益"记录。
//
// 两个入口共用：一口价成交（Buy 返回的新 Payout 行）与竞拍成交
// （SettleWonListings 返回的每一条 Settlement 里的 Payout 行）。两处的钱都
// 一样从托管钱包走到卖家手里（成交款 + 保证金 - 手续费），字段口径必须一致。
//
// delivered 记的是"这一轮信发出去了没有"：Payout 行在成交那一刻就已经落库
// （钱确实从托管划走了），而邮件可能因为任何原因延后 —— 记录里如实写出来，
// 免得运营把"已划账但信还没发"读成"信已经收到了"。
func auctionPayoutRecordFields(s *Service, payout dnfauction.Payout, mailed bool) map[string]any {
	fields := map[string]any{
		"listing_id":      payout.ListingID,
		"item_id":         payout.ItemID,
		"item_name":       s.currentAuctionItemDisplayName(payout.ItemID),
		"item_count":      payout.SoldCount,
		"payout_id":       payout.ID,
		"seller_id":       strings.TrimSpace(payout.SellerID),
		"seller_name":     strings.TrimSpace(payout.SellerName),
		"seller_proceeds": payout.Gold,
		"refund":          payout.Refund,
		"delivered":       mailed,
	}
	if payout.HasStatement {
		fields["sale_price"] = payout.Statement.GrossGold
		fields["fee_gold"] = payout.Statement.FeeGold
		fields["deposit_gold"] = payout.Statement.DepositGold
	}
	return fields
}

func (s *Service) searchCurrentAuctionItemsLocked(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketAuctionSearchByItemkey)
	// 金币寄售 shares op186 with the item auction and reuses its page/count/
	// itemId layout, but sets mode 1 and omits the 8-byte detail tail. Live
	// capture 2026-09-21T09:35:47Z, packet_log.txt: op186 body
	// `0100000000001F01A59C7DEB2800` = mode 1 + page 0 + count 1 +
	// category 40101 + item 2681725 (「1百万」), 14 bytes, no tail. Only that
	// mode byte is diverted, so every item-panel search keeps the path below.
	if len(body) > 0 && body[0] == currentAuctionPayTypeConsignment {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, currentAuctionFailureCode, 1}, 1)
	}
	page, itemIDs, ok := decodeCurrentAuctionSearchItemIDs(body)
	if !ok {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	listings, err := owner.Search(ctx, itemIDs)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	bodyOut := buildCurrentAuctionSearchBodyPage(listings, s.currentAuctionItemEligible, page, 100)
	return s.sendGameUpperSuccess(session, opcode, bodyOut)
}

// searchCurrentAuctionCategoryLocked answers the 拍卖行 "分类搜索" page: the
// browse-by-tree-node request the player drives by clicking a category in the
// left panel.
//
// The body is the fixed 20-byte request NoPack.exe sends, and the selected tree
// node's u16 code sits at body[5:7]; the rest of the request is carried but not
// interpreted (see currentAuctionCategoryRequestCode). The 金币寄售 panel reuses
// this opcode with mode 1 and is diverted to searchCurrentGoldConsignCategoryLocked
// before any item-auction parsing happens.
func (s *Service) searchCurrentAuctionCategoryLocked(session *gameSession, body []byte) error {
	responseOpcode := uint16(dnfenum.CmdPacketAuctionSearchByItemkey)
	// 金币寄售 has its own 金币分类 request on the same opcode. Its exact body
	// is uncaptured (see the gold protocol file); only mode 1 is diverted, and
	// the reply keeps the item panel's convention of answering on op186.
	if len(body) > 0 && body[0] == currentAuctionPayTypeConsignment {
		return s.sendGameUpperRawClass(session, responseOpcode, []byte{0, currentAuctionFailureCode, 1}, 1)
	}
	// Current NoPack.exe sends a fixed 20-byte category/filter request. Live
	// captures 2026-09-27 (packet_log.txt, conn game-000168/game-000127): the
	// selected tree node's u16 code sits at body[5:7] - 10000 武器, 11000 防具,
	// 12000 首饰, 32000 特殊装备, and a leaf such as 10103 for
	// 武器 > 鬼剑士/黑暗武士/守护者 > 太刀. Until this pass every one of them was
	// answered with the same whole-market snapshot, which is why a listed
	// bracelet appeared in every category. The category is now honoured through
	// currentAuctionCategoryEligible; an unknown code keeps the old no-filter
	// behaviour so a client this build has never seen still gets a list.
	if len(body) != 20 || body[0] != currentAuctionPayTypeGold {
		return s.sendGameUpperFailure(session, responseOpcode, currentAuctionFailureCode)
	}
	page := binary.LittleEndian.Uint32(body[1:5])
	category := currentAuctionCategoryRequestCode(body)
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return s.sendGameUpperFailure(session, responseOpcode, currentAuctionFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	listings, err := owner.Search(ctx, nil)
	if err != nil {
		return s.sendGameUpperFailure(session, responseOpcode, currentAuctionFailureCode)
	}
	eligible := func(itemID uint32) bool {
		return s.currentAuctionItemEligible(itemID) && s.currentAuctionCategoryEligible(category, itemID)
	}
	bodyOut := buildCurrentAuctionSearchBodyPage(listings, eligible, page, 100)
	// A code the client sends but this build cannot place is worth a log line: it is
	// either a newer client tree or a node whose membership rule is still unproven.
	if _, found := currentAuctionCategoryNodeFor(category); !found {
		s.logGameEvent(session, "game-auction-category-unmapped", "category", category, "page", page)
	} else if currentAuctionCategoryIsUnpinned(category) {
		s.logGameEvent(session, "game-auction-category-unpinned", "category", category, "page", page)
	}
	return s.sendGameUpperSuccess(session, responseOpcode, bodyOut)
}

func (s *Service) sendCurrentAuctionAveragePriceLocked(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketAuctionAskAveragePrice)
	// 金币寄售 shares op182 with the item auction but is a different request
	// shape: 6 bytes with mode 1, against 8 bytes with mode 0. Only that exact
	// combination is diverted here, so every request the item panel can produce
	// still reaches the code below byte for byte.
	if len(body) == currentAuctionGoldAveragePriceRequestSize &&
		body[0] == currentAuctionPayTypeConsignment {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, currentAuctionFailureCode, 1}, 1)
	}
	if len(body) != 8 || body[0] != currentAuctionPayTypeGold {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	// Live current-client request: pay@0, itemID@1..4, reserved@5..7.
	// Captures for items 24006 and 10081 both use this exact offset.
	itemID := binary.LittleEndian.Uint32(body[1:5])
	if itemID == 0 {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	listings, err := owner.Search(ctx, []uint32{itemID})
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	// The current reader consumes pay + one scalar + an auxiliary count,
	// followed by SIX unit prices and SIX matching quantities. Those twelve
	// slots are not history: the 上架 dialog's 「拍卖行最低价(以一口价为准)」 list is
	// drawn straight from them.
	//
	//	FUN_01d1d720 (class-1 op182) reads {route u8, u32 a, u32 b, u32 x6, u32 x6}
	//	    then calls FUN_0300e2f0(a, prices, counts) for route 0
	//	    (-> FUN_03007d00(a) + FUN_0300a520(prices, counts)).
	//	FUN_0300b190 is that list's row painter. It walks the two parallel int
	//	    vectors the filler stored on the panel, steps 0xf per row, and prints
	//	    each pair as dstr 46422 「%d个」 plus dstr 46252+60160 「每个 %s 金币」;
	//	    a slot whose price or quantity is < 1 prints dstr 46450 「暂无拍卖品」.
	//
	// Live 2026-09-21T11:43Z, one listing of 3037 x2000 at unit price 100: the
	// six slots were all the same average and the six counts all the same stack,
	// so the dialog showed SIX identical rows for a market that had exactly one
	// offer. The twelve slots now carry the real ladder instead.
	var count, total int64
	for _, listing := range listings {
		if listing.Item.Count <= 0 || listing.UnitPrice <= 0 || listing.UnitPrice > math.MaxInt64/listing.Item.Count {
			continue
		}
		listingTotal := listing.UnitPrice * listing.Item.Count
		if total > math.MaxInt64-listingTotal {
			continue
		}
		count += listing.Item.Count
		total += listingTotal
	}
	var average uint32
	if count > 0 {
		average = currentAuctionUint32(total / count)
	} else {
		// The native registration dialog does not complete its price controls
		// when the first scalar is zero. With no live listings, use the active
		// PVF's own positive item price as a reference value; the ladder stays
		// empty, so this is not fabricated market history. This also makes a
		// brand-new tradeable item listable.
		average = s.currentAuctionPVFReferencePrice(itemID)
	}
	// 实际成交均价优先；右侧六档仍显示真实在售报价，不当作历史记录。
	if settledAverage, samples, err := owner.RecentSaleAverage(ctx, int64(itemID)); err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	} else if samples > 0 {
		average = currentAuctionUint32(settledAverage)
	}
	ladder := currentAuctionPriceLadder(listings, currentAuctionPriceLadderSlots)
	var response packetWriter
	response.writeByte(currentAuctionPayTypeGold)
	response.writeUint32(average)
	response.writeUint32(currentAuctionUint32(count))
	for index := 0; index < currentAuctionPriceLadderSlots; index++ {
		response.writeUint32(ladder[index].unitPrice)
	}
	for index := 0; index < currentAuctionPriceLadderSlots; index++ {
		response.writeUint32(ladder[index].quantity)
	}
	return s.sendGameUpperSuccess(session, opcode, response.bytes())
}

// currentAuctionPriceLadderSlots is how many (unit price, quantity) rungs the
// client's op182 reader always takes: two fixed groups of six.
const currentAuctionPriceLadderSlots = 6

// currentAuctionPriceRung is one row of the 上架 dialog's price list.
type currentAuctionPriceRung struct {
	unitPrice uint32
	quantity  uint32
}

// currentAuctionPriceLadder is the cheapest-first view of what is actually on
// sale for one item: one rung per distinct unit price, with the total units
// offered at it. Rungs the market has no data for stay (0, 0), which the client
// renders as 「暂无拍卖品」 - the honest answer for a thin market, and the reason
// a single listing shows a single row instead of six copies of itself.
//
// Listings sharing a price are merged rather than repeated, because the list is
// labelled 最低价 (the lowest prices), not "the sellers".
func currentAuctionPriceLadder(listings []dnfauction.Listing, slots int) []currentAuctionPriceRung {
	ladder := make([]currentAuctionPriceRung, slots)
	if slots <= 0 {
		return ladder
	}
	byPrice := make(map[int64]int64, len(listings))
	for _, listing := range listings {
		if listing.UnitPrice <= 0 || listing.Item.Count <= 0 {
			continue
		}
		byPrice[listing.UnitPrice] += listing.Item.Count
	}
	prices := make([]int64, 0, len(byPrice))
	for price := range byPrice {
		prices = append(prices, price)
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i] < prices[j] })
	for index, price := range prices {
		if index >= slots {
			break
		}
		ladder[index] = currentAuctionPriceRung{
			unitPrice: currentAuctionUint32(price),
			quantity:  currentAuctionUint32(byPrice[price]),
		}
	}
	return ladder
}

// sendCurrentAuctionGoldAveragePriceLocked now lives in
// auction_gold_consignment.go, where it reports the ledger's real rolling
// average instead of a hardcoded zero. Its request size constant stays here
// because the op182 branch above is part of the item handler.

func (s *Service) currentAuctionPVFReferencePrice(itemID uint32) uint32 {
	if s == nil || itemID == 0 {
		return 0
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return 0
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil ||
		(definition.Kind != dungeonDropItemStackable && definition.Kind != dungeonDropItemEquipment) ||
		!currentAuctionTradeable(definition) ||
		(definition.AuctionRuleFound && !definition.AuctionAllowed) {
		return 0
	}
	if definition.PriceFound && definition.Price > 0 {
		return currentAuctionUint32(definition.Price)
	}
	// Some current-PVF tradeable enchant beads and materials intentionally
	// carry no shop price. The current auction dialog treats a zero first
	// scalar as "price discovery still pending" even when all six history
	// counts are zero. Use the protocol's smallest valid unit price only as the
	// no-history control bound; it is not emitted as a historical sale.
	switch definition.Kind {
	case dungeonDropItemStackable:
		return 1
	case dungeonDropItemEquipment:
		switch definition.Rarity {
		case 0, 1, 2, 6:
			return 1
		}
	}
	return 0
}

func decodeCurrentAuctionSearchBody(body []byte, mode byte) (uint32, []uint32, bool) {
	if len(body) < 10 || body[0] != mode {
		return 0, nil, false
	}
	page := binary.LittleEndian.Uint32(body[1:5])
	count := int(body[7])
	end := 10 + count*4
	if count == 0 || end > len(body) || len(body)-end > 8 {
		return 0, nil, false
	}
	items := make([]uint32, count)
	for index := range items {
		items[index] = binary.LittleEndian.Uint32(body[10+index*4 : 14+index*4])
		if items[index] == 0 {
			return 0, nil, false
		}
	}
	return page, items, true
}

// decodeCurrentAuctionSearchItemIDs is the item-auction entry point for the
// shape above. It keeps its own name and signature so every item call site is
// untouched; the gold panel calls decodeCurrentAuctionSearchBody with mode 1.
func decodeCurrentAuctionSearchItemIDs(body []byte) (uint32, []uint32, bool) {
	return decodeCurrentAuctionSearchBody(body, currentAuctionPayTypeGold)
}

func buildCurrentAuctionSearchBody(listings []dnfauction.Listing, stackable func(uint32) bool) []byte {
	return buildCurrentAuctionSearchBodyPage(listings, stackable, 0, math.MaxUint16)
}

func buildCurrentAuctionSearchBodyPage(listings []dnfauction.Listing, eligible func(uint32) bool, page uint32, pageSize int) []byte {
	rows := make([]dnfauction.Listing, 0, len(listings))
	for _, listing := range listings {
		if listing.Item.ItemID <= 0 || listing.Item.ItemID > math.MaxUint32 || eligible == nil || !eligible(uint32(listing.Item.ItemID)) {
			continue
		}
		rows = append(rows, listing)
	}
	total := len(rows)
	if pageSize <= 0 || pageSize > math.MaxUint16 {
		pageSize = math.MaxUint16
	}
	start64 := uint64(page) * uint64(pageSize)
	if start64 >= uint64(len(rows)) {
		rows = nil
	} else {
		start := int(start64)
		end := min(len(rows), start+pageSize)
		rows = rows[start:end]
	}
	var response packetWriter
	response.writeByte(currentAuctionPayTypeGold)
	response.writeUint32(uint32(min(total, math.MaxUint32)))
	response.writeUint16(uint16(len(rows)))
	for _, listing := range rows {
		response.writeBytes(buildCurrentAuctionSearchRow(listing))
	}
	return response.bytes()
}

func (s *Service) currentAuctionStackableItem(itemID uint32) bool {
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return false
	}
	definition, err := catalog.ResolveItem(itemID)
	return err == nil && definition.Kind == dungeonDropItemStackable
}

func (s *Service) currentAuctionItemEligible(itemID uint32) bool {
	if s != nil && s.auctionItemEligibility != nil {
		return s.auctionItemEligibility(itemID)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return false
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil || (definition.Kind != dungeonDropItemStackable && definition.Kind != dungeonDropItemEquipment) ||
		!currentAuctionTradeable(definition) {
		return false
	}
	return !definition.AuctionRuleFound || definition.AuctionAllowed
}

// currentAuctionRegistrationPolicy applies the native market categories at
// the source-row boundary. The `[attach type]` whitelist is shared with the
// private store and the dungeon discard path (only `[free]` and `[sealing]`),
// stackables must be unbound, `[sealing]` equipment must be sealed, and rarity
// plays no part. The escrow owner rechecks ExpectedBind transactionally.
func (s *Service) currentAuctionRegistrationPolicy(itemID uint32, stack dnfrepo.ItemStack) (expectedBind bool, allowSealed bool, eligible bool) {
	if s == nil || itemID == 0 || stack.ItemID != int64(itemID) || stack.Count <= 0 ||
		currentDungeonDiscardStackLocked(stack) || currentDungeonDiscardStackExpired(stack, s.gameplayNow().UTC()) {
		return false, false, false
	}
	if s.auctionItemEligibility != nil {
		return stack.Bind, false, s.auctionItemEligibility(itemID) && !stack.Bind
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return false, false, false
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		return false, false, false
	}
	return currentAuctionRegistrationPolicyForDefinition(definition, stack)
}

func currentAuctionRegistrationPolicyForDefinition(definition dungeonDropItemDefinition, stack dnfrepo.ItemStack) (expectedBind bool, allowSealed bool, eligible bool) {
	if stack.ItemID != int64(definition.ItemID) || stack.Count <= 0 ||
		!currentAuctionTradeable(definition) ||
		(definition.AuctionRuleFound && !definition.AuctionAllowed) {
		return false, false, false
	}
	switch definition.Kind {
	case dungeonDropItemStackable:
		return stack.Bind, false, !stack.Bind
	case dungeonDropItemEquipment:
		if stack.Count != 1 {
			return false, false, false
		}
		// `[attach type]` 白名单已在上面判过；`[sealing]` 再要求封装态，
		// `[free]` 不看封装状态。品级 [rarity] 不参与判断。
		if currentAuctionSealRequired(definition) && !currentAuctionSealed(stack) {
			return false, false, false
		}
		// ExpectedBind/AllowSealed 冻结的是 escrow owner 事务里回查的 stack.Bind
		// （owner.go: `stack.Bind != cmd.ExpectedBind` 与 `stack.Bind && !cmd.AllowSealed`），
		// 所以必须回传 Bind 本身，不能拿封装位顶替 —— 否则「已封装但
		// bind_flag=0」的装备会在托管那一步被 ErrItemUnavailable 打回。
		return stack.Bind, stack.Bind, true
	default:
		return false, false, false
	}
}

// buildCurrentAuctionSearchRow renders one 0xA8-byte op186 row. Its three price
// slots are, in order, the current bid total, the buyout total and the buyout
// unit price - see currentAuctionBidTotalPrice for why the first two are not
// the same value.
func buildCurrentAuctionSearchRow(listing dnfauction.Listing) []byte {
	row := make([]byte, 0xA8)
	binary.LittleEndian.PutUint64(row[0x00:0x08], listing.ID)
	binary.LittleEndian.PutUint32(row[0x09:0x0D], currentAuctionBidTotalPrice(listing))
	binary.LittleEndian.PutUint32(row[0x0D:0x11], currentAuctionBuyoutTotalPrice(listing))
	binary.LittleEndian.PutUint32(row[0x11:0x15], currentAuctionBuyoutUnitPrice(listing))
	writeCurrentAuctionName(row[0x15:0x2A], listing.SellerName)
	row[0x2A] = currentAuctionRemainingHours(listing, time.Now().UTC())
	copy(row[0x2B:0x7E], buildCurrentAuctionItem83(listing.Item))
	// row[0xA0] is the seller-warning tier, not a duration: the current EXE
	// row painter (sub_2076540) feeds it to sub_20743D0, which renders text
	// 21671/21672/21673 - 注意！赖皮小人！/无耻之徒！/全民公敌！ - as a red
	// second line under the item name for 100/200/300, and asserts below 100.
	// We have no such judgement to publish, so the slot stays 0 and the client
	// skips that line. 期限 is row[0x2A], written above.
	return row
}

func buildCurrentAuctionOwnedBody(listings []dnfauction.Listing, sellerID string, stackable func(uint32) bool) []byte {
	rows := make([]dnfauction.Listing, 0, len(listings))
	for _, listing := range listings {
		if listing.SellerID != sellerID || listing.Item.ItemID <= 0 || listing.Item.ItemID > math.MaxUint32 || stackable == nil || !stackable(uint32(listing.Item.ItemID)) {
			continue
		}
		rows = append(rows, listing)
		if len(rows) == math.MaxUint8 {
			break
		}
	}
	var response packetWriter
	response.writeByte(currentAuctionPayTypeGold)
	response.writeByte(byte(len(rows)))
	now := time.Now().UTC()
	for _, listing := range rows {
		// Same three price slots as an op186 row, in the same order, only with
		// the unit price moved to the end of the entry.
		row := make([]byte, 0x97)
		binary.LittleEndian.PutUint64(row[0x00:0x08], listing.ID)
		binary.LittleEndian.PutUint32(row[0x08:0x0C], currentAuctionBidTotalPrice(listing))
		binary.LittleEndian.PutUint32(row[0x0C:0x10], currentAuctionBuyoutTotalPrice(listing))
		writeCurrentAuctionName(row[0x10:0x1D], listing.SellerName)
		// 期限: the same one-byte hour count the op186 row carries at +0x2A.
		// The op188 row puts it one byte lower because its name field is 13
		// bytes instead of 21; both land on struct+0x2A in the shared 0xA8 row
		// the client builds for the painter.
		row[0x1D] = currentAuctionRemainingHours(listing, now)
		copy(row[0x1E:0x71], buildCurrentAuctionItem83(listing.Item))
		binary.LittleEndian.PutUint32(row[0x93:0x97], currentAuctionBuyoutUnitPrice(listing))
		response.writeBytes(row)
	}
	return response.bytes()
}

// buildCurrentAuctionBiddingBody renders the 我的竞拍 tab: `u8 payType |
// u16 count | count x 0x9F rows`, listing every live consignment this
// character currently holds the top bid on.
//
// The row layout is the gold panel's own, read off sub_3006310's mode-2 branch
// (the list sub_2FFAC60 hands back, panel+0x5A0, which op189 fills through
// sub_3011630): id at +0x00, the standing bid at +0x08, the buyout at +0x0C, a
// 13-byte NUL-terminated seller name at +0x10, the 期限 hour byte at +0x25
// (`mov al, [ebx+0x25]`), the 83-byte item at +0x26, the 30-byte jewel block at
// +0x79, the auxiliary colour at +0x97 and the buyout unit price last at +0x9B.
// Bytes 0x1D..0x25 are never read by the renderer and go out zero.
func buildCurrentAuctionBiddingBody(listings []dnfauction.Listing, bidderID string, eligible func(uint32) bool, now time.Time) []byte {
	rows := make([]dnfauction.Listing, 0, len(listings))
	for _, listing := range listings {
		if listing.BidTotal <= 0 || listing.BidderID != bidderID ||
			listing.Item.ItemID <= 0 || listing.Item.ItemID > math.MaxUint32 ||
			eligible == nil || !eligible(uint32(listing.Item.ItemID)) {
			continue
		}
		rows = append(rows, listing)
		// The count is a u16 on the wire but sub_3011630 ingests
		// `movzx edi, byte ptr [ebp+8]`, so anything past 255 would be read
		// off the wire and then dropped. Stop at what the client can hold.
		if len(rows) == math.MaxUint8 {
			break
		}
	}
	var response packetWriter
	response.writeByte(currentAuctionPayTypeGold)
	response.writeUint16(uint16(len(rows)))
	for _, listing := range rows {
		row := make([]byte, 0x9F)
		binary.LittleEndian.PutUint64(row[0x00:0x08], listing.ID)
		binary.LittleEndian.PutUint32(row[0x08:0x0C], currentAuctionBidTotalPrice(listing))
		binary.LittleEndian.PutUint32(row[0x0C:0x10], currentAuctionBuyoutTotalPrice(listing))
		writeCurrentAuctionName(row[0x10:0x1D], listing.SellerName)
		row[0x25] = currentAuctionRemainingHours(listing, now)
		copy(row[0x26:0x79], buildCurrentAuctionItem83(listing.Item))
		binary.LittleEndian.PutUint32(row[0x9B:0x9F], currentAuctionBuyoutUnitPrice(listing))
		response.writeBytes(row)
	}
	return response.bytes()
}

func buildCurrentAuctionItem83(stack dnfrepo.ItemStack) []byte {
	item := make([]byte, 0x53)
	if stack.Bind {
		item[0] = 1
	}
	binary.LittleEndian.PutUint32(item[0x01:0x05], currentAuctionUint32(stack.ItemID))
	binary.LittleEndian.PutUint32(item[0x06:0x0A], currentAuctionUint32(stack.Count))
	raw := stack.RawEntry
	if len(raw) == currentItemListEntryWireSize {
		if raw[0x0D] != 0 {
			item[0] = 1
		}
		item[0x05] = raw[0x0A]
		copy(item[0x0A:0x0C], raw[0x0B:0x0D])
		copy(item[0x0C:0x10], raw[0x0E:0x12])
		item[0x10] = raw[0x13]
		copy(item[0x11:0x13], raw[0x14:0x16])
		item[0x33] = raw[0x57] & 0x1F
		copy(item[0x36:0x3A], raw[0x38:0x3C])
		item[0x3D] = raw[0x12]
		item[0x3E] = raw[0x5D]
		item[0x3F] = raw[0x58]
		copy(item[0x41:0x43], raw[0x61:0x63])
		copy(item[0x43:0x45], raw[0x63:0x65])
		copy(item[0x45:0x49], raw[0x72:0x76])
		item[0x49] = raw[0x76]
	}
	if !stack.ExpireAt.IsZero() {
		binary.LittleEndian.PutUint32(item[0x36:0x3A], uint32(stack.ExpireAt.Unix()))
	}
	return item
}

// currentAuctionRemainingHours is the 期限 column both auction lists render.
// The current EXE row painter reads one byte - op186 row+0x2A, op188 row+0x1D -
// and formats it with text 60159 ("%d小时") into the column headed by text
// 60155 ("期限"), so the field is whole hours, not a tier code and not a
// timestamp. A listing that still has a partial hour left must not read 0, so
// the remainder rounds up; anything already past its deadline is 0 and the
// expiry sweep returns it.
func currentAuctionRemainingHours(listing dnfauction.Listing, now time.Time) byte {
	if listing.ExpiresAt.IsZero() {
		return 0
	}
	remaining := listing.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	hours := (remaining + time.Hour - 1) / time.Hour
	if hours > math.MaxUint8 {
		return math.MaxUint8
	}
	return byte(hours)
}

func currentAuctionUint32(value int64) uint32 {
	if value <= 0 {
		return 0
	}
	if value > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(value)
}

func currentAuctionTotalPrice(unitPrice, count int64) uint32 {
	if unitPrice <= 0 || count <= 0 || unitPrice > math.MaxUint32/count {
		return math.MaxUint32
	}
	return uint32(unitPrice * count)
}

// currentAuctionNoPrice is the "this price column does not exist" sentinel the
// current client's own row painters test. It is NOT zero.
//
// Every auction entry carries three price slots - current bid, buyout total,
// buyout unit - and each of them is rendered only when the slot is not -1:
//
//	owner 7 (item auction), row painter FUN_02fffab0, row struct at widget+0x3c4:
//	  02fffa36  CMP dword ptr [ESI + 0x3d1], -1   ; +0x0D = 一口价总额
//	            JZ  -> the 60058 「一口价」 line is skipped entirely
//	  02fffa7a  CMP dword ptr [ESI + 0x3cd], -1   ; +0x09 = 当前竞拍价总额
//	            JZ  -> the 60161 「当前竞拍」 line is skipped entirely
//	owner 8 (金币寄售), row painter FUN_02076540, row struct at widget+0x3c8:
//	  020768c9  CMP dword ptr [ESI + 0x3d1], -1   ; +0x09, same meaning
//	  0207673e  CMP dword ptr [ESI + 0x3d5], -1   ; +0x0D, same meaning
//	and the row tooltip FUN_0207a760 tests the third slot the same way:
//	  0207a84d  CMP dword ptr [ECX + 0x3d9], -1   ; +0x11 = 一口价单价
//
// The unit slot doubles as the tooltip's "derive it yourself" flag: when it is
// -1 the tooltip divides the buyout total by the stack count to print
// dstr 46262 「每个: %s」, which is exactly how the 0x97 normalisation leaves it.
//
// So a listing that has no bidding (or no buyout) must publish -1 in the
// matching slot, not 0. A zero is a real price: it draws "当前竞拍 0 金币", and -
// as the live report showed - the buy path reads the bid slot as "a start price
// is set" and then refuses to sell fewer than the whole stack with dstr 46263
// 「已设定拍卖起始价的物品无法单个购买。」.
const currentAuctionNoPrice = uint32(0xFFFFFFFF)

// currentAuctionBidTotalPrice fills the 当前竞拍价 slot: the standing bid once
// somebody has bid, otherwise the seller's opening bid. Same term as 86JP's
// DisplayBidPrice, and the reason a bid has to show up here is that the client
// reads its own "must beat this" number out of this slot.
//
// A listing the seller registered buyout-only has no bidding at all, so the
// slot is the sentinel and the client hides the whole line. Leaving a number
// here is what made the buy dialog offer only the entire stack.
func currentAuctionBidTotalPrice(listing dnfauction.Listing) uint32 {
	if !listing.HasBidding() && listing.BidTotal <= 0 {
		return currentAuctionNoPrice
	}
	return currentAuctionUint32(listing.DisplayBidTotal())
}

// currentAuctionBuyoutTotalPrice and currentAuctionBuyoutUnitPrice fill the
// second and third price slots. A listing registered with an opening bid and no
// 一口价 has no buyout to show, so both go out as the sentinel and the client
// draws neither the 一口价 line nor a derived 每个 line. Every listing escrowed
// before the buyout became optional keeps a buyout equal to its unit price, so
// their rows are unchanged.
func currentAuctionBuyoutTotalPrice(listing dnfauction.Listing) uint32 {
	if !listing.HasBuyout() {
		return currentAuctionNoPrice
	}
	return currentAuctionTotalPrice(listing.BuyoutUnitPrice(), listing.Item.Count)
}

func currentAuctionBuyoutUnitPrice(listing dnfauction.Listing) uint32 {
	if !listing.HasBuyout() {
		return currentAuctionNoPrice
	}
	return currentAuctionUint32(listing.BuyoutUnitPrice())
}

func writeCurrentAuctionName(dst []byte, value string) {
	encoded := rosterNameBytes(strings.TrimSpace(value))
	if len(encoded) > len(dst) {
		encoded = encoded[:len(dst)]
	}
	copy(dst, encoded)
}

func (s *Service) deliverCurrentAuctionPendingPayoutsLocked(ctx context.Context, owner *dnfauction.Owner, sellerID string) error {
	payouts, err := owner.PendingPayouts(ctx, sellerID)
	if err != nil {
		return err
	}
	for _, payout := range payouts {
		if err := s.settleCurrentAuctionPayoutLocked(ctx, owner, payout); err != nil {
			return err
		}
	}
	return nil
}

// settleCurrentAuctionPayoutLocked pays one gold outbox row. A released bid
// goes back to the wallet, everything else by mail - see
// dnfauction.Owner.RefundBidToBidder for why the two differ. A wallet credit
// that fails falls through to the mail so the gold is never stuck on one path.
func (s *Service) settleCurrentAuctionPayoutLocked(ctx context.Context, owner *dnfauction.Owner, payout dnfauction.Payout) error {
	if !payout.Refund {
		return s.deliverCurrentAuctionPayoutMailLocked(ctx, owner, payout)
	}
	goldAfter, err := owner.RefundBidToBidder(ctx, payout)
	if err != nil {
		s.logGameEvent(nil, "game-auction-bid-refund-wallet-failed",
			"refund_id", payout.ID, "bidder_id", payout.SellerID, "gold", payout.Gold, "error", err)
		return s.deliverCurrentAuctionPayoutMailLocked(ctx, owner, payout)
	}
	s.logGameEvent(nil, "game-auction-bid-refund-credited",
		"refund_id", payout.ID, "bidder_id", payout.SellerID, "gold", payout.Gold, "gold_after", goldAfter)
	// The bidder may be standing in the auction house watching their own
	// wallet; without this their client keeps the pre-refund number until
	// something else refreshes it.
	if characterID, parseErr := strconv.ParseUint(payout.SellerID, 10, 16); parseErr == nil && characterID != 0 {
		if bidderSession, online := s.onlineGameSession(uint16(characterID)); online {
			if err := s.sendGameUpperRawClass(bidderSession, 14, buildCurrentGoldStateBody(goldAfter), 0); err != nil {
				s.logGameEvent(nil, "game-auction-bid-refund-gold-refresh-failed",
					"bidder_id", payout.SellerID, "error", err)
			}
		}
	}
	return nil
}

// settleCurrentAuctionWonListingsLocked closes every listing that ran out its
// duration with a bid standing on it and sends both halves - the item to the
// winner, the proceeds to the seller. Neither party has to be online: the rows
// are durable, so a mail failure only defers the delivery.
func (s *Service) settleCurrentAuctionWonListingsLocked(session *gameSession) {
	if s == nil {
		return
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.SettleWonListings(ctx)
	if err != nil {
		s.logGameEvent(session, "game-auction-bid-settlement-deferred", "error", err)
		return
	}
	for _, settlement := range result.Settlements {
		s.logGameEvent(session, "game-auction-bid-won",
			"listing_id", settlement.ListingID,
			"winner_id", settlement.Delivery.BuyerID,
			"seller_id", settlement.Payout.SellerID,
			"item_id", settlement.Delivery.Item.ItemID,
			"count", settlement.Delivery.Item.Count,
			"proceeds", settlement.Payout.Gold)
		buyerMailed := false
		if deliveryErr := s.deliverCurrentAuctionBuyerMailLocked(ctx, owner, settlement.Delivery); deliveryErr != nil {
			s.logGameEvent(session, "game-auction-bid-win-mail-deferred",
				"delivery_id", settlement.Delivery.ID, "winner_id", settlement.Delivery.BuyerID, "error", deliveryErr)
		} else {
			buyerMailed = true
		}
		payoutMailed := false
		if payoutErr := s.deliverCurrentAuctionPayoutMailLocked(ctx, owner, settlement.Payout); payoutErr != nil {
			s.logGameEvent(session, "game-auction-bid-payout-mail-deferred",
				"payout_id", settlement.Payout.ID, "seller_id", settlement.Payout.SellerID, "error", payoutErr)
		} else {
			payoutMailed = true
		}
		// 竞拍成交记录：SettleWonListings 只返回**本次真的结算掉**的那些挂单
		// （已经写过 delivery/payout 行的行会被它跳过，结算完还会把挂单行删掉），
		// 所以这份列表本身就是"这一次的跃迁集合"，重复跑这个扫描拿到的是空列表，
		// 不会重复记。这正是每个拍卖命令开头都能安全调用它的原因。
		//
		// 没有 quantity/unit_price 这类字段：挂单行在结算时已经被删除，
		// 结算结果里只带得走成交价与件数，缺的就留空，不为凑字段回查数据库。
		won := map[string]any{
			"listing_id":      settlement.ListingID,
			"item_id":         settlement.Delivery.Item.ItemID,
			"item_name":       s.currentAuctionItemDisplayName(settlement.Delivery.Item.ItemID),
			"item_count":      settlement.Delivery.Item.Count,
			"buyout":          false,
			"whole_listing":   true,
			"buyer_id":        strings.TrimSpace(settlement.Delivery.BuyerID),
			"seller_id":       strings.TrimSpace(settlement.Payout.SellerID),
			"seller_name":     strings.TrimSpace(settlement.Payout.SellerName),
			"seller_proceeds": settlement.Payout.Gold,
			"buyer_mailed":    buyerMailed,
			"delivered":       payoutMailed,
		}
		if settlement.Payout.HasStatement {
			won["sale_price"] = settlement.Payout.Statement.GrossGold
			won["fee_gold"] = settlement.Payout.Statement.FeeGold
			won["deposit_gold"] = settlement.Payout.Statement.DepositGold
		}
		s.recordAuction(session, "won", won)
		// 卖家收益记录与竞拍成交记录成对出现：成交那一行回答"东西卖给了谁、
		// 卖了多少钱"，这一行回答"钱怎么到了卖家手里"。Payout 行同样是本次
		// 新建的，延后重发的轮次不记。
		s.recordAuction(session, "payout", auctionPayoutRecordFields(s, settlement.Payout, payoutMailed))
	}
}

func (s *Service) retryCurrentAuctionMailsLocked(session *gameSession) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := selectedCharacterIDString(session)
	if err := s.deliverCurrentAuctionPendingBuyerMailsLocked(ctx, owner, characterID); err != nil {
		s.logGameEvent(session, "game-auction-buyer-mail-retry-deferred", "buyer_id", characterID, "error", err)
	}
	if err := s.deliverCurrentAuctionPendingPayoutsLocked(ctx, owner, characterID); err != nil {
		s.logGameEvent(session, "game-auction-payout-mail-retry-deferred", "seller_id", characterID, "error", err)
	}
}

func (s *Service) deliverCurrentAuctionPendingBuyerMailsLocked(ctx context.Context, owner *dnfauction.Owner, buyerID string) error {
	deliveries, err := owner.PendingDeliveries(ctx, buyerID)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		// The outbox carries purchases, cancelled consignments and expired
		// ones; a deferred row must be re-sent with the wording it was queued
		// under.
		deliver := s.deliverCurrentAuctionBuyerMailLocked
		switch {
		case delivery.Expired:
			deliver = s.deliverCurrentAuctionExpiredMailLocked
		case delivery.Return:
			deliver = s.deliverCurrentAuctionReturnMailLocked
		}
		if err := deliver(ctx, owner, delivery); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deliverCurrentAuctionBuyerMailLocked(ctx context.Context, owner *dnfauction.Owner, delivery dnfauction.Delivery) error {
	return s.deliverCurrentAuctionItemMailLocked(ctx, owner, delivery, currentAuctionPurchaseMail)
}

// deliverCurrentAuctionReturnMailLocked hands a cancelled consignment back to
// its seller through the same durable outbox a purchase uses, so a full bag
// can no longer strand the item in escrow and the seller sees the ordinary
// mailbox alarm instead of a row that only appears after reselecting.
func (s *Service) deliverCurrentAuctionReturnMailLocked(ctx context.Context, owner *dnfauction.Owner, delivery dnfauction.Delivery) error {
	return s.deliverCurrentAuctionItemMailLocked(ctx, owner, delivery, currentAuctionReturnMail)
}

// deliverCurrentAuctionExpiredMailLocked is the same return, worded for a
// listing that ran out its duration rather than one the seller pulled.
func (s *Service) deliverCurrentAuctionExpiredMailLocked(ctx context.Context, owner *dnfauction.Owner, delivery dnfauction.Delivery) error {
	return s.deliverCurrentAuctionItemMailLocked(ctx, owner, delivery, currentAuctionExpiredMail)
}

type currentAuctionItemMailKind struct {
	title  string
	body   func(itemName string) string
	source string
	// noticeKind is the class0/0xA9 banner this delivery owes the recipient.
	//
	// Every delivery used to be announced as kind 2 (dstr 60088
	// 「您在拍卖行竞拍的[%s]已拍下，请确认邮件」), which is only true for a
	// purchase. A return delivery carries the SELLER in BuyerID, so a cancelled
	// listing told the seller they had *won* their own auction - the wrong-text
	// complaint. FUN_01d80150's switch has an exact text for each case:
	//   0 60086 竞拍中出现更高价格，金额返还        outbid / released bid
	//   1 60087 上架的[%s]已被拍下                  sale payout to the seller
	//   2 60088 竞拍的[%s]已拍下                    purchase to the buyer
	//   3 60089 上架的[%s]竞拍失败                  expiry (流拍) back to the seller
	//   4 60090 [%s]的拍卖已被取消，金额返还        cancel releasing a bid
	//   5 60091 [%s]的竞拍已被取消                  cancel returning the item
	noticeKind byte
}

var (
	currentAuctionPurchaseMail = currentAuctionItemMailKind{
		title:  "拍卖行购买物品",
		body:   func(itemName string) string { return fmt.Sprintf("您购买的%s已通过邮件送达。", itemName) },
		source: "auction_purchase_delivery",
		// The buyer won, so 60088.
		noticeKind: currentAuctionNoticeKindBuyerWon,
	}
	currentAuctionReturnMail = currentAuctionItemMailKind{
		title: "拍卖行取消上架",
		body: func(itemName string) string {
			return fmt.Sprintf("您取消上架的%s已通过邮件返还。", itemName)
		},
		source: "auction_listing_return",
		// The seller pulled the listing, so 60091
		// 「[%s]的竞拍已被取消，请确认邮件」.
		noticeKind: currentAuctionNoticeKindCancelBid,
	}
	currentAuctionExpiredMail = currentAuctionItemMailKind{
		title: "拍卖行流拍返还",
		body: func(itemName string) string {
			return fmt.Sprintf("您上架的%s已到期流拍，通过邮件返还。", itemName)
		},
		source: "auction_listing_expired",
		// Nobody bought it, so 60089 「上架的[%s]竞拍失败」 - the kind the
		// notice table listed as unused until this path claimed it.
		noticeKind: currentAuctionNoticeKindSellerFailed,
	}
)

func (s *Service) deliverCurrentAuctionItemMailLocked(ctx context.Context, owner *dnfauction.Owner, delivery dnfauction.Delivery, kind currentAuctionItemMailKind) error {
	if owner == nil || delivery.ID == "" || delivery.BuyerID == "" || delivery.ListingID == 0 || delivery.Item.ItemID <= 0 || delivery.Item.Count <= 0 {
		return dnfauction.ErrInvalidRequest
	}
	_, ok := s.repositoryGroup()
	if !ok {
		return dnfauction.ErrUnavailable
	}
	itemName := s.currentAuctionItemDisplayName(delivery.Item.ItemID)
	attachment := dnfrepo.MailAttachment{
		ItemID:   delivery.Item.ItemID,
		Count:    delivery.Item.Count,
		Bind:     delivery.Item.Bind,
		ExpireAt: delivery.Item.ExpireAt,
		RawEntry: append([]byte(nil), delivery.Item.RawEntry...),
		Extra:    cloneCurrentAuctionAttachmentExtra(delivery.Item.Extra),
	}
	mailDelivery := dnfrepo.SystemMailDelivery{RecipientCharacterID: delivery.BuyerID, Title: kind.title, Body: kind.body(itemName), Gold: delivery.Gold, Attachments: []dnfrepo.MailAttachment{attachment}, Source: kind.source, CreatedAt: s.gameplayNow()}
	err := owner.DeliverItemMail(ctx, dnfauction.ConfirmDeliveryCommand{DeliveryID: delivery.ID, BuyerID: delivery.BuyerID, ListingID: delivery.ListingID, ItemID: delivery.Item.ItemID, Count: delivery.Item.Count, Gold: delivery.Gold}, mailDelivery)
	if err != nil {
		return err
	}

	if characterID, parseErr := strconv.ParseUint(delivery.BuyerID, 10, 16); parseErr == nil && characterID != 0 {
		if err := s.sendMailboxAlarmToOnlineRecipient(uint16(characterID)); err != nil {
			s.logGameEvent(nil, "game-auction-buyer-mail-alarm-failed", "buyer_id", delivery.BuyerID, "delivery_id", delivery.ID, "error", err)
		}
	}
	// The recipient's banner. It is the delivery kind that decides the text:
	// a purchase is 60088 (竞拍的[%s]已拍下), a cancel is 60091 (竞拍的[%s]已被取消)
	// and an expiry is 60089 (上架的[%s]竞拍失败). Announcing all three as 60088 is
	// what told a cancelling seller they had won their own auction.
	s.notifyCurrentAuctionNoticeID(delivery.BuyerID, currentAuctionNoticeRouteItem, kind.noticeKind, delivery.Item.ItemID)
	return nil
}

func (s *Service) deliverCurrentAuctionPayoutMailLocked(ctx context.Context, owner *dnfauction.Owner, payout dnfauction.Payout) error {
	if owner == nil || payout.ID == "" || payout.SellerID == "" || payout.Gold <= 0 {
		return dnfauction.ErrInvalidRequest
	}
	_, ok := s.repositoryGroup()
	if !ok {
		return dnfauction.ErrUnavailable
	}
	itemName := s.currentAuctionItemDisplayName(payout.ItemID)
	// The same gold outbox carries sale proceeds to a seller and a released
	// bid back to its bidder, so a deferred row has to be re-sent with the
	// wording it was queued under - Refund is what remembers which.
	title, body, source := "拍卖行成交款",
		fmt.Sprintf("%s已成交%d个，成交款%d金币。", itemName, payout.SoldCount, payout.Gold),
		"auction_sale_proceeds"
	if payout.Refund {
		title, body, source = "拍卖行竞拍返还",
			fmt.Sprintf("您对%s的竞拍已被更高的出价超越或该物品已结束，返还竞拍金额%d金币。", itemName, payout.Gold),
			"auction_bid_refund"
	} else if payout.HasStatement {
		// A settled sale prints the itemised statement the seller's own client
		// shows, so the three numbers behind the payout stay visible instead of
		// only the net. The breakdown rides the outbox row, so a deferred retry
		// prints the same letter after the listing row is gone.
		title, body = currentAuctionSaleStatement(payout, itemName)
	}
	mailDelivery := dnfrepo.SystemMailDelivery{RecipientCharacterID: payout.SellerID, Title: title, Body: body, Gold: payout.Gold, Source: source, CreatedAt: s.gameplayNow()}
	err := owner.DeliverPayoutMail(ctx, dnfauction.ConfirmPayoutCommand{PayoutID: payout.ID, SellerID: payout.SellerID, Gold: payout.Gold}, mailDelivery)
	if err != nil {
		return err
	}

	if characterID, parseErr := strconv.ParseUint(payout.SellerID, 10, 16); parseErr == nil && characterID != 0 {
		if err := s.sendMailboxAlarmToOnlineRecipient(uint16(characterID)); err != nil {
			s.logGameEvent(nil, "game-auction-sale-mail-alarm-failed", "seller_id", payout.SellerID, "payout_id", payout.ID, "error", err)
		}
	}
	// The seller's banner. A settled sale is kind 1 (dstr 60087
	// 「您在拍卖行上架的[%s]已被拍下，请确认邮件」); a released bid rides the same
	// outbox with Refund set and is kind 0 (dstr 60086).
	noticeKind := currentAuctionNoticeKindSellerSold
	if payout.Refund {
		noticeKind = currentAuctionNoticeKindOutbid
	}
	s.notifyCurrentAuctionNoticeID(payout.SellerID, currentAuctionNoticeRouteItem, noticeKind, payout.ItemID)
	return nil
}

// currentAuctionSaleStatement renders a settled sale as the itemised letter the
// seller's client shows. The line set and its wording are the operator's target
// format: what the item sold for, the deposit that came home with it, the
// commission taken out of it, and the net the letter pays.
//
//	[已售出 金刚石 1个。]
//	出售价 : + 10000 金币
//	保证金 : + 10000 金币
//	手续费 : - 500 金币
//	已售出 金刚石 并获得 19500 金币。
//
// The first line is BOTH the letter's subject and the body's first line. The
// client's mail detail pane draws the body only - a subject alone is invisible
// there, which is what the live letter showed - while the mailbox list draws the
// subject, so carrying it in both places is what makes all five lines appear
// exactly as the target format has them.
//
// 保证金 appears only when a deposit actually came back - a partial sale keeps
// it riding the remaining pieces, and a listing registered before deposits
// existed never had one - and 手续费 only when a commission was charged, so a
// legacy fee-free listing does not print "- 0 金币".
func currentAuctionSaleStatement(payout dnfauction.Payout, itemName string) (string, string) {
	statement := payout.Statement
	lines := []string{fmt.Sprintf("[已售出 %s %d个。]", itemName, payout.SoldCount)}
	lines = append(lines, fmt.Sprintf("出售价 : + %d 金币", statement.GrossGold))
	if statement.DepositGold > 0 {
		lines = append(lines, fmt.Sprintf("保证金 : + %d 金币", statement.DepositGold))
	}
	if statement.FeeGold > 0 {
		lines = append(lines, fmt.Sprintf("手续费 : - %d 金币", statement.FeeGold))
	}
	lines = append(lines, fmt.Sprintf("已售出 %s 并获得 %d 金币。", itemName, payout.Gold))
	return lines[0], strings.Join(lines, "\n")
}

func (s *Service) currentAuctionItemDisplayName(itemID int64) string {
	fallback := fmt.Sprintf("物品%d", itemID)
	if s == nil || itemID <= 0 || itemID > math.MaxUint32 {
		return fallback
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return fallback
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil || strings.TrimSpace(definition.Name) == "" {
		return fallback
	}
	return strings.TrimSpace(definition.Name)
}

func cloneCurrentAuctionAttachmentExtra(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (s *Service) sendCurrentAuctionOwnedItemsLocked(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketAuctionMyRegistedItemInfo)
	// 金币寄售 has its own 我寄售的金币 tab on op188. It sends the same one-byte
	// shape as the item tab but with mode 1. Live capture 2026-09-21T09:43:18Z,
	// packet_log.txt: op188 body `01`, answered 00 99 00 - the third popup.
	if len(body) == 1 && body[0] == currentAuctionPayTypeConsignment {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, currentAuctionFailureCode, 1}, 1)
	}
	if len(body) != 1 || body[0] != currentAuctionPayTypeGold {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if err := s.deliverCurrentAuctionPendingPayoutsLocked(ctx, owner, selectedCharacterIDString(session)); err != nil {
		s.logGameEvent(session, "game-auction-payout-mail-retry-deferred", "seller_id", selectedCharacterIDString(session), "error", err)
	}
	claim, err := owner.ReturnExpiredListings(ctx, selectedCharacterIDString(session), strings.TrimSpace(s.accountIDForSession(session)))
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	for _, expired := range claim.ExpiredReturns {
		if deliveryErr := s.deliverCurrentAuctionExpiredMailLocked(ctx, owner, expired); deliveryErr != nil {
			s.logGameEvent(session, "game-auction-return-mail-deferred",
				"delivery_id", expired.ID, "seller_id", expired.BuyerID,
				"listing_id", expired.ListingID, "item_id", expired.Item.ItemID,
				"reason", "expired", "error", deliveryErr)
		}
	}
	listings, err := owner.Search(ctx, nil)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	// Recover listings created by older builds that accepted equipment/avatar
	// rows the current 83-byte projector cannot preserve. They must never stay
	// invisible and uncancellable in escrow.
	for _, listing := range listings {
		if listing.SellerID != selectedCharacterIDString(session) ||
			(listing.Item.ItemID > 0 && listing.Item.ItemID <= math.MaxUint32 && s.currentAuctionItemEligible(uint32(listing.Item.ItemID))) {
			continue
		}
		returned, cancelErr := owner.Cancel(ctx, dnfauction.CancelCommand{
			SellerID: listing.SellerID, AccountID: strings.TrimSpace(s.accountIDForSession(session)), ListingID: listing.ID,
		})
		if cancelErr != nil {
			s.logGameEvent(session, "game-auction-unsupported-listing-return-deferred", "listing_id", listing.ID, "item_id", listing.Item.ItemID, "error", cancelErr)
			continue
		}
		if returned.Delivery.ID != "" {
			if deliveryErr := s.deliverCurrentAuctionReturnMailLocked(ctx, owner, returned.Delivery); deliveryErr != nil {
				s.logGameEvent(session, "game-auction-return-mail-deferred",
					"delivery_id", returned.Delivery.ID, "seller_id", returned.Delivery.BuyerID,
					"listing_id", returned.Delivery.ListingID, "item_id", returned.Delivery.Item.ItemID,
					"error", deliveryErr)
			}
		}
	}
	listings, err = owner.Search(ctx, nil)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	return s.sendGameUpperSuccess(session, opcode, buildCurrentAuctionOwnedBody(listings, selectedCharacterIDString(session), s.currentAuctionItemEligible))
}

// sendCurrentAuctionGoldOwnedItemsLocked now lives in
// auction_gold_consignment.go, where it renders the ledger's real rows instead
// of an empty list. The op188 mode-1 branch above stays here.

// sendCurrentAuctionBiddingItemsLocked answers 我的竞拍. Anything this
// character has outbid, been outbid on, or already won has left the live
// listing set, so the tab is exactly "consignments I am currently winning".
func (s *Service) sendCurrentAuctionBiddingItemsLocked(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketAuctionMyBiddingInfo)
	if len(body) != 1 || body[0] != currentAuctionPayTypeGold {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	listings, err := owner.Search(ctx, nil)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAuctionFailureCode)
	}
	return s.sendGameUpperSuccess(session, opcode, buildCurrentAuctionBiddingBody(
		listings, selectedCharacterIDString(session), s.currentAuctionItemEligible, time.Now().UTC()))
}

// sendCurrentAuctionInventorySlot republishes one source cell after a listing
// took its item out.
//
// The empty row must carry item id -1, not 0. The client's op14 list-0 reader
// sub_1D73120 only RemoveItem's when the id is exactly -1 (0x1D73912); anything
// else is an in-place update that leaves the old object alive, and the next row
// for that cell is then rejected by the 0x1D73AB2 type check because its id
// changed. The visible symptom is precisely the live report: an item that went
// out on a listing and came back by mail stays invisible in the bag until the
// player reselects the character, because the claim's op14 row never became a
// real "add". Same fix, same reason, as the 2026-09-11 装备再生 entry.
//
// Account-shared crystal/soul cells are the one exception and keep (0, 0): the
// right-hand crystal panel recognises a zero pair as "empty" and does not
// understand 0xFFFFFFFF (see sendSelectedIncrementalItemSlotRefreshes).
func (s *Service) sendCurrentAuctionInventorySlot(session *gameSession, listType byte, slot int16, inventory dnfrepo.InventoryRecord) error {
	stack, exists := inventory.Slots[inventorySlotKey(listType, slot)]
	var entry currentItemListEntry
	switch {
	case exists && stack.ItemID > 0:
		entry = currentItemListEntryFromStack(listType, slot, stack)
	case dnfrepo.IsAccountSharedInventorySlot(listType, slot):
		entry.patchCore(slot, 0, 0)
	default:
		entry.patchCore(slot, math.MaxUint32, 0)
	}
	return s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(listType, []currentItemListEntry{entry}), 0)
}

func inventorySlotKey(listType byte, slot int16) string {
	return fmt.Sprintf("%d:%d", listType, slot)
}
