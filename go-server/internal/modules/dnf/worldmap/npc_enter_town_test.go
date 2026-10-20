package worldmap

import (
	"fmt"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

type npcEnterTownSource map[string]string

func (s npcEnterTownSource) ReadText(p string) (string, error) {
	v, ok := s[p]
	if !ok {
		return "", fmt.Errorf("missing %s", p)
	}
	return v, nil
}

func TestNPCEnterTownLandingUsesPVFConnection(t *testing.T) {
	for _, name := range []string{"valid", "wrong-role", "wrong-id", "wrong-x", "wrong-y", "hidden", "missing-source", "duplicate-target", "duplicate-list", "missing-import"} {
		t.Run(name, func(t *testing.T) {
			src := npcEnterTownSource{"map/source.map": "[import script]\n`map/shared.map`", "map/shared.map": "[NPC]\n20275 `[left]` 450 200 0\n[/NPC]", "map/target.map": "[NPC]\n20276 `[left]` 1151 229 0\n[/NPC]", "npc/npc.lst": "20275 `source.npc`", "npc/source.npc": "[role]\n`[enter town]` 20276 `[item shop]` 1277\n[/role]"}
			id, x, y := int64(20276), int64(1151), int64(259)
			switch name {
			case "wrong-role":
				src["npc/source.npc"] = "[role]\n`[enter town]` 20277\n[/role]"
			case "wrong-id":
				id = 20277
			case "wrong-x":
				x++
			case "wrong-y":
				y++
			case "hidden":
				src["npc/source.npc"] += "\n[hide at town]\n38\n[/hide at town]"
			case "missing-source":
				src["map/shared.map"] = "[name]\n`empty`"
			case "duplicate-target":
				src["map/target.map"] = "[NPC]\n20276 `[left]` 1151 229 0 20276 `[right]` 1151 229 0\n[/NPC]"
			case "duplicate-list":
				src["npc/npc.lst"] += "\n20275 `other.npc`"
			case "missing-import":
				delete(src, "map/shared.map")
			}
			ok, err := NPCEnterTownLanding(src, 38, "source.map", "target.map", id, x, y)
			if ok != (name == "valid") {
				t.Fatalf("landing=%v err=%v", ok, err)
			}
			if name == "valid" && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRealPVFAbisNPCEnterTownLanding(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set real published PVF path")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := NPCEnterTownLanding(a, 38, "Cataclysm/Town/Elvengard/new_seria_room.map", "Town/DayDungeon/DayDungeon_town.map", 20276, 1151, 259)
	if err != nil || !ok {
		t.Fatalf("actual Abis route=%v err=%v", ok, err)
	}
}
