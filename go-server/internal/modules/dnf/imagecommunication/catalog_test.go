package imagecommunication

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const fixtureConfig = `[image communication]
[charge]
10000
[summon time]
20000
[summon pos]
1274 140 0
[effect pos]
1274 210 0
[npc]
[npc index]
2000
[npc pos]
5 55
[require quest]
3734
[/npc]
[npc]
[npc index]
2001
[npc pos]
5 42
[require quest]
3741
[/npc]
[npc]
[npc index]
3000
[npc pos]
7 33
[require quest]
100 101
[/npc]
[/image communication]
`

const fixtureNPCList = `2000 ` + "`Melvin2.npc`" + `
2001 ` + "`erje2.npc`" + `
3000 ` + "`Multi.npc`" + `
`

type textSource map[string]string

func (s textSource) ReadText(relativePath string) (string, error) {
	value, ok := s[strings.ToLower(relativePath)]
	if !ok {
		return "", fmt.Errorf("fixture path %s: %w", relativePath, errors.New("not found"))
	}
	return value, nil
}

func fixtureSource(configText string) textSource {
	return textSource{
		strings.ToLower(ConfigPath):  configText,
		strings.ToLower(NPCListPath): fixtureNPCList,
		"npc/melvin2.npc":            "[npc]\n[name] `Melvin2`\n[/npc]\n",
		"npc/erje2.npc":              "[npc]\n[name] `erje2`\n[/npc]\n",
		"npc/multi.npc":              "[npc]\n[name] `Multi`\n[/npc]\n",
	}
}

func loadFixture(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := Load(context.Background(), fixtureSource(fixtureConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return catalog
}

func TestLoadParsesSettingsTargetsAndDefinitions(t *testing.T) {
	catalog := loadFixture(t)
	config := catalog.Config()
	if config.Charge != 10000 || config.SummonTime != 20000 {
		t.Fatalf("charge/time = %d/%d", config.Charge, config.SummonTime)
	}
	if config.SummonPosition != (Position3D{X: 1274, Y: 140, Z: 0}) ||
		config.EffectPosition != (Position3D{X: 1274, Y: 210, Z: 0}) {
		t.Fatalf("positions = %+v/%+v", config.SummonPosition, config.EffectPosition)
	}
	if len(config.Targets) != 3 {
		t.Fatalf("targets = %+v", config.Targets)
	}
	target, ok := catalog.Target(2000)
	if !ok || target.Position != (Position2D{X: 5, Y: 55}) ||
		len(target.RequireQuests) != 1 || target.RequireQuests[0] != 3734 ||
		target.DefinitionPath != "npc/Melvin2.npc" {
		t.Fatalf("NPC2000 = %+v found=%t", target, ok)
	}
}

func TestEligibilityUsesOnlyActiveQuestsAndRequiresEveryQuest(t *testing.T) {
	catalog := loadFixture(t)

	for _, test := range []struct {
		name   string
		active map[int64]struct{}
		want   []int64
	}{
		{name: "no active quests", active: nil},
		{name: "completed quest is absent from active set", active: map[int64]struct{}{3733: {}, 3740: {}, 99: {}}},
		{name: "NPC2000", active: map[int64]struct{}{3734: {}}, want: []int64{2000}},
		{name: "NPC2001", active: map[int64]struct{}{3741: {}}, want: []int64{2001}},
		{name: "multiple AND one missing", active: map[int64]struct{}{100: {}}, want: nil},
		{name: "multiple AND complete", active: map[int64]struct{}{100: {}, 101: {}}, want: []int64{3000}},
		{name: "all configured", active: map[int64]struct{}{100: {}, 101: {}, 3734: {}, 3741: {}}, want: []int64{2000, 2001, 3000}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := catalog.Eligible(test.active)
			var ids []int64
			for _, target := range got {
				ids = append(ids, target.NPCID)
			}
			if fmt.Sprint(ids) != fmt.Sprint(test.want) {
				t.Fatalf("eligible IDs = %v, want %v", ids, test.want)
			}
		})
	}
	if catalog.IsEligible(3000, map[int64]struct{}{100: {}}) {
		t.Fatal("AND condition passed with one of two active quests")
	}
}

func TestUnknownTargetIsNotEligible(t *testing.T) {
	catalog := loadFixture(t)
	if _, ok := catalog.Target(9999); ok {
		t.Fatal("unknown NPC was returned")
	}
	if catalog.IsEligible(9999, map[int64]struct{}{3734: {}}) {
		t.Fatal("unknown NPC was eligible")
	}
}

func TestOnlyCompletedRequirementIsNotEligible(t *testing.T) {
	catalog := loadFixture(t)
	states := map[int64]string{3734: "completed"}
	activeQuestIDs := make(map[int64]struct{})
	for questID, state := range states {
		if state == "active" {
			activeQuestIDs[questID] = struct{}{}
		}
	}
	if eligible := catalog.Eligible(activeQuestIDs); len(eligible) != 0 {
		t.Fatalf("completed-only quest ID made targets eligible: %+v", eligible)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
		want   error
	}{
		{
			name: "negative charge",
			mutate: func(files map[string]string) {
				files[strings.ToLower(ConfigPath)] = strings.Replace(fixtureConfig, "10000", "-1", 1)
			},
			want: ErrInvalidConfig,
		},
		{
			name: "invalid position structure",
			mutate: func(files map[string]string) {
				files[strings.ToLower(ConfigPath)] = strings.Replace(fixtureConfig, "1274 140 0", "1274 140", 1)
			},
			want: ErrInvalidConfig,
		},
		{
			name: "missing npc close",
			mutate: func(files map[string]string) {
				files[strings.ToLower(ConfigPath)] = strings.Replace(fixtureConfig, "3734\n[/npc]", "3734", 1)
			},
			want: ErrInvalidConfig,
		},
		{
			name: "duplicate target",
			mutate: func(files map[string]string) {
				files[strings.ToLower(ConfigPath)] = strings.Replace(fixtureConfig, "[npc index]\n2001", "[npc index]\n2000", 1)
				files[strings.ToLower(NPCListPath)] = strings.Replace(fixtureNPCList, "2001", "2000", 1)
			},
			want: ErrDuplicateNPC,
		},
		{
			name: "duplicate require quest",
			mutate: func(files map[string]string) {
				files[strings.ToLower(ConfigPath)] = strings.Replace(fixtureConfig, "3734\n[/npc]", "3734 3734\n[/npc]", 1)
			},
			want: ErrInvalidConfig,
		},
		{
			name: "missing NPC list entry",
			mutate: func(files map[string]string) {
				files[strings.ToLower(NPCListPath)] = strings.Replace(fixtureNPCList, "2000 `Melvin2.npc`\n", "", 1)
			},
			want: ErrMissingNPC,
		},
		{
			name: "missing NPC definition",
			mutate: func(files map[string]string) {
				delete(files, "npc/melvin2.npc")
			},
			want: ErrMissingNPC,
		},
		{
			name: "conflicting NPC list duplicate",
			mutate: func(files map[string]string) {
				files[strings.ToLower(NPCListPath)] = fixtureNPCList + "2000 `Other.npc`\n"
				files["npc/other.npc"] = "[npc]\n[name] `Other`\n[/npc]\n"
			},
			want: ErrDuplicateNPC,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := fixtureSource(fixtureConfig)
			files := make(map[string]string, len(source))
			for key, value := range source {
				files[key] = value
			}
			test.mutate(files)
			_, err := Load(context.Background(), textSource(files))
			if !errors.Is(err, test.want) {
				t.Fatalf("Load error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}
