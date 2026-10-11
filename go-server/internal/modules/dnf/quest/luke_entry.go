package quest

import (
	"fmt"
	"math"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// These are channel entrance requirements, not the separately consumed raid
// ticket. The roster's Luke flag still represents only its original quest.
type LukeEntryRequirement struct {
	QuestID, AntonQuestID int64
	ItemID, Count         int64
}

func LoadLukeEntryRequirement(source dnfpvf.Source) (LukeEntryRequirement, error) {
	if source == nil {
		return LukeEntryRequirement{}, fmt.Errorf("Luke entry PVF source missing")
	}
	text, err := source.ReadText(SpecialChannelRulePath)
	if err != nil {
		return LukeEntryRequirement{}, err
	}
	doc, err := dnfpvf.Parse(SpecialChannelRulePath, text)
	if err != nil {
		return LukeEntryRequirement{}, err
	}
	var values []int64
	for _, name := range []string{"luke", "lukeAntonQuest", "lukerequireditem", "lukerequireditemcount"} {
		v := doc.Ints(name)
		if len(v) != 1 || v[0] <= 0 || v[0] > math.MaxUint32 {
			return LukeEntryRequirement{}, fmt.Errorf("Luke entry requirement %s unavailable", name)
		}
		values = append(values, v[0])
	}
	if values[0] > 65535 || values[1] > 65535 {
		return LukeEntryRequirement{}, fmt.Errorf("Luke entry quest exceeds current quest table")
	}
	return LukeEntryRequirement{values[0], values[1], values[2], values[3]}, nil
}

func (c *Catalog) LukeEntryQuestsCompleted(rule LukeEntryRequirement, record dnfrepo.QuestRecord) bool {
	if c == nil || record.CharacterID == "" || rule.QuestID <= 0 || rule.AntonQuestID <= 0 {
		return false
	}
	completed, _ := questStateSets(record)
	for _, id := range []int64{rule.QuestID, rule.AntonQuestID} {
		if _, exists := c.Find(id); !exists {
			return false
		}
		if _, done := completed[id]; !done {
			return false
		}
	}
	return true
}
