package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Exercise the commands, including persistence and the post-commit packet
// sequence. A level-up-only notification loses ordinary progress between levels.
func TestRealPVFExpertJobExperienceAfterCommit(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching real PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	jobs, err := dnfexpertjob.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	enchanter, _ := jobs.Enchanter()
	recipe := enchanter.CardRecipes[10015129]
	config, _ := jobs.Config(dnfexpertjob.EnchanterType)
	const initialExperience = int64(100)
	beadPlan, err := jobs.PlanEnchanterBeads(initialExperience, recipe.RecipeItemID, 3619, 1, nil)
	if err != nil || beadPlan.ExperienceGain <= 0 || beadPlan.LevelChanged {
		t.Fatalf("bead fixture: %+v %v", beadPlan, err)
	}
	metadata, err := jobs.Equipment(100050285)
	if err != nil {
		t.Fatal(err)
	}
	metadata.State = currentExpertJobEquipmentState(dnfrepo.ItemStack{ItemID: 100050285, Count: 1})
	const extractorID = int64(2600482)
	originalExtractor := config.Extractors[extractorID]
	if originalExtractor.MinimumExperienceGain != 0 || originalExtractor.MaximumExperienceGain != 1 {
		t.Fatalf("unexpected actual extraction range: %+v", originalExtractor)
	}
	plan, err := jobs.PlanExtraction(dnfexpertjob.EnchanterType, initialExperience, extractorID, metadata, func(n int) int { return n - 1 })
	if err != nil || plan.ExperienceGain != 1 || plan.LevelChanged {
		t.Fatalf("actual extraction fixture: %+v %v", plan, err)
	}
	for _, operation := range []string{"compound", "extraction", "extraction_zero"} {
		for _, mode := range []string{"town", "dungeon", "rollback"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				t.Setenv(currentExpertJobExpNotifyEnv, "")
				// Pin each endpoint of the real 0..1 range in this isolated
				// catalog so full-handler coverage does not depend on random rolls.
				extractor := originalExtractor
				if operation == "extraction" {
					extractor.MinimumExperienceGain = 1
				}
				if operation == "extraction_zero" {
					extractor.MaximumExperienceGain = 0
				}
				config.Extractors[extractorID] = extractor
				t.Cleanup(func() { config.Extractors[extractorID] = originalExtractor })
				repos := expertSharedRecipeFixture(t, dnfexpertjob.Recipe{RecipeItemID: recipe.RecipeItemID, Materials: recipe.Materials})
				inv, _, _ := repos.Inventory.Load(ctx, "19")
				inv.Slots["0:75"] = dnfrepo.ItemStack{ItemID: 3619, Count: 1}
				inv.Slots["0:76"] = dnfrepo.ItemStack{ItemID: extractorID, Count: 1}
				inv.Slots["0:9"] = dnfrepo.ItemStack{ItemID: 100050285, Count: 1}
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
				before, _, _ := repos.Character.Load(ctx, "19")
				before.Name = "r134"
				before.Stats["expert_job_exp"] = initialExperience
				if err := repos.Character.Save(ctx, before); err != nil {
					t.Fatal(err)
				}
				beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				if mode == "rollback" {
					repos.CharacterSettlement = rejectingCurrentNPCShopCommit{inner: repos.CharacterSettlement, err: errors.New("injected final commit failure")}
				}
				service := currentDisjointTestService(repos, items)
				service.expertJobCatalog = jobs
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 19, itemListMigrationsDone: true}
				if mode == "dungeon" {
					session.dungeon.runtime = &runtimeDungeonState{}
				}
				var handlerErr error
				var opcode uint16
				if operation == "compound" {
					opcode = 238
					body := make([]byte, 8)
					binary.LittleEndian.PutUint32(body, uint32(recipe.RecipeItemID))
					binary.LittleEndian.PutUint16(body[4:], 1)
					binary.LittleEndian.PutUint16(body[6:], 75)
					handlerErr = service.handleCurrentExpertJobCompound(session, body)
				} else {
					opcode = 416
					handlerErr = service.handleCurrentExpertJobExtraction(session, []byte{dnfexpertjob.EnchanterType, 76, 0, 0, 9, 0})
				}
				if handlerErr != nil {
					t.Fatal(handlerErr)
				}
				after, _, _ := repos.Character.Load(ctx, "19")
				packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
				if len(packets) == 0 || packets[0].Header.MsgID != opcode {
					t.Fatal("missing command ACK")
				}
				if mode == "rollback" {
					afterInv, _, _ := repos.Inventory.Load(ctx, "19")
					afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
					if len(packets) != 1 || packets[0].Body[0] != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inv, afterInv) || !reflect.DeepEqual(beforeAccount, afterAccount) {
						t.Fatal("failed transaction changed assets or published success/progress")
					}
					return
				}
				experience := after.Stats["expert_job_exp"]
				wantGain := operation != "extraction_zero"
				if packets[0].Body[0] != 1 || (experience > initialExperience) != wantGain || config.Level(experience) != config.Level(initialExperience) {
					t.Fatalf("expected non-level-up gain, got exp=%d ACK=%x", experience, packets[0].Body)
				}
				var infoCount, actorCount int
				for _, packet := range packets[1:] {
					if packet.Header.Classification != 0 {
						continue
					}
					if packet.Header.MsgID == currentExpertJobInfoNotification {
						infoCount++
						body := packet.Body
						if len(body) < 8 || string(body[len(body)-8:len(body)-4]) != "EJXP" || binary.LittleEndian.Uint32(body[len(body)-4:]) != uint32(experience) {
							t.Fatalf("profession cache did not receive committed experience: %x", body)
						}
					}
					if packet.Header.MsgID == 2 && len(packet.Body) > 0 && packet.Body[0] == 0 {
						actorCount++
						if infoCount != 1 {
							t.Fatal("actor refresh preceded profession cache")
						}
						_, offset := expertJobExpNotifySubtype0BodyOffsets(after.Name)
						if len(packet.Body) < offset+4 || binary.LittleEndian.Uint32(packet.Body[offset:]) != uint32(experience) {
							t.Fatal("actor refresh carried stale experience")
						}
					}
				}
				wantInfo, wantActor := 1, 1
				if mode == "dungeon" {
					wantActor = 0
				}
				if !wantGain {
					wantInfo, wantActor = 0, 0
				}
				if infoCount != wantInfo || actorCount != wantActor {
					t.Fatalf("exp=%d info=%d actor=%d; want info=%d actor=%d", experience, infoCount, actorCount, wantInfo, wantActor)
				}
			})
		}
	}
}
