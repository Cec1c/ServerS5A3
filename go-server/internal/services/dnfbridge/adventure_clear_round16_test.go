package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Round 16 / original issue 85 (adventure group shows no clear count).
//
// The counter has two ends, and each end already has its own narrow test:
//
//	producer: dnfdungeon.SettlementCommand.RecommendedDungeonClear ->
//	          character.Stats[adventuregroup.RecommendedDungeonClearStatKey]
//	          (modules/dnf/dungeon/settlement_owner.go, receipt guarded)
//	consumer: adventuregroup.ProjectActivity ->
//	          Projection.ContentCounts[ContentTypeRecommendedDungeon]
//	          (modules/dnf/adventuregroup/activity.go) ->
//	          1339/1401 adventure info payload
//	          (services/dnfbridge/account_adventure_protocol.go) plus the
//	          send log in account_adventure_push.go.
//
// This file runs the real settlement owner and the real account-info
// construction/send in one flow, so a key or trigger mismatch between the two
// ends cannot hide behind a synthetic projection hand-written per side.
//
// Fixtures: the settlement uses the existing synthetic progression source
// helper currentDungeonSettlementTestResources (shared with
// dungeon_settlement_reward_owner_test.go) and the account-info state uses the
// existing synthetic adventure-group table helper loadAdventureGroupTestTables.
// TestRound16AdventureClearWithFixedClientPVF repeats the same chain against the
// published fixed Script.pvf when DNFBRIDGE_REAL_PVF_SMOKE points at it.

const round16AdventureClearAccountID = "dnf:round16-adventure-clear"

// round16RecommendedClearTrigger mirrors the production trigger call site
// services/dnfbridge/dungeon_settlement_reward_owner.go:532, where
// RecommendedDungeonClear is exactly currentDungeonIsRecommendedClear(runtime).
// Driving that call site needs a live dungeon session, so this helper feeds the
// same production predicate the same runtime fields.
func round16RecommendedClearTrigger(dungeonID int64, recommendedLevels []int64, level int, accepted bool) bool {
	return currentDungeonIsRecommendedClear(&runtimeDungeonState{
		Dungeon: worldmap.Dungeon{
			ID:       dungeonID,
			Metadata: worldmap.DungeonMetadata{RecommendedLevels: recommendedLevels},
		},
		Character:                      dnfrepo.CharacterRecord{Level: level},
		entryCharacterLevel:            level,
		ordinaryFinalRoomClearAccepted: accepted,
	})
}

func round16SeedAdventureClearAccount(t *testing.T, repos dnfrepo.Group, characterIDs ...string) {
	t.Helper()
	ctx := context.Background()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: round16AdventureClearAccountID,
		State:     "active",
		Metadata:  map[string]string{adventureGroupCreatedDateMetadataKey: "2026-07-16"},
	}); err != nil {
		t.Fatalf("round16 seed account: %v", err)
	}
	for slot, characterID := range characterIDs {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: characterID,
			AccountID:   round16AdventureClearAccountID,
			Slot:        slot,
			Name:        "clear" + characterID,
			Job:         "0",
			Level:       currentAdventureCharacterLevelCap,
			Stats:       map[string]int64{"exp": 0, "grow_type": 0x21},
		}); err != nil {
			t.Fatalf("round16 seed character %s: %v", characterID, err)
		}
		if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
			CharacterID: characterID,
			Points:      dnfrepo.SkillPointState{SyncedLevel: currentAdventureCharacterLevelCap},
		}); err != nil {
			t.Fatalf("round16 seed skill %s: %v", characterID, err)
		}
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: characterID,
			Slots:       map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatalf("round16 seed inventory %s: %v", characterID, err)
		}
	}
}

// round16CommitAdventureClear is the real production writer of
// RecommendedDungeonClearStatKey; nothing in this file writes that stat itself.
func round16CommitAdventureClear(
	t *testing.T,
	repos dnfrepo.Group,
	tables *progression.Tables,
	characterID string,
	completionKey string,
	recommended bool,
) dnfdungeon.SettlementResult {
	t.Helper()
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		t.Fatalf("round16 settlement owner: %v", err)
	}
	result, err := owner.CommitSettlement(context.Background(), dnfdungeon.SettlementCommand{
		AccountID:               round16AdventureClearAccountID,
		CharacterID:             characterID,
		CompletionKey:           completionKey,
		Tables:                  tables,
		Experience:              20,
		RecommendedDungeonClear: recommended,
		MaximumCharacterLevel:   currentAdventureCharacterLevelCap,
	})
	if err != nil {
		t.Fatalf("round16 CommitSettlement(%s key=%s recommended=%t): %v", characterID, completionKey, recommended, err)
	}
	return result
}

func round16CharacterClearCount(t *testing.T, repos dnfrepo.Group, characterID string) int64 {
	t.Helper()
	character, found, err := repos.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("round16 load character %s found=%t err=%v", characterID, found, err)
	}
	return character.Stats[adventuregroup.RecommendedDungeonClearStatKey]
}

func round16AdventureClearService(t *testing.T, repos dnfrepo.Group) *Service {
	t.Helper()
	return &Service{
		options: options{
			accountID:          round16AdventureClearAccountID,
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		adventureGroupTable: loadAdventureGroupTestTables(t),
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repos, true },
	}
}

// round16SelectCollectionCard stores the same account-side character-collection
// choice the op1474 handler writes, so the real CollectionCounts projection
// produces a nonzero group counter in the same payload slot the clear count
// would need.
func round16SelectCollectionCard(t *testing.T, repos dnfrepo.Group, key, characterID string) {
	t.Helper()
	ctx := context.Background()
	account, found, err := repos.Account.Load(ctx, round16AdventureClearAccountID)
	if err != nil || !found {
		t.Fatalf("round16 load account for collection selection found=%t err=%v", found, err)
	}
	if account.Metadata == nil {
		account.Metadata = map[string]string{}
	}
	account.Metadata[adventuregroup.CollectionMetadataPrefix+key] = characterID
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatalf("round16 save collection selection %s=%s: %v", key, characterID, err)
	}
}

// round16AdventureContentSlot reads one entry of the 20 (type.byte, count.u16)
// triples the client-facing info payload carries at raw+7348.
func round16AdventureContentSlot(raw []byte, contentType int) (byte, uint16) {
	offset := currentAdventureInfoTripleOffset + contentType*currentAdventureInfoTripleSize
	return raw[offset+currentAdventureInfoTripleTypeOffset],
		binary.LittleEndian.Uint16(raw[offset+currentAdventureInfoTripleCountOffset : offset+currentAdventureInfoTripleCountOffset+2])
}

func round16DescribeAdventureContentSlots(raw []byte) string {
	builder := strings.Builder{}
	for index := 0; index < currentAdventureInfoTripleCount; index++ {
		contentType, count := round16AdventureContentSlot(raw, index)
		fmt.Fprintf(&builder, "[%d]=type:%d/count:%d ", index, contentType, count)
	}
	return strings.TrimSpace(builder.String())
}

// round16RequestAdventureInfo drives the real 1401 request handler and returns
// the payload after the one-byte success prefix. Callers take the 7424-byte
// account state at payload[18:18+currentAdventureInfoRawLength], matching the
// existing info-payload tests.
func round16RequestAdventureInfo(t *testing.T, service *Service) []byte {
	t.Helper()
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "round16-adventure-clear",
		channel:             channelcatalog.Channel{ID: 19},
		selectedCharacterID: 101,
	}
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		currentAdventureInfoRequestMsgID,
		make([]byte, currentAdventureInfoRequestWireLength),
	); err != nil {
		t.Fatalf("round16 adventure info request: %v", err)
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("round16 adventure info trailing packet bytes = %d", len(rest))
	}
	if packet.Header.MsgID != currentAdventureInfoRequestMsgID {
		t.Fatalf("round16 adventure info msg id = %d", packet.Header.MsgID)
	}
	if len(packet.Body) < 1+18+currentAdventureInfoRawLength || packet.Body[0] != 1 {
		t.Fatalf("round16 adventure info body len/prefix = %d/%x", len(packet.Body), packet.Body[:1])
	}
	return packet.Body[1:]
}

// TestRound16AdventureClearChainSettlementToAccountInfoState is the green half:
// the settlement trigger, the durable per-character key and the account-wide
// projection the info payload is built from are the same counter.
func TestRound16AdventureClearChainSettlementToAccountInfoState(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	// These characters are level 90; the cap proof must have that entry
	// threshold rather than the unrelated four-level settlement fixture.
	tables := finishBridgeCapProgression(t)
	round16SeedAdventureClearAccount(t, repos, "101", "102")

	inRange := round16RecommendedClearTrigger(11, []int64{85, 90}, currentAdventureCharacterLevelCap, true)
	outOfRange := round16RecommendedClearTrigger(11, []int64{85, 90}, 91, true)
	notAccepted := round16RecommendedClearTrigger(11, []int64{85, 90}, currentAdventureCharacterLevelCap, false)
	if !inRange || outOfRange || notAccepted {
		t.Fatalf("round16 trigger sanity: inRange=%t outOfRange=%t notAccepted=%t", inRange, outOfRange, notAccepted)
	}

	first := round16CommitAdventureClear(t, repos, tables, "101", "round16-run-1", inRange)
	if first.Replayed {
		t.Fatal("round16 first clear reported as replay")
	}
	if got := round16CharacterClearCount(t, repos, "101"); got != 1 {
		t.Fatalf("round16 character 101 clears after first accepted clear = %d, want 1", got)
	}

	replay := round16CommitAdventureClear(t, repos, tables, "101", "round16-run-1", inRange)
	if !replay.Replayed {
		t.Fatal("round16 replayed completion key was not recognized as a replay")
	}
	if got := round16CharacterClearCount(t, repos, "101"); got != 1 {
		t.Fatalf("round16 character 101 clears after replay = %d, want 1 (replay must not double count)", got)
	}

	round16CommitAdventureClear(t, repos, tables, "101", "round16-run-2", inRange)
	if got := round16CharacterClearCount(t, repos, "101"); got != 2 {
		t.Fatalf("round16 character 101 clears after second key = %d, want 2", got)
	}

	round16CommitAdventureClear(t, repos, tables, "101", "round16-run-3", outOfRange)
	if got := round16CharacterClearCount(t, repos, "101"); got != 2 {
		t.Fatalf("round16 out-of-range clear changed character 101 clears to %d, want 2", got)
	}
	round16CommitAdventureClear(t, repos, tables, "101", "round16-run-4", notAccepted)
	if got := round16CharacterClearCount(t, repos, "101"); got != 2 {
		t.Fatalf("round16 unaccepted clear changed character 101 clears to %d, want 2", got)
	}

	round16CommitAdventureClear(t, repos, tables, "102", "round16-run-5", inRange)
	if got := round16CharacterClearCount(t, repos, "102"); got != 1 {
		t.Fatalf("round16 character 102 clears = %d, want 1", got)
	}

	// A fresh load is what the info state reads; nothing is taken from the
	// in-memory settlement result above.
	state, err := round16AdventureClearService(t, repos).currentAccountAdventureGroupInfoState(
		ctx,
		dnfrepo.CharacterRecord{},
		false,
		&gameSession{},
	)
	if err != nil {
		t.Fatalf("round16 account adventure info state: %v", err)
	}
	if len(state.Characters) != 2 {
		t.Fatalf("round16 account roster = %d characters, want 2", len(state.Characters))
	}
	aggregated := state.Projection.ContentCounts[adventuregroup.ContentTypeRecommendedDungeon]
	if aggregated != 3 {
		t.Fatalf("round16 account-wide recommended dungeon clears = %d, want 3 (2 on character 101 + 1 on character 102, read through %s)",
			aggregated, adventuregroup.RecommendedDungeonClearStatKey)
	}
}

// TestRound16AdventureClearWithFixedClientPVF repeats the same chain against the
// published fixed client PVF used for this baseline, so the key agreement is not
// only asserted on synthetic tables. It logs, rather than asserts, the payload
// collection slot without asserting an unproved clear-counter wire location.
func TestRound16AdventureClearWithFixedClientPVF(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the fixed published Script.pvf")
	}
	ctx := context.Background()
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("round16 open fixed PVF %s: %v", pvfPath, err)
	}
	groupTables, err := adventuregroup.LoadComplete(ctx, archive)
	if err != nil {
		t.Fatalf("round16 load adventure-group tables from fixed PVF: %v", err)
	}
	progressionTables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatalf("round16 load progression tables from fixed PVF: %v", err)
	}

	repos := dnfrepomemory.NewMemoryGroup()
	round16SeedAdventureClearAccount(t, repos, "101", "102")
	round16SelectCollectionCard(t, repos, "0_1", "101")
	inRange := round16RecommendedClearTrigger(11, []int64{85, 90}, currentAdventureCharacterLevelCap, true)
	round16CommitAdventureClear(t, repos, progressionTables, "101", "round16-pvf-run-1", inRange)
	round16CommitAdventureClear(t, repos, progressionTables, "101", "round16-pvf-run-2", inRange)
	round16CommitAdventureClear(t, repos, progressionTables, "102", "round16-pvf-run-3", inRange)

	service := &Service{
		options: options{
			accountID:          round16AdventureClearAccountID,
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		adventureGroupTable: groupTables,
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repos, true },
	}
	state, err := service.currentAccountAdventureGroupInfoState(ctx, dnfrepo.CharacterRecord{}, false, &gameSession{})
	if err != nil {
		t.Fatalf("round16 fixed-PVF account adventure info state: %v", err)
	}
	aggregated := state.Projection.ContentCounts[adventuregroup.ContentTypeRecommendedDungeon]
	if aggregated != 3 {
		t.Fatalf("round16 fixed-PVF account-wide clears = %d, want 3", aggregated)
	}
	if groups := len(groupTables.Runtime().CollectionGroups); groups == 0 {
		t.Fatal("round16 fixed PVF produced no collection groups; the payload build path is incomplete")
	}
	if got := state.Projection.CollectionCounts[0]; got == 0 {
		t.Fatalf("round16 fixed-PVF collection counter for the selected male card = %d, want nonzero", got)
	}
	payload := round16RequestAdventureInfo(t, service)
	raw := payload[18 : 18+currentAdventureInfoRawLength]
	contentType, count := round16AdventureContentSlot(raw, adventuregroup.ContentTypeRecommendedDungeon)
	if count == 0 {
		t.Fatalf("round16 fixed-PVF payload slot[%d] did not carry the selected character-collection counter: %s",
			adventuregroup.ContentTypeRecommendedDungeon, round16DescribeAdventureContentSlots(raw))
	}
	t.Logf("round16 fixed-PVF payload slot[%d] = type:%d/count:%d carries the character-collection counter, not the durable recommended-dungeon clear count (%d), whose native wire/display location remains unresolved",
		adventuregroup.ContentTypeRecommendedDungeon, contentType, count, aggregated)
	t.Logf("round16 fixed-PVF payload slots = %s", round16DescribeAdventureContentSlots(raw))
}
