package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Change only the level between the handler's preflight and its real asset
// transaction. This is a controlled interleaving, not a player-state capture.
type epicStartLevelInterleave struct {
	repo.CharacterAssetUnitOfWork
	characters repo.CharacterRepository
	level      int
}

func (u epicStartLevelInterleave) WithinCharacterAssets(ctx context.Context, id string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	c, found, err := u.characters.Load(ctx, id)
	if err != nil || !found {
		return errCurrentEpicProductionInvalid
	}
	c.Level = u.level
	if err := u.characters.Save(ctx, c); err != nil {
		return err
	}
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, apply)
}

func TestRealPVFEpicStartLevelRequirementRound15(t *testing.T) {
	for _, name := range []string{"negative", "zero", "below_limit", "at_limit", "above_limit", "existing_target_below_limit", "level_changed_after_preflight"} {
		t.Run(name, func(t *testing.T) {
			s, session, conn := newCurrentEpicProductionRealPVFService(t, epicProductionSessionAccount, epicProductionAccountScopeSession, epicProductionInfoPushAlways)
			s.options.currentClientVersion = "official-24.9"
			catalog, err := parseCurrentEpicProductionCatalog(s.initialEquipmentArchive)
			if err != nil || catalog.levelLimit < 2 {
				t.Fatalf("real level limit: catalog=%+v err=%v", catalog, err)
			}
			g, _ := s.repositoryGroup()
			ctx := context.Background()
			before, _, err := g.Character.Load(ctx, "3")
			if err != nil {
				t.Fatal(err)
			}
			before.Stats[currentEpicProductionChargeStat] = 1234
			before.Stats[currentEpicProductionWeeklyCountStat] = 2
			before.Stats[currentEpicProductionCarryGroup1Stat] = 456
			before.Level = int(catalog.levelLimit) - 1
			reject := true
			switch name {
			case "negative":
				before.Level = -1
			case "zero":
				before.Level = 0
			case "at_limit":
				before.Level = int(catalog.levelLimit)
				reject = false
			case "above_limit":
				before.Level = int(catalog.levelLimit) + 1
				reject = false
			case "existing_target_below_limit":
				before.Stats[currentEpicProductionTargetStat] = int64(epicProductionLiveTargetItemID)
			case "level_changed_after_preflight":
				before.Level = int(catalog.levelLimit)
				modified := g
				modified.CharacterAssets = epicStartLevelInterleave{g.CharacterAssets, g.Character, int(catalog.levelLimit) - 1}
				s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
			}
			if err := g.Character.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			if err := s.handleCurrentEpicProductionStart(session, epicProductionStartRequestBody(t, epicProductionLiveTargetItemID)); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 1415 || len(rest) != 0 {
				t.Fatalf("start reply=%+v trailing=%x", packet.Header, rest)
			}
			after, _, err := g.Character.Load(ctx, "3")
			if err != nil {
				t.Fatal(err)
			}
			want := repo.CloneCharacter(before)
			if name == "level_changed_after_preflight" {
				want.Level = int(catalog.levelLimit) - 1
			}
			if reject {
				if !bytes.Equal(packet.Body, []byte{0, currentEpicProductionGenericError}) {
					t.Errorf("level=%d limit=%d reply=%x want rejection", after.Level, catalog.levelLimit, packet.Body)
				}
			} else {
				want.Stats[currentEpicProductionTargetStat] = int64(epicProductionLiveTargetItemID)
				if !bytes.Equal(packet.Body, upperSuccessBody(buildCurrentEpicProductionStartSuccessBody(epicProductionLiveTargetItemID))) {
					t.Errorf("valid start reply=%x", packet.Body)
				}
			}
			if !reflect.DeepEqual(after, want) {
				t.Errorf("start changed unexpected fields: got=%+v want=%+v", after, want)
			}
		})
	}
}
