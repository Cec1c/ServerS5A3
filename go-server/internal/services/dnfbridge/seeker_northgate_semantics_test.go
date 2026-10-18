package dnfbridge

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestSeekerNorthgateRandomizedEvidence(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, e := platformpvf.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	table, e := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	monsters, e := newPVFDungeonMonsterCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	d, ok := table.FindDungeon(88)
	if !ok {
		t.Fatal("missing dungeon88")
	}
	counts := map[int64]int{}
	for _, dungeon := range table.Dungeons() {
		for _, m := range dungeon.Mazes {
			for g, s := range m.RandomizedObjects {
				counts[s.Regenerate.Value]++
				if s.Regenerate.Value != 99 {
					continue
				}
				for _, o := range s.Objects {
					t.Logf("regen99 d=%d maze=%d group=%d connection=%v select=%d candidates=%d object=%d type=%q team=%s room=%+v pos=%+v", dungeon.ID, m.Index, g, m.QuestConnection, s.SelectCount.Value, len(s.Objects), o.ObjectIndex.Value, o.ObjectType, o.Team, o.MazeCoordinate, o.Position)
				}
			}
		}
	}
	keys := make([]int64, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		t.Logf("regenerate distribution value=%d groups=%d", k, counts[k])
	}
	for _, lst := range []string{"monster/monster.lst", "passiveobject/passiveobject.lst"} {
		text, e := a.ReadText(lst)
		if e != nil {
			t.Fatal(e)
		}
		doc, e := dnfpvf.Parse(lst, text)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range dnfpvf.ParseList(doc) {
			if entry.ID != 61235 {
				continue
			}
			ref := strings.ReplaceAll(entry.Path, "\\", "/")
			if !strings.HasPrefix(strings.ToLower(ref), path.Dir(lst)+"/") {
				ref = path.Join(path.Dir(lst), ref)
			}
			text, e = a.ReadText(ref)
			if e != nil {
				t.Fatal(e)
			}
			doc, e = dnfpvf.Parse(ref, text)
			if e != nil {
				t.Fatal(e)
			}
			name, _ := doc.Text("name")
			kind, _ := doc.Text("monster object type")
			t.Logf("namespace=%s id61235 path=%s name=%s monster_object_type=%q", lst, ref, name, kind)
		}
	}
	svc := &Service{initialEquipmentArchive: a, dungeonMonsterTable: monsters, dungeonChoice: func(int) (int, error) { return 0, nil }}
	for _, mi := range []int{0, 2, 3, 4, 5} {
		top, e := worldmap.BuildDungeonLayout(resolver, 88, mi, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
		if e != nil {
			t.Fatal(e)
		}
		owner, e := svc.prepareCurrentDungeonRandomizedOwner(d, mi, top, nil)
		if e != nil {
			t.Fatalf("maze%d actual regenerate99: %v", mi, e)
		}
		if e = owner.validate(d, mi); e != nil {
			t.Fatal(e)
		}
		if len(owner.Entries) != 1 {
			t.Fatalf("maze%d entries=%v", mi, owner.Entries)
		}
		entry := owner.Entries[0]
		if entry.Source.ObjectID != 61235 || entry.Source.Regenerate != (worldmap.OptionalInt{Set: true, Value: 99}) || entry.SpawnMode != 0 || entry.Faction != 0 {
			t.Fatalf("wrong source policy/namespace/faction: %+v", entry)
		}
		rows := owner.roomGroups(entry.Source.Map)
		t.Logf("actual regen99 maze=%d entry=%+v rows=%+v minimap=%v", mi, entry, rows, owner.minimapGroups())
		svc.dungeonChoice = func(int) (int, error) { t.Fatal("follower or singleton redrew selection"); return 0, nil }
		follower, e := svc.prepareCurrentDungeonRandomizedOwner(d, mi, top, owner)
		if e != nil || !reflect.DeepEqual(owner.Entries, follower.Entries) {
			t.Fatalf("follower changed frozen choice: %v", e)
		}
		follower.Entries[0].Source.Regenerate.Value = 0
		if owner.Entries[0].Source.Regenerate.Value != 99 || follower.validate(d, mi) == nil {
			t.Fatal("mutable follower policy leaked or passed owner signature")
		}
		// Source-policy mutation remains rejected after initial-spawn support.
		data, e := json.Marshal(d)
		if e != nil {
			t.Fatal(e)
		}
		var control worldmap.Dungeon
		if e = json.Unmarshal(data, &control); e != nil {
			t.Fatal(e)
		}
		control.Mazes[mi].RandomizedObjects[0].Regenerate.Value = 0
		if e = owner.validate(control, mi); e == nil {
			t.Fatal("owner accepted a changed regeneration source")
		}
		for _, id := range []int64{61801, 18865, int64(^uint32(0))} {
			control.Mazes[mi].RandomizedObjects[0].Regenerate.Value = 99
			control.Mazes[mi].RandomizedObjects[0].Objects[0].ObjectIndex.Value = id
			if rejected, e := svc.prepareCurrentDungeonRandomizedOwner(control, mi, top, nil); e == nil || rejected != nil {
				t.Fatalf("maze%d non-ridable or missing template %d admitted: %+v %v", mi, id, rejected, e)
			}
		}
	}
}
