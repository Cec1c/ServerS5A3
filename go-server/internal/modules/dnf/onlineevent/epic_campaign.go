package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

const EpicCampaignScope = "server:epic-road-campaign:v1"
const EpicCampaignVersion = 1

// New local policy: first actual enabled owner establishes 28 Chinese activity
// days beginning06:00. Restart/control changes never restart or extend it.
type EpicCampaign struct {
	Version   int       `json:"version"`
	Starts    time.Time `json:"starts"`
	Ends      time.Time `json:"ends"`
	CreatedAt time.Time `json:"created_at"`
}

func epicCampaignStart(now time.Time) time.Time {
	d := now.In(ChinaCalendar).Add(-6 * time.Hour)
	day := time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, ChinaCalendar)
	return day
}
func (s EpicCampaign) Validate() error {
	if s.Version != EpicCampaignVersion || s.Starts.IsZero() || s.Ends.IsZero() || s.CreatedAt.IsZero() || !s.Starts.Equal(epicCampaignStart(s.CreatedAt)) || !s.Ends.Equal(s.Starts.AddDate(0, 0, 28)) {
		return fmt.Errorf("epic campaign season record invalid")
	}
	return nil
}
func (s EpicCampaign) Active(now time.Time) bool {
	return s.Validate() == nil && !now.Before(s.CreatedAt) && !now.Before(s.Starts) && now.Before(s.Ends)
}
func (s EpicCampaign) Key() string {
	return "event_epic_road_v1:" + s.Starts.UTC().Format("20060102T150405Z")
}
func LoadEpicCampaign(ctx context.Context, settings repo.SettingsRepository) (EpicCampaign, bool, error) {
	if settings == nil {
		return EpicCampaign{}, false, ErrOwnerUnavailable
	}
	record, found, err := settings.Load(ctx, EpicCampaignScope)
	if err != nil || !found {
		return EpicCampaign{}, found, err
	}
	if record.Scope != EpicCampaignScope {
		return EpicCampaign{}, false, fmt.Errorf("epic campaign season scope mismatch")
	}
	var season EpicCampaign
	if err := json.Unmarshal([]byte(record.Values["season"]), &season); err != nil {
		return EpicCampaign{}, false, err
	}
	if err := season.Validate(); err != nil {
		return EpicCampaign{}, false, err
	}
	return season, true, nil
}

// A narrow adapter uses the existing transaction's bound settings namespace;
// no account, character, inventory or equipment writes are performed.
func ActivateEpicCampaign(ctx context.Context, repos repo.Group, accountID, characterID string, now time.Time, validate func() bool) (EpicCampaign, error) {
	if repos.CeraShopAssets == nil || now.IsZero() || strings.TrimSpace(accountID) == "" || strings.TrimSpace(characterID) == "" || validate == nil || !validate() {
		return EpicCampaign{}, ErrOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result EpicCampaign
	err := repos.CeraShopAssets.WithinCeraShopAssets(ctx, accountID, characterID, EpicCampaignScope, func(accounts repo.AccountRepository, characters repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, settings repo.SettingsRepository) (callbackErr error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				callbackErr = fmt.Errorf("epic campaign activation callback panic: %v", recovered)
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
			return fmt.Errorf("epic campaign activation character owner mismatch")
		}
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != accountID {
			return fmt.Errorf("epic campaign activation account owner mismatch")
		}
		season, found, err := LoadEpicCampaign(ctx, settings)
		if err != nil {
			return err
		}
		if found {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !validate() {
				return ErrOwnerUnavailable
			}
			result = season
			return nil
		}
		result = EpicCampaign{Version: EpicCampaignVersion, Starts: epicCampaignStart(now), CreatedAt: now.UTC()}
		result.Ends = result.Starts.AddDate(0, 0, 28)
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
		if err := settings.Save(ctx, repo.SettingsRecord{Scope: EpicCampaignScope, Values: map[string]string{"season": string(raw)}, UpdatedAt: now.UTC()}); err != nil {
			return err
		}
		if !validate() {
			return ErrOwnerUnavailable
		}
		return nil
	})
	if err != nil {
		return EpicCampaign{}, err
	}
	return result, nil
}
