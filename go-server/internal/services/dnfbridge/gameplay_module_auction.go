package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

func auctionGameplayModule() gameplayModuleDefinition {
	handler := func(service *Service, session *gameSession, request gameplayRequest) error {
		return service.handleCurrentAuctionCommand(session, request.Opcode, request.Body)
	}
	opcodes := []uint16{
		uint16(dnfenum.CmdPacketAuctionAskAveragePrice),
		uint16(dnfenum.CmdPacketAuctionRegistItem),
		uint16(dnfenum.CmdPacketAuctionRegistCancel),
		uint16(dnfenum.CmdPacketAuctionBidding),
		uint16(dnfenum.CmdPacketAuctionSearchByItemkey),
		uint16(dnfenum.CmdPacketAuctionSearchByNoitemkey),
		uint16(dnfenum.CmdPacketAuctionMyRegistedItemInfo),
		uint16(dnfenum.CmdPacketAuctionMyBiddingInfo),
		uint16(dnfenum.CmdPacketAuctionBuyItemApiece),
	}
	legacy := make(map[uint16]gameplayHandler, len(opcodes))
	upper := make(map[uint16]gameplayHandler, len(opcodes))
	for _, opcode := range opcodes {
		legacy[opcode] = handler
		upper[opcode] = defaultClassGameplayHandler("拍卖请求分类拒绝", "不是客户端请求", func(s *Service, p *gameSession, b []byte) error { return s.handleCurrentAuctionCommand(p, opcode, b) })
	}
	return gameplayModuleDefinition{Name: "auction", LegacyHandlers: legacy, UpperHandlers: upper}
}
