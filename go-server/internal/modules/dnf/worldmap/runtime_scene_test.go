package worldmap

import (
	"reflect"
	"testing"
)

func runtimeSceneFixture() *DungeonSession {
	m := compactTokenFixture()
	compactMapSourceTokens(&m)
	return &DungeonSession{run: &DungeonRun{}, scene: DungeonRoomScene{
		Map: ResolvedMap{Map: m}, Monsters: []MonsterSpawn{{MonsterID: 77}},
		RuntimeObjects:  map[uint32]HostileReference{10: {Kind: HostileMonster, Index: 0}},
		DefeatedObjects: []uint32{9}, ExpectedHostiles: []HostileReference{{Kind: HostileMonster, Index: 0}},
	}}
}

func TestRuntimeScenePreservesGameplayAndMutationIsolation(t *testing.T) {
	s := runtimeSceneFixture()
	full, _ := s.Scene()
	view, ok := s.RuntimeScene()
	if !ok || view.Map.Map.SourceSections != nil {
		t.Fatal("raw token tree returned on combat path")
	}
	full.Map.Map.SourceSections = nil
	if !reflect.DeepEqual(full, view) {
		t.Fatal("gameplay scene changed")
	}
	view.Monsters[0].MonsterID = 999
	view.Map.Map.Monsters[0].MonsterID = 999
	view.RuntimeObjects[10] = HostileReference{}
	view.DefeatedObjects[0] = 999
	again, _ := s.RuntimeScene()
	if !reflect.DeepEqual(full, again) {
		t.Fatal("mutable alias escaped")
	}
	archive, _ := s.Scene()
	if len(archive.Map.Map.SourceSections[0].Tokens) != 256 {
		t.Fatal("archival API lost original tokens")
	}
}

func BenchmarkDungeonSceneRead(b *testing.B) {
	s := runtimeSceneFixture()
	for _, full := range []bool{true, false} {
		name := "runtime"
		if full {
			name = "full"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if full {
					s.Scene()
				} else {
					s.RuntimeScene()
				}
			}
		})
	}
}
