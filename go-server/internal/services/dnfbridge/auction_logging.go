package dnfbridge

import (
	"context"
	"strconv"
	"strings"
)

func selectedCharacterIDString(p *gameSession) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(int(p.selectedCharacterID))
}
func (s *Service) recordAuction(p *gameSession, event string, fields map[string]any) {
	args := []any{"操作", auctionEventName(event)}
	for k, v := range fields {
		args = append(args, k, v)
	}
	s.logGameEvent(p, "拍卖行事务", args...)
}
func activityRejectionFields(reason string) map[string]any {
	return map[string]any{"result": "rejected", "reason": reason}
}

func activityIdentityOverride(fields map[string]any, accountID, characterID string) {
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		fields["account_id"] = accountID
	}
	if characterID = strings.TrimSpace(characterID); characterID != "" {
		fields["character_id"] = characterID
	}
}

func (s *Service) activityCharacterName(p *gameSession) string {
	if p == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	_, name, _, ok := s.selectedCharacterForEnter(ctx, p)
	if !ok {
		return ""
	}
	return name
}

func auctionEventName(event string) string {
	switch event {
	case "list":
		return "上架"
	case "cancel":
		return "下架"
	case "bid":
		return "竞拍出价"
	case "buyout":
		return "一口价成交"
	case "won":
		return "竞拍成交"
	case "payout":
		return "卖家领取收益"
	default:
		return event
	}
}
