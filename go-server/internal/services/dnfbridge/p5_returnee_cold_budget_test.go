package dnfbridge

import (
	"context"
	"errors"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	town "longheng.io/server/internal/modules/dnf/town"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

type returneeBudgetProbe struct {
	remaining time.Duration
	calls     int
}

func (p *returneeBudgetProbe) WithinAccountCharacterAssets(ctx context.Context, _, _ string, _ func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	p.calls++
	deadline, ok := ctx.Deadline()
	if ok {
		p.remaining = time.Until(deadline)
	}
	return errors.New("budget probe: no asset mutation")
}

func TestP5ReturneePreparationDoesNotConsumeAssetWriteBudget(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current real PVF")
	}
	archive, err := pvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, session, g := newTownMoveTest(t)
	s.initialEquipmentArchive = archive
	s.townCatalog, err = town.Load(context.Background(), archive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	session.townSceneReadyCharacterID = session.selectedCharacterID
	session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: session.selectedCharacterID, TownID: 38, AreaID: 1, PositionX: 450, PositionY: 310, PositionValid: true}
	rules, err := oe.LoadReturneeGiftRules(archive, 2)
	if err != nil {
		t.Fatal(err)
	}
	// A completed cache flight establishes the same rules as a fresh load. Slow
	// repository admission is deterministic and must not spend the write budget.
	_, err = s.returneeGiftCache.get(context.Background(), archive, 2, func(*pvf.Archive, uint32) (oe.ReturneeGiftRules, error) { return rules, nil })
	if err != nil {
		t.Fatal(err)
	}
	probe := &returneeBudgetProbe{}
	g.AccountAssets = probe
	s.repositoryProvider = func() (repo.Group, bool) { time.Sleep(250 * time.Millisecond); return g, true }
	err = s.handleCurrentReturneeNPCGift(session, []byte{0, 2, 0, 0, 0, 36, 101, 1})
	if err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 {
		t.Fatalf("asset admission calls=%d", probe.calls)
	}
	if probe.remaining < createWriteTimeout-100*time.Millisecond {
		t.Fatalf("asset budget=%s, want fresh %s after preparation", probe.remaining, createWriteTimeout)
	}
}
