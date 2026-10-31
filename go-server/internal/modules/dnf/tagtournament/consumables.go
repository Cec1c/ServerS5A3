package tagtournament

import (
	"fmt"
	"math"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// Consumable 保留真实PVF的效果单位。例如速度原值300，不能直接当作300%。
type Consumable struct {
	Resource
	Cooldown            time.Duration
	Duration            time.Duration
	RecoverySection     string
	RecoveryPercent     int64
	RecoveryIntervalRaw int64
	SuperArmorHits      int64
	StatChanges         []StatChange
}

type StatChange struct {
	Unit  string
	Value int64
	Stat  string
}

func loadConsumable(source dnfpvf.Source, resource Resource) (Consumable, error) {
	doc, err := readDocument(source, resource.Path)
	if err != nil {
		return Consumable{}, err
	}
	cooldown, ok := doc.Int("cool time")
	if !ok || cooldown <= 0 || cooldown > math.MaxUint32 {
		return Consumable{}, fmt.Errorf("帝国竞技场专用道具%d冷却时间无效", resource.ID)
	}
	typeName, typeOK := doc.Text("stackable type")
	if !typeOK || typeName != "[unlimited waste]" {
		return Consumable{}, fmt.Errorf("帝国竞技场专用道具%d不是无限使用道具", resource.ID)
	}
	rule := Consumable{Resource: resource, Cooldown: time.Duration(cooldown) * time.Millisecond}
	for _, section := range []string{"hp recovery", "mp recovery"} {
		if values, exists := doc.Section(section); exists {
			if rule.RecoverySection != "" || len(values) != 4 || values[0].Value != "%" || values[1].Kind != dnfpvf.TokenInt || values[1].Int <= 0 || values[1].Int > 100 || values[2].Kind != dnfpvf.TokenInt || values[2].Int <= 0 || values[3].Value != "myself" {
				return Consumable{}, fmt.Errorf("帝国竞技场恢复道具%d效果无效", resource.ID)
			}
			rule.RecoverySection, rule.RecoveryPercent, rule.RecoveryIntervalRaw = section, values[1].Int, values[2].Int
		}
	}
	if values, exists := doc.Section("stat change"); exists {
		if len(values) == 0 || len(values)%3 != 0 {
			return Consumable{}, fmt.Errorf("帝国竞技场专用道具%d属性效果截断", resource.ID)
		}
		for i := 0; i < len(values); i += 3 {
			if (values[i].Value != "%" && values[i].Value != "+") || values[i+1].Kind != dnfpvf.TokenInt || values[i+2].Kind != dnfpvf.TokenString {
				return Consumable{}, fmt.Errorf("帝国竞技场专用道具%d属性效果类型错误", resource.ID)
			}
			rule.StatChanges = append(rule.StatChanges, StatChange{values[i].Value, values[i+1].Int, values[i+2].Value})
		}
		duration, exists := doc.Section("stat change duration")
		if !exists || len(duration) != 2 || duration[0].Kind != dnfpvf.TokenInt || duration[0].Int <= 0 || duration[0].Int > math.MaxUint32 || duration[1].Value != "myself" {
			return Consumable{}, fmt.Errorf("帝国竞技场专用道具%d效果时长无效", resource.ID)
		}
		rule.Duration = time.Duration(duration[0].Int) * time.Millisecond
	}
	if values, exists := doc.Section("buff"); exists {
		if len(values) != 4 || values[0].Value != "superarmor" || values[1].Kind != dnfpvf.TokenInt || values[1].Int <= 0 || values[1].Int > math.MaxUint32 || values[2].Value != "myself" || values[3].Kind != dnfpvf.TokenInt || values[3].Int <= 0 {
			return Consumable{}, fmt.Errorf("帝国竞技场专用道具%d霸体效果无效", resource.ID)
		}
		rule.Duration = time.Duration(values[1].Int) * time.Millisecond
		rule.SuperArmorHits = values[3].Int
	}
	if rule.RecoverySection == "" && rule.SuperArmorHits == 0 && len(rule.StatChanges) == 0 {
		return Consumable{}, fmt.Errorf("帝国竞技场专用道具%d缺少已支持效果", resource.ID)
	}
	return rule, nil
}

func (c *Catalog) Consumables() [6]Consumable {
	var result [6]Consumable
	if c == nil {
		return result
	}
	result = c.consumables
	for i := range result {
		result[i].StatChanges = append([]StatChange(nil), result[i].StatChanges...)
	}
	return result
}
