package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

const currentRaidWeeklyAvailabilityMsgID uint16 = 1442

// This is an explicit service policy, not a weekday inferred from packet1442.
// Deployment defaults to Thursday06:00 UTC+8. Existing v1 counters retain their
// recorded epoch; no old history is synthesized when the v1 owner has no rows.
func currentRaidWeeklyPolicy() (raid.WeeklyPolicy, error) {
	policy := raid.WeeklyPolicy{ResetWeekday: time.Thursday, ResetHour: 6}
	for _, field := range []struct {
		name string
		max  int
		set  func(int)
	}{
		{"DNFBRIDGE_RAID_WEEKLY_RESET_DAY", 6, func(v int) { policy.ResetWeekday = time.Weekday(v) }},
		{"DNFBRIDGE_RAID_WEEKLY_RESET_HOUR", 23, func(v int) { policy.ResetHour = v }},
	} {
		raw := strings.TrimSpace(os.Getenv(field.name))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > field.max {
			return raid.WeeklyPolicy{}, fmt.Errorf("invalid raid weekly policy: %s", field.name)
		}
		field.set(value)
	}
	return policy, nil
}

func (s *Service) sendCurrentRaidWeeklyAvailability(session *gameSession, source string) error {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" || !s.currentRaidAuthenticated(session) {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	repos, available := s.repositoryGroup()
	if !bound || !available || repos.Character == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil {
		return err
	}
	if !found || character.AccountID != s.accountIDForSession(session) || !s.boundGameSessionCharacterCurrent(identity) {
		return fmt.Errorf("raid weekly availability owner changed")
	}
	policy, err := currentRaidWeeklyPolicy()
	if err != nil {
		return err
	}
	now := time.Now()
	used := make(map[byte]int, 3)
	for mode := byte(1); mode <= 3; mode++ {
		count, err := raid.WeeklyClears(character, mode, now, policy)
		if err != nil {
			return err
		}
		used[mode] = count
	}
	body, err := raid.BuildWeeklyClearBody(used)
	if err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return fmt.Errorf("raid weekly availability generation changed")
	}
	if err := s.sendGameUpperRawClass(session, currentRaidWeeklyAvailabilityMsgID, body, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-raid-weekly-availability-sent", "char_id", identity.character,
		"msg_id", currentRaidWeeklyAvailabilityMsgID, "classification", 0, "body_len", len(body),
		"anton_used", used[1], "luke_used", used[2], "fiendwar_used", used[3], "source", source)
	return nil
}
