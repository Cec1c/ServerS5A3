package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// This regression covers the gap between an explicit cross-scene inspection
// and an automatic roster/wear profile refresh. The explicit sender adds
// nativePeerBuffInspectKey, so mode3 carries only the selected Buff skill's
// learned base level. Automatic refresh must use the same context: both
// mode8 packets target the same auxiliary cache, so a later refresh must
// not replace the selected Buff skill with an empty or full skill page.
//
// The fixture uses a real enabled Buff registration with one registered item,
// one unrelated private bag item, and one unrelated learned skill. A correct
// automatic refresh must retain exactly skill 38 at base Lv10 in both pages,
// exclude skill 99, and keep the skill-page tail byte-identical to the
// explicit mode3 record.

const (
	// These offsets are relative to the mode3 tail copied into mode8, whose
	// leading subtype/count prefix has already been stripped.
	crossSceneBuffMode3TailExtraSlotOffset   = 23 + currentMode1StatBlobWireSize - currentCrossSceneSourcePrefixBytes
	crossSceneBuffMode3TailCreateCountOffset = crossSceneBuffMode3TailExtraSlotOffset + 1
	crossSceneBuffMode3TailCreateRowsOffset  = crossSceneBuffMode3TailCreateCountOffset + 1
	crossSceneBuffMode3TailSkillPageOffset   = crossSceneBuffMode3TailCreateRowsOffset + 4 + 4 + 4
	crossSceneBuffMode3TailSkillPageBytes    = 9
)

func newCrossSceneBuffRegressionFixture(t *testing.T) (*currentCrossSceneProfileBatchFixture, dnfrepo.Group) {
	t.Helper()
	f := newCurrentCrossSceneProfileBatchFixture(t)
	old, ok := f.service.repositoryGroup()
	if !ok {
		t.Fatal("cross-scene Buff fixture has no repository group")
	}
	ctx := context.Background()
	// The town fixture replaces Character independently of the memory UOW.
	// Use one coherent group so real BUFF owner transactions see these rows.
	repos := dnfrepomemory.NewMemoryGroup()
	for _, id := range []string{"17", "29", "1001"} {
		character, found, err := old.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("copy owner %s: found=%v err=%v", id, found, err)
		}
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: character.AccountID, State: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "17",
		Slots: map[string]dnfrepo.ItemStack{
			"0:55": {ItemID: 100350891, Count: 1},
			"0:56": {ItemID: 987654321, Count: 1},
		},
	}); err != nil {
		t.Fatalf("seed Buff inventory: %v", err)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "17"}); err != nil {
		t.Fatalf("seed empty worn equipment: %v", err)
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: "17",
		Skills: map[int64]dnfrepo.SkillState{
			38: {Level: 10},
			99: {Level: 9},
		},
	}); err != nil {
		t.Fatalf("seed Buff base skill and unrelated learned skill: %v", err)
	}
	if err := buffswap.ApplyCurrent(ctx, repos, "17", "dnf:2", buffswap.Command{
		Skill: 38,
		Slot:  23,
		List:  0,
		Index: 55,
	}); err != nil {
		t.Fatalf("enable real Buff registration: %v", err)
	}
	state, found, err := buffswap.Load(ctx, repos, "17", "dnf:2")
	if err != nil || !found || state.Skill != 38 {
		t.Fatalf("Buff registration not active: state=%+v found=%v err=%v", state, found, err)
	}
	if binding, present := state.Slots[23]; !present || binding.List != 0 || binding.Index != 55 || binding.ItemID != 100350891 {
		t.Fatalf("Buff registration has wrong selected binding: %+v present=%v", binding, present)
	}
	return f, repos
}

func crossSceneBuffMode3Tail(t *testing.T, mode8Body []byte, mode0Len int) []byte {
	t.Helper()
	if mode0Len <= currentCrossSceneSourcePrefixBytes || len(mode8Body) <= mode0Len {
		t.Fatalf("mode8 body cannot be split at mode0 length %d: %d bytes", mode0Len, len(mode8Body))
	}
	mode3 := mode8Body[mode0Len:]
	if len(mode3) < crossSceneBuffMode3TailSkillPageOffset+crossSceneBuffMode3TailSkillPageBytes {
		t.Fatalf("mode3 tail is too short for its skill page: %d bytes", len(mode3))
	}
	if mode3[0] != mode8Body[3] || mode3[1] != mode8Body[4] {
		t.Fatalf("mode3 route/owner=%d/%d does not match mode0 route/owner=%d/%d",
			mode3[0], mode3[1], mode8Body[3], mode8Body[4])
	}
	if key := binary.LittleEndian.Uint16(mode3[10:12]); key != 17 {
		t.Fatalf("mode3 tail object key=%d, want actor 17", key)
	}
	if count := mode3[crossSceneBuffMode3TailCreateCountOffset]; count != 0 {
		t.Fatalf("fixture unexpectedly has %d worn equipment create rows; empty worn state is required for the fixed page offset", count)
	}
	return mode3
}

func crossSceneBuffMode3SkillPage(t *testing.T, mode8Body []byte, mode0Len int) []byte {
	t.Helper()
	mode3 := crossSceneBuffMode3Tail(t, mode8Body, mode0Len)
	return mode3[crossSceneBuffMode3TailSkillPageOffset : crossSceneBuffMode3TailSkillPageOffset+crossSceneBuffMode3TailSkillPageBytes]
}

func TestCrossSceneBuffAutomaticMode8MatchesExplicitSelectedSkill(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
	}{
		{name: "peer_default", mode: "peer"},
		{name: "page_off", mode: "off"},
		{name: "page_all", mode: "all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			t.Setenv(peerSkillPageEnv, tc.mode)

			f, repos := newCrossSceneBuffRegressionFixture(t)
			ctx := context.Background()
			// The explicit cross-scene path resolves the target through
			// characterForEnter, while the batch reuses the raw repository row.
			// Keep each split anchored to the mode0 record that path actually
			// emitted; the regression below is the mode3 skill-page segment.
			_, explicitName, explicitCharacter, explicitFound := f.service.characterForEnter(ctx, f.actor, 17)
			if !explicitFound {
				t.Fatal("explicit path could not resolve the inspected actor")
			}
			owner := currentConnectionTownActorOwnerContext(f.target)
			if owner == 0 {
				t.Fatal("fixture has no connection owner")
			}
			explicitMode0, err := f.service.buildCurrentSceneObjectListBodyForSessionInContextStrict(
				ctx, f.actor, 17, explicitName, explicitCharacter, true, owner,
			)
			if err != nil {
				t.Fatalf("build explicit mode0 for the shared actor: %v", err)
			}

			wantPage := []byte{
				peerSkillPageSelectedResourceGroup,
				1, 38, 0, 10,
				1, 38, 0, 10,
			}
			privateItemBytes := []byte{0xb1, 0x68, 0xde, 0x3a} // 987654321

			f.reset()
			if err := f.service.sendCurrentCrossScenePeerProfile(ctx, f.target, f.actor, 29, 17); err != nil {
				t.Fatalf("explicit cross-scene inspection: %v", err)
			}
			explicitBody, _ := assertCurrentCrossSceneProfileFramePair(
				t,
				f.targetConn.write.Bytes(),
				f.service.gameUpperHeaderSize(),
				17,
				f.service.currentSceneActorRoute(),
				owner,
			)
			explicitMode3 := crossSceneBuffMode3Tail(t, explicitBody, len(explicitMode0))
			explicitPage := crossSceneBuffMode3SkillPage(t, explicitBody, len(explicitMode0))
			if !bytes.Equal(explicitPage, wantPage) {
				t.Fatalf("explicit mode8 mode3 page=%x, want selected Buff skill only %x", explicitPage, wantPage)
			}
			if bytes.Contains(explicitBody, privateItemBytes) {
				t.Fatal("explicit mode8 leaked the unregistered private bag item")
			}

			// This is the real automatic roster/wear path: it creates a fresh
			// batch and refreshes the same actor without an explicit request.
			automaticCharacter, found, err := repos.Character.Load(ctx, "17")
			if err != nil || !found {
				t.Fatalf("load inspected actor for the automatic path: found=%v err=%v", found, err)
			}
			automaticMode0, err := f.service.buildCurrentSceneObjectListBodyForSessionInContextStrict(
				ctx, f.actor, 17, automaticCharacter.Name, automaticCharacter, true, owner,
			)
			if err != nil {
				t.Fatalf("build automatic mode0 for the shared actor: %v", err)
			}
			f.reset()
			batch := newCurrentCrossSceneProfileBatch(f.service, ctx)
			if batch == nil {
				t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
			}
			if err := batch.send(f.target, f.actor, owner); err != nil {
				t.Fatalf("automatic cross-scene profile refresh: %v", err)
			}
			automaticBody := requireCurrentCrossSceneProfileBatchMode8(
				t,
				splitUpperFrames(t, f.targetConn.write.Bytes()),
				17,
				owner,
			)
			automaticMode3 := crossSceneBuffMode3Tail(t, automaticBody, len(automaticMode0))
			automaticPage := crossSceneBuffMode3SkillPage(t, automaticBody, len(automaticMode0))
			if !bytes.Equal(automaticPage, wantPage) {
				t.Fatalf("automatic mode8 mode3 page=%x, want selected Buff skill only %x; this refresh would replace the explicit inspection cache", automaticPage, wantPage)
			}
			if bytes.Contains(automaticBody, privateItemBytes) {
				t.Fatal("automatic mode8 leaked the unregistered private bag item")
			}
			if !bytes.Equal(
				automaticMode3[crossSceneBuffMode3TailSkillPageOffset:],
				explicitMode3[crossSceneBuffMode3TailSkillPageOffset:],
			) {
				t.Fatalf("automatic and explicit mode8 mode3 skill-page tails differ: automatic_page=%x explicit_page=%x",
					automaticPage, explicitPage)
			}
		})
	}
}
