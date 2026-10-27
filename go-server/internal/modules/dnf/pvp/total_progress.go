package pvp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const TotalProgressMetadataKey = "pvp_total_match_progress_v1"

type TotalMatchFacts struct {
	Match
	Kills        [3]uint32
	Remaining    int
	ReportsCount map[uint16]uint32
}

type TotalProgress struct {
	Stats map[string]int64 `json:"stats"`
	Last  *totalReceipt    `json:"last,omitempty"`
}

type totalReceipt struct {
	Key                           string              `json:"key"`
	CompletedAt                   int64               `json:"completed_at"`
	Characters                    [3]string           `json:"characters"`
	Before                        [3]map[string]int64 `json:"before"`
	After                         [3]map[string]int64 `json:"after"`
	BeforeTeam                    map[string]int64    `json:"before_team"`
	AfterTeam                     map[string]int64    `json:"after_team"`
	BeforeRows, Rows, ChangedRows []Row
	BeforeNormalRows, NormalRows  []Row
}

type TotalSnapshot struct {
	Rewards                       TotalRewards
	Characters                    [3]repo.CharacterRecord
	Before                        [3]repo.CharacterRecord
	Stats, BeforeStats            map[string]int64
	Rows, BeforeRows, ChangedRows []Row
	NormalRows, BeforeNormalRows  []Row
}

// TotalMissionSnapshot is account progress, independent of a registered team.
// Opening the native task UI must not require or invent three combat actors.
type TotalMissionSnapshot struct {
	Character        repo.CharacterRecord
	Stats            map[string]int64
	Rows, NormalRows []Row
	Day              int64
	EmblemsGranted   int64
}

func totalMissionCharacter(accountID string, stats map[string]int64) repo.CharacterRecord {
	return repo.CharacterRecord{CharacterID: accountID + ":total-match", Stats: stats}
}

func PrepareTotalMissions(ctx context.Context, repos repo.Group, c *Catalog, accountID, selected string, now time.Time) (TotalMissionSnapshot, error) {
	var result TotalMissionSnapshot
	if repos.RentalAssets == nil || c == nil || now.IsZero() {
		return result, fmt.Errorf("total-match mission dependencies missing")
	}
	err := repos.RentalAssets.WithinRentalAssets(ctx, accountID, selected,
		func(accounts repo.AccountRepository, characters repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
			account, found, err := accounts.Load(ctx, accountID)
			if err != nil {
				return err
			}
			if !found || account.AccountID != accountID {
				return fmt.Errorf("total-match account missing")
			}
			character, found, err := characters.Load(ctx, selected)
			if err != nil {
				return err
			}
			if !found || character.AccountID != accountID || character.Stats["delete_flag"] != 0 {
				return fmt.Errorf("total-match mission character owner mismatch")
			}
			p, err := ReadTotalProgress(account)
			if err != nil {
				return err
			}
			daily, _, err := c.snapshot(totalMissionCharacter(accountID, p.Stats), now)
			if err != nil {
				return err
			}
			p.Stats = daily.Character.Stats
			character = repo.CloneCharacter(character)
			if character.Stats == nil {
				character.Stats = map[string]int64{}
			}
			grant := p.Stats[EmblemsKey]
			if grant < 0 || character.Stats[EmblemsKey] < 0 || character.Stats[EmblemsKey] > math.MaxInt32-grant {
				return fmt.Errorf("total-match emblem overflow")
			}
			if grant > 0 {
				character.Stats[EmblemsKey] += grant
				if err := repo.SaveCharacterFields(ctx, characters, character, repo.CharacterFieldStats); err != nil {
					return err
				}
			}
			delete(p.Stats, EmblemsKey)
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if err := repo.SaveAccountMetadataEntry(ctx, accounts, account, TotalProgressMetadataKey, string(raw), now); err != nil {
				return err
			}
			result = TotalMissionSnapshot{Character: character, Stats: copyTotalStats(p.Stats), Rows: daily.Rows,
				NormalRows: daily.NormalRows, Day: daily.Day, EmblemsGranted: grant}
			return nil
		})
	return result, err
}

func ReadTotalProgress(account repo.AccountRecord) (TotalProgress, error) {
	var p TotalProgress
	if raw := account.Metadata[TotalProgressMetadataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return p, err
		}
	}
	if p.Stats == nil {
		p.Stats = map[string]int64{}
	}
	return p, nil
}

func copyTotalStats(src map[string]int64) map[string]int64 {
	dst := make(map[string]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func PrepareTotal(ctx context.Context, repos repo.Group, catalog *Catalog, accountID, selected string, roster [3]string, now time.Time) (TotalSnapshot, error) {
	return updateTotal(ctx, repos, catalog, accountID, selected, roster, now, nil)
}

func CompleteTotal(ctx context.Context, repos repo.Group, catalog *Catalog, accountID, selected string, roster [3]string, facts TotalMatchFacts) (TotalSnapshot, error) {
	if facts.Key == "" || facts.StartedAt.IsZero() || facts.CompletedAt.Before(facts.StartedAt) || facts.Remaining < 0 || facts.Remaining > 3 {
		return TotalSnapshot{}, fmt.Errorf("invalid total-match facts")
	}
	var kills uint32
	for _, n := range facts.Kills {
		if n > 3 {
			return TotalSnapshot{}, fmt.Errorf("invalid total-match kills")
		}
		kills += n
	}
	if kills > 3 {
		return TotalSnapshot{}, fmt.Errorf("invalid total-match total kills")
	}
	return updateTotal(ctx, repos, catalog, accountID, selected, roster, facts.CompletedAt, &facts)
}

func updateTotal(ctx context.Context, repos repo.Group, c *Catalog, accountID, selected string, roster [3]string, now time.Time, match *TotalMatchFacts) (TotalSnapshot, error) {
	var result TotalSnapshot
	uow, ok := repos.RentalAssets.(repo.PVPTotalUnitOfWork)
	if !ok || c == nil || c.RatingRules == nil || now.IsZero() {
		return result, fmt.Errorf("total-match progress dependencies missing")
	}
	selectedIndex := -1
	for i, id := range roster {
		if id == selected {
			selectedIndex = i
		}
	}
	if selectedIndex < 0 {
		return result, fmt.Errorf("selected character is outside total-match roster")
	}
	err := uow.WithinPVPTotal(ctx, accountID, roster, func(accounts repo.AccountRepository, characters repo.CharacterRepository) error {
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != accountID {
			return fmt.Errorf("total-match account missing")
		}
		p, err := ReadTotalProgress(account)
		if err != nil {
			return err
		}
		for i, id := range roster {
			v, found, err := characters.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || v.AccountID != accountID || v.Stats["delete_flag"] != 0 {
				return fmt.Errorf("total-match character owner mismatch")
			}
			v = repo.CloneCharacter(v)
			if v.Stats == nil {
				v.Stats = map[string]int64{}
			}
			result.Characters[i], result.Before[i] = v, repo.CloneCharacter(v)
		}
		if match != nil && p.Last != nil && p.Last.Key == match.Key {
			if p.Last.Characters != roster {
				return fmt.Errorf("total-match receipt roster mismatch")
			}
			for i := range roster {
				for k, v := range p.Last.Before[i] {
					result.Before[i].Stats[k] = v
				}
				for k, v := range p.Last.After[i] {
					result.Characters[i].Stats[k] = v
				}
			}
			result.Stats, result.BeforeStats = copyTotalStats(p.Last.AfterTeam), copyTotalStats(p.Last.BeforeTeam)
			result.Rows, result.BeforeRows, result.ChangedRows = p.Last.Rows, p.Last.BeforeRows, p.Last.ChangedRows
			// Rebuild from the frozen receipt facts, also covering receipts written
			// before total target rows were published.
			result.BeforeNormalRows, err = c.normalRows(totalMissionCharacter(accountID, result.BeforeStats))
			if err != nil {
				return err
			}
			result.NormalRows, err = c.normalRows(totalMissionCharacter(accountID, result.Stats))
			if err != nil {
				return err
			}
			return nil
		}
		if match != nil && p.Last != nil && p.Last.CompletedAt >= match.CompletedAt.UnixNano() {
			return fmt.Errorf("stale total-match completion")
		}
		// Daily selection is account-owned; this internal projection is never an
		// actor or a database character. It reuses the PVF weighted daily rules.
		daily, _, err := c.snapshot(totalMissionCharacter(accountID, p.Stats), now)
		if err != nil {
			return err
		}
		p.Stats = daily.Character.Stats
		result.BeforeStats = copyTotalStats(p.Stats)
		result.BeforeRows = append([]Row(nil), daily.Rows...)
		result.BeforeNormalRows = append([]Row(nil), daily.NormalRows...)
		if match != nil {
			for i := range roster {
				// The three real actors gain PVF total-match EXP. Their ordinary
				// personal rating/win/daily counters are untouched.
				m := match.Match
				m.Manual = true
				rules := *c.RatingRules
				if !match.Manual {
					rules.ManualExperiencePercent = rules.MatchExperiencePercent
				}
				actorCatalog := *c
				actorCatalog.RatingRules = &rules
				if _, err := actorCatalog.applyRating(result.Characters[i].Stats, m, 0); err != nil {
					return err
				}
			}
			if !match.Manual {
				played := p.Stats[CertificationKey]
				if played < 0 || played >= math.MaxInt32 {
					return fmt.Errorf("invalid total-match played count")
				}
				if _, err := c.applyRating(p.Stats, match.Match, played); err != nil {
					return err
				}
				p.Stats[CertificationKey] = played + 1
				key := LossesKey
				if match.Draw {
					key = DrawsKey
				} else if match.Won {
					key = WinsKey
				}
				if p.Stats[key] < 0 || p.Stats[key] >= math.MaxInt32 {
					return fmt.Errorf("invalid total-match result counter")
				}
				p.Stats[key]++
				if premium.ServiceDayID(match.StartedAt) == daily.Day {
					for tier, row := range daily.Rows {
						for _, m := range c.Pools[tier] {
							if m.ID != row.ID || row.Progress == row.Target {
								continue
							}
							advance := totalMissionAdvance(m, *match, p.Stats["pvp_win_streak"])
							if advance > 0 {
								row.Progress += min(advance, row.Target-row.Progress)
								p.Stats[dailyKey(tier, "progress")] = int64(row.Progress)
								daily.Rows[tier] = row
								result.ChangedRows = append(result.ChangedRows, row)
							}
						}
					}
				}
			}
		}
		if _, _, err := c.grantDailyEmblems(p.Stats); err != nil {
			return err
		}
		wallet := result.Characters[selectedIndex].Stats
		grant := p.Stats[EmblemsKey]
		if grant < 0 || wallet[EmblemsKey] < 0 || wallet[EmblemsKey] > math.MaxInt32-grant {
			return fmt.Errorf("total-match emblem overflow")
		}
		wallet[EmblemsKey] += grant
		delete(p.Stats, EmblemsKey)
		result.Stats, result.Rows = copyTotalStats(p.Stats), daily.Rows
		result.NormalRows, err = c.normalRows(totalMissionCharacter(accountID, p.Stats))
		if err != nil {
			return err
		}
		for i, row := range result.NormalRows {
			if row != result.BeforeNormalRows[i] {
				result.ChangedRows = append(result.ChangedRows, row)
			}
		}
		if match != nil {
			r := &totalReceipt{Key: match.Key, CompletedAt: match.CompletedAt.UnixNano(), Characters: roster, BeforeTeam: result.BeforeStats, AfterTeam: result.Stats, BeforeRows: result.BeforeRows, Rows: result.Rows, ChangedRows: result.ChangedRows}
			r.BeforeNormalRows, r.NormalRows = result.BeforeNormalRows, result.NormalRows
			for i := range roster {
				r.Before[i], r.After[i] = map[string]int64{}, map[string]int64{}
				for _, k := range []string{ExperienceKey, GradeKey, EmblemsKey, "pvp_max_grade", "pvp_result_before_exp", "pvp_result_before_grade", "pvp_result_experience_delta"} {
					r.Before[i][k], r.After[i][k] = result.Before[i].Stats[k], result.Characters[i].Stats[k]
				}
			}
			p.Last = r
		}
		for i := range roster {
			if match != nil || i == selectedIndex && grant > 0 {
				if err := repo.SaveCharacterFields(ctx, characters, result.Characters[i], repo.CharacterFieldStats); err != nil {
					return err
				}
			}
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return repo.SaveAccountMetadataEntry(ctx, accounts, account, TotalProgressMetadataKey, string(raw), now)
	})
	return result, err
}

func totalMissionAdvance(m Mission, match TotalMatchFacts, streak int64) uint32 {
	win := match.Won && !match.Draw
	switch m.Condition {
	case "hereafter join":
		return 1
	case "hereafter winning count":
		if win {
			return 1
		}
	case "total match win left character":
		if win && int64(match.Remaining) >= m.Parameter {
			return 1
		}
	case "kill count":
		return match.Kills[0] + match.Kills[1] + match.Kills[2]
	case "total match win all kill":
		if win {
			for _, n := range match.Kills {
				if n == 3 {
					return 1
				}
			}
		}
	case "winning streak":
		if win && m.Parameter > 0 && streak%m.Parameter == 0 {
			return 1
		}
	case "reduce hp":
		return min(match.ReportsCount[m.ID], 5)
	}
	return 0
}
