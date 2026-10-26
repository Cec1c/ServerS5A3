package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The map and award amounts are fixtures. The progression thresholds and
// HonorExpert tables are read from the actual runtime PVF, and the production
// experience commit, settlement producer and packet sender run unchanged.
func TestRealPVFMaximumLevelExperienceCommitsRepeatedHonorAndSettlement(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for the level-cap progression regression")
	}
	ctx := context.Background()
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	honorTables, err := dnfhonor.LoadTables(archive)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := tables.ThresholdToNext(89)
	if err != nil || entry != 1732686583 {
		t.Fatalf("90-level entry=%d err=%v", entry, err)
	}
	next, err := tables.ThresholdToNext(90)
	if err != nil || next != 1961802808 {
		t.Fatalf("91-level threshold=%d err=%v", next, err)
	}
	service, runtime, session, conn, _ := prepareCompletedSettlementRuntime(t)
	service.honorTable = honorTables
	repos, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group missing")
	}
	character, found, err := repos.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("fixture character found=%t err=%v", found, err)
	}
	character.Level = 90
	character.Stats["exp"] = int64(next + 100)
	character.Stats[currentHonorExpertLevelStatKey] = 58
	character.Stats[currentHonorExpertProgressExperienceStatKey] = 6338510
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, _, err := repos.Skill.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	skill.CharacterID = "99"
	skill.Points = dnfrepo.SkillPointState{SyncedLevel: 90, TotalSP: 11970, RemainingSP: 270, TotalTP: 41, RemainingTP: 7}
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	runtime.Character = dnfrepo.CloneCharacter(character)
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: character.AccountID, Metadata: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	expectedHonor := dnfhonor.ExpertProgress{Level: 58, CurrentLevelExperience: 6338510}
	const gain = uint32(140000000)
	for index := 0; index < 2; index++ {
		result, err := service.commitCurrentDungeonExperience(ctx, session, runtime, tables, currentDungeonMonsterExperienceCommitResult{Award: currentDungeonMonsterExperienceAward{Gain: gain}})
		if err != nil || !result.Applied || result.Idempotent || result.HonorExpertGain != gain {
			t.Fatalf("award %d result=%+v err=%v", index, result, err)
		}
		expectedHonor, err = honorTables.AdvanceExpert(expectedHonor, uint64(gain))
		if err != nil {
			t.Fatal(err)
		}
		if result.Character.Level != 90 || result.Character.Stats["exp"] != int64(entry) || result.Skill.Points != skill.Points ||
			result.Character.Stats[currentHonorExpertLevelStatKey] != int64(expectedHonor.Level) || result.Character.Stats[currentHonorExpertProgressExperienceStatKey] != int64(expectedHonor.CurrentLevelExperience) {
			t.Fatalf("award %d escaped cap or lost honor: %+v want honor=%+v", index, result, expectedHonor)
		}
		runtime.Character = dnfrepo.CloneCharacter(result.Character)
		body := buildCurrentFinishLoadingCharacterStateBody(result.Character, result.Skill.Points)
		if body[0] != 90 || binary.LittleEndian.Uint32(body[1:5]) != entry || binary.LittleEndian.Uint32(body[55:59]) != expectedHonor.Level || binary.LittleEndian.Uint64(body[59:67]) != expectedHonor.CurrentLevelExperience {
			t.Fatalf("award %d op37=%x", index, body)
		}
	}
	resources := currentDungeonSettlementTestResources(t)
	resources.Progression = tables
	runtime.Dungeon.Metadata.BasisLevel = worldmap.OptionalInt{Value: 1, Set: true}
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint = worldmap.OptionalNumber{Value: 1, Set: true}
	runtime.Request.Difficulty = 1
	runtime.settlementPlayResultReceived = true
	runtime.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
	runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
	if err := service.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, resources); err != nil {
		t.Fatal(err)
	}
	if runtime.settlementResultPlan == nil || runtime.Character.Level != 90 || runtime.Character.Stats["exp"] != int64(entry) {
		t.Fatal("capped experience blocked the committed settlement plan")
	}
	beforeReplay, _, _ := repos.Character.Load(ctx, "99")
	accountBeforeReplay, found, err := repos.Account.Load(ctx, character.AccountID)
	if err != nil || !found || accountBeforeReplay.HonorExp == 0 || beforeReplay.Stats[currentHonorExpertProgressExperienceStatKey] == int64(expectedHonor.CurrentLevelExperience) {
		t.Fatalf("settlement did not advance character/account honor: account=%+v found=%t err=%v", accountBeforeReplay, found, err)
	}
	conn.write.Reset()
	if err := service.handleDungeonSetPlayResult(session, append([]byte(nil), runtime.settlementPlayResultBody...)); err != nil {
		t.Fatal(err)
	}
	stream := conn.write.Bytes()
	for _, want := range []uint16{34, 37, 35} {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.MsgID != want || packet.Header.Classification != 0 {
			t.Fatalf("settlement packet=%+v want op%d", packet.Header, want)
		}
		if want == 37 && (packet.Body[0] != 90 || binary.LittleEndian.Uint32(packet.Body[1:5]) != entry) {
			t.Fatalf("settlement op37=%x", packet.Body)
		}
		stream = rest
	}
	written := conn.write.Len()
	if err := service.handleDungeonSetPlayResult(session, append([]byte(nil), runtime.settlementPlayResultBody...)); err != nil || conn.write.Len() != written {
		t.Fatalf("op46 replay resent the settlement: err=%v", err)
	}
	afterReplay, _, _ := repos.Character.Load(ctx, "99")
	accountAfterReplay, _, err := repos.Account.Load(ctx, character.AccountID)
	if err != nil || accountAfterReplay.HonorExp != accountBeforeReplay.HonorExp || accountAfterReplay.Metadata[adventuregroup.RuntimeStateMetadataKey] != accountBeforeReplay.Metadata[adventuregroup.RuntimeStateMetadataKey] {
		t.Fatalf("op46 replay duplicated account honor: before=%+v after=%+v err=%v", accountBeforeReplay, accountAfterReplay, err)
	}
	if afterReplay.Level != beforeReplay.Level || afterReplay.Stats["exp"] != beforeReplay.Stats["exp"] ||
		afterReplay.Stats[currentHonorExpertLevelStatKey] != beforeReplay.Stats[currentHonorExpertLevelStatKey] || afterReplay.Stats[currentHonorExpertProgressExperienceStatKey] != beforeReplay.Stats[currentHonorExpertProgressExperienceStatKey] {
		t.Fatal("op46 replay duplicated progression")
	}
}
