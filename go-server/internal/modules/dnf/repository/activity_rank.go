package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ErrActivityRank = errors.New("账号排行榜参数或数据无效")
var ErrActivityRankConflict = errors.New("周榜已经按另一套奖励规则结算")

const ActivityRankMaxContributions = 100000
const ActivityRankSettlementGrace = 5 * time.Minute

type ActivityRankWindow struct {
	Period     string `json:"period"`
	FromDay    string `json:"from_day,omitempty"`
	ThroughDay string `json:"through_day,omitempty"`
}

func ActivityRankPeriod(period string, now time.Time) (ActivityRankWindow, error) {
	if now.IsZero() {
		return ActivityRankWindow{}, ErrActivityRank
	}
	day, _ := time.Parse("2006-01-02", ActivityDay(now))
	switch period {
	case "day":
		return ActivityRankWindow{period, day.Format("2006-01-02"), day.Format("2006-01-02")}, nil
	case "week":
		day = day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		return ActivityRankWindow{period, day.Format("2006-01-02"), day.AddDate(0, 0, 6).Format("2006-01-02")}, nil
	case "all":
		return ActivityRankWindow{Period: period}, nil
	default:
		return ActivityRankWindow{}, ErrActivityRank
	}
}

func ActivityRankWeek(start string) (ActivityRankWindow, error) {
	t, err := time.Parse("2006-01-02", start)
	if err != nil || t.Format("2006-01-02") != start || t.Weekday() != time.Monday {
		return ActivityRankWindow{}, ErrActivityRank
	}
	return ActivityRankWindow{"week", start, t.AddDate(0, 0, 6).Format("2006-01-02")}, nil
}

func (w ActivityRankWindow) Validate() error {
	if w.Period == "all" && w.FromDay == "" && w.ThroughDay == "" {
		return nil
	}
	if w.Period == "day" {
		d, err := time.Parse("2006-01-02", w.FromDay)
		if err == nil && d.Format("2006-01-02") == w.FromDay && w.FromDay == w.ThroughDay {
			return nil
		}
	}
	if w.Period == "week" {
		if want, err := ActivityRankWeek(w.FromDay); err == nil && want == w {
			return nil
		}
	}
	return ErrActivityRank
}

func (w ActivityRankWindow) Closed(now time.Time) bool {
	if w.Validate() != nil || w.Period != "week" || now.IsZero() {
		return false
	}
	end, _ := time.ParseInLocation("2006-01-02", w.ThroughDay, time.FixedZone("Asia/Shanghai", 8*3600))
	return !now.Before(end.AddDate(0, 0, 1).Add(6*time.Hour + ActivityRankSettlementGrace))
}

func ValidActivityRankMetric(metric string) bool {
	return metric == ActivityFatigue || metric == ActivityEpic || metric == ActivityRecommendedDungeon || metric == ActivityColorless || metric == ActivityGoldSpent
}

type ActivityRankContribution struct {
	AccountID   string `json:"account_id"`
	CharacterID string `json:"character_id"`
	Score       int64  `json:"score"`
}
type ActivityRankCharacter struct {
	CharacterID string `json:"character_id"`
	Score       int64  `json:"score"`
}
type ActivityRankEntry struct {
	AccountID  string                  `json:"account_id"`
	Rank       int                     `json:"rank"`
	Score      int64                   `json:"score"`
	Characters []ActivityRankCharacter `json:"characters"`
}
type ActivityRankRewardTier struct {
	FromRank       int    `json:"from_rank"`
	ThroughRank    int    `json:"through_rank"`
	AppearanceCode string `json:"appearance_code"`
}
type ActivityRankPolicy struct {
	Version          string                   `json:"version"`
	ExcludedAccounts []string                 `json:"excluded_accounts,omitempty"`
	Rewards          []ActivityRankRewardTier `json:"rewards,omitempty"`
}

var appearanceCodePattern = regexp.MustCompile(`^appearance:[a-z0-9_-]{1,64}$`)

func (p ActivityRankPolicy) Validate() error {
	if strings.TrimSpace(p.Version) == "" || len(p.Version) > 64 || len(p.ExcludedAccounts) > 1000 || len(p.Rewards) > 30 {
		return ErrActivityRank
	}
	seen := map[string]bool{}
	for _, a := range p.ExcludedAccounts {
		if strings.TrimSpace(a) == "" || len(a) > 128 || seen[a] {
			return ErrActivityRank
		}
		seen[a] = true
	}
	last := 0
	for _, r := range p.Rewards {
		if r.FromRank <= last || r.ThroughRank < r.FromRank || r.ThroughRank > ActivityRankMaxContributions || !appearanceCodePattern.MatchString(r.AppearanceCode) {
			return ErrActivityRank
		}
		last = r.ThroughRank
	}
	return nil
}
func (p ActivityRankPolicy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	p.ExcludedAccounts = append([]string(nil), p.ExcludedAccounts...)
	sort.Strings(p.ExcludedAccounts)
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// 同分并列，采用1、1、3名次；账号ID仅使同分展示顺序稳定，不影响奖励档位。
func BuildActivityRank(rows []ActivityRankContribution, excluded []string) ([]ActivityRankEntry, error) {
	if len(rows) > ActivityRankMaxContributions {
		return nil, ErrActivityRank
	}
	blocked := map[string]bool{}
	for _, a := range excluded {
		blocked[a] = true
	}
	accounts := map[string]*ActivityRankEntry{}
	seen := map[[2]string]bool{}
	for _, row := range rows {
		if strings.TrimSpace(row.AccountID) == "" || len(row.AccountID) > 128 || strings.TrimSpace(row.CharacterID) == "" || len(row.CharacterID) > 128 || row.Score <= 0 {
			return nil, ErrActivityRank
		}
		k := [2]string{row.AccountID, row.CharacterID}
		if seen[k] {
			return nil, ErrActivityRank
		}
		seen[k] = true
		if blocked[row.AccountID] {
			continue
		}
		a := accounts[row.AccountID]
		if a == nil {
			a = &ActivityRankEntry{AccountID: row.AccountID}
			accounts[row.AccountID] = a
		}
		if a.Score > math.MaxInt64-row.Score {
			return nil, ErrActivityRank
		}
		a.Score += row.Score
		a.Characters = append(a.Characters, ActivityRankCharacter{row.CharacterID, row.Score})
	}
	out := make([]ActivityRankEntry, 0, len(accounts))
	for _, a := range accounts {
		sort.Slice(a.Characters, func(i, j int) bool { return a.Characters[i].CharacterID < a.Characters[j].CharacterID })
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].AccountID < out[j].AccountID
	})
	for i := range out {
		if i > 0 && out[i].Score == out[i-1].Score {
			out[i].Rank = out[i-1].Rank
		} else {
			out[i].Rank = i + 1
		}
	}
	return out, nil
}

type ActivityRankReward struct {
	BoardKey       string `json:"board_key"`
	AccountID      string `json:"account_id"`
	Rank           int    `json:"rank"`
	Score          int64  `json:"score"`
	AppearanceCode string `json:"appearance_code"`
	State          string `json:"state"`
}
type ActivityRankSnapshot struct {
	Metric       string               `json:"metric"`
	Window       ActivityRankWindow   `json:"window"`
	SettledAt    time.Time            `json:"settled_at"`
	Policy       ActivityRankPolicy   `json:"policy"`
	PolicyDigest string               `json:"policy_digest"`
	Entries      []ActivityRankEntry  `json:"entries"`
	Rewards      []ActivityRankReward `json:"rewards"`
}
type ActivityRankSettlement struct {
	Metric string             `json:"metric"`
	Week   string             `json:"week"`
	Policy ActivityRankPolicy `json:"policy"`
	Now    time.Time          `json:"-"`
}

func (s ActivityRankSettlement) Validate() error {
	w, err := ActivityRankWeek(s.Week)
	if err != nil || !ValidActivityRankMetric(s.Metric) || !w.Closed(s.Now) {
		return ErrActivityRank
	}
	return s.Policy.Validate()
}
func (s ActivityRankSettlement) Key() string { return s.Metric + ":" + s.Week }
func NewActivityRankSnapshot(s ActivityRankSettlement, rows []ActivityRankContribution) (ActivityRankSnapshot, error) {
	if err := s.Validate(); err != nil {
		return ActivityRankSnapshot{}, err
	}
	w, _ := ActivityRankWeek(s.Week)
	digest, _ := s.Policy.Digest()
	entries, err := BuildActivityRank(rows, s.Policy.ExcludedAccounts)
	if err != nil {
		return ActivityRankSnapshot{}, err
	}
	out := ActivityRankSnapshot{Metric: s.Metric, Window: w, SettledAt: s.Now.UTC(), Policy: s.Policy, PolicyDigest: digest, Entries: entries, Rewards: []ActivityRankReward{}}
	for _, e := range entries {
		for _, r := range s.Policy.Rewards {
			if e.Rank >= r.FromRank && e.Rank <= r.ThroughRank {
				out.Rewards = append(out.Rewards, ActivityRankReward{s.Key(), e.AccountID, e.Rank, e.Score, r.AppearanceCode, "pending"})
				break
			}
		}
	}
	return CloneActivityRankSnapshot(out), nil
}
func CloneActivityRankSnapshot(s ActivityRankSnapshot) ActivityRankSnapshot {
	s.Policy.ExcludedAccounts = append([]string(nil), s.Policy.ExcludedAccounts...)
	s.Policy.Rewards = append([]ActivityRankRewardTier(nil), s.Policy.Rewards...)
	s.Entries = CloneActivityRankEntries(s.Entries)
	s.Rewards = append([]ActivityRankReward(nil), s.Rewards...)
	return s
}
func CloneActivityRankEntries(rows []ActivityRankEntry) []ActivityRankEntry {
	out := append([]ActivityRankEntry(nil), rows...)
	for i := range out {
		out[i].Characters = append([]ActivityRankCharacter(nil), out[i].Characters...)
	}
	return out
}

// 可选能力独立于单账号查询；所有排名只读取已提交汇总，不扫描全服拾取明细。
type ActivityRankingRepository interface {
	RankContributions(context.Context, string, ActivityRankWindow) ([]ActivityRankContribution, error)
	RankWeek(context.Context, string, string) (ActivityRankSnapshot, bool, error)
	SettleRankWeek(context.Context, ActivityRankSettlement) (ActivityRankSnapshot, bool, error)
	PendingRankRewards(context.Context, string, int) ([]ActivityRankReward, error)
}
