package dnfbridge

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
	"strings"
)

const (
	currentAuctionNoticeMsgID            uint16 = 169
	currentAuctionNoticeRouteItem        byte   = 0
	currentAuctionNoticeKindOutbid       byte   = 0
	currentAuctionNoticeKindSellerSold   byte   = 1
	currentAuctionNoticeKindBuyerWon     byte   = 2
	currentAuctionNoticeKindSellerFailed byte   = 3
	currentAuctionNoticeKindCancelBid    byte   = 5
)

func currentAuctionTradeable(d dungeonDropItemDefinition) bool {
	switch normalizeMagicBoxPVFType(d.AttachType) {
	case "free", "sealing":
		return true
	}
	return false
}
func buildCurrentAuctionNoticeBody(route, kind byte, itemID uint32) []byte {
	var body packetWriter
	body.writeByte(route)
	body.writeByte(kind)
	body.writeUint32(itemID)
	// FUN_01d80150 reads this u16 into a short[3]; only count[0] is consulted,
	// and only to decide whether to run one extra UI refresh. Zero is the quiet
	// value and is what every notice so far should carry.
	body.writeUint16(0)
	return body.bytes()
}

func (s *Service) sendCurrentAuctionNoticeToOnlineRecipient(characterID uint16, route, kind byte, itemID int64) error {
	if s == nil || characterID == 0 || itemID <= 0 || itemID > math.MaxUint32 {
		return nil
	}
	session, online := s.onlineGameSession(characterID)
	if !online {
		return nil
	}
	return s.sendGameUpperRawClass(
		session,
		currentAuctionNoticeMsgID,
		buildCurrentAuctionNoticeBody(route, kind, uint32(itemID)),
		0,
	)
}

func (s *Service) notifyCurrentAuctionNoticeID(characterID string, route, kind byte, itemID int64) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(characterID), 10, 16)
	if err != nil || parsed == 0 {
		return
	}
	if err := s.sendCurrentAuctionNoticeToOnlineRecipient(uint16(parsed), route, kind, itemID); err != nil {
		s.logGameEvent(nil, "game-auction-notice-failed",
			"recipient", characterID, "route", route, "kind", kind, "item_id", itemID, "error", err)
	}
}

func currentAuctionSealRequired(definition dungeonDropItemDefinition) bool {
	return normalizeMagicBoxPVFType(definition.AttachType) == "sealing"
}

func currentAuctionSealed(stack dnfrepo.ItemStack) bool {
	return currentItemListEntryFromStack(0, 0, stack).data[0x0d] == 1
}
