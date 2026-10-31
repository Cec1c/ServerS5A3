package tagtournament

import (
	"fmt"
	"path"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type Resource struct {
	ID   uint32
	Path string
	Name string
}

type Dungeon struct {
	Resource
	MinimumLevel uint32
	BasisLevel   uint32
}

// Catalog 只保留已从索引、正文交叉验证的资源；配置和内部映射不向调用者暴露。
type Catalog struct {
	config       Config
	dungeons     [2]Dungeon
	maps         map[uint32]Resource
	monsters     map[uint32]Resource
	items        map[uint32]Resource
	consumables  [6]Consumable
	weeklyReward WeeklyReward
}

// RewardDisplayIcons只投影本模式奖励道具，数量0表示本次没有新附件。
// 保持配置顺序并合并同道具的随机数量行；不报价、不改变每日领取状态。
func (c *Catalog) RewardDisplayIcons(mode Mode, optional bool) ([]RewardRow, error) {
	if c == nil || mode != Normal && mode != Hard || mode == Hard && optional {
		return nil, fmt.Errorf("帝国竞技场奖励图标模式无效")
	}
	table := c.config.NormalRewards
	if mode == Hard {
		table = c.config.HardRewards
	} else if optional {
		table = c.config.SpecialRewards
	}
	var out []RewardRow
	seen := map[uint32]bool{}
	for _, row := range table {
		if !seen[row.ItemID] {
			out = append(out, RewardRow{ItemID: row.ItemID})
			seen[row.ItemID] = true
		}
	}
	return out, nil
}

func LoadCatalog(source dnfpvf.Source) (*Catalog, error) {
	config, err := LoadConfig(source)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{config: config}
	mapIDs, monsterIDs, itemIDs := map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}
	for _, pool := range []MapPool{config.NormalPool, config.SpecialPool, config.HardPool} {
		for _, encounter := range pool.Rows {
			if mapIDs[encounter.MapID] || monsterIDs[encounter.MonsterID] {
				return nil, fmt.Errorf("帝国竞技场不同地图池使用重复的地图或怪物编号")
			}
			mapIDs[encounter.MapID], monsterIDs[encounter.MonsterID] = true, true
		}
	}
	for _, table := range [][]RewardRow{config.NormalRewards, config.SpecialRewards, config.HardRewards} {
		for _, row := range table {
			itemIDs[row.ItemID] = true
		}
	}
	for _, id := range config.Consumables {
		itemIDs[id] = true
	}
	if config.HardWeeklyRewardItemID != 0 {
		itemIDs[config.HardWeeklyRewardItemID] = true
	}
	if catalog.items, err = loadResources(source, "stackable", itemIDs); err != nil {
		return nil, err
	}
	if config.HardWeeklyRewardItemID != 0 {
		catalog.weeklyReward, err = loadWeeklyReward(source, catalog.items[config.HardWeeklyRewardItemID])
		if err != nil {
			return nil, err
		}
	}
	for i, id := range config.Consumables {
		catalog.consumables[i], err = loadConsumable(source, catalog.items[id])
		if err != nil {
			return nil, err
		}
	}
	if catalog.monsters, err = loadResources(source, "monster", monsterIDs); err != nil {
		return nil, err
	}
	if catalog.maps, err = loadResources(source, "map", mapIDs); err != nil {
		return nil, err
	}
	dungeons, err := loadResources(source, "dungeon", map[uint32]bool{config.DungeonIDs[0]: true, config.DungeonIDs[1]: true})
	if err != nil {
		return nil, err
	}
	for index, id := range config.DungeonIDs {
		resource := dungeons[id]
		doc, e := readDocument(source, resource.Path)
		if e != nil {
			return nil, e
		}
		mode, modeOK := doc.Int("tag tournament dungeon")
		level, levelOK := doc.Int("minimum required level")
		basis, basisOK := doc.Int("basis level")
		if !modeOK || mode != int64(index+1) || !levelOK || level <= 0 || level > 255 || !basisOK || basis <= 0 || basis > 65535 {
			return nil, fmt.Errorf("帝国竞技场副本%d模式、等级或基准等级错误", id)
		}
		for _, flag := range []string{"disable exp", "disable drop item", "no fatigue"} {
			if _, ok := doc.Section(flag); !ok {
				return nil, fmt.Errorf("帝国竞技场副本%d缺少[%s]约束", id, flag)
			}
		}
		catalog.dungeons[index] = Dungeon{Resource: resource, MinimumLevel: uint32(level), BasisLevel: uint32(basis)}
	}
	for _, pool := range []MapPool{config.NormalPool, config.SpecialPool, config.HardPool} {
		for _, encounter := range pool.Rows {
			resource := catalog.maps[encounter.MapID]
			doc, e := readDocument(source, resource.Path)
			if e != nil {
				return nil, e
			}
			parsed := worldmap.ParseMap(int64(encounter.MapID), resource.Path, doc)
			found := false
			for _, monster := range parsed.Monsters {
				found = found || monster.MonsterID == int64(encounter.MonsterID)
			}
			if !found {
				return nil, fmt.Errorf("帝国竞技场地图%d未定义配置怪物%d", encounter.MapID, encounter.MonsterID)
			}
		}
	}
	return catalog, nil
}

func readDocument(source dnfpvf.Source, name string) (*dnfpvf.Document, error) {
	text, err := source.ReadText(name)
	if err != nil {
		return nil, fmt.Errorf("读取帝国竞技场资源%s: %w", name, err)
	}
	return dnfpvf.Parse(name, text)
}

func loadResources(source dnfpvf.Source, folder string, wanted map[uint32]bool) (map[uint32]Resource, error) {
	doc, err := readDocument(source, folder+"/"+folder+".lst")
	if err != nil {
		return nil, err
	}
	result := make(map[uint32]Resource, len(wanted))
	for _, entry := range dnfpvf.ParseList(doc) {
		if entry.ID <= 0 || entry.ID > 0xffffffff || !wanted[uint32(entry.ID)] {
			continue
		}
		id := uint32(entry.ID)
		if _, exists := result[id]; exists {
			return nil, fmt.Errorf("帝国竞技场%s索引%d重复", folder, id)
		}
		ref := strings.ReplaceAll(entry.Path, "\\", "/")
		if strings.Contains(ref, ":") || strings.HasPrefix(ref, "/") {
			return nil, fmt.Errorf("帝国竞技场资源引用不是归档内相对路径")
		}
		if !strings.HasPrefix(strings.ToLower(ref), folder+"/") {
			ref = folder + "/" + ref
		}
		ref = path.Clean(ref)
		if !strings.HasPrefix(strings.ToLower(ref), folder+"/") {
			return nil, fmt.Errorf("帝国竞技场资源引用越过%s目录", folder)
		}
		resourceDoc, e := readDocument(source, ref)
		if e != nil {
			return nil, e
		}
		name, _ := resourceDoc.Text("name")
		if folder != "map" && strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("帝国竞技场资源%s缺少名称", ref)
		}
		result[id] = Resource{ID: id, Path: ref, Name: name}
	}
	for id := range wanted {
		if _, found := result[id]; !found {
			return nil, fmt.Errorf("帝国竞技场%s索引缺少编号%d", folder, id)
		}
	}
	return result, nil
}

func (c *Catalog) DailyPlan(now time.Time, mode Mode) (DailyPlan, error) {
	if c == nil {
		return DailyPlan{}, fmt.Errorf("帝国竞技场资源目录未加载")
	}
	return c.config.DailyPlan(now, mode)
}

func (c *Catalog) Dungeon(mode Mode) (Dungeon, bool) {
	if c == nil || (mode != Normal && mode != Hard) {
		return Dungeon{}, false
	}
	return c.dungeons[int(mode)-1], true
}

func (c *Catalog) Monster(id uint32) (Resource, bool) {
	if c == nil {
		return Resource{}, false
	}
	r, ok := c.monsters[id]
	return r, ok
}

func (c *Catalog) Item(id uint32) (Resource, bool) {
	if c == nil {
		return Resource{}, false
	}
	r, ok := c.items[id]
	return r, ok
}
