package dnfbridge

import (
	"strconv"
	"strings"
)

// The accepted-final-room flag is also used by hell and tournament clears.
// Apply the approved ordinary-only rule separately, using the frozen entry
// level and actual DGN metadata. Legacy ordinary scripts omit dungeon type.
func currentDungeonEarnsGuildMileage(r *runtimeDungeonState) bool {
	if !currentDungeonIsRecommendedClear(r) || r.entryCharacterLevel <= 0 || r.tutorialFinalRoomClearAccepted ||
		r.Dungeon.ID == 5000 || r.Tournament != nil || r.IgnoreDefaultDungeonClear ||
		r.raidEntry != nil || r.semesterRun != nil || r.epicEntry != nil ||
		dungeonRequestedHell(dungeonHellRequestOption(r.Request)) || r.Request.SpecialMode != 0 || r.HellPartyStarted || r.StartMapHellFogFlag != 0 {
		return false
	}
	m := r.Dungeon.Metadata
	if m.TutorialDungeon.Set && m.TutorialDungeon.Value != 0 {
		return false
	}
	if kind := strings.ToLower(strings.TrimSpace(m.DungeonType)); kind != "" && kind != "[normal]" && kind != "dungeon" {
		return false
	}
	for _, name := range guildMileageSpecialSections {
		if m.Flags[name] || m.IntegerValues[name] != 0 {
			return false
		}
		for _, section := range r.Dungeon.SourceSections {
			if strings.EqualFold(section.Name, name) {
				return false
			}
		}
	}
	return true
}

var guildMileageSpecialSections = []string{
	"guild dungeon", "raid dungeon", "raid normal dungeon", "normal luke dungeon", "tournament dungeon", "tower of despair", "death tower map indexes",
	"defense dungeon", "blood dungeon", "dimension dungeon", "powerwar dungeon", "risk dungeon",
	"ancient dungeon", "event dungeon", "crack of dimension dungeon", "special dungeon", "kronos dungeon",
	"sao dungeon", "inferno dungeon", "ignore default dungeon clear", "advance altar type", "lost land parameters",
}

// The settlement caller holds dungeon.mu. Do not reserve peers or acquire town
// locks here. Send the authoritative balance to this participant only; other
// members obtain the same committed pool through their existing guild refresh.
func (s *Service) refreshCurrentDungeonGuildMileage(session *gameSession, guildID uint32) {
	g, err := s.currentGuildForSelectedCharacter(session)
	if err == nil && g != nil && g.ID == guildID {
		m := g.Members[strconv.Itoa(int(session.selectedCharacterID))]
		err = s.sendGameUpperRawClass(session, 70, buildCurrentGuildManagementBody(g, len(s.currentGuildOnlineChannels(g, session)), s.gameplayNow(), m.Grade), 0)
	}
	if err != nil {
		s.logGameEvent(session, "game-guild-dungeon-mileage-refresh-failed", "error", err)
	}
}
