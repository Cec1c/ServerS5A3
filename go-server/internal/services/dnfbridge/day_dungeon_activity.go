package dnfbridge

import (
	"fmt"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// Enable the weekday flag only with all five actual published PVF schedules.
// Missing or changed assets cannot silently enable unrelated dungeons.
func (s *Service) currentDayDungeonEvent() uint16 {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return 0
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil || archive == nil {
		return 0
	}
	for id, expected := range dnfdungeon.DayDungeonSchedules() {
		path := fmt.Sprintf("dungeon/gcontents/171116_dayofdungeon/chn_daydungeon_%02d.dgn", id-4905)
		text, _, err := readInitialPVFText(archive, path)
		if err != nil {
			return 0
		}
		doc, err := dnfpvf.Parse(path, text)
		if err != nil {
			return 0
		}
		actual := doc.Ints("schedule")
		if len(actual) != len(expected) {
			return 0
		}
		for i, event := range expected {
			if actual[i] != event {
				return 0
			}
		}
	}
	return dnfdungeon.DayDungeonEvent(s.gameplayNow())
}

func (s *Service) armCurrentDayDungeonCatalogRefresh(session *gameSession) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 || session.activityCatalogDayEvent == 0 {
		return nil
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return errGameplayTimeQueueUnavailable
	}
	characterID, generation, err := gameSessionCharacterEventIdentity(session, session.selectedCharacterID)
	if err != nil {
		return err
	}
	now := s.gameplayNow()
	next := time.Unix((premium.ServiceDayID(now)+1)*86400-7200, 0)
	name := fmt.Sprintf("dnf-day-dungeon-catalog:%s", session.connID)
	return queue.ScheduleAfter(name, next.Sub(now), func(time.Time) {
		err := s.postGameSessionCharacterEvent(session, "day-dungeon-service-day-refresh", characterID, generation, func() error {
			if session.selectedCharacterID != characterID || session.characterGeneration != generation {
				return nil
			}
			catalog, available, err := s.currentSpendTimeRuntimeCatalogIfAvailable(nil)
			if err != nil || !available || catalog == nil {
				return err
			}
			session.spendTime.mu.Lock()
			online, completed := session.spendTime.onlineSeconds, session.spendTime.completedStages
			session.spendTime.mu.Unlock()
			_, err = s.sendCurrentSpendTimeProtocolState(session, catalog, online, completed, false)
			return err
		})
		if err != nil && !isClosedGameSessionEventError(err) {
			s.logGameEvent(session, "game-day-dungeon-refresh-deferred", "error", err)
		}
	})
}

func (s *Service) cancelCurrentDayDungeonCatalogRefresh(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	s.mu.Lock()
	queue := s.gameplayTimers
	s.mu.Unlock()
	if queue != nil {
		queue.Cancel(fmt.Sprintf("dnf-day-dungeon-catalog:%s", session.connID))
	}
}
