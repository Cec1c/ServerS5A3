package dnfbridge

import (
	"context"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentEpicRoadEventID uint16 = 350

func validateCurrentEpicRoadActivity(source dnfpvf.Source) error {
	if source == nil {
		return fmt.Errorf("epic road source unavailable")
	}
	read := func(path string) (*dnfpvf.Document, error) {
		text, e := source.ReadText(path)
		if e != nil {
			return nil, e
		}
		return dnfpvf.Parse(path, text)
	}
	require := func(path, tag string, want ...int64) error {
		d, e := read(path)
		if e != nil {
			return e
		}
		v := d.Ints(tag)
		if len(v) != len(want) {
			return fmt.Errorf("epic road %s/%s count mismatch", path, tag)
		}
		for i := range v {
			if v[i] != want[i] {
				return fmt.Errorf("epic road %s/%s mismatch", path, tag)
			}
		}
		return nil
	}
	if e := require("npc/event_npc_soryu_2018.npc", "open visible event", 350); e != nil {
		return e
	}
	for _, p := range []string{"n_quest/event/18_epciroad2_1.qst", "n_quest/event/18_epciroad2_2.qst"} {
		if e := require(p, "show quest on event", 350); e != nil {
			return e
		}
		if e := require(p, "level", 17, 99); e != nil {
			return e
		}
	}
	if e := require("n_quest/event/18_epciroad2_2.qst", "pre required quest", 5578); e != nil {
		return e
	}
	if e := require("itemshop/2018_epicroad_kairyu.shp", "NPC", 3136); e != nil {
		return e
	}
	table, e := dnftown.Load(context.Background(), source, dnftown.Options{})
	if e != nil {
		return e
	}
	area, ok := table.FindArea(50, 0)
	if !ok || area.MinLevel != 17 || len(area.NeedQuests) != 1 || area.NeedQuests[0] != 5579 {
		return fmt.Errorf("epic road actual town50 prerequisite mismatch")
	}
	landing, ok, e := worldmap.ResolveNPCEnterTownLanding(source, 1, "map/common/(r)gate_seria.map", area.MapPath, 3136)
	if e != nil {
		return e
	}
	if !ok || landing.X != 441 || landing.Y != 207 {
		return fmt.Errorf("epic road actual NPC landing mismatch")
	}
	for i, p := range []string{"forestofsilent", "forestofdesire", "forestofcorruption", "forestofunrevert"} {
		path := "dungeon/event/2018_epciroad2/" + p + ".dgn"
		level := int64(85)
		if i == 0 {
			level = 17
		}
		if e := require(path, "minimum required level", level); e != nil {
			return e
		}
		if i > 0 {
			if e := require(path, "required item", 3330, int64(15*(i+1)), 1); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Service) currentEpicRoadActivityAvailable() bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" || s.activityControls != nil && !s.activityControls.Enabled(currentEpicRoadEventID) {
		return false
	}
	s.initialEquipmentMu.Lock()
	a, e := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	return e == nil && validateCurrentEpicRoadActivity(a) == nil
}
