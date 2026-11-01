package battlepass

import (
	"fmt"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"math"
	"unicode/utf16"
)

const Path = "etc/chn_battlepass.etc"

type Mission struct {
	ID, Target, XP, Kind uint32
	Text                 string
	Param                int64
}
type Reward struct{ ItemID, Count uint32 }
type Catalog struct {
	Missions      map[uint32]Mission
	Daily, Weekly [][4]uint32
	Season        []uint32
	Experience    [81]uint32
	Tier          [81]uint8
	Rewards       [2][81][]Reward
	PremiumRatio  uint32
}

// 直接读PVF的重复段，保留95编号和分组；阅读文本中的引号由通用解析器处理。
func Load(source pvf.Source) (*Catalog, error) {
	if source == nil {
		return nil, fmt.Errorf("战令PVF来源缺失")
	}
	text, e := source.ReadText(Path)
	if e != nil {
		return nil, e
	}
	return Parse(text)
}
func Parse(text string) (*Catalog, error) {
	d, e := pvf.Parse(Path, text)
	if e != nil {
		return nil, e
	}
	c := &Catalog{Missions: map[uint32]Mission{}}
	number := func(ts []pvf.Token, i int) (uint32, error) {
		if i >= len(ts) || ts[i].Kind != pvf.TokenInt || ts[i].Int < 0 || ts[i].Int > math.MaxUint32 {
			return 0, fmt.Errorf("战令整数无效")
		}
		return uint32(ts[i].Int), nil
	}
	seenExp, seenReward := map[uint32]bool{}, map[uint32]bool{}
	for _, s := range d.Sections {
		ts := d.Tokens[s.Start:s.End]
		switch s.Name {
		case "mission":
			if len(ts) != 6 || ts[4].Kind != pvf.TokenString || ts[5].Kind != pvf.TokenInt {
				return nil, fmt.Errorf("战令任务结构无效")
			}
			nums := [4]uint32{}
			for i := range nums {
				nums[i], e = number(ts, i)
				if e != nil {
					return nil, e
				}
			}
			m := Mission{nums[0], nums[1], nums[2], nums[3], ts[4].Value, ts[5].Int}
			if m.ID == 0 || m.ID > 65535 || m.Target == 0 || m.XP == 0 || m.Kind > 45 || m.Param < -1 || c.Missions[m.ID].ID != 0 || len(utf16.Encode([]rune(m.Text))) > 63 {
				return nil, fmt.Errorf("战令任务%d无效或重复", m.ID)
			}
			c.Missions[m.ID] = m
		case "daily mission", "weekly mission", "season mission":
			ids := make([]uint32, len(ts))
			for i := range ts {
				ids[i], e = number(ts, i)
				if e != nil {
					return nil, e
				}
			}
			if s.Name == "season mission" {
				if len(c.Season) > 0 || len(ids) != 18 {
					return nil, fmt.Errorf("战令赛季任务必须18项")
				}
				c.Season = ids
				continue
			}
			if len(ids) == 0 || len(ids)%4 != 0 {
				return nil, fmt.Errorf("战令每日每周必须四项分组")
			}
			groups := make([][4]uint32, len(ids)/4)
			for i := range groups {
				copy(groups[i][:], ids[i*4:i*4+4])
			}
			if s.Name == "daily mission" {
				if len(c.Daily) > 0 {
					return nil, fmt.Errorf("重复每日表")
				}
				c.Daily = groups
			} else {
				if len(c.Weekly) > 0 {
					return nil, fmt.Errorf("重复每周表")
				}
				c.Weekly = groups
			}
		case "exp table":
			if len(ts) != 160 {
				return nil, fmt.Errorf("战令经验表必须80级")
			}
			for i := 0; i < len(ts); i += 2 {
				level, err := number(ts, i)
				if err != nil {
					return nil, err
				}
				xp, err := number(ts, i+1)
				if err != nil {
					return nil, err
				}
				if level < 1 || level > 80 || seenExp[level] || (level < 80 && xp == 0) || (level == 80 && xp != 0) {
					return nil, fmt.Errorf("战令经验等级无效")
				}
				seenExp[level] = true
				c.Experience[level] = xp
			}
		case "level reward":
			if len(ts) < 1 || (len(ts)-1)%3 != 0 {
				return nil, fmt.Errorf("战令奖励三元组无效")
			}
			level, err := number(ts, 0)
			if err != nil {
				return nil, err
			}
			if level < 1 || level > 80 || seenReward[level] {
				return nil, fmt.Errorf("战令奖励等级无效")
			}
			seenReward[level] = true
			for i := 1; i < len(ts); i += 3 {
				id, err := number(ts, i)
				if err != nil {
					return nil, err
				}
				count, err := number(ts, i+1)
				if err != nil {
					return nil, err
				}
				track, err := number(ts, i+2)
				if err != nil {
					return nil, err
				}
				if id == 0 || count == 0 || count > math.MaxInt32 || track > 1 {
					return nil, fmt.Errorf("战令奖励无效")
				}
				c.Rewards[track][level] = append(c.Rewards[track][level], Reward{id, count})
				if len(c.Rewards[track][level]) > 4 {
					return nil, fmt.Errorf("战令每级每轨超过原生四项奖励容量")
				}
			}
		case "tier table":
			if len(ts) != 20 {
				return nil, fmt.Errorf("战令徽章等级表结构无效")
			}
			previous := uint32(0)
			for i := 0; i < 10; i++ {
				first, a := number(ts, i*2)
				last, b := number(ts, i*2+1)
				if a != nil || b != nil || first != previous+1 || last < first || last > 80 {
					return nil, fmt.Errorf("战令徽章等级范围无效")
				}
				for level := first; level <= last; level++ {
					c.Tier[level] = uint8(i)
				}
				previous = last
			}
			if previous != 80 {
				return nil, fmt.Errorf("战令徽章等级表未覆盖80级")
			}
		case "golden mission exp ratio":
			if len(ts) != 1 {
				return nil, fmt.Errorf("高级经验倍率结构无效")
			}
			c.PremiumRatio, e = number(ts, 0)
			if e != nil {
				return nil, e
			}
		}
	}
	if len(c.Missions) == 0 || len(c.Missions) > 70 || len(c.Daily) != 100 || len(c.Weekly) != 15 || len(c.Season) != 18 || len(seenExp) != 80 || len(seenReward) != 80 || c.PremiumRatio < 1 || c.PremiumRatio > 10 {
		return nil, fmt.Errorf("战令配置缺表或超出当前窗口容量")
	}
	check := func(ids []uint32) error {
		seen := map[uint32]bool{}
		for _, id := range ids {
			if c.Missions[id].ID == 0 || seen[id] {
				return fmt.Errorf("战令排程引用缺失/重复任务%d", id)
			}
			seen[id] = true
		}
		return nil
	}
	for _, g := range c.Daily {
		if e = check(g[:]); e != nil {
			return nil, e
		}
	}
	for _, g := range c.Weekly {
		if e = check(g[:]); e != nil {
			return nil, e
		}
	}
	if e = check(c.Season); e != nil {
		return nil, e
	}
	return c, nil
}
