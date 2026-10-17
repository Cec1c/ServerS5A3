package progression

import (
	"context"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"math"
	"strings"
)

const LevelUpRewardPVFPath = "etc/serverparameter_chn.etc"

type LevelUpReward struct {
	Level         int
	ItemID, Count int64
}
type LevelUpRewardCatalog struct{ rewards map[int]LevelUpReward }

func LoadLevelUpRewards(ctx context.Context, source dnfpvf.Source) (*LevelUpRewardCatalog, error) {
	if source == nil {
		return nil, dnfpvf.ErrSourceRequired
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	text, err := source.ReadText(LevelUpRewardPVFPath)
	if err != nil {
		return nil, err
	}
	return ParseLevelUpRewards(text)
}

func ParseLevelUpRewards(text string) (*LevelUpRewardCatalog, error) {
	doc, err := dnfpvf.Parse(LevelUpRewardPVFPath, text)
	if err != nil {
		return nil, err
	}
	catalog := &LevelUpRewardCatalog{rewards: make(map[int]LevelUpReward)}
	for i, section := range doc.Sections {
		if strings.TrimSpace(strings.ToLower(section.Name)) != "level up reward" {
			continue
		}
		if i+1 >= len(doc.Sections) || strings.TrimSpace(strings.ToLower(doc.Sections[i+1].Name)) != "/level up reward" || section.End-section.Start != 5 {
			return nil, fmt.Errorf("%w: %s [level up reward]", ErrTableMalformed, LevelUpRewardPVFPath)
		}
		values := doc.Tokens[section.Start:section.End]
		for _, token := range values {
			if token.Kind != dnfpvf.TokenInt {
				return nil, ErrTableMalformed
			}
		}
		// 中间两列为尚未闭环的客户端文本参数，不解释成职业或活动条件。
		level, item, count := values[0].Int, values[3].Int, values[4].Int
		if level < 2 || level > 255 || item <= 0 || item > math.MaxInt32 || count <= 0 || count > math.MaxInt32 {
			return nil, ErrTableMalformed
		}
		if _, duplicate := catalog.rewards[int(level)]; duplicate {
			return nil, ErrTableMalformed
		}
		catalog.rewards[int(level)] = LevelUpReward{Level: int(level), ItemID: item, Count: count}
	}
	return catalog, nil
}

func (c *LevelUpRewardCatalog) Reward(level int) (LevelUpReward, bool) {
	if c == nil {
		return LevelUpReward{}, false
	}
	reward, ok := c.rewards[level]
	return reward, ok
}
