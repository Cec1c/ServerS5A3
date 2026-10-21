package dnfbridge

import "log/slog"

type fatiguePotionCallStage uint8

const (
	fatiguePotionOwnerCommitted fatiguePotionCallStage = iota
	fatiguePotionACKCallFailed
	fatiguePotionACKCallReturnedOK
	fatiguePotionInventoryRefreshCallFailed
	fatiguePotionInventoryRefreshCallReturnedOK
	fatiguePotionFatigueCallFailed
	fatiguePotionFatigueCallReturnedOK
)

func (s fatiguePotionCallStage) String() string {
	switch s {
	case fatiguePotionOwnerCommitted:
		return "owner_committed"
	case fatiguePotionACKCallFailed:
		return "ack_call_failed"
	case fatiguePotionACKCallReturnedOK:
		return "ack_call_returned_ok"
	case fatiguePotionInventoryRefreshCallFailed:
		return "inventory_refresh_call_failed"
	case fatiguePotionInventoryRefreshCallReturnedOK:
		return "inventory_refresh_call_returned_ok"
	case fatiguePotionFatigueCallFailed:
		return "fatigue_call_failed"
	case fatiguePotionFatigueCallReturnedOK:
		return "fatigue_call_returned_ok"
	default:
		return "unknown"
	}
}

type fatiguePotionCallProgress struct {
	stage                         fatiguePotionCallStage
	ackOK, inventoryOK, fatigueOK bool
}

// One application event per committed owner result, even when a later service
// call returns an error. A nil call result may include suppression/buffering or
// a short write: these fields do NOT assert socket delivery or client receipt.
// Only typed identifiers/counts and finite stages enter this independent helper.
func (s *Service) logFatiguePotionCommitted(characterID uint16, channel int, slot int16, itemID int64, action uint32, remaining int64, calls fatiguePotionCallProgress) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Info("dnfbridge fatigue potion committed",
		slog.String("kind", "game-fatigue-potion-committed"),
		slog.Uint64("char_id", uint64(characterID)),
		slog.Int("channel_id", channel),
		slog.Int("slot", int(slot)),
		slog.Int64("item_id", itemID),
		slog.Uint64("action", uint64(action)),
		slog.Bool("committed", true),
		slog.Int64("before", remaining+1),
		slog.Int64("after", remaining),
		slog.String("stage", calls.stage.String()),
		slog.Bool("ack_call_returned_ok", calls.ackOK),
		slog.Bool("inventory_refresh_call_returned_ok", calls.inventoryOK),
		slog.Bool("fatigue_call_returned_ok", calls.fatigueOK))
}
