package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

const WeeklySeasonScope = "server:weekly-attendance-season:v1"
const WeeklySeasonVersion = 1

// This is a new local operating policy: the first real enabled owner binds
// the ongoing Thursday06:00 Chinese week. Restarts never roll the season.
type WeeklySeason struct {
	Version   int       `json:"version"`
	Starts    time.Time `json:"starts"`
	Ends      time.Time `json:"ends"`
	CreatedAt time.Time `json:"created_at"`
}

func weeklySeasonStart(now time.Time) time.Time {
	d := now.In(ChinaCalendar).Add(-6 * time.Hour)
	day := time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, ChinaCalendar)
	back := (int(d.Weekday()) - int(time.Thursday) + 7) % 7
	return day.AddDate(0, 0, -back)
}
func (s WeeklySeason) Validate() error {
	if s.Version != WeeklySeasonVersion || s.Starts.IsZero() || s.Ends.IsZero() || s.CreatedAt.IsZero() || !s.Starts.Equal(weeklySeasonStart(s.CreatedAt)) || !s.Ends.Equal(s.Starts.AddDate(0, 0, 70)) {
		return fmt.Errorf("weekly season record invalid")
	}
	return nil
}
func (s WeeklySeason) Week(now time.Time) int {
	if s.Validate() != nil || now.Before(s.CreatedAt) || now.Before(s.Starts) || !now.Before(s.Ends) {
		return -1
	}
	return int(now.Sub(s.Starts) / (7 * 24 * time.Hour))
}
func (s WeeklySeason) Key() string {
	return "event_weekly_attendance_v1:" + s.Starts.UTC().Format("20060102T150405Z")
}
func LoadWeeklySeason(ctx context.Context, settings repo.SettingsRepository) (WeeklySeason, bool, error) {
	if settings == nil {
		return WeeklySeason{}, false, ErrOwnerUnavailable
	}
	record, found, err := settings.Load(ctx, WeeklySeasonScope)
	if err != nil || !found {
		return WeeklySeason{}, found, err
	}
	if record.Scope != WeeklySeasonScope {
		return WeeklySeason{}, false, fmt.Errorf("weekly season scope mismatch")
	}
	var season WeeklySeason
	if err := json.Unmarshal([]byte(record.Values["season"]), &season); err != nil {
		return WeeklySeason{}, false, err
	}
	if err := season.Validate(); err != nil {
		return WeeklySeason{}, false, err
	}
	return season, true, nil
}

// A narrow adapter uses the existing transaction's bound settings namespace;
// no account, character, inventory or equipment writes are performed.
func ActivateWeeklySeason(ctx context.Context, repos repo.Group, accountID, characterID string, now time.Time, validate func() bool) (WeeklySeason, error) {
	if repos.CeraShopAssets == nil || now.IsZero() || strings.TrimSpace(accountID) == "" || strings.TrimSpace(characterID) == "" || validate == nil || !validate() {
		return WeeklySeason{}, ErrOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result WeeklySeason
	err := repos.CeraShopAssets.WithinCeraShopAssets(ctx, accountID, characterID, WeeklySeasonScope, func(accounts repo.AccountRepository, characters repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, settings repo.SettingsRepository) (callbackErr error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				callbackErr = fmt.Errorf("weekly activation callback panic: %v", recovered)
			}
		}()
		if !validate() {
			return ErrOwnerUnavailable
		}
		char, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || char.CharacterID != characterID || char.AccountID != accountID {
			return fmt.Errorf("weekly activation character owner mismatch")
		}
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != accountID {
			return fmt.Errorf("weekly activation account owner mismatch")
		}
		season, found, err := LoadWeeklySeason(ctx, settings)
		if err != nil {
			return err
		}
		if found {
			result = season
			return nil
		}
		result = WeeklySeason{Version: WeeklySeasonVersion, Starts: weeklySeasonStart(now), CreatedAt: now.UTC()}
		result.Ends = result.Starts.AddDate(0, 0, 70)
		if err := result.Validate(); err != nil {
			return err
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if !validate() {
			return ErrOwnerUnavailable
		}
		if err := settings.Save(ctx, repo.SettingsRecord{Scope: WeeklySeasonScope, Values: map[string]string{"season": string(raw)}, UpdatedAt: now.UTC()}); err != nil {
			return err
		}
		if !validate() {
			return ErrOwnerUnavailable
		}
		return nil
	})
	if err != nil {
		return WeeklySeason{}, err
	}
	return result, nil
}
