package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The paired PVF town/GuildAgit.twn resolves its areas to this map directory.
// Callers pass the map resolved from the runtime town catalog, never wire text.
func townMapPathLooksLikeGuildHall(mapPath string) bool {
	value := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(mapPath), "\\", "/"))
	value = strings.TrimPrefix(value, "map/")
	return strings.HasPrefix(value, "guildagit/") && strings.HasSuffix(value, ".map")
}

// Guild IDs are scoped by town/area/channel in the existing presence key, so
// they cannot collide with character IDs in a different Seria-room map.
func (s *Service) currentTownAreaInstance(ctx context.Context, session *gameSession, character dnfrepo.CharacterRecord, mapPath string) (uint32, error) {
	if !townMapPathLooksLikeGuildHall(mapPath) {
		return townAreaInstanceForMapPath(mapPath, session.selectedCharacterID), nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || character.CharacterID != strconv.Itoa(int(identity.character)) || character.AccountID != s.accountIDForSession(session) {
		return 0, dnfguild.ErrCharacterNotFound
	}
	_, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return 0, err
	}
	guild, member, found := registry.GuildForCharacter(character.CharacterID)
	if !found || guild.ID == 0 || member.AccountID != character.AccountID {
		return 0, dnfguild.ErrNotMember
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return 0, dnfguild.ErrCharacterNotFound
	}
	return guild.ID, nil
}

// Matched EXE callback RVA 0x01bd08d0 reads town and area after every failed
// command-36 header, including its generic failure branch. A bare error byte
// would leave this reader short. Do not use a quest/level-specific error here.
func (s *Service) rejectCurrentGuildHallMove(session *gameSession, request currentTownSetUserAreaRequest) error {
	s.retireCurrentGuildHallPresence(session)
	return s.sendGameUpperRawClass(session, 36, []byte{0, 1, request.TownID, request.AreaID}, 1)
}

func (s *Service) currentGuildHallArea(townID, areaID byte) bool {
	area, found := s.townArea(int64(townID), int64(areaID))
	return found && townMapPathLooksLikeGuildHall(area.MapPath)
}

// A failed refresh must not leave the old hall presence available to chat,
// movement or interaction lookups. Never retire a replacement connection.
func (s *Service) retireCurrentGuildHallPresence(session *gameSession) {
	if s == nil || s.onlinePlayers == nil || session == nil {
		return
	}
	actor, found := s.onlinePlayers.PlayerForCharacter(session.selectedCharacterID)
	if !found || actor.Session != session || !s.currentGuildHallArea(actor.TownID, actor.AreaID) {
		return
	}
	_, peers, removed := s.onlinePlayers.LeaveAreaSession(session.selectedCharacterID, session)
	if removed {
		s.broadcastTownPlayerLeave(session.selectedCharacterID, peers)
	}
}
