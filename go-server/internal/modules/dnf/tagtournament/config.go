// Package tagtournament 提供帝国竞技场资源、日计划、双角色状态及账号事务规则。
// 桥接层在扣费前准备双角色资料，接通实际进图、Tag战斗、回城及账号结算。
package tagtournament

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const ConfigPath = "etc/tagtournament/tagtournament.etc"

type Mode byte

const (
	Normal Mode = 1
	Hard   Mode = 2
)

type Encounter struct {
	MapID     uint32
	MonsterID uint32
}

// RewardRow 保留原表首列。普通表是分组、困难表是阶段索引；不能混为同一语义。
type RewardRow struct {
	Index  byte
	ItemID uint32
	Count  uint16
	Weight byte // 困难三列表没有权重，此字段为零。
}

type MapPool struct {
	Take int
	Rows []Encounter
}

type Config struct {
	Fingerprint             string
	DailyRewardCount        byte
	DailyExtraRewardCount   byte
	NormalRewards           []RewardRow
	SpecialRewards          []RewardRow
	HardRewards             []RewardRow
	NormalPool              MapPool
	SpecialPool             MapPool
	HardPool                MapPool
	ReturnVillageTimes      [3]uint32
	Consumables             [6]uint32
	EntryGold               uint32
	NormalBuffStat          uint32
	HardBuffStat            uint32
	HardBalance             []float64
	DungeonIDs              [2]uint32
	CharacterTagCooldownRaw uint32
	HardChallengeTimeMS     uint32
	HardWeeklyRewardItemID  uint32
}

func LoadConfig(source dnfpvf.Source) (Config, error) {
	if source == nil {
		return Config{}, fmt.Errorf("帝国竞技场缺少PVF源")
	}
	text, err := source.ReadText(ConfigPath)
	if err != nil {
		return Config{}, fmt.Errorf("读取帝国竞技场配置: %w", err)
	}
	return ParseConfig(text)
}

func ParseConfig(text string) (Config, error) {
	doc, err := dnfpvf.Parse(ConfigPath, text)
	if err != nil {
		return Config{}, err
	}
	// 通用PVF解析器保留每个段落，故能识别原文件的倒序拼写闭合标记。
	// 不全局修正文档，也不忽略重复段或非整数值。
	sections := make(map[string][]dnfpvf.Token)
	blocks := map[string]string{}
	for _, name := range []string{"tag tournament reward", "tag tournament special reward", "tag tournament hard reward", "tag tournament random map", "tag tournament special map", "tag tournament hard map", "tag tournament stackable", "tag tournament hard mode balance", "tag tournament dungeon list"} {
		blocks[name] = "/" + name
	}
	for i, section := range doc.Sections {
		key := strings.ToLower(strings.TrimSpace(section.Name))
		if _, exists := sections[key]; exists {
			return Config{}, fmt.Errorf("帝国竞技场重复配置段[%s]", key)
		}
		sections[key] = doc.Tokens[section.Start:section.End]
		if close, block := blocks[key]; block {
			if i+1 == len(doc.Sections) {
				return Config{}, fmt.Errorf("帝国竞技场配置段[%s]缺少闭合", key)
			}
			next := strings.ToLower(strings.TrimSpace(doc.Sections[i+1].Name))
			if next != close && !(key == "tag tournament hard reward" && next == "/tag tournament reward hard") {
				return Config{}, fmt.Errorf("帝国竞技场配置段[%s]闭合错位", key)
			}
		}
	}
	ints := func(name string, count int) ([]uint32, error) {
		values, ok := sections[name]
		if !ok || (count >= 0 && len(values) != count) || len(values) == 0 {
			return nil, fmt.Errorf("帝国竞技场配置段[%s]缺失或字段数错误", name)
		}
		result := make([]uint32, len(values))
		for i, value := range values {
			if value.Kind != dnfpvf.TokenInt || value.Int < 0 || value.Int > math.MaxUint32 {
				return nil, fmt.Errorf("帝国竞技场配置段[%s]第%d字段不是有效无符号整数", name, i+1)
			}
			result[i] = uint32(value.Int)
		}
		return result, nil
	}
	var c Config
	// 周挑战读取PVF的毫秒基准；第一字段的排行含义不作为本轮发奖规则。
	if _, present := sections["tag tournament hard mode time attack standard rank"]; present {
		values, e := ints("tag tournament hard mode time attack standard rank", 2)
		if e != nil || values[0] == 0 || values[1] == 0 || values[1] > math.MaxInt32 {
			return Config{}, fmt.Errorf("帝国竞技场周挑战时间配置无效")
		}
		c.HardChallengeTimeMS = values[1]
	}
	if _, present := sections["tag tournament hard mode time attack reward"]; present {
		values, e := ints("tag tournament hard mode time attack reward", 4)
		if e != nil || values[0] == 0 || c.HardChallengeTimeMS == 0 {
			return Config{}, fmt.Errorf("帝国竞技场周挑战奖励配置无效")
		}
		// 后三项保留原PVF语义，不把1668/1669/15猜成附件数量或期限。
		c.HardWeeklyRewardItemID = values[0]
	}
	for _, field := range []struct {
		name string
		dest *uint32
	}{
		{"tag tournament enter gold", &c.EntryGold},
		{"tag tournament buff add stat", &c.NormalBuffStat},
		{"tag tournament hard mode buff add state", &c.HardBuffStat},
		{"tag tournament dungeon charac tag cool time", &c.CharacterTagCooldownRaw},
	} {
		values, e := ints(field.name, 1)
		if e != nil {
			return Config{}, e
		}
		*field.dest = values[0]
	}
	for i, name := range []string{"daily default reward count", "daily default extra reward count"} {
		values, e := ints(name, 1)
		if e != nil || values[0] > 255 {
			return Config{}, fmt.Errorf("帝国竞技场每日奖励次数无效[%s]: %v", name, e)
		}
		if i == 0 {
			c.DailyRewardCount = byte(values[0])
		} else {
			c.DailyExtraRewardCount = byte(values[0])
		}
	}
	for _, field := range []struct {
		name string
		dest []uint32
	}{
		{"tag tournament return village time", c.ReturnVillageTimes[:]},
		{"tag tournament stackable", c.Consumables[:]},
		{"tag tournament dungeon list", c.DungeonIDs[:]},
	} {
		values, e := ints(field.name, len(field.dest))
		if e != nil {
			return Config{}, e
		}
		copy(field.dest, values)
	}
	for _, field := range []struct {
		name string
		dest *MapPool
	}{
		{"tag tournament random map", &c.NormalPool},
		{"tag tournament special map", &c.SpecialPool},
		{"tag tournament hard map", &c.HardPool},
	} {
		values, e := ints(field.name, -1)
		if e != nil {
			return Config{}, e
		}
		if len(values) < 3 || len(values)%2 != 1 || values[0] == 0 || values[0] > uint32((len(values)-1)/2) || len(values) > 511 {
			return Config{}, fmt.Errorf("帝国竞技场地图池[%s]抽取数量或配对错误", field.name)
		}
		pool := MapPool{Take: int(values[0])}
		seenMap, seenMonster := map[uint32]bool{}, map[uint32]bool{}
		for i := 1; i < len(values); i += 2 {
			if values[i] == 0 || values[i+1] == 0 || seenMap[values[i]] || seenMonster[values[i+1]] {
				return Config{}, fmt.Errorf("帝国竞技场地图池[%s]含零编号或重复条目", field.name)
			}
			seenMap[values[i]], seenMonster[values[i+1]] = true, true
			pool.Rows = append(pool.Rows, Encounter{values[i], values[i+1]})
		}
		*field.dest = pool
	}
	for _, field := range []struct {
		name  string
		dest  *[]RewardRow
		width int
	}{
		{"tag tournament reward", &c.NormalRewards, 4},
		{"tag tournament special reward", &c.SpecialRewards, 4},
		{"tag tournament hard reward", &c.HardRewards, 3},
	} {
		values, e := ints(field.name, -1)
		if e != nil {
			return Config{}, e
		}
		if len(values)%field.width != 0 {
			return Config{}, fmt.Errorf("帝国竞技场奖励表[%s]列数错误", field.name)
		}
		for i := 0; i < len(values); i += field.width {
			if values[i] > 255 || values[i+1] == 0 || values[i+2] == 0 || values[i+2] > math.MaxUint16 {
				return Config{}, fmt.Errorf("帝国竞技场奖励表[%s]编号或数量越界", field.name)
			}
			row := RewardRow{Index: byte(values[i]), ItemID: values[i+1], Count: uint16(values[i+2])}
			if field.width == 4 {
				if values[i+3] == 0 || values[i+3] > 100 {
					return Config{}, fmt.Errorf("帝国竞技场奖励表[%s]权重无效", field.name)
				}
				row.Weight = byte(values[i+3])
			}
			*field.dest = append(*field.dest, row)
		}
	}
	for _, table := range [][]RewardRow{c.NormalRewards, c.SpecialRewards} {
		weights := map[byte]int{}
		for _, row := range table {
			weights[row.Index] += int(row.Weight)
		}
		for _, sum := range weights {
			if sum != 100 {
				return Config{}, fmt.Errorf("帝国竞技场普通奖励分组权重总和不是100")
			}
		}
	}
	stages := make(map[byte]map[uint32]bool)
	for _, row := range c.HardRewards {
		if int(row.Index) >= c.HardPool.Take {
			return Config{}, fmt.Errorf("帝国竞技场困难奖励阶段越界")
		}
		if stages[row.Index] == nil {
			stages[row.Index] = map[uint32]bool{}
		}
		if stages[row.Index][row.ItemID] {
			return Config{}, fmt.Errorf("帝国竞技场困难同阶段物品重复")
		}
		stages[row.Index][row.ItemID] = true
	}
	if len(stages) != c.HardPool.Take {
		return Config{}, fmt.Errorf("帝国竞技场困难奖励阶段不完整")
	}
	balance, ok := sections["tag tournament hard mode balance"]
	if !ok || len(balance) != c.HardPool.Take+1 || balance[0].Kind != dnfpvf.TokenInt || balance[0].Int != int64(c.HardPool.Take) {
		return Config{}, fmt.Errorf("帝国竞技场困难平衡表阶段数错误")
	}
	for _, token := range balance[1:] {
		var n float64
		switch token.Kind {
		case dnfpvf.TokenInt:
			n = float64(token.Int)
		case dnfpvf.TokenFloat:
			n = token.Float
		default:
			return Config{}, fmt.Errorf("帝国竞技场困难平衡表含非数值")
		}
		if n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return Config{}, fmt.Errorf("帝国竞技场困难平衡倍率无效")
		}
		c.HardBalance = append(c.HardBalance, n)
	}
	if c.DungeonIDs[0] == 0 || c.DungeonIDs[1] == 0 || c.DungeonIDs[0] == c.DungeonIDs[1] {
		return Config{}, fmt.Errorf("帝国竞技场副本编号无效")
	}
	// 这些段必须有空闭合标记；保留现用PVF的 hard/reward 倒序拼写兼容。
	for _, name := range []string{"tag tournament reward", "tag tournament special reward", "tag tournament random map", "tag tournament special map", "tag tournament hard map", "tag tournament stackable", "tag tournament hard mode balance", "tag tournament dungeon list"} {
		if values, ok := sections["/"+name]; !ok || len(values) != 0 {
			return Config{}, fmt.Errorf("帝国竞技场配置段[%s]未正确闭合", name)
		}
	}
	_, ordinaryClose := sections["/tag tournament hard reward"]
	_, legacyClose := sections["/tag tournament reward hard"]
	if ordinaryClose == legacyClose || len(sections["/tag tournament hard reward"])+len(sections["/tag tournament reward hard"]) != 0 {
		return Config{}, fmt.Errorf("帝国竞技场困难奖励段闭合错误")
	}
	c.Fingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	return c, nil
}

// HardRewardAtStage 返回该阶段原始表行，不把多阶段相加，也不作为可领凭据。
// 发奖时机、跨模式日额度和失败退出处理仍须经原生结算及事务验证。
func (c Config) HardRewardAtStage(stage int) ([]RewardRow, error) {
	if stage < 0 || stage >= c.HardPool.Take {
		return nil, fmt.Errorf("帝国竞技场困难奖励阶段无效")
	}
	var result []RewardRow
	for _, row := range c.HardRewards {
		if int(row.Index) == stage {
			result = append(result, row)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("帝国竞技场困难奖励阶段缺失")
	}
	return result, nil
}
