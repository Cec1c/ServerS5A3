package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type guardianShieldPVFTestSource map[string]string

func (source guardianShieldPVFTestSource) ReadText(path string) (string, error) {
	text, found := source[path]
	if !found {
		return "", os.ErrNotExist
	}
	return text, nil
}

func TestParseCurrentGuardianShieldCatalogPreservesRepeatedShieldSections(t *testing.T) {
	catalog, err := parseCurrentGuardianShieldCatalog(`
[tab]
` + "`quest shield tab grid`" + `
[shield]
[item index]
1001
[get condition]
quest
[grow type]
1
[clear quest index]
12723
[/shield]
[shield]
[item index]
2001
[get condition]
quest
[grow type]
2
[clear quest index]
3046
[/shield]
[shield]
[item index]
3001
[get condition]
level
[grow type]
0
[get shield level]
20
[/shield]
[/shield]
[/tab]
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := catalog[1][1001]; !found {
		t.Fatalf("grow1 catalog=%v", catalog[1])
	}
	if _, found := catalog[2][2001]; !found {
		t.Fatalf("grow2 catalog=%v", catalog[2])
	}
	if catalog[1][1001].ClearQuestID != 12723 || catalog[2][2001].ClearQuestID != 3046 {
		t.Fatalf("catalog clear quests=%+v", catalog)
	}
	if entry := catalog[0][3001]; entry.UnlockKind != alignedcmd.GuardianShieldUnlockLevel || entry.MinimumLevel != 20 {
		t.Fatalf("shared level catalog=%+v", catalog[0])
	}
}

func TestParseCurrentGuardianShieldCatalogRejectsIncompleteOpenedEntry(t *testing.T) {
	_, err := parseCurrentGuardianShieldCatalog(`
[shield]
[item index]
4001
[/shield]
`)
	if err == nil {
		t.Fatal("incomplete opened shield entry was accepted")
	}
}

func TestCurrentGuardianShieldResolverRequiresCatalogTypeJobGroupAndPath(t *testing.T) {
	const (
		goodPath  = "equipment/character/knight/weapon/shield/good.equ"
		levelPath = "equipment/character/knight/weapon/shield/level.equ"
	)
	shieldText := "[equipment type] `[support weapon]` 21\n[usable job] `[knight]`\n[/usable job]\n[item group name] `shield`\n"
	resolver := currentGuardianShieldPVFResolver{
		source: guardianShieldPVFTestSource{
			goodPath:  shieldText,
			levelPath: shieldText,
		},
		equipmentPaths: map[int64]string{
			1001: "character/knight/weapon/shield/good.equ",
			3001: "character/knight/weapon/shield/level.equ",
		},
		catalog: map[byte]map[uint32]currentGuardianShieldCatalogEntry{
			0: {3001: {UnlockKind: alignedcmd.GuardianShieldUnlockLevel, MinimumLevel: 20}},
			1: {1001: {UnlockKind: alignedcmd.GuardianShieldUnlockQuest, ClearQuestID: 12723}},
			2: {2001: {UnlockKind: alignedcmd.GuardianShieldUnlockQuest, ClearQuestID: 3046}},
		},
	}
	resolution, err := resolver.Resolve(context.Background(), 1001, 0x21)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.ItemID != 1001 || resolution.PVFPath != goodPath ||
		resolution.EquipmentType != "[support weapon]" ||
		resolution.UnlockKind != alignedcmd.GuardianShieldUnlockQuest || resolution.ClearQuestID != 12723 {
		t.Fatalf("resolution=%+v", resolution)
	}
	if _, err := resolver.Resolve(context.Background(), 1001, 2); !errors.Is(err, errGuardianShieldNotInCatalog) {
		t.Fatalf("cross-grow error=%v", err)
	}
	for _, growType := range []byte{1, 2, 0x21, 0x32} {
		levelResolution, err := resolver.Resolve(context.Background(), 3001, growType)
		if err != nil {
			t.Fatalf("shared level shield grow=%d: %v", growType, err)
		}
		if levelResolution.UnlockKind != alignedcmd.GuardianShieldUnlockLevel || levelResolution.MinimumLevel != 20 {
			t.Fatalf("shared level resolution grow=%d: %+v", growType, levelResolution)
		}
	}

	resolver.source = guardianShieldPVFTestSource{
		goodPath: "[equipment type] `[support weapon]` 21\n[usable job] `[swordman]`\n[/usable job]\n[item group name] `shield`\n",
	}
	if _, err := resolver.Resolve(context.Background(), 1001, 1); !errors.Is(err, errGuardianShieldItemInvalid) {
		t.Fatalf("wrong usable job error=%v", err)
	}
}

func TestCurrentGuardianShieldCatalogRealPVFHas25Rows(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run real shieldwindownewdata smoke")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText(currentGuardianShieldCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCurrentGuardianShieldCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entries := range catalog {
		count += len(entries)
	}
	if count != 25 {
		t.Fatalf("shieldwindownewdata rows=%d, want 25 (level=%d grow1=%d grow2=%d)", count, len(catalog[0]), len(catalog[1]), len(catalog[2]))
	}
}
