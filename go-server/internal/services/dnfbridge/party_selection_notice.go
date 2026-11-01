package dnfbridge

import (
	"context"
	"strconv"

	"longheng.io/server/internal/modules/dnf/party"
)

// Use the established native system-text channel. A walkout/kick notice would
// incorrectly describe a player's explicit character/channel selection.
func (s *Service) notifyRuntimePartySelectionDeparture(leaving *gameSession, result party.RuntimePartyResult, source string) {
	var action string
	switch source {
	case "channel_exit":
		action = "选择频道"
	case "return_select_character", "selected_character_changed":
		action = "选择角色"
	default:
		return
	}
	if !result.OK || result.Previous == nil {
		return
	}
	name := "一名队员"
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if repositories, ok := s.repositoryGroup(); ok && repositories.Character != nil {
		if c, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(result.TargetUser))); err == nil && found && c.Name != "" {
			name = c.Name
		}
	}
	var w packetWriter
	w.writeByte(0)
	w.writeUint16(0xffff)
	w.writeByte(0)
	w.writeRawDstr(rosterNameBytes(name + "已" + action + "并退出队伍。"))
	w.writeRawDstr(nil)
	for _, member := range result.Previous.Members {
		if member.UserID == result.TargetUser {
			continue
		}
		identity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || identity.session == leaving || identity.generation != member.SessionGeneration {
			continue
		}
		if err := s.sendGameUpperRawClass(identity.session, currentAreaChatNotifyOpcode, w.bytes(), 0); err != nil {
			s.logWarn("dnfbridge party selection departure notice failed", "source", source, "char_id", member.UserID, "error", err)
		}
	}
}
