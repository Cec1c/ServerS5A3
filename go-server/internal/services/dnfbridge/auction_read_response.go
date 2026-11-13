package dnfbridge

import (
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// A read response owns its fully built body. No repository, session search state
// or mutable listing is consulted after releasing auctionMu. Encoding and the
// requester's socket write still run on that requester's original event loop.
type currentAuctionReadResponse struct {
	opcode         uint16
	classification byte
	body           []byte
}

func currentAuctionReadRaw(opcode uint16, body []byte, classification byte) currentAuctionReadResponse {
	return currentAuctionReadResponse{opcode: opcode, classification: classification, body: append([]byte(nil), body...)}
}

func currentAuctionReadSuccess(opcode uint16, body []byte) currentAuctionReadResponse {
	return currentAuctionReadRaw(opcode, upperSuccessBody(body), dnfproto.DefaultChannelClassification)
}

func currentAuctionReadFailure(opcode uint16, code byte) currentAuctionReadResponse {
	body := []byte{0, code}
	if currentAuctionFailureNeedsPayFeature(opcode, code) {
		body = append(body, currentAuctionPayTypeGold)
	}
	return currentAuctionReadRaw(opcode, body, dnfproto.DefaultChannelClassification)
}

func (r currentAuctionReadResponse) send(s *Service, session *gameSession) error {
	return s.sendGameUpperRawClass(session, r.opcode, r.body, r.classification)
}

func isCurrentAuctionReadCommand(opcode uint16) bool {
	switch dnfenum.CmdPacket(opcode) {
	case dnfenum.CmdPacketAuctionSearchByItemkey,
		dnfenum.CmdPacketAuctionSearchByNoitemkey,
		dnfenum.CmdPacketAuctionAskAveragePrice,
		dnfenum.CmdPacketAuctionMyRegistedItemInfo,
		dnfenum.CmdPacketAuctionMyBiddingInfo:
		return true
	default:
		return false
	}
}

// Existing owned-list recovery and gold-consignment maintenance may commit
// assets while preparing a query. They retain the same lock and transaction
// boundaries. Only the final requester response leaves this critical section.
func (s *Service) prepareCurrentAuctionReadCommand(session *gameSession, opcode uint16, body []byte) currentAuctionReadResponse {
	s.auctionMu.Lock()
	defer s.auctionMu.Unlock()
	defer s.scheduleCurrentAuctionMaintenanceLocked(30 * time.Second)
	if len(body) > 0 && body[0] == currentAuctionPayTypeConsignment {
		return s.prepareCurrentGoldConsignReadLocked(session, opcode, body)
	}
	switch dnfenum.CmdPacket(opcode) {
	case dnfenum.CmdPacketAuctionSearchByItemkey:
		s.rememberCurrentAuctionSearchLocked(session, opcode, body)
		return s.prepareCurrentAuctionItemsLocked(session, body)
	case dnfenum.CmdPacketAuctionSearchByNoitemkey:
		s.rememberCurrentAuctionSearchLocked(session, opcode, body)
		return s.prepareCurrentAuctionCategoryLocked(session, body)
	case dnfenum.CmdPacketAuctionAskAveragePrice:
		return s.prepareCurrentAuctionAveragePriceLocked(session, body)
	case dnfenum.CmdPacketAuctionMyRegistedItemInfo:
		return s.prepareCurrentAuctionOwnedItemsLocked(session, body)
	case dnfenum.CmdPacketAuctionMyBiddingInfo:
		return s.prepareCurrentAuctionBiddingItemsLocked(session, body)
	default:
		return currentAuctionReadFailure(opcode, currentAuctionFailureCode)
	}
}
