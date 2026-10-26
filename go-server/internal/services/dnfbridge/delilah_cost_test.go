package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func loadDelilahCostTestCharacter(t *testing.T, repositories dnfrepo.Group) dnfrepo.CharacterRecord {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	return character
}

func loadDelilahCostTestGold(t *testing.T, repositories dnfrepo.Group) int64 {
	t.Helper()
	return loadDelilahCostTestCharacter(t, repositories).Stats["gold"]
}

func setDelilahCostTestGold(t *testing.T, repositories dnfrepo.Group, gold int64) {
	t.Helper()
	character := loadDelilahCostTestCharacter(t, repositories)
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	character.Stats["gold"] = gold
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
}

func attachDelilahCostCompletedRuntime(t *testing.T, session *gameSession) {
	t.Helper()
	_, runtime, _, _, _ := prepareCompletedSettlementRuntime(t)
	runtime.Character.CharacterID = "19"
	runtime.Character.AccountID = "account-1"
	runtime.AccountID = "account-1"
	session.selectedCharacterID = 19
	session.dungeon.runtime = runtime
}

func attachDelilahCostActiveRuntime(t *testing.T, session *gameSession) {
	t.Helper()
	_, runtime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{singleMonster: true})
	runtime.Character.CharacterID = "19"
	runtime.Character.AccountID = "account-1"
	runtime.AccountID = "account-1"
	session.selectedCharacterID = 19
	session.dungeon.runtime = runtime
}

func assertDelilahCostSourcePresent(t *testing.T, repositories dnfrepo.Group, slots ...int16) {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	for _, slot := range slots {
		stack := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)]
		if stack.ItemID != 5000 || stack.Count != 1 {
			t.Fatalf("source slot %d mutated: %+v", slot, stack)
		}
	}
}

func assertDelilahCostNoAccountReward(t *testing.T, repositories dnfrepo.Group) {
	t.Helper()
	account, found, err := repositories.AccountInventory.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if found && len(account.Slots) != 0 {
		t.Fatalf("failed disassembly granted account rewards: %+v", account.Slots)
	}
}

func TestDelilahDisjointFormalHandlerCharges500PerItem(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
			"0:65": {ItemID: 5000, Count: 1},
		})
		setDelilahCostTestGold(t, repositories, 2000)
		connection := &bufferConn{}
		service := currentDisjointTestService(repositories, catalog)
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		attachDelilahCostCompletedRuntime(t, session)
		if err := service.handleCurrentDisjointItem(session,
			currentDisjointItemTestBody(65, 0, -1, 1000)); err != nil {
			t.Fatal(err)
		}
		ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(ack.Body) == 0 || ack.Body[0] != 1 {
			t.Fatalf("op26 ACK=%x trailing=%d", ack.Body, len(trailing))
		}
		wallet, rest := splitGameServerUpperPacket(t, trailing)
		if len(rest) != 0 || wallet.Header.MsgID != 14 || wallet.Header.Classification != 0 || !bytes.Equal(wallet.Body, buildCurrentGoldStateBody(1500)) {
			t.Fatalf("committed wallet=%x rest=%d", wallet.Body, len(rest))
		}
		if got := loadDelilahCostTestGold(t, repositories); got != 1500 {
			t.Fatalf("single Delilah fee left gold=%d want=1500", got)
		}
		inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
		if _, found := inventory.Slots["0:65"]; found {
			t.Fatal("single Delilah source was not consumed")
		}
	})

	t.Run("batch", func(t *testing.T) {
		repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
			"0:65": {ItemID: 5000, Count: 1},
			"0:66": {ItemID: 5000, Count: 1},
		})
		setDelilahCostTestGold(t, repositories, 2000)
		connection := &bufferConn{}
		service := currentDisjointTestService(repositories, catalog)
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		attachDelilahCostCompletedRuntime(t, session)
		if err := service.handleCurrentDisjointItem(session,
			currentDisjointBatchBody([]int16{65, 66}, -1, 1000)); err != nil {
			t.Fatal(err)
		}
		ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(ack.Body) == 0 || ack.Body[0] != 1 {
			t.Fatalf("batch op26 ACK=%x", ack.Body)
		}
		var lastGold []byte
		for len(trailing) != 0 {
			frame, rest := splitGameServerUpperPacket(t, trailing)
			if frame.Header.Classification == 0 && frame.Header.MsgID == 14 && bytes.Equal(frame.Body, buildCurrentGoldStateBody(1000)) {
				if len(rest) != 0 || lastGold != nil {
					t.Fatal("wallet must be sent once, after ACK/slot refreshes")
				}
				lastGold = frame.Body
			}
			trailing = rest
		}
		if lastGold == nil {
			t.Fatal("batch did not publish committed 1000 gold")
		}
		if got := loadDelilahCostTestGold(t, repositories); got != 1000 {
			t.Fatalf("two-item Delilah fee left gold=%d want=1000", got)
		}
		sources := map[string]dnfrepo.ItemStack{"0:65": {ItemID: 5000, Count: 1}, "0:66": {ItemID: 5000, Count: 1}}
		inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
		for slot := range sources {
			if _, found := inventory.Slots[slot]; found {
				t.Fatalf("batch Delilah source %s was not consumed", slot)
			}
		}
	})
}

func TestDelilahDisjointInsufficientGoldHasZeroConsumption(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
			"0:65": {ItemID: 5000, Count: 1},
		})
		setDelilahCostTestGold(t, repositories, 499)
		connection := &bufferConn{}
		service := currentDisjointTestService(repositories, catalog)
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		attachDelilahCostCompletedRuntime(t, session)
		if err := service.handleCurrentDisjointItem(session,
			currentDisjointItemTestBody(65, 0, -1, 1000)); err != nil {
			t.Fatal(err)
		}
		failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(trailing) != 0 || failure.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || !bytes.Equal(failure.Body, []byte{0, 19}) {
			t.Fatalf("single insufficient failure=%x trailing=%d", failure.Body, len(trailing))
		}
		if got := loadDelilahCostTestGold(t, repositories); got != 499 {
			t.Fatalf("single insufficient gold=%d want=499", got)
		}
		assertDelilahCostSourcePresent(t, repositories, 65)
		assertDelilahCostNoAccountReward(t, repositories)
	})

	t.Run("batch", func(t *testing.T) {
		repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
			"0:65": {ItemID: 5000, Count: 1},
			"0:66": {ItemID: 5000, Count: 1},
		})
		setDelilahCostTestGold(t, repositories, 999)
		connection := &bufferConn{}
		service := currentDisjointTestService(repositories, catalog)
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		attachDelilahCostCompletedRuntime(t, session)
		if err := service.handleCurrentDisjointItem(session,
			currentDisjointBatchBody([]int16{65, 66}, -1, 1000)); err != nil {
			t.Fatal(err)
		}
		failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(trailing) != 0 || failure.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || !bytes.Equal(failure.Body, []byte{0, 19}) {
			t.Fatalf("batch insufficient failure=%x trailing=%d", failure.Body, len(trailing))
		}
		if got := loadDelilahCostTestGold(t, repositories); got != 999 {
			t.Fatalf("batch insufficient gold=%d want=999", got)
		}
		assertDelilahCostSourcePresent(t, repositories, 65, 66)
		assertDelilahCostNoAccountReward(t, repositories)
	})
}

func TestDisjointFeeScopeKeepsOrdinaryMachinesFree(t *testing.T) {
	for _, contextValue := range []uint32{317, 0} {
		t.Run(map[uint32]string{317: "arad-machine", 0: "no-npc-context"}[contextValue], func(t *testing.T) {
			repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
				"0:65": {ItemID: 5000, Count: 1},
			})
			setDelilahCostTestGold(t, repositories, 100)
			connection := &bufferConn{}
			service := currentDisjointTestService(repositories, catalog)
			if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19},
				currentDisjointItemTestBody(65, 0, -1, contextValue)); err != nil {
				t.Fatal(err)
			}
			ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
			if len(trailing) != 0 || ack.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(ack.Body) == 0 || ack.Body[0] != 1 {
				t.Fatalf("context=%d ACK=%x trailing=%d", contextValue, ack.Body, len(trailing))
			}
			if got := loadDelilahCostTestGold(t, repositories); got != 100 {
				t.Fatalf("context=%d changed non-Delilah gold to %d", contextValue, got)
			}
		})
	}
}

type delilahFailInventorySave struct {
	dnfrepo.InventoryRepository
	saveCalls *int
	err       error
}

func (f delilahFailInventorySave) SaveFields(context.Context, dnfrepo.InventoryRecord, ...dnfrepo.InventoryField) error {
	(*f.saveCalls)++
	return f.err
}

type delilahFailingInventoryAssets struct {
	inner     dnfrepo.AccountCharacterAssetUnitOfWork
	saveCalls *int
	err       error
}

func (f delilahFailingInventoryAssets) WithinAccountCharacterAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	return f.inner.WithinAccountCharacterAssets(ctx, accountID, characterID, func(
		accounts dnfrepo.AccountInventoryRepository,
		characters dnfrepo.CharacterRepository,
		inventory dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		return apply(accounts, characters, delilahFailInventorySave{InventoryRepository: inventory, saveCalls: f.saveCalls, err: f.err}, equipment)
	})
}

func TestDelilahLateInventorySaveFailureRollsBackFeeAndItems(t *testing.T) {
	repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
		"0:65": {ItemID: 5000, Count: 1},
	})
	setDelilahCostTestGold(t, repositories, 2000)
	saveCalls := 0
	wantErr := errors.New("injected late inventory save failure")
	repositories.AccountAssets = delilahFailingInventoryAssets{
		inner: repositories.AccountAssets, saveCalls: &saveCalls, err: wantErr,
	}
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	attachDelilahCostCompletedRuntime(t, session)
	if err := service.handleCurrentDisjointItem(session,
		currentDisjointItemTestBody(65, 0, -1, 1000)); err != nil {
		t.Fatal(err)
	}
	if saveCalls == 0 {
		t.Fatal("late inventory SaveFields failure was not injected")
	}
	failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || failure.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || !bytes.Equal(failure.Body, []byte{0, 19}) {
		t.Fatalf("late failure reply=%x trailing=%d", failure.Body, len(trailing))
	}
	if got := loadDelilahCostTestGold(t, repositories); got != 2000 {
		t.Fatalf("late failure persisted fee: gold=%d want=2000", got)
	}
	assertDelilahCostSourcePresent(t, repositories, 65)
	assertDelilahCostNoAccountReward(t, repositories)
}

func TestDelilahContextWithoutCompletedDungeonIsRejected(t *testing.T) {
	for _, test := range []struct {
		name  string
		apply func(*testing.T, *gameSession)
	}{
		{name: "missing-runtime"},
		{name: "active-runtime", apply: attachDelilahCostActiveRuntime},
		{name: "wrong-account", apply: func(t *testing.T, session *gameSession) {
			attachDelilahCostCompletedRuntime(t, session)
			session.dungeon.runtime.AccountID = "different-account"
		}},
		{name: "wrong-character", apply: func(t *testing.T, session *gameSession) {
			attachDelilahCostCompletedRuntime(t, session)
			session.dungeon.runtime.Character.CharacterID = "20"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
				"0:65": {ItemID: 5000, Count: 1},
			})
			setDelilahCostTestGold(t, repositories, 2000)
			connection := &bufferConn{}
			service := currentDisjointTestService(repositories, catalog)
			session := &gameSession{conn: connection, selectedCharacterID: 19}
			if test.apply != nil {
				test.apply(t, session)
			}
			if err := service.handleCurrentDisjointItem(session, currentDisjointItemTestBody(65, 0, -1, 1000)); err != nil {
				t.Fatal(err)
			}
			failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
			if len(trailing) != 0 || failure.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || !bytes.Equal(failure.Body, []byte{0, 19}) {
				t.Fatalf("spoofed Delilah context reply=%x trailing=%d", failure.Body, len(trailing))
			}
			if got := loadDelilahCostTestGold(t, repositories); got != 2000 {
				t.Fatalf("spoofed Delilah context changed gold=%d", got)
			}
			assertDelilahCostSourcePresent(t, repositories, 65)
			assertDelilahCostNoAccountReward(t, repositories)
		})
	}
}

func TestDelilahA56IdentityAndPVFHasNoFeeField(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the fixed a56 Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	npcListText, err := archive.ReadText("npc/npc.lst")
	if err != nil {
		t.Fatal(err)
	}
	npcList, err := dnfpvf.Parse("npc/npc.lst", npcListText)
	if err != nil {
		t.Fatal(err)
	}
	foundDelilah := false
	for _, entry := range dnfpvf.ParseList(npcList) {
		if entry.ID == 1000 && strings.Contains(strings.ToLower(entry.Path), "delilah.npc") {
			foundDelilah = true
			break
		}
	}
	if !foundDelilah {
		t.Fatal("a56 npc/npc.lst does not map NPC 1000 to Delilah.npc")
	}
	delilahText, err := archive.ReadText("npc/delilah.npc")
	if err != nil {
		t.Fatal(err)
	}
	delilahDoc, err := dnfpvf.Parse("npc/delilah.npc", delilahText)
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := delilahDoc.Text("name"); name != "德利拉" {
		t.Fatalf("NPC 1000 name=%q want=德利拉", name)
	}
	if !strings.Contains(delilahText, "`[simple shop]` 1000") || !strings.Contains(delilahText, "`[disjoint item]` 0") {
		t.Fatalf("a56 Delilah role changed: %s", delilahText)
	}
	disjointText, err := archive.ReadText("etc/disjoint.etc")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(disjointText)
	for _, feeField := range []string{"disjoint cost", "disjoint fee", "disjoint price", "gold cost", "fee"} {
		if strings.Contains(lower, feeField) {
			t.Fatalf("a56 etc/disjoint.etc unexpectedly contains %q", feeField)
		}
	}
}
