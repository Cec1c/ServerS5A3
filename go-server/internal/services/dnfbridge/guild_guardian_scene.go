package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
)

type guildGuardianNPC struct{ id, x, y, dx, dy int }
type guildGuardianScene struct {
	area   int
	first  map[uint32]guildGuardianNPC
	active map[uint32][5]guildGuardianNPC
}

// The NPCs are generated from agitwar.etc, not static MAP NPC rows.
func loadGuildGuardianScene(source pvf.Source) (*guildGuardianScene, error) {
	bad := guild.ErrGuardianInteraction
	read := func(root, name string) (*pvf.Document, error) {
		path, text, err := readDungeonDropText(source, root, name)
		if err != nil {
			return nil, err
		}
		return pvf.Parse(path, text)
	}
	doc, err := read("etc", "agitwar.etc")
	if err != nil {
		return nil, err
	}
	ints := func(tokens []pvf.Token, count int) ([]int, bool) {
		if len(tokens) != count {
			return nil, false
		}
		v := make([]int, count)
		for i, t := range tokens {
			if t.Kind != pvf.TokenInt || t.Int < 0 || t.Int > 65535 {
				return nil, false
			}
			v[i] = int(t.Int)
		}
		return v, true
	}
	area, found := doc.Section("guardian npc visible area")
	a, ok := ints(area, 1)
	if !found || !ok || a[0] > 255 {
		return nil, bad
	}
	position, found := doc.Section("attend guardian npc position")
	p, ok := ints(position, 2)
	if !found || !ok {
		return nil, bad
	}
	list, err := read("npc", "npc.lst")
	if err != nil {
		return nil, err
	}
	paths := make(map[int64]string)
	for _, row := range pvf.ParseList(list) {
		if _, exists := paths[row.ID]; exists {
			return nil, bad
		}
		paths[row.ID] = row.Path
	}
	load := func(id, x, y int, role string, value int) (guildGuardianNPC, error) {
		path := paths[int64(id)]
		if path == "" {
			return guildGuardianNPC{}, bad
		}
		npc, err := read("npc", path)
		if err != nil {
			return guildGuardianNPC{}, err
		}
		r, found := npc.Section("role")
		if !found || len(r) != 2 || r[0].Value != role || r[1].Kind != pvf.TokenInt || int(r[1].Int) != value {
			return guildGuardianNPC{}, bad
		}
		n := guildGuardianNPC{id: id, x: x, y: y, dx: 40, dy: 40}
		if extra, found := npc.Section("clickable distance add"); found {
			v, ok := ints(extra, 2)
			if !ok {
				return guildGuardianNPC{}, bad
			}
			n.dx += v[0]
			n.dy += v[1]
		}
		return n, nil
	}
	scene := &guildGuardianScene{area: a[0], first: make(map[uint32]guildGuardianNPC), active: make(map[uint32][5]guildGuardianNPC)}
	inFirst, id := false, uint32(0)
	for _, section := range doc.Sections {
		v := doc.Tokens[section.Start:section.End]
		switch strings.ToLower(strings.TrimSpace(section.Name)) {
		case "first guardian":
			inFirst = true
		case "/first guardian":
			inFirst = false
		case "guardian npc":
			if !inFirst || len(scene.first) != 0 {
				return nil, bad
			}
			values, ok := ints(v, 6)
			if !ok {
				return nil, bad
			}
			for i := 0; i < 2; i++ {
				// Role212's payload is the guardian ID, not the NPC number.
				n, err := load(values[i*3], values[i*3+1], values[i*3+2], "[attend guardian]", i+1)
				if err != nil {
					return nil, err
				}
				scene.first[uint32(i+1)] = n
			}
		case "guardian":
			values, ok := ints(v, 1)
			if !ok || values[0] < 1 || values[0] > 2 {
				return nil, bad
			}
			id = uint32(values[0])
		case "/guardian":
			id = 0
		case "npc index":
			if id == 0 {
				continue
			}
			if _, exists := scene.active[id]; exists {
				return nil, bad
			}
			values, ok := ints(v, 5)
			if !ok {
				return nil, bad
			}
			var rows [5]guildGuardianNPC
			for i, npc := range values {
				rows[i], err = load(npc, p[0], p[1], "[agitwar guardian window]", 0)
				if err != nil {
					return nil, err
				}
			}
			scene.active[id] = rows
		}
	}
	if len(scene.first) != 2 || len(scene.active) != 2 {
		return nil, bad
	}
	return scene, nil
}

func (s *guildGuardianScene) hasInteractionNPC(g *guild.Guild, target uint32) bool {
	var npc guildGuardianNPC
	if g.Guardians == nil || g.Guardians.ActiveID == 0 {
		npc = s.first[target]
	} else {
		for _, row := range g.Guardians.Owned {
			if row.ID == g.Guardians.ActiveID && row.Level >= 1 && row.Level <= 50 {
				npc = s.active[row.ID][(row.Level-1)/10]
				break
			}
		}
	}
	// V176 mouse hit-testing opens the NPC menu without the keyboard
	// nearby-NPC finder. The hall and current NPC still have to exist.
	return npc.id != 0
}
