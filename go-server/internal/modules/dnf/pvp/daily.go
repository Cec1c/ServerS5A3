package pvp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	CertificationKey = "pvp_certification_count"
	WinsKey          = "pvp_wins"
	LossesKey        = "pvp_losses"
	DrawsKey         = "pvp_draws"
	MaxWinStreakKey  = "pvp_max_win_streak"
	RecentResultsKey = "pvp_recent_results"
	EmblemsKey       = "finish_loading_currency_slot2_total"
)

type Mission struct {
	ID                            uint16
	Tier                          int
	Weight                        int64
	RankMin, RankMax              int64
	Condition                     string
	Target                        uint32
	Parameter                     int64
	Jobs                          []int64
	Emblems                       int64
	RewardItemID, RewardItemCount int64
	Category                      string
	PreRequired                   uint16
	JobTag                        string
}

type Catalog struct {
	Pools [3][]Mission
	// Normal target cards are independent of the three daily pool indexes.
	Normal           []Mission
	ExperienceBounds []uint32
	RatingRules      *RatingRules
}

// Load reads ordinary daily pools and normal target cards. Total-match
// definitions have separate native grades and are not assigned here.
func Load(source pvf.Source) (*Catalog, error) {
	return loadCatalog(source, false)
}

func LoadTotal(source pvf.Source) (*Catalog, error) {
	return loadCatalog(source, true)
}

func loadCatalog(source pvf.Source, totalMatch bool) (*Catalog, error) {
	if source == nil {
		return nil, pvf.ErrSourceRequired
	}
	read := func(p string) (*pvf.Document, error) {
		s, err := source.ReadText(p)
		if err != nil {
			return nil, err
		}
		return pvf.Parse(p, s)
	}
	config, err := read("etc/missionsystem.etc")
	if err != nil {
		return nil, err
	}
	list, err := read("pvp_mission/mission.lst")
	if err != nil {
		return nil, err
	}
	paths := make(map[int64]string)
	for _, entry := range pvf.ParseList(list) {
		if _, exists := paths[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate PVP mission %d", entry.ID)
		}
		paths[entry.ID] = path.Join("pvp_mission", entry.Path)
	}
	c := &Catalog{}
	poolTag, gradeTag, modeTag := "casual daily mission", "[daily]", "[all]"
	if totalMatch {
		poolTag, gradeTag, modeTag = "total match daily mission", "[total match daily]", "[total match mode]"
	}
	for _, section := range config.Sections {
		if section.Name != poolTag {
			continue
		}
		var values []int64
		for _, token := range config.Tokens[section.Start:section.End] {
			if token.Kind == pvf.TokenInt {
				values = append(values, token.Int)
			}
		}
		if len(values) < 3 || len(values)%2 != 1 || values[0] < 0 || values[0] > 3 {
			return nil, fmt.Errorf("invalid PVF daily mission pool")
		}
		if values[0] == 3 {
			continue
		}
		tier := int(values[0])
		if len(c.Pools[tier]) != 0 {
			return nil, fmt.Errorf("duplicate daily tier %d", tier)
		}
		seen := map[int64]bool{}
		for i := 1; i < len(values); i += 2 {
			id, weight := values[i], values[i+1]
			if id <= 0 || id > math.MaxInt16 || weight <= 0 || weight > math.MaxInt32 || seen[id] || paths[id] == "" {
				return nil, fmt.Errorf("invalid daily mission %d", id)
			}
			seen[id] = true
			d, err := read(paths[id])
			if err != nil {
				return nil, err
			}
			grade, ranks, args := d.Ints("grade"), d.Ints("rank range"), d.Ints("condition")
			kind, _ := d.Text("grade")
			tags := d.Texts("condition")
			if kind != gradeTag || len(grade) != 1 || grade[0] != int64(tier) || len(ranks) != 2 ||
				ranks[0] < 0 || ranks[1] < ranks[0] || len(tags) != 2 || tags[1] != modeTag ||
				len(args) < 2 || args[0] <= 0 || args[0] > math.MaxInt32 {
				return nil, fmt.Errorf("invalid daily mission definition %s", paths[id])
			}
			m := Mission{ID: uint16(id), Tier: tier, Weight: weight, RankMin: ranks[0], RankMax: ranks[1],
				Condition: strings.ToLower(strings.Trim(tags[0], "[]")), Target: uint32(args[0]), Parameter: args[1], Jobs: args[2:]}
			if m.Condition == "winning job" && (m.Parameter != int64(len(m.Jobs)) || m.Parameter <= 0) {
				return nil, fmt.Errorf("invalid mission %d job list", id)
			}
			if reward := d.Ints("reward victory emblem"); len(reward) == 1 {
				m.Emblems = reward[0]
			}
			if m.Emblems < 0 || m.Emblems > math.MaxInt32 {
				return nil, fmt.Errorf("invalid mission %d emblem reward", id)
			}
			c.Pools[tier] = append(c.Pools[tier], m)
		}
	}
	for tier, pool := range c.Pools {
		if len(pool) == 0 {
			return nil, fmt.Errorf("missing PVF daily mission tier %d", tier)
		}
	}
	normalGrade := "[normal]"
	if totalMatch {
		normalGrade = "[total match normal]"
	}
	for id, p := range paths {
		d, err := read(p)
		if err != nil {
			return nil, err
		}
		grade, _ := d.Text("grade")
		if grade != normalGrade {
			continue
		}
		category, categoryOK := d.Text("normal category")
		ranks, args, tags := d.Ints("rank range"), d.Ints("condition"), d.Texts("condition")
		if id <= 0 || id > math.MaxInt16 || !categoryOK || len(ranks) != 2 || ranks[0] < 0 || ranks[1] < ranks[0] || len(tags) != 2 || len(args) < 2 || args[0] < -1 || args[0] == 0 || args[0] > math.MaxInt32 {
			return nil, fmt.Errorf("invalid normal mission definition %s", p)
		}
		switch category {
		case "[training]", "[rating]", "[battle]", "[victory]":
		default:
			return nil, fmt.Errorf("invalid normal mission category %s", p)
		}
		pre, _ := d.Int("pre required mission")
		if pre < -1 || pre > math.MaxInt16 || pre == id {
			return nil, fmt.Errorf("invalid normal mission prerequisite %d", id)
		}
		if pre == -1 {
			pre = 0
		}
		m := Mission{ID: uint16(id), RankMin: ranks[0], RankMax: ranks[1], Category: strings.ToLower(strings.Trim(category, "[]")),
			Condition: strings.ToLower(strings.Trim(tags[0], "[]")), Target: uint32(args[0]), Parameter: args[1], PreRequired: uint16(pre)}
		if m.Condition == "combo clear" {
			m.JobTag = tags[1]
			known := false
			for job := 0; job <= jobmap.MaxID; job++ {
				known = known || jobmap.MatchesQuestTag(m.JobTag, job)
			}
			if !known {
				return nil, fmt.Errorf("invalid normal mission job %s", p)
			}
		} else if tags[1] != modeTag {
			return nil, fmt.Errorf("invalid normal mission mode %s", p)
		}
		if totalMatch {
			v := d.Ints("reward victory emblem")
			if len(v) > 1 || len(v) == 1 && (v[0] < 0 || v[0] > math.MaxInt32) {
				return nil, fmt.Errorf("invalid total mission emblem reward %d", id)
			}
			if len(v) == 1 {
				m.Emblems = v[0]
			}
			v = d.Ints("reward item")
			if len(v) != 0 {
				if len(v) != 2 || v[0] <= 0 || v[0] > math.MaxUint32 || v[1] <= 0 || v[1] > math.MaxInt32 {
					return nil, fmt.Errorf("invalid total mission item reward %d", id)
				}
				m.RewardItemID, m.RewardItemCount = v[0], v[1]
			}
		}
		c.Normal = append(c.Normal, m)
	}
	sort.Slice(c.Normal, func(i, j int) bool { return c.Normal[i].ID < c.Normal[j].ID })
	normalByID := make(map[uint16]Mission, len(c.Normal))
	for _, mission := range c.Normal {
		normalByID[mission.ID] = mission
	}
	for _, mission := range c.Normal {
		seen := map[uint16]bool{mission.ID: true}
		for id := mission.PreRequired; id != 0; {
			pre, found := normalByID[id]
			if !found || seen[id] || pre.JobTag != mission.JobTag {
				return nil, fmt.Errorf("invalid normal mission prerequisite chain %d", mission.ID)
			}
			seen[id] = true
			id = pre.PreRequired
		}
	}
	ref, err := read("etc/pvp_ref.etc")
	if err != nil {
		return nil, err
	}
	values := ref.Ints("pvp experience")
	if len(values) == 0 || len(values)%2 != 0 {
		return nil, fmt.Errorf("invalid PVP experience bounds")
	}
	c.ExperienceBounds = []uint32{0}
	for i := 0; i < len(values); i += 2 {
		if values[i] != int64(len(c.ExperienceBounds)) || values[i+1] <= int64(c.ExperienceBounds[len(c.ExperienceBounds)-1]) || values[i+1] > math.MaxInt32 {
			return nil, fmt.Errorf("invalid PVP grade threshold")
		}
		c.ExperienceBounds = append(c.ExperienceBounds, uint32(values[i+1]))
	}
	matching, err := read("etc/matchingmode.etc")
	if err != nil {
		return nil, err
	}
	pointTag := "pvp grade point"
	if totalMatch {
		pointTag = "pvp grade point total match"
	}
	c.RatingRules, err = loadRatingRulesWithPoints(ref, matching, len(c.ExperienceBounds)-1, pointTag)
	if err != nil {
		return nil, err
	}
	return c, nil
}

type Row struct {
	ID               uint16
	Progress, Target uint32
}
type Snapshot struct {
	Character        repo.CharacterRecord
	Day              int64
	Rows             []Row
	BeforeRows       []Row
	ChangedRows      []Row
	NormalRows       []Row
	BeforeNormalRows []Row
	EmblemsGranted   int64
	ExperienceDelta  int64
	BeforeGrade      *uint32
}

func dailyKey(tier int, field string) string { return "pvp_daily_" + strconv.Itoa(tier) + "_" + field }

func (c *Catalog) snapshot(character repo.CharacterRecord, now time.Time) (Snapshot, bool, error) {
	character = repo.CloneCharacter(character)
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	stats := character.Stats
	// Settle completed rows before the service-day reset discards them. The
	// marker also repairs rows completed by the previous progress-only server.
	granted, paid, err := c.grantDailyEmblems(stats)
	if err != nil {
		return Snapshot{}, false, err
	}
	day := premium.ServiceDayID(now)
	fresh := stats["pvp_daily_day"] != day
	out := Snapshot{Character: character, Day: day, EmblemsGranted: granted}
	for tier, pool := range c.Pools {
		var chosen *Mission
		if !fresh {
			for i := range pool {
				if int64(pool[i].ID) == stats[dailyKey(tier, "id")] {
					chosen = &pool[i]
					break
				}
			}
			if chosen == nil {
				return Snapshot{}, false, fmt.Errorf("persisted PVP mission missing from tier %d", tier)
			}
		} else {
			var total int64
			for _, m := range pool {
				if stats["pvp_grade"] >= m.RankMin && stats["pvp_grade"] <= m.RankMax {
					total += m.Weight
				}
			}
			if total == 0 {
				return Snapshot{}, false, fmt.Errorf("no daily mission for grade %d", stats["pvp_grade"])
			}
			// Stable weighted selection prevents reconnects and transaction retries
			// from rerolling a character's missions within the same service day.
			digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", character.CharacterID, day, tier)))
			pick := int64(binary.LittleEndian.Uint64(digest[:8]) % uint64(total))
			for i, m := range pool {
				if stats["pvp_grade"] < m.RankMin || stats["pvp_grade"] > m.RankMax {
					continue
				}
				pick -= m.Weight
				if pick < 0 {
					chosen = &pool[i]
					break
				}
			}
			stats[dailyKey(tier, "id")] = int64(chosen.ID)
			stats[dailyKey(tier, "progress")] = 0
			stats[dailyKey(tier, "paid")] = 0
		}
		progress := stats[dailyKey(tier, "progress")]
		if progress < 0 || progress > int64(chosen.Target) {
			return Snapshot{}, false, fmt.Errorf("invalid daily mission progress")
		}
		out.Rows = append(out.Rows, Row{chosen.ID, uint32(progress), chosen.Target})
	}
	out.NormalRows, err = c.normalRows(character)
	if err != nil {
		return Snapshot{}, false, err
	}
	stats["pvp_daily_day"] = day
	return out, fresh || paid, nil
}

func normalKey(id uint16) string { return "pvp_normal_" + strconv.Itoa(int(id)) + "_progress" }

func (c *Catalog) normalRows(character repo.CharacterRecord) ([]Row, error) {
	var rows []Row
	job, jobErr := strconv.Atoi(character.Job)
	for _, mission := range c.Normal {
		if mission.JobTag != "" && (jobErr != nil || !jobmap.MatchesQuestTag(mission.JobTag, job)) {
			continue
		}
		progress := character.Stats[normalKey(mission.ID)]
		if progress < 0 || progress > math.MaxInt32 {
			return nil, fmt.Errorf("invalid normal mission progress %d", mission.ID)
		}
		// Only persisted match facts can restore cumulative participation/wins.
		// ponytail: other conditions retain saved progress until their event and
		// reward transactions are implemented; never infer dojo/combo completion.
		switch mission.Condition {
		case "hereafter join":
			progress = max(progress, character.Stats[CertificationKey])
		case "hereafter winning count":
			progress = max(progress, character.Stats[WinsKey])
		case "rank":
			progress = max(progress, character.Stats[GradeKey], character.Stats["pvp_max_grade"])
		}
		if progress > math.MaxInt32 {
			return nil, fmt.Errorf("invalid normal mission counter %d", mission.ID)
		}
		// Preserve -1 as FFFFFFFF: sub_10A9D80 suppresses its count label and
		// sub_1C500C0 compares it unsigned. Zero would falsely complete the card.
		rows = append(rows, Row{mission.ID, min(uint32(progress), mission.Target), mission.Target})
	}
	return rows, nil
}

func (c *Catalog) grantDailyEmblems(stats map[string]int64) (int64, bool, error) {
	var granted int64
	changed := false
	for tier, pool := range c.Pools {
		id, progress, paid := stats[dailyKey(tier, "id")], stats[dailyKey(tier, "progress")], stats[dailyKey(tier, "paid")]
		if id == 0 && progress == 0 && paid == 0 {
			continue
		}
		if paid != 0 && paid != 1 {
			return 0, false, fmt.Errorf("invalid PVP reward marker")
		}
		found := false
		for _, mission := range pool {
			if int64(mission.ID) != id {
				continue
			}
			found = true
			if progress < 0 || progress > int64(mission.Target) || (paid == 1 && progress != int64(mission.Target)) {
				return 0, false, fmt.Errorf("invalid PVP reward progress")
			}
			if paid == 1 || progress != int64(mission.Target) {
				break
			}
			balance := stats[EmblemsKey]
			if mission.Emblems < 0 || mission.Emblems > math.MaxInt32 || balance < 0 || balance > math.MaxInt32-mission.Emblems {
				return 0, false, fmt.Errorf("invalid PVP emblem balance/reward")
			}
			stats[EmblemsKey] = balance + mission.Emblems
			stats[dailyKey(tier, "paid")] = 1
			granted += mission.Emblems
			changed = true
			break
		}
		if !found {
			return 0, false, fmt.Errorf("persisted PVP mission missing from tier %d", tier)
		}
	}
	return granted, changed, nil
}

func Prepare(ctx context.Context, repos repo.Group, catalog *Catalog, accountID, characterID string, now time.Time) (Snapshot, error) {
	return update(ctx, repos, catalog, accountID, characterID, now, nil)
}

// Match carries facts from a validated ranked 1v1 or manual room. Reports are
// admitted by the bridge only for assigned conditions in the current match.
type Match struct {
	Key                    string
	StartedAt, CompletedAt time.Time
	Won, Draw              bool
	Manual                 bool
	OpponentJob            int64
	OpponentRP             int64
	OpponentRated          bool
	Reports                map[uint16]bool
}

func Complete(ctx context.Context, repos repo.Group, catalog *Catalog, accountID, characterID string, match Match) (Snapshot, error) {
	if match.Key == "" || match.StartedAt.IsZero() || match.CompletedAt.Before(match.StartedAt) {
		return Snapshot{}, fmt.Errorf("invalid PVP completion identity/time")
	}
	return update(ctx, repos, catalog, accountID, characterID, match.CompletedAt, &match)
}

func update(ctx context.Context, repos repo.Group, catalog *Catalog, accountID, characterID string, now time.Time, match *Match) (Snapshot, error) {
	if repos.CharacterSettlement == nil || catalog == nil || accountID == "" || characterID == "" || now.IsZero() {
		return Snapshot{}, fmt.Errorf("PVP state dependencies or identity missing")
	}
	var result Snapshot
	err := repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		if tx.Character == nil {
			return fmt.Errorf("PVP character repository missing")
		}
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != accountID {
			return fmt.Errorf("PVP character owner mismatch")
		}
		var changed bool
		result, changed, err = catalog.snapshot(character, now)
		if err != nil {
			return err
		}
		stats := result.Character.Stats
		result.BeforeRows = append([]Row(nil), result.Rows...)
		result.BeforeNormalRows = append([]Row(nil), result.NormalRows...)
		if match != nil {
			digest := sha256.Sum256([]byte(match.Key))
			replay := true
			for i := 0; i < 4; i++ {
				v, exists := stats[fmt.Sprintf("pvp_match_receipt_%d", i)]
				replay = replay && exists && v == int64(binary.LittleEndian.Uint64(digest[i*8:]))
			}
			if !replay {
				if previous := stats["pvp_match_completed_at"]; previous > match.CompletedAt.UnixNano() {
					return fmt.Errorf("stale PVP completion")
				}
				count := stats[CertificationKey]
				if count < 0 || count >= math.MaxInt32 {
					return fmt.Errorf("invalid certification count %d", count)
				}
				previousStreak := stats["pvp_win_streak"]
				if _, err := catalog.applyRating(stats, *match, count); err != nil {
					return err
				}
				if !match.Manual {
					stats[CertificationKey] = count + 1
					key, outcome := LossesKey, int64(2)
					if match.Draw {
						key, outcome = DrawsKey, 3
					} else if match.Won {
						key, outcome = WinsKey, 1
					}
					if stats[key] < 0 || stats[key] >= math.MaxInt32 || stats[MaxWinStreakKey] < 0 ||
						stats[MaxWinStreakKey] > math.MaxInt32 || stats[RecentResultsKey] < 0 || stats[RecentResultsKey] >= 1<<20 {
						return fmt.Errorf("invalid PVP result count/history")
					}
					stats[key]++
					stats[MaxWinStreakKey] = max(stats[MaxWinStreakKey], previousStreak, stats["pvp_win_streak"])
					// Local recent-history policy: last ten committed ranked outcomes,
					// two bits each (1 win, 2 loss, 3 draw); no historical backfill.
					stats[RecentResultsKey] = (stats[RecentResultsKey]<<2 | outcome) & ((1 << 20) - 1)
					// A match begun yesterday must not satisfy newly assigned missions.
					if premium.ServiceDayID(match.StartedAt) == result.Day {
						for tier, row := range result.Rows {
							for _, m := range catalog.Pools[tier] {
								if m.ID != row.ID || row.Progress >= row.Target {
									continue
								}
								advance := false
								switch m.Condition {
								case "hereafter join":
									advance = true
								case "hereafter winning count":
									advance = match.Won && !match.Draw
								case "winning job":
									for _, job := range m.Jobs {
										advance = advance || (match.Won && !match.Draw && job == match.OpponentJob)
									}
								case "reduce hp", "first aerial attack", "combo damage rate":
									advance = match.Reports[m.ID]
								case "remains hp", "remain hp":
									advance = match.Won && !match.Draw && match.Reports[m.ID]
									// Streak-group and pose tuple semantics are not yet proven;
									// retain their PVF cards without granting speculative progress.
								}
								if advance {
									row.Progress++
									result.Rows[tier] = row
									result.ChangedRows = append(result.ChangedRows, row)
									stats[dailyKey(tier, "progress")] = int64(row.Progress)
								}
							}
						}
					}
				}
				for i := 0; i < 4; i++ {
					stats[fmt.Sprintf("pvp_match_receipt_%d", i)] = int64(binary.LittleEndian.Uint64(digest[i*8:]))
				}
				stats["pvp_match_completed_at"] = match.CompletedAt.UnixNano()
				changed = true
			}
			result.ExperienceDelta = stats["pvp_result_experience_delta"]
			beforeGrade := uint32(stats["pvp_result_before_grade"])
			result.BeforeGrade = &beforeGrade
		}
		granted, paid, err := catalog.grantDailyEmblems(stats)
		if err != nil {
			return err
		}
		result.EmblemsGranted += granted
		changed = changed || paid
		result.NormalRows, err = catalog.normalRows(result.Character)
		if err != nil {
			return err
		}
		for i, row := range result.NormalRows {
			if row != result.BeforeNormalRows[i] {
				result.ChangedRows = append(result.ChangedRows, row)
			}
		}
		if changed {
			return repo.SaveCharacterFields(ctx, tx.Character, result.Character, repo.CharacterFieldStats)
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return result, nil
}
