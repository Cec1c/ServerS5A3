package pvp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const TotalTeamMetadataKey = "pvp_total_match_team_v1"

// Stable character IDs survive selector-slot reordering and character deletion.
type TotalTeam struct {
	Name       string    `json:"name"`
	Characters [3]string `json:"characters"`
}

type TotalTeamSnapshot struct {
	Team       TotalTeam
	Characters [3]repo.CharacterRecord
	Slots      [3]byte
}

func ValidateTotalTeamName(name string) error {
	if strings.TrimSpace(name) != name || len([]rune(name)) < 2 || len([]rune(name)) > 12 {
		return fmt.Errorf("invalid total-match team name length")
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return fmt.Errorf("invalid total-match team name character")
		}
	}
	return nil
}

func ReadTotalTeam(ctx context.Context, repos repo.Group, accountID string) (TotalTeamSnapshot, error) {
	result := TotalTeamSnapshot{Slots: [3]byte{255, 255, 255}}
	if repos.Account == nil || repos.Character == nil || accountID == "" {
		return result, fmt.Errorf("total-match repositories or account missing")
	}
	a, found, err := repos.Account.Load(ctx, accountID)
	if err != nil || !found {
		return result, err
	}
	if raw := a.Metadata[TotalTeamMetadataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &result.Team); err != nil {
			return result, fmt.Errorf("invalid persisted total-match team: %w", err)
		}
	}
	rows, err := repos.Character.ListByAccount(ctx, accountID, repo.DefaultCharacterSlotLimit)
	if err != nil {
		return result, err
	}
	for i, id := range result.Team.Characters {
		for _, c := range rows {
			if id != "" && c.CharacterID == id && c.AccountID == accountID && c.Stats["delete_flag"] == 0 && c.Slot >= 0 && c.Slot < repo.DefaultCharacterSlotLimit {
				result.Characters[i], result.Slots[i] = c, byte(c.Slot)
				break
			}
		}
	}
	return result, nil
}

// ResolveTotalTeam validates all three real account characters before any write.
func ResolveTotalTeam(accountID, name string, slots [3]byte, rows []repo.CharacterRecord, source pvf.Source) (TotalTeamSnapshot, error) {
	result := TotalTeamSnapshot{Team: TotalTeam{Name: name}, Slots: slots}
	if source == nil || accountID == "" {
		return result, fmt.Errorf("total-match source or account missing")
	}
	if err := ValidateTotalTeamName(name); err != nil {
		return result, err
	}
	text, err := source.ReadText("etc/pvpparameter.etc")
	if err != nil {
		return result, err
	}
	doc, err := pvf.Parse("etc/pvpparameter.etc", text)
	if err != nil {
		return result, err
	}
	pairs := doc.Ints("pvp matching possible character")
	if len(pairs) == 0 || len(pairs)%2 != 0 {
		return result, fmt.Errorf("missing PVP professions")
	}
	seen := map[string]bool{}
	professions := map[[2]int64]bool{}
	for i, slot := range slots {
		if int(slot) >= repo.DefaultCharacterSlotLimit {
			return result, fmt.Errorf("total-match selector slot out of range")
		}
		for _, c := range rows {
			if c.Slot == int(slot) && c.AccountID == accountID && c.Stats["delete_flag"] == 0 {
				result.Characters[i] = c
				break
			}
		}
		c := result.Characters[i]
		id, idErr := strconv.ParseUint(c.CharacterID, 10, 16)
		job, jobErr := strconv.ParseInt(c.Job, 10, 32)
		profession := [2]int64{job, c.Stats["grow_type"] & 15}
		allowed := false
		for n := 0; n < len(pairs); n += 2 {
			allowed = allowed || profession == [2]int64{pairs[n], pairs[n+1]}
		}
		if idErr != nil || id == 0 || id == 65535 || jobErr != nil || c.Level < 1 || c.Level > 255 || seen[c.CharacterID] || professions[profession] || !allowed {
			return result, fmt.Errorf("invalid total-match character in slot %d", slot)
		}
		seen[c.CharacterID], professions[profession] = true, true
		result.Team.Characters[i] = c.CharacterID
	}
	return result, nil
}

func SaveTotalTeam(ctx context.Context, accounts repo.AccountRepository, accountID string, team TotalTeam, now time.Time) error {
	a, found, err := accounts.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || a.AccountID != accountID {
		return fmt.Errorf("total-match account missing")
	}
	raw, err := json.Marshal(team)
	if err != nil {
		return err
	}
	return repo.SaveAccountMetadataEntry(ctx, accounts, a, TotalTeamMetadataKey, string(raw), now)
}
