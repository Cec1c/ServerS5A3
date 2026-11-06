package dnfbridge

import (
	"context"
	"errors"
	"fmt"

	"longheng.io/server/internal/modules/dnf/wedding"
)

func currentWeddingCeremonyFailureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "repository_timeout"
	case errors.Is(err, wedding.ErrOwnership):
		return "ownership"
	case errors.Is(err, wedding.ErrUnavailable):
		return "resource_unavailable"
	case errors.Is(err, wedding.ErrState):
		return "state_revalidation"
	case errors.Is(err, wedding.ErrItem):
		return "gift_unavailable"
	default:
		return "persistence_failed"
	}
}

func (s *Service) currentWeddingChurchPresenceFailure(a, b boundGameSessionCharacter, route wedding.CeremonyRoute) string {
	if s.onlinePlayers == nil || !route.Valid() {
		return "presence_unavailable"
	}
	for index, owner := range []boundGameSessionCharacter{a, b} {
		prefix := "first_"
		if index == 1 {
			prefix = "second_"
		}
		if !s.boundGameSessionCharacterCurrent(owner) {
			return prefix + "owner_unavailable"
		}
		p, found := s.onlinePlayers.PlayerForCharacter(owner.character)
		if !found || p.Session != owner.session {
			return prefix + "presence_owner"
		}
		if p.TownID != 39 || p.AreaID != route.Area || p.ChannelID != int(route.Channel) || p.Instance != weddingPairInstance(a.character, b.character) {
			return prefix + "church_scope"
		}
		if !s.currentWeddingChurchTownSceneCurrent(owner, p, route) {
			return prefix + "town_scene_owner"
		}
	}
	return "church_presence_changed"
}

func weddingFinishLoadingMask(session *gameSession) byte {
	gates := session.finishLoadingGateSnapshot()
	var mask byte
	if gates.State {
		mask |= 1
	}
	if gates.Completion {
		mask |= 2
	}
	if gates.PostFinish {
		mask |= 4
	}
	return mask
}

// Caller holds wedding.mu. Log only changes to this reservation's fixed reason
// and scene proof, without names, account IDs, packet bodies or raw errors.
func (s *Service) logCurrentWeddingCeremonyDiagnosticLocked(ceremony *currentWeddingCeremony, a, b boundGameSessionCharacter, reason, outcome string, firstSent, secondSent bool) {
	var first, second onlinePlayerInfo
	if s.onlinePlayers != nil {
		first, _ = s.onlinePlayers.PlayerForCharacter(a.character)
		second, _ = s.onlinePlayers.PlayerForCharacter(b.character)
	}
	firstMask, secondMask := weddingFinishLoadingMask(a.session), weddingFinishLoadingMask(b.session)
	key := fmt.Sprint(reason, ":", outcome, ":", a.generation, ":", b.generation, ":", first.presenceGeneration, ":", second.presenceGeneration,
		":", firstMask, ":", secondMask, ":", firstSent, ":", secondSent)
	if ceremony.lastDiagnostic == key {
		return
	}
	ceremony.lastDiagnostic = key
	s.logInfo("dnfbridge wedding ceremony diagnostic", "event", "game-wedding-ceremony-progress",
		"first_char_id", ceremony.first.CharacterID, "second_char_id", ceremony.second.CharacterID,
		"ceremony_at", ceremony.at, "channel_id", ceremony.route.Channel, "area_id", ceremony.route.Area,
		"reason", reason, "outcome", outcome,
		"first_generation", a.generation, "second_generation", b.generation,
		"first_presence_generation", first.presenceGeneration, "second_presence_generation", second.presenceGeneration,
		"first_town", first.TownID, "first_area", first.AreaID, "first_channel", first.ChannelID, "first_instance", first.Instance,
		"second_town", second.TownID, "second_area", second.AreaID, "second_channel", second.ChannelID, "second_instance", second.Instance,
		"first_loading_mask", firstMask, "second_loading_mask", secondMask,
		"first_begin_sent", firstSent, "second_begin_sent", secondSent)
}
