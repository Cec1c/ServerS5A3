package dnfbridge

import (
	"os"
	"path/filepath"
	"testing"

	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestExportPetGrowthNativeFixtures(t *testing.T) {
	dir := os.Getenv("DNF90_PET_NATIVE_FIXTURE_DIR")
	if dir == "" {
		t.Skip("offline native fixture export is opt-in")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, entry := range map[string]dnfrepo.PetEntry{
		"pet-exp-normal.bin":  {Level: 1, Exp: 1},
		"pet-exp-leveled.bin": {Level: 2, Exp: 17},
		"pet-exp-mode1.bin":   {Level: 7, Exp: 123456, ModeFlag: 1, Mode1Field0A: 3, Mode1Field0B: 9},
	} {
		body, err := dnfpet.BuildCreatureGrowthBody(entry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
