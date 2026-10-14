package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"testing"
)

func TestRealPVFJonathanSceneAuthorization(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching real PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(context.Background(), archive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var townID, areaID int64
	found := false
	for _, v := range towns.Towns() {
		for _, a := range v.Areas {
			if strings.Contains(strings.ToLower(a.MapPath), "centralpark_metrocenter.map") {
				townID, areaID, found = v.ID, a.ID, true
			}
		}
	}
	if !found {
		t.Fatal("Jonathan town area absent")
	}
	service := &Service{initialEquipmentArchive: archive, townCatalog: towns}
	session := &gameSession{selectedCharacterID: 77, townSceneReadyCharacterID: 77, townPositionSnapshot: currentTownPositionSnapshot{CharacterID: 77, TownID: byte(townID), AreaID: byte(areaID), PositionValid: true}}
	service.gameSessions = map[uint16]*gameSession{77: session}
	identity, ok := service.boundGameSessionCharacterSnapshot(session)
	if !ok {
		t.Fatal("fixture identity")
	}
	if err := service.validateUpgradeNPCScene(identity, 476); err != nil {
		t.Fatal(err)
	}
	if err := service.validateUpgradeNPCScene(identity, 65534); err == nil {
		t.Fatal("accepted NPC absent from area")
	}
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "candidate")
	resolver := service.candidateUpgradeCostResolver(session)
	if q, err := resolver("reinforce", 28309, 0, true, 476); err != nil || q.ConversionCount != 1 {
		t.Fatalf("valid scene quote=%+v err=%v", q, err)
	}
	session.townPositionSnapshot.PositionValid = false
	if _, err := resolver("reinforce", 28309, 0, true, 476); err == nil {
		t.Fatal("accepted unready position")
	}
	session.townPositionSnapshot.PositionValid = true
	session.townPositionSnapshot.CharacterID = 78
	if _, err := resolver("reinforce", 28309, 0, true, 476); err == nil {
		t.Fatal("accepted stale character town")
	}
	session.townPositionSnapshot.CharacterID = 77
	session.characterGeneration++
	if _, err := resolver("reinforce", 28309, 0, true, 476); err == nil {
		t.Fatal("accepted stale request generation")
	}
	if q, err := resolver("reinforce", 28309, 0, false, 65535); err != nil || q.GoldCost != 84975 {
		t.Fatalf("ordinary no-NPC quote=%+v err=%v", q, err)
	}
}
