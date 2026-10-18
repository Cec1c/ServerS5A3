package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	shop "longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
)

// 现用EXE发送器：0233f692注册28字节，02331c72撤销9字节，0233419d购买
// 13字节或带物品号17字节；0233dc51分类12字节。参考版的20字节分类假设不适用。
const (
	currentAuctionGoldRegisterRequestSize       = 28
	currentAuctionGoldCancelRequestSize         = 9
	currentAuctionGoldBuyRequestSize            = 13
	currentAuctionGoldBuyRequestSizeWithTrailer = 17
	currentAuctionGoldCategoryRequestSize       = 12
	currentGoldConsignSearchHeaderSize          = 7
	currentGoldConsignSearchRowSize             = 0xA8
	currentGoldConsignSearchBidOffset           = 0x09
	currentGoldConsignOwnedHeaderSize           = 2
	currentGoldConsignOwnedRowSize              = 0x97
	currentGoldConsignOwnedBidOffset            = 0x08
	currentGoldConsignNoBid                     = currentAuctionNoPrice
	currentAuctionNoticeRouteGold               = byte(1)
)

type currentGoldConsignRegisterRequest struct{ GoldItemID, PriceCera, DurationHour int64 }
type currentGoldConsignBuyRequest struct {
	ListingID     uint64
	ExpectedCera  int64
	GoldItemID    uint32
	HasGoldItemID bool
}

func markCurrentGoldConsignNoBid(body []byte, headerSize, rowSize, bidOffset int) {
	if rowSize <= 0 || bidOffset < 0 || bidOffset+4 > rowSize {
		return
	}
	// A record is only rewritten once it is complete. The builders above always
	// emit whole rows, so this is belt and braces: it keeps a future partial row
	// from being written past its own end.
	for row := 0; ; row++ {
		rowStart := headerSize + row*rowSize
		if rowStart < 0 || rowStart+rowSize > len(body) {
			return
		}
		start := rowStart + bidOffset
		binary.LittleEndian.PutUint32(body[start:start+4], currentGoldConsignNoBid)
	}
}

func decodeCurrentGoldConsignRegisterRequest(body []byte) (currentGoldConsignRegisterRequest, error) {
	if len(body) != currentAuctionGoldRegisterRequestSize || body[0] != currentAuctionPayTypeConsignment {
		return currentGoldConsignRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	// body[1] and the u16 at 2 are fixed zero in every capture; the quantity is
	// fixed at 1 and the bid price at -1 because a consignment has no stack
	// count control and no bidding. Anything else is not a shape this client
	// can produce, so it is refused rather than reinterpreted.
	if body[1] != 0 || binary.LittleEndian.Uint16(body[2:4]) != 0 {
		return currentGoldConsignRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	goldItemID := int64(binary.LittleEndian.Uint32(body[4:8]))
	quantity := int64(int32(binary.LittleEndian.Uint32(body[8:12])))
	bidPrice := int64(int32(binary.LittleEndian.Uint32(body[12:16])))
	priceCera := int64(binary.LittleEndian.Uint32(body[16:20]))
	reserved := binary.LittleEndian.Uint32(body[20:24])
	lifetime := int64(binary.LittleEndian.Uint32(body[24:28]))
	if quantity != 1 || bidPrice != -1 || reserved != 0 || priceCera <= 0 {
		return currentGoldConsignRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	if lifetime != dnfauction.GoldConsignMinDurationHours && lifetime != dnfauction.GoldConsignMaxDurationHours {
		return currentGoldConsignRegisterRequest{}, dnfauction.ErrInvalidRequest
	}
	if _, ok := dnfauction.GoldConsignTierAmount(goldItemID); !ok {
		return currentGoldConsignRegisterRequest{}, dnfauction.ErrGoldConsignNotTier
	}
	return currentGoldConsignRegisterRequest{
		GoldItemID:   goldItemID,
		PriceCera:    priceCera,
		DurationHour: lifetime,
	}, nil
}

func decodeCurrentGoldConsignBuyRequest(body []byte) (currentGoldConsignBuyRequest, error) {
	if len(body) != currentAuctionGoldBuyRequestSize &&
		len(body) != currentAuctionGoldBuyRequestSizeWithTrailer {
		return currentGoldConsignBuyRequest{}, dnfauction.ErrInvalidRequest
	}
	if body[0] != currentAuctionPayTypeConsignment {
		return currentGoldConsignBuyRequest{}, dnfauction.ErrInvalidRequest
	}
	expected := int64(int32(binary.LittleEndian.Uint32(body[1:5])))
	listingID := binary.LittleEndian.Uint64(body[5:13])
	if expected <= 0 || listingID == 0 {
		return currentGoldConsignBuyRequest{}, dnfauction.ErrInvalidRequest
	}
	request := currentGoldConsignBuyRequest{ListingID: listingID, ExpectedCera: expected}
	if len(body) == currentAuctionGoldBuyRequestSizeWithTrailer {
		tier := binary.LittleEndian.Uint32(
			body[currentAuctionGoldBuyRequestSize:currentAuctionGoldBuyRequestSizeWithTrailer])
		if tier == 0 {
			return currentGoldConsignBuyRequest{}, dnfauction.ErrInvalidRequest
		}
		request.GoldItemID = tier
		request.HasGoldItemID = true
	}
	return request, nil
}

func decodeCurrentGoldConsignCancelRequest(body []byte) (uint64, error) {
	if len(body) != currentAuctionGoldCancelRequestSize ||
		body[0] != currentAuctionPayTypeConsignment {
		return 0, dnfauction.ErrInvalidRequest
	}
	listingID := binary.LittleEndian.Uint64(body[1:9])
	if listingID == 0 {
		return 0, dnfauction.ErrInvalidRequest
	}
	return listingID, nil
}

func currentGoldConsignAsListing(g dnfauction.GoldConsignListing) dnfauction.Listing {
	return dnfauction.Listing{
		ID:         g.ID,
		SellerID:   g.SellerID,
		SellerName: g.SellerName,
		Item:       dnfrepo.ItemStack{ItemID: g.GoldItemID, Count: 1},
		// A consignment has no bidding: the captured op183 always carries -1,
		// so the row is buyout-only and UnitPrice is the Cera price.
		UnitPrice:   g.PriceCera,
		StartPrice:  -1,
		BuyoutPrice: g.PriceCera,
		FeePercent:  g.FeePercent,
		CreatedAt:   g.CreatedAt,
		ExpiresAt:   g.ExpiresAt,
	}
}

func currentGoldConsignAsListings(listings []dnfauction.GoldConsignListing) []dnfauction.Listing {
	out := make([]dnfauction.Listing, 0, len(listings))
	for _, listing := range listings {
		out = append(out, currentGoldConsignAsListing(listing))
	}
	return out
}

func buildCurrentGoldConsignSearchBody(listings []dnfauction.GoldConsignListing, page uint32, pageSize int) []byte {
	body := buildCurrentAuctionSearchBodyPage(
		currentGoldConsignAsListings(listings),
		// Gold certificates are deliberately NOT item-auction eligible, so the
		// item predicate must not filter them out here.
		func(uint32) bool { return true },
		page,
		pageSize,
	)
	if len(body) > 0 {
		body[0] = currentAuctionPayTypeConsignment
	}
	markCurrentGoldConsignNoBid(
		body,
		currentGoldConsignSearchHeaderSize,
		currentGoldConsignSearchRowSize,
		currentGoldConsignSearchBidOffset,
	)
	return body
}

func buildCurrentGoldConsignOwnedBody(listings []dnfauction.GoldConsignListing, sellerID string) []byte {
	body := buildCurrentAuctionOwnedBody(
		currentGoldConsignAsListings(listings),
		sellerID,
		func(uint32) bool { return true },
	)
	if len(body) > 0 {
		body[0] = currentAuctionPayTypeConsignment
	}
	markCurrentGoldConsignNoBid(
		body,
		currentGoldConsignOwnedHeaderSize,
		currentGoldConsignOwnedRowSize,
		currentGoldConsignOwnedBidOffset,
	)
	return body
}

func (s *Service) currentGoldConsignHasCoupon(session *gameSession) bool {
	if s == nil || session == nil {
		return false
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	_, _, feePercent := s.currentAuctionEntitlementLocked(ctx, session, repositories)
	return feePercent == dnfauction.DiscountedSaleFeePercent
}

func (s *Service) currentGoldConsignOwner() (*dnfauction.GoldConsignOwner, error) {
	r, ok := s.repositoryGroup()
	if !ok {
		return nil, dnfauction.ErrUnavailable
	}
	return dnfauction.NewGoldConsignOwner(r)
}
func currentGoldConsignFailureCode(err error) byte {
	switch {
	case errors.Is(err, dnfauction.ErrGoldConsignNotTier):
		return currentAuctionErrorItemRejected
	case errors.Is(err, dnfauction.ErrGoldConsignBuyerIsSeller):
		return currentAuctionErrorOwnListing
	case errors.Is(err, dnfauction.ErrGoldConsignBuyerLevel), errors.Is(err, dnfauction.ErrGoldConsignSellerLevel), errors.Is(err, dnfauction.ErrNotOwner):
		return currentAuctionErrorForbidden
	case errors.Is(err, dnfauction.ErrGoldUnavailable), errors.Is(err, shop.ErrCeraInsufficient):
		return currentAuctionErrorGoldShort
	case errors.Is(err, dnfauction.ErrGoldConsignLimit):
		return currentAuctionErrorListingLimit
	case errors.Is(err, dnfauction.ErrGoldConsignPriceRejected), errors.Is(err, dnfauction.ErrPriceMismatch):
		return currentAuctionErrorPriceInvalid
	case errors.Is(err, dnfauction.ErrListingMissing), errors.Is(err, dnfauction.ErrListingExpired), errors.Is(err, dnfauction.ErrGoldConsignNotActive):
		return currentAuctionErrorNoListing
	default:
		return currentAuctionErrorUnknown
	}
}
func (s *Service) sendCurrentGoldConsignFailure(session *gameSession, opcode uint16, err error) error {
	s.logGameEvent(session, "金币寄售请求拒绝", "opcode", opcode, "error", err)
	return s.sendGameUpperRawClass(session, opcode, []byte{0, currentGoldConsignFailureCode(err), 1}, 1)
}
func (s *Service) notifyGoldConsignMailbox(id string) {
	value, e := strconv.ParseUint(id, 10, 16)
	if e == nil && value > 0 {
		if e = s.sendMailboxAlarmToOnlineRecipient(uint16(value)); e != nil {
			s.logGameEvent(nil, "金币寄售邮件提示暂缓", "character_id", id, "error", e)
		}
	}
}
func (s *Service) handleCurrentGoldConsignLocked(session *gameSession, opcode uint16, body []byte) error {
	replyOpcode := opcode
	if opcode == uint16(dnfenum.CmdPacketAuctionSearchByNoitemkey) {
		replyOpcode = uint16(dnfenum.CmdPacketAuctionSearchByItemkey)
	}
	fail := func(e error) error { return s.sendCurrentGoldConsignFailure(session, replyOpcode, e) }
	owner, e := s.currentGoldConsignOwner()
	if e != nil {
		return fail(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	// 所有交易结果都先持久化；维护失败留待离线时钟重试，不把已提交交易倒退为失败。
	if _, e = owner.Maintain(ctx); e != nil {
		s.logGameEvent(session, "金币寄售邮件或到期结算暂缓", "error", e)
	}
	id, account := selectedCharacterIDString(session), s.accountIDForSession(session)
	switch opcode {
	case uint16(dnfenum.CmdPacketAuctionAskAveragePrice):
		if len(body) != 6 || body[5] != 0 {
			return fail(dnfauction.ErrInvalidRequest)
		}
		avg, n, e := owner.AverageCeraPrice(ctx, int64(binary.LittleEndian.Uint32(body[1:5])))
		if e != nil {
			return fail(e)
		}
		var w packetWriter
		w.writeByte(1)
		w.writeUint32(uint32(avg))
		w.writeUint32(uint32(n))
		// 没有成交时为0；价格阶梯来自当前实际挂单，不重复伪造成交均价。
		listings, e := owner.Search(ctx, []int64{int64(binary.LittleEndian.Uint32(body[1:5]))})
		if e != nil {
			return fail(e)
		}
		prices, counts := [6]uint32{}, [6]uint32{}
		index := -1
		for _, l := range listings {
			price := uint32(l.PriceCera)
			if index >= 0 && prices[index] == price {
				counts[index]++
				continue
			}
			if index == 5 {
				break
			}
			index++
			prices[index] = price
			counts[index] = 1
		}
		for _, v := range prices {
			w.writeUint32(v)
		}
		for _, v := range counts {
			w.writeUint32(v)
		}
		return s.sendGameUpperSuccess(session, opcode, w.bytes())
	case uint16(dnfenum.CmdPacketAuctionRegistItem):
		q, e := decodeCurrentGoldConsignRegisterRequest(body)
		if e != nil {
			return fail(e)
		}
		ordinary, e := s.currentAuctionOwner()
		if e != nil {
			return fail(e)
		}
		listingID, e := ordinary.AllocateListingID(ctx)
		if e != nil {
			return fail(e)
		}
		result, e := owner.Register(ctx, dnfauction.GoldConsignRegisterCommand{ListingID: listingID, SellerID: id, AccountID: account, GoldItemID: q.GoldItemID, PriceCera: q.PriceCera, DurationHour: q.DurationHour, HasCoupon: s.currentGoldConsignHasCoupon(session)})
		if e != nil {
			return fail(e)
		}
		s.logGameEvent(session, "金币寄售上架成功", "listing_id", listingID, "gold", result.Listing.GoldAmount, "cera", q.PriceCera, "fee_percent", result.Listing.FeePercent)
		if e = s.sendGameUpperSuccess(session, opcode, []byte{1}); e != nil {
			return e
		}
		return s.sendGameUpperRawClass(session, 14, buildCurrentGoldStateBody(result.GoldAfter), 0)
	case uint16(dnfenum.CmdPacketAuctionRegistCancel):
		listingID, e := decodeCurrentGoldConsignCancelRequest(body)
		if e != nil {
			return fail(e)
		}
		result, e := owner.Cancel(ctx, id, account, listingID)
		if e != nil {
			return fail(e)
		}
		s.notifyGoldConsignMailbox(id)
		s.notifyCurrentAuctionNoticeID(id, currentAuctionNoticeRouteGold, currentAuctionNoticeKindCancelBid, result.Listing.GoldItemID)
		return s.sendGameUpperSuccess(session, opcode, []byte{1})
	case uint16(dnfenum.CmdPacketAuctionBidding):
		q, e := decodeCurrentGoldConsignBuyRequest(body)
		if e != nil {
			return fail(e)
		}
		result, e := owner.Buy(ctx, dnfauction.GoldConsignBuyCommand{ListingID: q.ListingID, BuyerID: id, AccountID: account, ExpectedCera: q.ExpectedCera, GoldItemID: int64(q.GoldItemID)})
		if e != nil {
			return fail(e)
		}
		if _, e = owner.Maintain(ctx); e != nil {
			s.logGameEvent(session, "金币寄售成交邮件暂缓", "listing_id", q.ListingID, "error", e)
		}
		s.notifyGoldConsignMailbox(id)
		s.notifyGoldConsignMailbox(result.Listing.SellerID)
		if !result.Repeated {
			s.notifyCurrentAuctionNoticeID(result.Listing.SellerID, currentAuctionNoticeRouteGold, currentAuctionNoticeKindSellerSold, result.Listing.GoldItemID)
			s.notifyCurrentAuctionNoticeID(id, currentAuctionNoticeRouteGold, currentAuctionNoticeKindBuyerWon, result.Listing.GoldItemID)
		}
		if e = s.sendGameUpperSuccess(session, opcode, []byte{1}); e != nil {
			return e
		}
		return s.sendCurrentAccountWallet(session)
	case uint16(dnfenum.CmdPacketAuctionSearchByItemkey):
		page, items, ok := decodeCurrentAuctionSearchBody(body, 1)
		if !ok && len(body) == 10 && body[7] == 0 {
			page = binary.LittleEndian.Uint32(body[1:5])
			ok = true
		}
		if !ok {
			return fail(dnfauction.ErrInvalidRequest)
		}
		ids := make([]int64, len(items))
		for n, v := range items {
			if !dnfauction.IsGoldConsignTier(int64(v)) {
				return fail(dnfauction.ErrGoldConsignNotTier)
			}
			ids[n] = int64(v)
		}
		listings, e := owner.Search(ctx, ids)
		if e != nil {
			return fail(e)
		}
		return s.sendGameUpperSuccess(session, opcode, buildCurrentGoldConsignSearchBody(listings, page, 100))
	case uint16(dnfenum.CmdPacketAuctionSearchByNoitemkey):
		if len(body) != currentAuctionGoldCategoryRequestSize {
			return fail(dnfauction.ErrInvalidRequest)
		}
		listings, e := owner.Search(ctx, nil)
		if e != nil {
			return fail(e)
		}
		return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketAuctionSearchByItemkey), buildCurrentGoldConsignSearchBody(listings, binary.LittleEndian.Uint32(body[1:5]), 100))
	case uint16(dnfenum.CmdPacketAuctionMyRegistedItemInfo):
		if len(body) != 1 {
			return fail(dnfauction.ErrInvalidRequest)
		}
		listings, e := owner.ListOwned(ctx, id)
		if e != nil {
			return fail(e)
		}
		return s.sendGameUpperSuccess(session, opcode, buildCurrentGoldConsignOwnedBody(listings, id))
	case uint16(dnfenum.CmdPacketAuctionMyBiddingInfo):
		if len(body) != 1 {
			return fail(dnfauction.ErrInvalidRequest)
		}
		return s.sendGameUpperSuccess(session, opcode, []byte{1, 0, 0})
	default:
		return fail(dnfauction.ErrInvalidRequest)
	}
}
