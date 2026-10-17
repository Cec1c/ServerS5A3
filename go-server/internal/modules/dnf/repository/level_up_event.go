package repository

import (
	"context"
	"strconv"
	"strings"
)

// 正数表示等待消费的真实升级事件，负数表示已经消费；永久记录独立于邮件。
// 使用既有角色额外数值表，使事件与等级、经验、技能点参与同一个事务。
const LevelUpEventStatPrefix = "level_up_event_"
const LevelUpRewardStatPrefix = "level_up_reward_"

type PendingLevelUpReader interface {
	ListPendingLevelUpCharacters(context.Context, string, int) ([]string, error)
}

func LevelUpEventKey(level int) string { return LevelUpEventStatPrefix + strconv.Itoa(level) }

func LevelUpEventLevel(key string) (int, bool) {
	if !strings.HasPrefix(key, LevelUpEventStatPrefix) {
		return 0, false
	}
	value := strings.TrimPrefix(key, LevelUpEventStatPrefix)
	level, err := strconv.Atoi(value)
	return level, err == nil && level >= 2 && level <= 255 && value == strconv.Itoa(level)
}

func HasPendingLevelUp(record CharacterRecord) bool {
	for key, value := range record.Stats {
		if _, ok := LevelUpEventLevel(key); ok && value > 0 {
			return true
		}
	}
	return false
}

// PreserveLevelUpHistory 防止其它玩法用旧角色快照覆盖已经提交的升级事实。
// 事件只能从待消费转为已消费；审计写入后保留首份值，邮件删除不影响领取资格。
func PreserveLevelUpHistory(next *CharacterRecord, previous CharacterRecord) {
	for key, value := range previous.Stats {
		_, event := LevelUpEventLevel(key)
		audit := strings.HasPrefix(key, LevelUpRewardStatPrefix)
		if value == 0 || !event && !audit {
			continue
		}
		if next.Stats == nil {
			next.Stats = make(map[string]int64)
		}
		if audit || value < 0 || next.Stats[key] >= 0 {
			next.Stats[key] = value
		}
	}
}
