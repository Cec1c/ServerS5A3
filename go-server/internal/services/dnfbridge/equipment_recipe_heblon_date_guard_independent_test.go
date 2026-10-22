package dnfbridge

import (
	"context"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"reflect"
	"testing"
)

// Independent review reproducer: the evidence-bound exception must not
// silently accept a new native date restriction omitted by the catalog.
func TestR137IndependentHeblonRejectsRawUsableDatetime(t *testing.T) {
	a, recipes, _ := heblonResources(t)
	entry, _, err := recipes.Recipe(10304471)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.ReadText(entry.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	raw += "\n[usable datetime]\n`2030-01-01 00:00:00`\n"
	if _, err := currentEquipmentRecipeUpgradeDocument(entry, dungeonDropCatalogTestSource{entry.PVFPath: raw}); err == nil {
		t.Fatal("new native usable datetime gate admitted by strict Heblon exception; raw date is unproven and must fail closed")
	}
}

type independentHeblonSourceEdit struct {
	dnfpvf.Source
	path, text string
}

func (s independentHeblonSourceEdit) ReadText(path string) (string, error) {
	if path == s.path {
		return s.text, nil
	}
	return s.Source.ReadText(path)
}

func TestR137IndependentHeblonDateGateFullHandler(t *testing.T) {
	a, recipes, _ := heblonResources(t)
	entry, _, err := recipes.Recipe(10304471)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.ReadText(entry.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	source := independentHeblonSourceEdit{Source: a, path: entry.PVFPath, text: raw + "\n[usable datetime]\n`2030-01-01 00:00:00`\n"}
	freshRecipes, err := newPVFEquipmentRecipeCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	fresh, found, err := freshRecipes.Recipe(entry.ItemID)
	if err != nil || !found {
		t.Fatalf("fresh recipe %v", err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repos, s, p := heblonFixture(t, a, items, fresh, 0)
	ctx := context.Background()
	before, _, _ := repos.Inventory.Load(ctx, "19")
	beforeAcc, _, _ := repos.AccountInventory.Load(ctx, "account-1")
	if err := s.handleGameCommand(p, 1, 25, append(equipmentRecipeCompoundTestSlotRequest(69, 1), 0, 1)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
	after, _, _ := repos.Inventory.Load(ctx, "19")
	afterAcc, _, _ := repos.AccountInventory.Load(ctx, "account-1")
	if len(ack.Body) == 0 || ack.Body[0] != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeAcc, afterAcc) {
		t.Fatalf("unproven raw date gate reached transaction: ACK=%x input=%d->%d drawing=%d->%d; expected fail closed", ack.Body, before.Slots["0:9"].ItemID, after.Slots["0:9"].ItemID, before.Slots["0:69"].Count, after.Slots["0:69"].Count)
	}
}
