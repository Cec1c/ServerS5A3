package tagtournament

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
)

// DailyPolicy 是服务端确定性日计划的版本，不是从PVF读出的官方星期轮换表。
// 同配置、同北京时间06:00服务日的所有账号/进程获得同一名单；重登不重抽。
const DailyPolicy = "local-tag-daily-v1"

type DailyPlan struct {
	Policy         string
	ConfigHash     string
	ServiceDay     int64
	Mode           Mode
	DungeonID      uint32
	Encounters     []DailyEncounter
	SpecialFrom    int // 普通模式的特殊池起点；困难模式等于总长度。
	CommonWeakness DamageWeakness
}

// 领域名称不等同于客户端弱点字节；协议投影须另行核实映射。
type ElementWeakness string
type DamageWeakness string

const (
	Fire     ElementWeakness = "火"
	Ice      ElementWeakness = "冰"
	Light    ElementWeakness = "光"
	Dark     ElementWeakness = "暗"
	Physical DamageWeakness  = "物理"
	Magical  DamageWeakness  = "魔法"
)

type DailyEncounter struct {
	Encounter
	Weakness ElementWeakness
	Balance  float64
	Optional bool
}

func (c Config) DailyPlan(now time.Time, mode Mode) (DailyPlan, error) {
	if c.Fingerprint == "" {
		return DailyPlan{}, fmt.Errorf("帝国竞技场日计划缺少已验证配置")
	}
	plan := DailyPlan{Policy: DailyPolicy, ConfigHash: c.Fingerprint, ServiceDay: premium.ServiceDayID(now), Mode: mode}
	var encounters []Encounter
	switch mode {
	case Normal:
		plan.DungeonID = c.DungeonIDs[0]
		normal, err := dailyPick(c.NormalPool, plan, "normal")
		if err != nil {
			return DailyPlan{}, err
		}
		special, err := dailyPick(c.SpecialPool, plan, "special")
		if err != nil {
			return DailyPlan{}, err
		}
		plan.SpecialFrom = len(normal)
		encounters = append(normal, special...)
	case Hard:
		plan.DungeonID = c.DungeonIDs[1]
		if c.HardPool.Take == 0 || c.HardPool.Take != len(c.HardPool.Rows) || len(c.HardBalance) != c.HardPool.Take {
			return DailyPlan{}, fmt.Errorf("帝国竞技场困难日计划要求完整阶段表")
		}
		var err error
		encounters, err = dailyPick(c.HardPool, plan, "hard")
		if err != nil {
			return DailyPlan{}, err
		}
		plan.SpecialFrom = len(encounters)
		if dailyByte(plan, "common-weakness", 0)%2 == 0 {
			plan.CommonWeakness = Physical
		} else {
			plan.CommonWeakness = Magical
		}
	default:
		return DailyPlan{}, fmt.Errorf("帝国竞技场模式无效: %d", mode)
	}
	elements := [...]ElementWeakness{Fire, Ice, Light, Dark}
	for i, encounter := range encounters {
		row := DailyEncounter{Encounter: encounter, Balance: 1, Optional: mode == Normal && i >= plan.SpecialFrom}
		if !row.Optional {
			row.Weakness = elements[dailyByte(plan, "element-weakness", encounter.MonsterID)%4]
		}
		// 强度属于回合而非怪物：每日打乱后再按阶段附加真实PVF倍率。
		if mode == Hard {
			row.Balance = c.HardBalance[i]
		}
		plan.Encounters = append(plan.Encounters, row)
	}
	return plan, nil
}

func dailyByte(plan DailyPlan, label string, monsterID uint32) byte {
	key := fmt.Sprintf("%s/%s/%d/%d/%s/%d", DailyPolicy, plan.ConfigHash, plan.ServiceDay, plan.Mode, label, monsterID)
	hash := sha256.Sum256([]byte(key))
	return hash[0]
}

func dailyPick(pool MapPool, plan DailyPlan, label string) ([]Encounter, error) {
	if pool.Take <= 0 || pool.Take > len(pool.Rows) {
		return nil, fmt.Errorf("帝国竞技场日计划抽取数量无效")
	}
	type ranked struct {
		encounter Encounter
		hash      [32]byte
	}
	rows := make([]ranked, 0, len(pool.Rows))
	seen := make(map[uint32]bool, len(pool.Rows))
	for _, encounter := range pool.Rows {
		if encounter.MapID == 0 || encounter.MonsterID == 0 || seen[encounter.MapID] {
			return nil, fmt.Errorf("帝国竞技场日计划含无效或重复地图")
		}
		seen[encounter.MapID] = true
		prefix := fmt.Sprintf("%s/%s/%d/%s/", DailyPolicy, plan.ConfigHash, plan.ServiceDay, label)
		key := make([]byte, len(prefix)+8)
		copy(key, prefix)
		binary.LittleEndian.PutUint32(key[len(prefix):], encounter.MapID)
		binary.LittleEndian.PutUint32(key[len(prefix)+4:], encounter.MonsterID)
		rows = append(rows, ranked{encounter, sha256.Sum256(key)})
	}
	sort.Slice(rows, func(i, j int) bool { return bytes.Compare(rows[i].hash[:], rows[j].hash[:]) < 0 })
	result := make([]Encounter, pool.Take)
	for i := range result {
		result[i] = rows[i].encounter
	}
	return result, nil
}
