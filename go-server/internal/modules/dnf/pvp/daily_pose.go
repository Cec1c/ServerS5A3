package pvp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 条件第一整数沿用任务目标次数，第二整数是每组需要的连续胜场。
// 仅在当天已提交的排位胜负上推进，输/平清空未完成的一组。
func advanceDailyStreak(stats map[string]int64, tier int, mission Mission, win bool) (bool, error) {
	n := stats[dailyKey(tier, "streak")]
	if mission.Parameter <= 0 || mission.Parameter > 100000 || n < 0 || n >= mission.Parameter {
		return false, fmt.Errorf("每日连胜条件或暂存计数无效")
	}
	if !win {
		stats[dailyKey(tier, "streak")] = 0
		return false, nil
	}
	n++
	if n == mission.Parameter {
		stats[dailyKey(tier, "streak")] = 0
		return true, nil
	}
	stats[dailyKey(tier, "streak")] = n
	return false, nil
}

type WinPose struct {
	MatchKey                           string
	MissionID                          uint16
	Pose                               byte
	StartedAt, CompletedAt, ReportedAt time.Time
}

type WinPoseResult struct {
	Snapshot
	Accepted, Replayed bool
}

// 姿态在47结算之后才上报。必须匹配当前角色最近一次已提交的排位胜场，
// 持久化场次摘要防重，并保存0..2姿态集合；不相信客户端提交的完成次数。
func RecordWinPose(ctx context.Context, repos repo.Group, c *Catalog, accountID, characterID string, pose WinPose) (WinPoseResult, error) {
	var result WinPoseResult
	if c == nil || repos.CharacterSettlement == nil || accountID == "" || characterID == "" || pose.MatchKey == "" ||
		pose.Pose > 2 || pose.StartedAt.IsZero() || pose.CompletedAt.Before(pose.StartedAt) || pose.ReportedAt.Before(pose.CompletedAt) || pose.ReportedAt.Sub(pose.CompletedAt) > 30*time.Second {
		return result, fmt.Errorf("胜利姿态身份或时间无效")
	}
	err := repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != accountID {
			return fmt.Errorf("胜利姿态角色归属不符")
		}
		stats := character.Stats
		digest := sha256.Sum256([]byte(pose.MatchKey))
		for i := 0; i < 4; i++ {
			v, ok := stats[fmt.Sprintf("pvp_match_receipt_%d", i)]
			if !ok || v != int64(binary.LittleEndian.Uint64(digest[i*8:])) {
				return nil
			}
		}
		day := premium.ServiceDayID(pose.ReportedAt)
		if stats["pvp_result_ranked_win"] != 1 || stats["pvp_match_completed_at"] != pose.CompletedAt.UnixNano() || stats["pvp_daily_day"] != day || premium.ServiceDayID(pose.StartedAt) != day {
			return nil
		}
		snap, _, err := c.snapshot(character, pose.ReportedAt)
		if err != nil {
			return err
		}
		result.Snapshot = snap
		stats = snap.Character.Stats
		for tier, row := range snap.Rows {
			if row.ID != pose.MissionID {
				continue
			}
			for _, m := range c.Pools[tier] {
				if m.ID != row.ID || m.Condition != "win pose" || m.Parameter != 3 {
					continue
				}
				replay := true
				for i := 0; i < 4; i++ {
					v, ok := stats[dailyKey(tier, fmt.Sprintf("pose_receipt_%d", i))]
					replay = replay && ok && v == int64(binary.LittleEndian.Uint64(digest[i*8:]))
				}
				if replay {
					result.Replayed = true
					return nil
				}
				if row.Progress >= row.Target {
					return nil
				}
				mask := stats[dailyKey(tier, "pose_mask")]
				if mask < 0 || mask > 7 {
					return fmt.Errorf("胜利姿态集合无效")
				}
				mask |= 1 << pose.Pose
				result.BeforeRows = append([]Row(nil), snap.Rows...)
				if mask == 7 {
					mask = 0
					row.Progress++
					stats[dailyKey(tier, "progress")] = int64(row.Progress)
					result.Rows[tier] = row
					result.ChangedRows = []Row{row}
				}
				stats[dailyKey(tier, "pose_mask")] = mask
				for i := 0; i < 4; i++ {
					stats[dailyKey(tier, fmt.Sprintf("pose_receipt_%d", i))] = int64(binary.LittleEndian.Uint64(digest[i*8:]))
				}
				granted, _, err := c.grantDailyEmblems(stats)
				if err != nil {
					return err
				}
				result.EmblemsGranted += granted
				result.Accepted = true
				return repo.SaveCharacterFields(ctx, tx.Character, result.Character, repo.CharacterFieldStats)
			}
		}
		return nil
	})
	if err != nil {
		return WinPoseResult{}, err
	}
	return result, nil
}
