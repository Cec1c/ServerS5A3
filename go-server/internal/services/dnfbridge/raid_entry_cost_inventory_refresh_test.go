package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const raidEntryCostInventoryRefreshTestItem = 10096296

type raidEntryCostInventoryRefreshFixture struct {
	service       *Service
	repos         dnfrepo.Group
	leader        *gameSession
	peer          *gameSession
	bystander     *gameSession
	leaderConn    *bufferConn
	peerConn      *bufferConn
	bystanderConn *bufferConn
	state         runtimeRaidState
	rule          raid.AdmissionRule
}

func newRaidEntryCostInventoryRefreshFixture(t *testing.T) *raidEntryCostInventoryRefreshFixture {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	for id, account := range map[string]string{"19": "account-a", "20": "account-b", "30": "account-c"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: account}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	leader := &gameSession{
		conn:                &bufferConn{},
		selectedCharacterID: 19,
		characterGeneration: 1,
		accountID:           "account-a",
		accountFromToken:    true,
		loginPacketSeen:     true,
		channel:             channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType},
	}
	peer := &gameSession{
		conn:                &bufferConn{},
		selectedCharacterID: 20,
		characterGeneration: 2,
		accountID:           "account-b",
		accountFromToken:    true,
		loginPacketSeen:     true,
		channel:             leader.channel,
	}
	bystander := &gameSession{
		conn:                &bufferConn{},
		selectedCharacterID: 30,
		characterGeneration: 3,
		accountID:           "account-c",
		accountFromToken:    true,
		loginPacketSeen:     true,
		channel:             leader.channel,
	}
	state := runtimeRaidState{
		RaidKey:         23<<16 | 99,
		LeaderID:        19,
		RouteOrRaidType: 0,
		CurrentLayout:   true,
		Members: []runtimeRaidMemberState{
			{CharID: 19, GroupIndex: 1, SlotOrder: 0},
			{CharID: 20, GroupIndex: 1, SlotOrder: 1},
		},
	}
	service := &Service{
		options: options{
			currentClientVersion: "official-24.9",
			gameUpperHeader:      gameUpperHeaderChannel13,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
		gameSessions: map[uint16]*gameSession{
			leader.selectedCharacterID:    leader,
			peer.selectedCharacterID:      peer,
			bystander.selectedCharacterID: bystander,
		},
		raids: map[uint32]*runtimeRaidState{state.RaidKey: &state},
	}
	// Exercise the same rule used by the outgoing batch, rather than only
	// injecting a before-image rule while the actual broadcast lacks a PVF.
	loadCurrentAdmissionArchive(t, service)
	rule, err := service.currentRaidAdmissionRule(leader)
	if err != nil {
		t.Fatalf("load actual raid admission rule: %v", err)
	}
	if rule.ItemID != raidEntryCostInventoryRefreshTestItem || rule.Count != 1 {
		t.Fatalf("unexpected deployed admission rule: item=%d count=%d", rule.ItemID, rule.Count)
	}
	return &raidEntryCostInventoryRefreshFixture{
		service:       service,
		repos:         repos,
		leader:        leader,
		peer:          peer,
		bystander:     bystander,
		leaderConn:    leader.conn.(*bufferConn),
		peerConn:      peer.conn.(*bufferConn),
		bystanderConn: bystander.conn.(*bufferConn),
		state:         state,
		rule:          rule,
	}
}

func (f *raidEntryCostInventoryRefreshFixture) setMaterial(t *testing.T, characterID uint16, count int64) {
	t.Helper()
	slots := map[string]dnfrepo.ItemStack{}
	if count > 0 {
		slots["0:41"] = dnfrepo.ItemStack{ItemID: raidEntryCostInventoryRefreshTestItem, Count: count}
	}
	id := strconv.FormatUint(uint64(characterID), 10)
	if err := f.repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: id, Slots: slots}); err != nil {
		t.Fatal(err)
	}
}

func (f *raidEntryCostInventoryRefreshFixture) capture(t *testing.T) runtimeRaidEntryCostInventoryBaseline {
	t.Helper()
	before := f.service.captureRuntimeRaidEntryCostInventoryBeforeCommand(
		f.leader,
		uint16(dnfenum.CmdPacketMailboxExtractItem),
	)
	if !before.tracked {
		t.Fatal("committed inventory command did not create an entry-cost baseline")
	}
	if before.rule != f.rule {
		t.Fatalf("before-image did not use the runtime-PVF rule: got=%+v want=%+v", before.rule, f.rule)
	}
	return before
}

func (f *raidEntryCostInventoryRefreshFixture) assertOneEntryCostPacket(t *testing.T, conn *bufferConn) []byte {
	t.Helper()
	packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
	conn.write.Reset()
	if len(packets) != 1 || packets[0].Header.MsgID != 598 || packets[0].Header.Classification != 0 {
		t.Fatalf("entry-cost refresh packets=%+v, want one class0 NOTI598", packets)
	}
	return packets[0].Body
}

func (f *raidEntryCostInventoryRefreshFixture) assertNoPackets(t *testing.T) {
	t.Helper()
	for name, conn := range map[string]*bufferConn{"leader": f.leaderConn, "peer": f.peerConn, "bystander": f.bystanderConn} {
		if conn.write.Len() != 0 {
			t.Fatalf("%s received unexpected packet bytes %x", name, conn.write.Bytes())
		}
	}
}

func assertEntryCostMemberStatus(t *testing.T, body []byte, memberIndex int, characterID uint16, ready byte, owned uint16) {
	t.Helper()
	offset := 4 + memberIndex*5
	if len(body) < offset+5 {
		t.Fatalf("entry-cost body too short: %x", body)
	}
	if binary.LittleEndian.Uint16(body[offset:offset+2]) != characterID || body[offset+2] != ready || binary.LittleEndian.Uint16(body[offset+3:offset+5]) != owned {
		t.Fatalf("member %d status=%x, want char=%d ready=%d owned=%d", memberIndex, body[offset:offset+5], characterID, ready, owned)
	}
}

func TestRaidEntryCostInventoryRefreshBroadcastsBothSameRaidMembers(t *testing.T) {
	f := newRaidEntryCostInventoryRefreshFixture(t)
	before := f.capture(t)
	f.setMaterial(t, f.leader.selectedCharacterID, 1)
	if err := f.service.refreshRuntimeRaidEntryCostInventoryAfterCommand(f.leader, before); err != nil {
		t.Fatal(err)
	}
	leaderBody := f.assertOneEntryCostPacket(t, f.leaderConn)
	peerBody := f.assertOneEntryCostPacket(t, f.peerConn)
	if len(leaderBody) != 14 || binary.LittleEndian.Uint32(leaderBody[:4]) != 2 {
		t.Fatalf("entry-cost member count/body=%x", leaderBody)
	}
	if !bytes.Equal(leaderBody, peerBody) {
		t.Fatalf("same-raid members received different committed snapshots: leader=%x peer=%x", leaderBody, peerBody)
	}
	assertEntryCostMemberStatus(t, leaderBody, 0, 19, 1, 1)
	assertEntryCostMemberStatus(t, leaderBody, 1, 20, 0, 0)
	if f.bystanderConn.write.Len() != 0 {
		t.Fatal("non-member online session received NOTI598")
	}
	stored, _, err := f.repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if raidEntryCostOwnedCount(stored, raidEntryCostInventoryRefreshTestItem) != 1 {
		t.Fatal("read-only admission refresh changed the committed inventory")
	}
}

func TestRaidEntryCostInventoryRefreshBroadcastsConsumptionAndCountChanges(t *testing.T) {
	t.Run("count_increase_above_threshold", func(t *testing.T) {
		f := newRaidEntryCostInventoryRefreshFixture(t)
		f.setMaterial(t, f.leader.selectedCharacterID, 1)
		before := f.capture(t)
		f.setMaterial(t, f.leader.selectedCharacterID, 2)
		if err := f.service.refreshRuntimeRaidEntryCostInventoryAfterCommand(f.leader, before); err != nil {
			t.Fatal(err)
		}
		body := f.assertOneEntryCostPacket(t, f.leaderConn)
		f.assertOneEntryCostPacket(t, f.peerConn)
		assertEntryCostMemberStatus(t, body, 0, 19, 1, 2)
	})

	t.Run("consumption_crosses_threshold_back_to_zero", func(t *testing.T) {
		f := newRaidEntryCostInventoryRefreshFixture(t)
		f.setMaterial(t, f.leader.selectedCharacterID, 1)
		before := f.capture(t)
		f.setMaterial(t, f.leader.selectedCharacterID, 0)
		if err := f.service.refreshRuntimeRaidEntryCostInventoryAfterCommand(f.leader, before); err != nil {
			t.Fatal(err)
		}
		body := f.assertOneEntryCostPacket(t, f.leaderConn)
		f.assertOneEntryCostPacket(t, f.peerConn)
		assertEntryCostMemberStatus(t, body, 0, 19, 0, 0)
	})
}

func TestRaidEntryCostInventoryRefreshSkipsUnchangedAndExcludesNonMembers(t *testing.T) {
	f := newRaidEntryCostInventoryRefreshFixture(t)
	f.setMaterial(t, f.leader.selectedCharacterID, 1)
	before := f.capture(t)
	// Taking the before-image is read-only. Until the durable command and its
	// post-commit path both succeed, no member may receive NOTI598.
	f.assertNoPackets(t)
	// A committed inventory operation may touch unrelated rows; only a change
	// in the usable main-bag entry material may produce NOTI598.
	inv, _, err := f.repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	inv.Slots["0:42"] = dnfrepo.ItemStack{ItemID: 999999, Count: 3}
	if err := f.repos.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if err := f.service.refreshRuntimeRaidEntryCostInventoryAfterCommand(f.leader, before); err != nil {
		t.Fatal(err)
	}
	f.assertNoPackets(t)
	if !runtimeRaidEntryCostInventoryMutationCommand(uint16(dnfenum.CmdPacketMailboxExtractItem)) ||
		runtimeRaidEntryCostInventoryMutationCommand(uint16(dnfenum.CmdPacketChangeSkillslot)) {
		t.Fatal("central admission tracking did not follow the inventory/mail/package boundary")
	}
}

func TestRaidEntryCostInventoryRefreshRejectsAccountRoleAndStartedState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*raidEntryCostInventoryRefreshFixture)
	}{
		{name: "account_switch", mutate: func(f *raidEntryCostInventoryRefreshFixture) { f.leader.accountID = "foreign-account" }},
		{name: "role_switch", mutate: func(f *raidEntryCostInventoryRefreshFixture) {
			f.leader.selectedCharacterID = f.bystander.selectedCharacterID
		}},
		{name: "raid_started", mutate: func(f *raidEntryCostInventoryRefreshFixture) { f.service.raids[f.state.RaidKey].Started = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newRaidEntryCostInventoryRefreshFixture(t)
			before := f.capture(t)
			f.setMaterial(t, before.characterID, 1)
			test.mutate(f)
			if err := f.service.refreshRuntimeRaidEntryCostInventoryAfterCommand(f.leader, before); err != nil {
				t.Fatal(err)
			}
			f.assertNoPackets(t)
		})
	}
}

// Fail after the real mail owner has saved both aggregates into its transaction,
// before the memory UOW publishes either one. This exercises a rejected commit,
// rather than a malformed request that never tried to transfer the material.
type raidEntryCostInventoryMailboxCommitControl struct {
	dnfrepo.MailboxAssetUnitOfWork
	reject          bool
	stagedCount     int
	stagedInventory dnfrepo.InventoryRecord
	stagedMailbox   dnfrepo.MailboxRecord
}

func (u *raidEntryCostInventoryMailboxCommitControl) WithinMailboxAssets(ctx context.Context, assetID, mailboxID string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error) error {
	return u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, assetID, mailboxID, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
		if err := apply(c, i, m); err != nil {
			return err
		}
		inventory, inventoryFound, err := i.Load(ctx, assetID)
		if err != nil {
			return err
		}
		mailbox, mailboxFound, err := m.Load(ctx, mailboxID)
		if err != nil {
			return err
		}
		if !inventoryFound || !mailboxFound {
			return errors.New("entry-cost mail transaction did not stage its aggregates")
		}
		u.stagedCount++
		u.stagedInventory = dnfrepo.CloneInventory(inventory)
		u.stagedMailbox = dnfrepo.CloneMailbox(mailbox)
		if u.reject {
			return errors.New("injected entry-cost mail final commit failure")
		}
		return nil
	})
}

func TestRaidEntryCostInventoryRefreshMailboxCommandCommitsBeforeBothMembers598(t *testing.T) {
	f := newRaidEntryCostInventoryRefreshFixture(t)
	ctx := context.Background()
	// The independent legacy attachment-repair UOW must not consume the fault
	// injection. The op95 owner still resolves and grants the actual PVF item.
	t.Setenv(mailboxClaimTypeRepairEnv, mailboxClaimTypeRepairValueOff)
	resolver, err := f.service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil || resolver == nil {
		t.Fatalf("load runtime-PVF mailbox resolver: %v", err)
	}
	resolution, err := resolver(f.rule.ItemID)
	if err != nil || resolution.Kind != "stackable" || resolution.PVFPath == "" || resolution.TargetListType != 0 || resolution.AccountWarehouseSlot != 0 || resolution.EpicPieceID != 0 {
		t.Fatalf("raid material must resolve to a real character-bag attachment: resolution=%+v err=%v", resolution, err)
	}
	if err := f.repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "19",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {
				MailID:               "1",
				SenderCharacterID:    "0",
				RecipientCharacterID: "19",
				CreatedAt:            time.Now().UTC(),
				Read:                 true,
				Attachments:          []dnfrepo.MailAttachment{{ItemID: int64(f.rule.ItemID), Count: f.rule.Count}},
				Metadata:             map[string]string{"system_mail": "true"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	beforeInventory, found, err := f.repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load initial inventory: found=%t err=%v", found, err)
	}
	beforeMailbox, found, err := f.repos.Mailbox.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load initial mailbox: found=%t err=%v", found, err)
	}
	// Establish each member's already-open material table. No query or roster
	// reopen follows: only op95 goes through handleGameCommand below.
	for _, session := range []*gameSession{f.leader, f.peer} {
		if err := f.service.sendCurrentRuntimeRaidEntryCost(session, f.state); err != nil {
			t.Fatal(err)
		}
		body := f.assertOneEntryCostPacket(t, session.conn.(*bufferConn))
		assertEntryCostMemberStatus(t, body, 0, 19, 0, 0)
		assertEntryCostMemberStatus(t, body, 1, 20, 0, 0)
	}
	f.assertNoPackets(t)

	commit := &raidEntryCostInventoryMailboxCommitControl{MailboxAssetUnitOfWork: f.repos.MailboxAssets, reject: true}
	controlled := f.repos
	controlled.MailboxAssets = commit
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return controlled, true }
	if err := f.service.handleGameCommand(f.leader, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(1)); err != nil {
		t.Fatal(err)
	}
	if commit.stagedCount != 1 || raidEntryCostOwnedCount(commit.stagedInventory, int64(f.rule.ItemID)) != f.rule.Count || !commit.stagedMailbox.Mails["1"].Claimed {
		t.Fatalf("fault did not reach the real owner's final commit: count=%d inventory=%+v mailbox=%+v", commit.stagedCount, commit.stagedInventory, commit.stagedMailbox)
	}
	failedPackets := splitAllGameServerUpperPackets(t, f.leaderConn.write.Bytes())
	if len(failedPackets) != 1 || failedPackets[0].Header.MsgID != 95 || failedPackets[0].Header.Classification != 1 || len(failedPackets[0].Body) == 0 || failedPackets[0].Body[0] != 0 {
		t.Fatalf("failed commit must send only a failure ACK, without inventory or 598 refresh: %+v", failedPackets)
	}
	f.leaderConn.write.Reset()
	f.assertNoPackets(t)
	afterInventory, _, err := f.repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	afterMailbox, _, err := f.repos.Mailbox.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeInventory, afterInventory) || !reflect.DeepEqual(beforeMailbox, afterMailbox) {
		t.Fatal("failed mail commit published its staged material or changed the attachment")
	}

	// Retry the exact attachment through dispatch -> gameAlignedRegistry ->
	// mail owner -> successful UOW -> the central post-commit notification.
	commit.reject = false
	if err := f.service.handleGameCommand(f.leader, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(1)); err != nil {
		t.Fatal(err)
	}
	if commit.stagedCount != 2 {
		t.Fatalf("retry did not execute the mailbox owner transaction: count=%d", commit.stagedCount)
	}
	packets := splitAllGameServerUpperPackets(t, f.leaderConn.write.Bytes())
	f.leaderConn.write.Reset()
	if len(packets) != 3 || packets[0].Header.MsgID != 95 || packets[0].Header.Classification != 1 || len(packets[0].Body) != 17 || packets[0].Body[0] != 1 || binary.LittleEndian.Uint32(packets[0].Body[1:5]) != 1 ||
		packets[1].Header.MsgID != 14 || packets[1].Header.Classification != 0 || len(packets[1].Body) != 3+currentItemListEntryWireSize || packets[1].Body[0] != 0 || binary.LittleEndian.Uint16(packets[1].Body[1:3]) != 1 ||
		packets[2].Header.MsgID != 598 || packets[2].Header.Classification != 0 {
		t.Fatalf("mail claim must send ACK95 -> changed bag rows14 -> material598 without reopening: %+v", packets)
	}
	leaderBody := packets[2].Body
	peerBody := f.assertOneEntryCostPacket(t, f.peerConn)
	if !bytes.Equal(leaderBody, peerBody) {
		t.Fatalf("members received different committed mail-material counts: leader=%x peer=%x", leaderBody, peerBody)
	}
	assertEntryCostMemberStatus(t, leaderBody, 0, 19, 1, uint16(f.rule.Count))
	assertEntryCostMemberStatus(t, leaderBody, 1, 20, 0, 0)
	f.assertNoPackets(t)
	afterInventory, found, err = f.repos.Inventory.Load(ctx, "19")
	if err != nil || !found || raidEntryCostOwnedCount(afterInventory, int64(f.rule.ItemID)) != f.rule.Count {
		t.Fatalf("598 did not represent the durable mail grant: inventory=%+v found=%t err=%v", afterInventory, found, err)
	}
	slot := int16(binary.LittleEndian.Uint16(packets[1].Body[3:5]))
	stack := afterInventory.Slots["0:"+strconv.Itoa(int(slot))]
	if stack.ItemID != int64(f.rule.ItemID) || stack.Count != f.rule.Count {
		t.Fatalf("op14 did not name the committed material slot: slot=%d stack=%+v", slot, stack)
	}
	afterMailbox, found, err = f.repos.Mailbox.Load(ctx, "19")
	if err != nil || !found || !afterMailbox.Mails["1"].Claimed || len(afterMailbox.Mails["1"].Attachments) != 0 {
		t.Fatalf("mail grant was not durably settled: mailbox=%+v found=%t err=%v", afterMailbox, found, err)
	}
}
