package repository

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

// 账号活动账本独立于角色存档；删除角色不删除其历史贡献。
const (
	ActivityFatigue   = "fatigue_spent"
	ActivityAbyss     = "abyss_completed"
	ActivityEpic      = "epic_pickup"
	ActivityUnique    = "unique_pickup"
	ActivityLegendary = "legendary_pickup"
)

var ErrActivityInvalid = errors.New("账号活动记录无效")
var ErrActivityTransaction = errors.New("账号活动记录需要所属角色事务")
var ErrActivityConflict = errors.New("账号活动事件编号对应不同业务内容")

type ActivityEvent struct {
	ID            string    `json:"event_id"`
	AccountID     string    `json:"account_id"`
	CharacterID   string    `json:"character_id"`
	CharacterName string    `json:"character_name"`
	Metric        string    `json:"metric"`
	Amount        int64     `json:"amount"`
	OccurredAt    time.Time `json:"occurred_at"`
	DungeonID     int64     `json:"dungeon_id"`
	ItemID        uint32    `json:"item_id,omitempty"`
	Rarity        int64     `json:"rarity,omitempty"`
	Source        string    `json:"source"`
}

func ActivityDay(at time.Time) string {
	return at.In(time.FixedZone("Asia/Shanghai", 8*3600)).Add(-6 * time.Hour).Format("2006-01-02")
}

func (e ActivityEvent) Validate() error {
	for _, b := range []byte(e.ID) {
		if b < 0x21 || b > 0x7e {
			return ErrActivityInvalid
		}
	}
	if strings.TrimSpace(e.ID) == "" || len(e.ID) > 128 || strings.TrimSpace(e.AccountID) == "" || len(e.AccountID) > 128 || strings.TrimSpace(e.CharacterID) == "" || len(e.CharacterID) > 128 || len([]rune(e.CharacterName)) > 64 || e.Amount <= 0 || e.OccurredAt.IsZero() || e.DungeonID < 0 {
		return ErrActivityInvalid
	}
	if source := GoldActivitySource(e.Metric); source != "" {
		if e.Source != source || e.DungeonID != 0 || e.ItemID != 0 || e.Rarity != 0 {
			return ErrActivityInvalid
		}
		return nil
	}
	if e.DungeonID == 0 && e.Metric != ActivityRecommendedDungeon && e.Metric != ActivityColorless && (e.Metric != ActivityFatigue || e.Source != "arcade") {
		return ErrActivityInvalid
	}
	switch e.Metric {
	case ActivityColorless:
		if e.ItemID != uint32(ColorlessCubeItemID) || e.Rarity != 0 {
			return ErrActivityInvalid
		}
		switch e.Source {
		case "skill", "upgrade", "contract", "npc_material", "craft", "quest":
		default:
			return ErrActivityInvalid
		}
	case ActivityRecommendedDungeon:
		if e.Amount != 1 || e.Source != "recommended_clear" || e.ItemID != 0 || e.Rarity != 0 {
			return ErrActivityInvalid
		}
	case ActivityFatigue:
		// Amount is the actual debit: room burning and guild entry may cost
		// more than one. Positivity is checked above; callers freeze it within
		// the owning asset transaction, never accept client supplied amounts.
		if (e.Source != "dungeon_room" && e.Source != "cycle_entry" && e.Source != "epic_entry" && e.Source != "arcade") || e.ItemID != 0 || e.Rarity != 0 || (e.Source == "arcade" && e.DungeonID != 0) {
			return ErrActivityInvalid
		}
	case ActivityAbyss:
		if e.Amount != 1 || (e.Source != "hell_party" && e.Source != "rift" && e.Source != "epic_road") || e.ItemID != 0 || e.Rarity != 0 {
			return ErrActivityInvalid
		}
	case ActivityEpic, ActivityUnique, ActivityLegendary:
		want := map[string]int64{ActivityEpic: 4, ActivityUnique: 3, ActivityLegendary: 5}[e.Metric]
		if e.Amount != 1 || e.Source != "dungeon_pickup" || e.ItemID == 0 || e.Rarity != want {
			return ErrActivityInvalid
		}
	default:
		return ErrActivityInvalid
	}
	return nil
}

// 时间不属于幂等业务载荷：同一已提交事件稍后重试仍返回原记录。
func SameActivityEvent(a, b ActivityEvent) bool {
	a.OccurredAt, b.OccurredAt = time.Time{}, time.Time{}
	a.CharacterName, b.CharacterName = "", ""
	return a == b
}

type ActivityQuery struct {
	IncludeEpicSources bool      `json:"include_epic_sources,omitempty"`
	AccountID          string    `json:"account_id"`
	CharacterID        string    `json:"character_id,omitempty"`
	FromDay            string    `json:"from_day,omitempty"`
	ThroughDay         string    `json:"through_day,omitempty"`
	BeforeTime         time.Time `json:"before_time,omitempty"`
	BeforeID           string    `json:"before_id,omitempty"`
	Limit              int       `json:"limit,omitempty"`
}

func (q ActivityQuery) Validate() error {
	if strings.TrimSpace(q.AccountID) == "" || len(q.AccountID) > 128 || len(q.CharacterID) > 128 || q.Limit < 0 || q.Limit > 200 || (q.FromDay == "") != (q.ThroughDay == "") || q.FromDay > q.ThroughDay || q.BeforeTime.IsZero() != (q.BeforeID == "") {
		return ErrActivityInvalid
	}
	for _, d := range []string{q.FromDay, q.ThroughDay} {
		if d != "" {
			t, err := time.Parse("2006-01-02", d)
			if err != nil || t.Format("2006-01-02") != d {
				return ErrActivityInvalid
			}
		}
	}
	return nil
}

type ActivityContribution struct {
	CharacterID string           `json:"character_id"`
	Metrics     map[string]int64 `json:"metrics"`
}

// 来源分项只从已经提交的事件按副本汇总，不从当前背包推断历史。
type ActivityEpicSource struct {
	CharacterID string `json:"character_id"`
	DungeonID   int64  `json:"dungeon_id"`
	Amount      int64  `json:"amount"`
}

type ActivitySummary struct {
	AccountID   string                 `json:"account_id"`
	Metrics     map[string]int64       `json:"metrics"`
	Characters  []ActivityContribution `json:"characters"`
	EpicSources []ActivityEpicSource   `json:"epic_sources,omitempty"`
}

func NewActivitySummary(accountID string) ActivitySummary {
	return ActivitySummary{AccountID: accountID, Metrics: map[string]int64{}, Characters: []ActivityContribution{}}
}
func (s *ActivitySummary) Add(character, metric string, n int64) error {
	if n < 0 || s.Metrics[metric] > math.MaxInt64-n {
		return ErrActivityInvalid
	}
	for i := range s.Characters {
		if s.Characters[i].CharacterID == character {
			if s.Characters[i].Metrics[metric] > math.MaxInt64-n {
				return ErrActivityInvalid
			}
			s.Characters[i].Metrics[metric] += n
			s.Metrics[metric] += n
			return nil
		}
	}
	s.Characters = append(s.Characters, ActivityContribution{CharacterID: character, Metrics: map[string]int64{metric: n}})
	s.Metrics[metric] += n
	return nil
}
func (s *ActivitySummary) Sort() {
	sort.Slice(s.Characters, func(i, j int) bool { return s.Characters[i].CharacterID < s.Characters[j].CharacterID })
	sort.Slice(s.EpicSources, func(i, j int) bool {
		a, b := s.EpicSources[i], s.EpicSources[j]
		if a.CharacterID != b.CharacterID {
			return a.CharacterID < b.CharacterID
		}
		return a.DungeonID < b.DungeonID
	})
}

type ActivityRepository interface {
	Record(context.Context, ActivityEvent) (bool, error)
	Summary(context.Context, ActivityQuery) (ActivitySummary, error)
	Events(context.Context, ActivityQuery) ([]ActivityEvent, error)
}

// 在原有资产事务内记录统计，保留调用方的门票、点券及角色归属保护。
type CharacterActivityRecorder interface {
	RecordActivity(context.Context, ActivityEvent) (bool, error)
}

func RecordCharacterActivity(ctx context.Context, characters CharacterRepository, event ActivityEvent) (bool, error) {
	r, ok := characters.(CharacterActivityRecorder)
	if !ok {
		return false, ErrActivityTransaction
	}
	return r.RecordActivity(ctx, event)
}

type characterActivityRepository struct {
	ActivityRepository
	character CharacterRecord
}

func ScopeCharacterActivity(base ActivityRepository, ch CharacterRecord) ActivityRepository {
	if base == nil {
		return nil
	}
	return &characterActivityRepository{ActivityRepository: base, character: ch}
}
func (r *characterActivityRepository) Record(ctx context.Context, e ActivityEvent) (bool, error) {
	if e.AccountID != r.character.AccountID || e.CharacterID != r.character.CharacterID || e.AccountID == "" {
		return false, ErrActivityTransaction
	}
	if err := e.Validate(); err != nil {
		return false, err
	}
	e.CharacterName = r.character.Name
	return r.ActivityRepository.Record(ctx, e)
}
