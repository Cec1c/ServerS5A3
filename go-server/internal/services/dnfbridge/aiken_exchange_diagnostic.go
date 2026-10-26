package dnfbridge

import "encoding/binary"

// Keep the upgrade-window exchange observable when full packet logging is off.
// Record committed quantities and serialized row cores, never item payloads.
func (s *Service) logAikenExchange(g *gameSession, request currentNPCShopBuyRequest, kind string, fields ...any) {
	if s.logger == nil || g == nil || request.ItemID != 10161536 || request.ShopContext != 247 {
		return
	}
	attrs := []any{"conn_id", g.connID, "char_id", g.selectedCharacterID,
		"item_id", request.ItemID, "requested_count", request.Count,
		"shop_context", request.ShopContext, "actor_context", request.ActorContext}
	s.logger.Info(kind, append(attrs, fields...)...)
}

func (s *Service) logAikenExchangeCommitted(g *gameSession, request currentNPCShopBuyRequest, result currentNPCShopBuyResult) {
	if s.logger == nil || g == nil || request.ItemID != 10161536 || request.ShopContext != 247 {
		return
	}
	slots := make([]uint16, 0, len(result.Updates))
	items := make([]uint32, 0, len(result.Updates))
	amounts := make([]uint32, 0, len(result.Updates))
	for _, entry := range result.Updates {
		slots = append(slots, binary.LittleEndian.Uint16(entry.data[0:2]))
		items = append(items, binary.LittleEndian.Uint32(entry.data[2:6]))
		amounts = append(amounts, binary.LittleEndian.Uint32(entry.data[6:10]))
	}
	s.logAikenExchange(g, request, "aiken-exchange-committed", "slot", result.Slot,
		"ack_quantity", binary.LittleEndian.Uint32(result.Item.data[6:10]),
		"cost_item_id", result.CostItemID, "cost_remaining", result.CostItemNewCount,
		"update_slots", slots, "update_item_ids", items, "update_amounts", amounts)
}
