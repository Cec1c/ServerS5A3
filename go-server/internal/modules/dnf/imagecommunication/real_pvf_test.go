package imagecommunication

import (
	"context"
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFImageCommunicationCatalog(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the matched QST-005 Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real Script.pvf: %v", err)
	}
	const wantSHA256 = "fef4f46f6c3096d10cd88c84579dea8bd4fffa18e5a75b4684a2e920864f5525"
	if got := archive.Snapshot().Checksum; !strings.EqualFold(got, wantSHA256) {
		t.Fatalf("unmatched QST-005 PVF SHA256 = %s", got)
	}

	catalog, err := Load(context.Background(), archive)
	if err != nil {
		t.Fatalf("Load real image-communication catalog: %v", err)
	}
	config := catalog.Config()
	if config.Charge != 10000 || config.SummonTime != 20000 {
		t.Fatalf("real charge/time = %d/%d", config.Charge, config.SummonTime)
	}
	if config.SummonPosition != (Position3D{X: 1274, Y: 140, Z: 0}) ||
		config.EffectPosition != (Position3D{X: 1274, Y: 210, Z: 0}) {
		t.Fatalf("real positions = %+v/%+v", config.SummonPosition, config.EffectPosition)
	}

	target2000, ok := catalog.Target(2000)
	if !ok || target2000.Position != (Position2D{X: 5, Y: 55}) ||
		len(target2000.RequireQuests) != 1 || target2000.RequireQuests[0] != 3734 ||
		!strings.HasSuffix(strings.ToLower(target2000.DefinitionPath), "/melvin2.npc") {
		t.Fatalf("real NPC2000 = %+v found=%t", target2000, ok)
	}
	target2001, ok := catalog.Target(2001)
	if !ok || target2001.Position != (Position2D{X: 5, Y: 42}) ||
		len(target2001.RequireQuests) != 1 || target2001.RequireQuests[0] != 3741 ||
		!strings.HasSuffix(strings.ToLower(target2001.DefinitionPath), "/erje2.npc") {
		t.Fatalf("real NPC2001 = %+v found=%t", target2001, ok)
	}
	if eligible := catalog.Eligible(map[int64]struct{}{3734: {}}); len(eligible) != 1 || eligible[0].NPCID != 2000 {
		t.Fatalf("active3734 eligibility = %+v", eligible)
	}
	if eligible := catalog.Eligible(map[int64]struct{}{3741: {}}); len(eligible) != 1 || eligible[0].NPCID != 2001 {
		t.Fatalf("active3741 eligibility = %+v", eligible)
	}
}
