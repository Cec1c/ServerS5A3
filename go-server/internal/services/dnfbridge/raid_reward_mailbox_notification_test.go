package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfmail "longheng.io/server/internal/modules/dnf/mail"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type raidMailboxAlarmAbortAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
}

func (u raidMailboxAlarmAbortAssets) WithinAccountCharacterMailboxAssets(
	ctx context.Context,
	accountID, characterID string,
	apply func(
		dnfrepo.AccountInventoryRepository,
		dnfrepo.CharacterRepository,
		dnfrepo.InventoryRepository,
		dnfrepo.EquipmentRepository,
		dnfrepo.MailboxRepository,
	) error,
) error {
	withMailbox := u.AccountCharacterAssetUnitOfWork.(dnfrepo.AccountCharacterMailboxAssetUnitOfWork)
	return withMailbox.WithinAccountCharacterMailboxAssets(ctx, accountID, characterID, func(
		accounts dnfrepo.AccountInventoryRepository,
		characters dnfrepo.CharacterRepository,
		inventory dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		if err := apply(accounts, characters, inventory, equipment, mailboxes); err != nil {
			return err
		}
		return errors.New("injected failure after raid overflow projection")
	})
}

func newRaidOverflowAlarmFixture(t *testing.T) (dnfrepo.Group, *pvfDungeonDropCatalog, currentAntonRewardPlan, raid.RewardCommand) {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "700001 `raid_reward.stk`\n",
		"stackable/raid_reward.stk": "[stackable type]\n`[material]` 20\n" +
			"[stack limit]\n2\n",
	})
	if err != nil {
		t.Fatalf("build synthetic raid reward catalog: %v", err)
	}
	definition, err := catalog.ResolveItem(700001)
	if err != nil {
		t.Fatalf("resolve synthetic raid reward: %v", err)
	}
	definition.SlotStart, definition.SlotEnd = 9, 56
	choice := raid.AntonRewardChoice{Type: "party_card", Weight: 1, ContainerID: 700001}
	plan := currentAntonRewardPlan{
		Choice: choice,
		Rewards: []currentBoosterReward{{
			Definition: definition,
			Count:      2,
			Placement:  currentBoosterRewardMain,
		}},
	}
	encoded, err := json.Marshal(struct {
		Choice  raid.AntonRewardChoice
		Rewards []currentBoosterReward
	}{plan.Choice, plan.Rewards})
	if err != nil {
		t.Fatal(err)
	}
	plan.Fingerprint = sha256.Sum256(encoded)

	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	if err := group.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := group.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "31", AccountID: "account", Stats: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	slots := make(map[string]dnfrepo.ItemStack, 256)
	for slot := int16(0); slot <= 255; slot++ {
		slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 700099, Count: 1}
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "31", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	completedAt := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	command := raid.RewardCommand{
		AccountID:     "account",
		CharacterID:   "31",
		InstanceID:    "00112233445566778899aabbccddeeff",
		Mode:          1,
		Phase:         0,
		Kind:          1,
		MinimumClears: 1,
		OwnedClears:   1,
		Fingerprint:   plan.Fingerprint,
		CompletedAt:   completedAt,
	}
	return group, catalog, plan, command
}

func raidAlarmMailboxTotals(t *testing.T, group dnfrepo.Group, characterID string) (int, int64) {
	t.Helper()
	mailbox, found, err := group.Mailbox.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return 0, 0
	}
	var count int64
	for _, message := range mailbox.Mails {
		for _, attachment := range message.Attachments {
			count += attachment.Count
		}
	}
	return len(mailbox.Mails), count
}

func TestRaidOverflowMailboxAlarmOnlyAfterCommitAndNotOnReplay(t *testing.T) {
	group, catalog, plan, command := newRaidOverflowAlarmFixture(t)
	originalAssets := group.AccountAssets
	group.AccountAssets = raidMailboxAlarmAbortAssets{AccountCharacterAssetUnitOfWork: originalAssets}
	result, err := grantCurrentAntonRewardPlan(context.Background(), group, catalog, plan, command)
	if err == nil {
		t.Fatal("post-projection failure was accepted")
	}
	if result.MailboxChanged {
		t.Fatal("failed transaction reported a mailbox commit")
	}
	if mailbox, found, loadErr := group.Mailbox.Load(context.Background(), "31"); loadErr != nil || found && len(mailbox.Mails) != 0 {
		t.Fatalf("failed transaction leaked mail found=%t mailbox=%+v err=%v", found, mailbox, loadErr)
	}

	group.AccountAssets = originalAssets
	connection := &bufferConn{}
	otherConnection := &bufferConn{}
	service := &Service{
		options: options{
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return group, true },
		gameSessions: map[uint16]*gameSession{
			31: &gameSession{conn: connection, connID: "raid-mail-31", channel: channelcatalog.Channel{ID: 19}, selectedCharacterID: 31, characterGeneration: 1},
			99: &gameSession{conn: otherConnection, connID: "raid-mail-99", channel: channelcatalog.Channel{ID: 19}, selectedCharacterID: 99, characterGeneration: 1},
		},
	}
	result, err = grantCurrentAntonRewardPlan(context.Background(), group, catalog, plan, command)
	if err != nil || result.Replayed || !result.MailboxChanged {
		t.Fatalf("first committed overflow result=%+v err=%v", result, err)
	}
	if otherConnection.write.Len() != 0 {
		t.Fatal("unrelated online character received raid mailbox state")
	}
	service.notifyCurrentRaidRewardMailbox(31, result.MailboxChanged)
	packet, trailing := splitGameServerUpperPacketWithHeader(t, connection.write.Bytes(), service.gameUpperHeaderSize())
	if len(trailing) != 0 || packet.Header.Classification != 0 || packet.Header.MsgID != dnfmail.MailboxAlarmNotificationMessageID ||
		!bytes.Equal(packet.Body, []byte{1, 0}) {
		t.Fatalf("raid overflow alarm header=%+v body=% X trailing=% X", packet.Header, packet.Body, trailing)
	}
	if otherConnection.write.Len() != 0 {
		t.Fatal("raid overflow alarm targeted a character without created mail")
	}

	beforeMails, beforeCount := raidAlarmMailboxTotals(t, group, "31")
	firstAlarmLength := connection.write.Len()
	replay, err := grantCurrentAntonRewardPlan(context.Background(), group, catalog, plan, command)
	if err != nil || !replay.Replayed || replay.MailboxChanged {
		t.Fatalf("replay result=%+v err=%v", replay, err)
	}
	service.notifyCurrentRaidRewardMailbox(31, replay.MailboxChanged)
	if connection.write.Len() != firstAlarmLength {
		t.Fatal("replayed receipt emitted another raid mailbox alarm")
	}
	afterMails, afterCount := raidAlarmMailboxTotals(t, group, "31")
	if beforeMails != 1 || beforeCount != 2 || afterMails != beforeMails || afterCount != beforeCount {
		t.Fatalf("raid overflow mail changed across replay before=%d/%d after=%d/%d", beforeMails, beforeCount, afterMails, afterCount)
	}
}

func TestRealPVFAntonAutomaticOverflowMailAlarmOnlyForCommittedRecipient(t *testing.T) {
	f, _, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	ctx := context.Background()
	inventory, found, err := f.repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character 19 inventory found=%t err=%v", found, err)
	}
	// Cover the full extended material page too; it ends at slot 359.
	for slot := int16(0); slot <= currentPVFMainMaterialExtraEnd; slot++ {
		inventory.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 10096296, Count: 1}
	}
	if err := f.repos.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	countAlarms := func() int {
		t.Helper()
		stream := append([]byte(nil), f.conn.write.Bytes()...)
		count := 0
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			if packet.Header.Classification == 0 && packet.Header.MsgID == dnfmail.MailboxAlarmNotificationMessageID {
				count++
			}
			stream = rest
		}
		return count
	}

	f.conn.write.Reset()
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	// The selected phase-0 party reward is fate currency (10304529), which
	// credits the account ledger even with a full bag. It must not alert mail.
	if got := countAlarms(); got != 0 {
		t.Fatalf("ledger-only party reward emitted %d mailbox alarms", got)
	}

	f.conn.write.Reset()
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := countAlarms(); got != 1 {
		t.Fatalf("squad overflow alarms=%d want exactly one for character 19", got)
	}
	if other, found, err := f.repos.Mailbox.Load(ctx, "20"); err != nil || found && len(other.Mails) != 0 {
		t.Fatalf("character 20 unexpectedly received overflow mail found=%t mailbox=%+v err=%v", found, other, err)
	}

	f.conn.write.Reset()
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := countAlarms(); got != 0 {
		t.Fatalf("reward replay emitted %d mailbox alarms", got)
	}
}
