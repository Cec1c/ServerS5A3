package quest

import (
	"fmt"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const SpecialChannelRulePath = "etc/enterablespecialchannel.etc"

// SpecialChannelRule is the actual PVF's entry quest requirement, separate
// from raid start tickets and daily clear limits.
type SpecialChannelRule struct {
	AntonQuestID    int64
	LukeQuestID     int64
	FiendWarQuestID int64
}

type SpecialChannelPermission struct {
	Anton    bool
	Luke     bool
	FiendWar bool
}

func LoadSpecialChannelRule(source dnfpvf.Source) (SpecialChannelRule, error) {
	if source == nil {
		return SpecialChannelRule{}, fmt.Errorf("special channel PVF source missing")
	}
	text, err := source.ReadText(SpecialChannelRulePath)
	if err != nil {
		return SpecialChannelRule{}, err
	}
	doc, err := dnfpvf.Parse(SpecialChannelRulePath, text)
	if err != nil {
		return SpecialChannelRule{}, err
	}
	anton, luke, fiend := doc.Ints("anton"), doc.Ints("luke"), doc.Ints("fiendwar")
	if len(anton) != 1 || len(luke) != 1 || len(fiend) != 1 || anton[0] <= 0 || luke[0] <= 0 || fiend[0] <= 0 || anton[0] > 65535 || luke[0] > 65535 || fiend[0] > 65535 {
		return SpecialChannelRule{}, fmt.Errorf("special channel entry quest shape unsupported")
	}
	return SpecialChannelRule{AntonQuestID: anton[0], LukeQuestID: luke[0], FiendWarQuestID: fiend[0]}, nil
}

// SpecialChannelPermissionForRecord is a read-only projection of completed
// persisted quests. An active quest with zero remaining is still awaiting its
// finish transaction and cannot grant channel admission. No quest is completed
// or reward settled by this query.
func (c *Catalog) SpecialChannelPermissionForRecord(rule SpecialChannelRule, record dnfrepo.QuestRecord) SpecialChannelPermission {
	if c == nil || record.CharacterID == "" {
		return SpecialChannelPermission{}
	}
	completed, _ := questStateSets(record)
	allowed := func(id int64) bool {
		if _, exists := c.Find(id); !exists {
			return false
		}
		_, done := completed[id]
		return done
	}
	return SpecialChannelPermission{Anton: allowed(rule.AntonQuestID), Luke: allowed(rule.LukeQuestID), FiendWar: allowed(rule.FiendWarQuestID)}
}
