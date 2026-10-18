package imagecommunication

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/town"
)

func TestSourceSceneUsesPVFMapRoleAndVisibility(t *testing.T) {
	for _, name := range []string{"valid", "wrong-area", "absent", "wrong-role", "wrong-role-value", "hidden", "bad-npc-row", "missing-definition"} {
		t.Run(name, func(t *testing.T) {
			s := fixtureSource(fixtureConfig)
			s[NPCListPath] += "808 `caller.npc`\n"
			s["npc/caller.npc"] = "[role]\n`[image communication]` 0\n[/role]\n"
			s["town/town.lst"] = "71 `example.twn`"
			s["town/example.twn"] = "[area]\n4 `Town/example.map`\n`[normal]`\n[/area]\n"
			s["map/town/example.map"] = "[npc]\n808 100 200 0\n[/npc]\n"
			area := int64(4)
			switch name {
			case "wrong-area":
				area++
			case "absent":
				s["map/town/example.map"] = "[map name] `empty`"
			case "wrong-role":
				s["npc/caller.npc"] = "[role]\n`[shop]` 0\n[/role]\n"
			case "wrong-role-value":
				s["npc/caller.npc"] = "[role]\n`[image communication]` 1\n[/role]\n"
			case "hidden":
				s["npc/caller.npc"] += "[hide at town]\n71\n"
			case "bad-npc-row":
				s["map/town/example.map"] = "[npc]\n808 100\n[/npc]\n"
			case "missing-definition":
				delete(s, "npc/caller.npc")
			}
			towns, err := town.Load(context.Background(), s, town.Options{})
			if err != nil {
				t.Fatal(err)
			}
			scene, err := LoadScene(context.Background(), s, towns, 71, area)
			if name == "valid" {
				if err != nil || !reflect.DeepEqual(scene.SourceNPCs, []int64{808}) {
					t.Fatalf("scene=%+v err=%v", scene, err)
				}
			} else if err == nil {
				t.Fatalf("invalid scene passed: %+v", scene)
			}
		})
	}
}
