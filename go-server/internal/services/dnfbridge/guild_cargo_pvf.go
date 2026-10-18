package dnfbridge

import (
	"fmt"
	"math"

	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Verify the configured warehouse limit and actual storage NPC in the current
// hall map. No hardcoded capacity default or guessed interaction radius.
func currentGuildCargoSceneLimit(source pvf.Source, mapPath string) (uint32, error) {
	if source == nil || !townMapPathLooksLikeGuildHall(mapPath) {
		return 0, fmt.Errorf("not a guild cargo scene")
	}
	read := func(root, name string) (*pvf.Document, error) {
		p, text, err := readDungeonDropText(source, root, name)
		if err != nil {
			return nil, err
		}
		return pvf.Parse(p, text)
	}
	config, err := read("etc", "guild.etc")
	if err != nil {
		return 0, err
	}
	values, found := config.Section("guild cargo size limit")
	if !found || len(values) != 1 || values[0].Kind != pvf.TokenInt || values[0].Int <= 0 || values[0].Int > math.MaxInt16+1 {
		return 0, fmt.Errorf("invalid cargo limit")
	}
	limit := uint32(values[0].Int)
	list, err := read("npc", "npc.lst")
	if err != nil {
		return 0, err
	}
	storagePath := ""
	for _, e := range pvf.ParseList(list) {
		if e.ID == 99 {
			if storagePath != "" {
				return 0, fmt.Errorf("duplicate storage NPC")
			}
			storagePath = e.Path
		}
	}
	if storagePath == "" {
		return 0, fmt.Errorf("missing storage NPC")
	}
	npc, err := read("npc", storagePath)
	if err != nil {
		return 0, err
	}
	roles, found := npc.Section("role")
	if !found || len(roles) != 2 || roles[0].Value != "[guild storage]" || roles[1].Kind != pvf.TokenInt || roles[1].Int != 0 {
		return 0, fmt.Errorf("storage NPC role mismatch")
	}
	doc, err := read("map", mapPath)
	if err != nil {
		return 0, err
	}
	m := worldmap.ParseMap(0, doc.Path, doc)
	for _, spawn := range m.NPCs {
		if spawn.NPCID == 99 {
			return limit, nil
		}
	}
	return 0, fmt.Errorf("current map has no storage NPC")
}
