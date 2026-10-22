package dnfbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type antonCardFailLateAssets struct {
	repo.AccountCharacterAssetUnitOfWork
}

func (u antonCardFailLateAssets) WithinAccountCharacterAssets(ctx context.Context, account, character string, apply func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character, func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := apply(a, c, i, e); err != nil {
			return err
		}
		return errors.New("injected failure after card assets and receipt saves")
	})
}

func ownedAntonCardFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, runtimeRaidState) {
	t.Helper()
	f, session, state, now := onlineAntonTerminalFixture(t)
	r195RewardParty(t, f.service, 19, 20)
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	return f, session, state
}

func dispatchAntonCardRequest(t *testing.T, f *raidStartGuardFixture, session *gameSession, op uint16, body []byte) {
	t.Helper()
	frame, err := dnfproto.BuildChannelPacket(op, body, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.handleGameUpper(session, frame); err != nil {
		t.Fatal(err)
	}
}

func antonGrantedItemCount(t *testing.T, f *raidStartGuardFixture, id string, item uint32) int64 {
	t.Helper()
	inventory, _, err := f.repos.Inventory.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == int64(item) {
			n += stack.Count
		}
	}
	return n
}

func antonNativeCardBodies(t *testing.T, f *raidStartGuardFixture) map[byte][]byte {
	t.Helper()
	stream := append([]byte(nil), f.conn.write.Bytes()...)
	bodies := map[byte][]byte{}
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		if p.Header.MsgID == 600 {
			bodies[p.Body[0]] = append([]byte(nil), p.Body...)
		}
		stream = rest
	}
	return bodies
}

func writeAntonNativeCardFixture(t *testing.T, name string, body []byte) {
	t.Helper()
	if dir := os.Getenv("DNF90_ANTON_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRealPVFAntonOnlineCardsActualCMTSequenceGrantsOnlyOnceAndUsesOwnedPhase(t *testing.T) {
	f, session, _ := ownedAntonCardFixture(t)
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 2})
	if antonGrantedItemCount(t, f, "19", 10304529) != 0 || len(f.state.Battle.RewardCards[1]) != 0 {
		t.Fatal("card accepted before actual movie completion")
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	masks := antonNativeCardBodies(t, f)
	if len(masks) != 2 || masks[1][1] != 2 || masks[0][1] != 2 {
		t.Fatal("natural completion did not prepare real eligible party cards", masks)
	}
	writeAntonNativeCardFixture(t, "go-anton-card-party-mask.bin", masks[1])
	writeAntonNativeCardFixture(t, "go-anton-card-gold-mask.bin", masks[0])
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	p, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if p.Header.MsgID != 659 || len(rest) != 0 {
		t.Fatal("duplicate movie completion replayed screen")
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 2})
	if antonGrantedItemCount(t, f, "19", 10304529) != 45 || !f.state.Battle.RewardCards[1][19].Committed {
		t.Fatal("actual phase1 reward did not persist")
	}
	reveal := antonNativeCardBodies(t, f)[1]
	want, _ := raid.BuildRewardListBody(1, []raid.RewardDisplayCard{{CharacterID: 19, CardType: 2, Items: []raid.RewardDisplayItem{{ItemID: 10304529, Quantity: 45}}}})
	if !reflect.DeepEqual(reveal, want) {
		t.Fatal("reveal disagrees with committed actual reward", reveal, want)
	}
	writeAntonNativeCardFixture(t, "go-anton-card-party-reveal.bin", reveal)
	before, _, _ := f.repos.Inventory.Load(context.Background(), "19")
	// Exercise a repeat after a wall-clock second changes, rather than only
	// immediate replays whose metadata serialization could conceal a write.
	time.Sleep(1100 * time.Millisecond)
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{0, 2})
	empty := antonNativeCardBodies(t, f)[0]
	if len(empty) != 6 || empty[5] != 0 {
		t.Fatal("absent gold pool fabricated an item", empty)
	}
	writeAntonNativeCardFixture(t, "go-anton-card-gold-empty.bin", empty)
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 3})
	dispatchAntonCardRequest(t, f, session, 660, []byte{0, 3})
	dispatchAntonCardRequest(t, f, session, 660, []byte{2, 3})
	after, _, _ := f.repos.Inventory.Load(context.Background(), "19")
	// Existing container projection refreshes the ticket's remaining use-period
	// at bytes11..12 from its unchanged absolute expiry, and persists UpdatedAt.
	// Compare every asset and every other raw byte, allowing only that clock view.
	before = repo.CloneInventory(before)
	after = repo.CloneInventory(after)
	before.UpdatedAt, after.UpdatedAt = time.Time{}, time.Time{}
	for _, inventory := range []*repo.InventoryRecord{&before, &after} {
		ticket := inventory.Slots["0:50"]
		if len(ticket.RawEntry) != currentItemListEntryWireSize {
			t.Fatal("ticket wire metadata missing")
		}
		ticket.RawEntry[11], ticket.RawEntry[12] = 0, 0
		inventory.Slots["0:50"] = ticket
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 45 || !reflect.DeepEqual(before, after) || f.state.Battle.RewardCards[1][19].Card != 2 {
		t.Fatalf("repeat/gold/PC changed frozen grant: count=%d card=%d before=%+v after=%+v", antonGrantedItemCount(t, f, "19", 10304529), f.state.Battle.RewardCards[1][19].Card, before, after)
	}
	if antonGrantedItemCount(t, f, "19", 10096296) != 3 {
		t.Fatal("card consumed another entrance ticket")
	}
}

func TestRealPVFAntonOnlineCardCollisionReservesUniqueCardAndFreezesRNGAcrossFailure(t *testing.T) {
	f, session, state := ownedAntonCardFixture(t)
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	catalog, _ := actualAntonRewardFixture(t)
	calls := 0
	roll := func(int64) (int64, error) { calls++; return 0, nil }
	card, command, err := f.service.freezeCurrentAntonPartyCard(session, state, 2, catalog, roll)
	if err != nil || calls == 0 || card.Card != 2 {
		t.Fatal(card, command, err, calls)
	}
	firstCalls := calls
	before, _, _ := f.repos.Inventory.Load(context.Background(), "19")
	original := f.repos.AccountAssets
	f.repos.AccountAssets = antonCardFailLateAssets{original}
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 2})
	after, _, _ := f.repos.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) || f.state.Battle.RewardCards[1][19].Committed {
		t.Fatal("late card transaction failure leaked committed assets")
	}
	f.repos.AccountAssets = original
	again, cmd, err := f.service.freezeCurrentAntonPartyCard(session, state, 3, catalog, roll)
	if err != nil || calls != firstCalls || again.Plan.Fingerprint != card.Plan.Fingerprint || again.Card != 2 || cmd.Card != 2 {
		t.Fatal("reselection rerolled the outcome", again, err, calls)
	}
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 3})
	if !f.state.Battle.RewardCards[1][19].Committed || antonGrantedItemCount(t, f, "19", 10304529) != 45 {
		t.Fatal("frozen card retry did not commit original result")
	}
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.loginPacketSeen, peer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(peer, 20)
	if err := f.service.sendCurrentAntonTerminalResult(peer, state); err != nil {
		t.Fatal(err)
	}
	dispatchAntonCardRequest(t, f, peer, 659, []byte{0})
	other, _, err := f.service.freezeCurrentAntonPartyCard(peer, state, 2, catalog, roll)
	if err != nil || other.Card != 0 {
		t.Fatal("two same-party players claimed same card", other, err)
	}
	copy := cloneRuntimeRaidState(*f.state)
	copy.Battle.RewardCards[1][19].Card = 3
	copy.Battle.RewardCards[1][19].Plan.Rewards[0].Count++
	if f.state.Battle.RewardCards[1][19].Card != 2 || f.state.Battle.RewardCards[1][19].Plan.Rewards[0].Count != 45 {
		t.Fatal("card plan clone shared mutable data")
	}
}

func TestRealPVFAntonOnlineCardsFirstPhaseUsesThirtyAndDoesNotCountWeekly(t *testing.T) {
	catalog, rewards := actualAntonRewardFixture(t)
	archive := catalog.source.(*platformpvf.Archive)
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, err := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 2)
	f.service.options.currentClientVersion = "official-24.9"
	session, peer := f.session(), f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.loginPacketSeen, peer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	f.service.bindGameSessionCharacter(peer, 20)
	for i := range f.state.Members {
		f.state.Members[i].GroupIndex = 1
	}
	start := time.Now().Add(-20 * time.Second)
	progress, err := program.BeginPhase(0, start, 91321)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Started = true
	f.state.Battle = &runtimeRaidBattle{InstanceID: "112233445566778899aabbccddeeff00", Mode: 1, ChannelID: session.channel.ID, ChannelType: session.channel.Type, StartedAt: start, EnterAfter: start, Deadline: start.Add(time.Duration(config.PhaseLimitSeconds[0]) * time.Second), Configuration: config, Anton: progress, AntonRewards: &rewards, Dungeons: progress.Dungeons(), MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20"}}
	for n, id := range []uint32{210, 210, 210, 210, 213, 213, 215, 215} {
		leaderRun, peerRun := raidCompletedRuntimeFixture(t, session, id), raidCompletedRuntimeFixture(t, peer, id)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, leaderRun, []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: peerRun}}); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, leaderRun, start.Add(time.Duration(n+1)*time.Second)); err != nil || !changed {
			t.Fatal(n, changed, err)
		}
	}
	state := cloneRuntimeRaidState(*f.state)
	if !state.Battle.PhaseCompleted || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("first phase invented final entitlement")
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	p, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if p.Header.MsgID != 601 || p.Body[1] != 0 {
		t.Fatal("first-phase result wrong", p)
	}
	p, rest = splitGameServerUpperPacket(t, rest)
	if p.Header.MsgID != 587 || p.Body[0] != 3 || p.Body[1] != 0 || len(rest) != 0 {
		t.Fatal("first-phase stage wrong", p)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	if antonGrantedItemCount(t, f, "19", 10304529) != 30 || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("first-phase reward/counter wrong")
	}
	writeAntonNativeCardFixture(t, "go-anton-card-party-first-phase.bin", antonNativeCardBodies(t, f)[1])
}

func TestRealPVFAntonOnlineCardCommittedBeforePartialProjectionRejoinsWithoutDuplicate(t *testing.T) {
	f, session, state := ownedAntonCardFixture(t)
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	conn := &antonPartialWriteConn{Conn: session.conn}
	session.conn = conn
	if err := f.service.handleCurrentAntonRewardCard(session, 660, []byte{1, 0}); err == nil || !conn.closed || !session.raidProjection.failed {
		t.Fatal("partial reveal/ACK batch remained usable", err)
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 45 || !f.state.Battle.RewardCards[1][19].Committed {
		t.Fatal("accepted asset transaction lost on transport failure")
	}
	f.conn = &bufferConn{}
	replacement := f.session()
	replacement.loginPacketSeen, replacement.accountFromToken = true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, replacement, 659, []byte{0})
	restored := antonNativeCardBodies(t, f)[1]
	want, _ := raid.BuildRewardListBody(1, []raid.RewardDisplayCard{{CharacterID: 19, CardType: 0, Items: []raid.RewardDisplayItem{{ItemID: 10304529, Quantity: 45}}}, {CharacterID: 20, CardType: 2}})
	if !reflect.DeepEqual(restored, want) {
		t.Fatal("rejoin lost committed reveal", restored, want)
	}
	dispatchAntonCardRequest(t, f, replacement, 660, []byte{1, 3})
	if antonGrantedItemCount(t, f, "19", 10304529) != 45 || f.state.Battle.RewardCards[1][19].Card != 0 {
		t.Fatal("new connection rerolled/regave committed card")
	}
}

func TestRealPVFAntonOnlineCardsObserverFinishesMovieWithoutAssetsAndRejectsForeignContext(t *testing.T) {
	f, _, state := ownedAntonCardFixture(t)
	observer := f.session()
	observer.selectedCharacterID, observer.accountID = 21, "account-21"
	observer.loginPacketSeen, observer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(observer, 21)
	if err := f.service.sendCurrentAntonTerminalResult(observer, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, observer, 659, []byte{0})
	if len(antonNativeCardBodies(t, f)) != 0 {
		t.Fatal("observer received eligible player's cards")
	}
	dispatchAntonCardRequest(t, f, observer, 660, []byte{1, 0})
	if len(f.state.Battle.RewardCards[1]) != 0 || antonGrantedItemCount(t, f, "21", 10304529) != 0 {
		t.Fatal("observer received a grant")
	}
	for _, name := range []string{"no_result", "no_movie", "uncommitted_final", "login", "account", "channel", "single", "unfinished", "timeout", "phase", "group", "removed", "malformed"} {
		t.Run(name, func(t *testing.T) {
			g, session, _ := ownedAntonCardFixture(t)
			dispatchAntonCardRequest(t, g, session, 659, []byte{0})
			body := []byte{1, 0}
			b := g.state.Battle
			switch name {
			case "no_result":
				session.raidProjection.resultSent = false
			case "no_movie":
				b.RewardCardReady[1] = nil
			case "uncommitted_final":
				g.service.raidWeeklyIntents[b.InstanceID].Committed = false
			case "login":
				session.loginPacketSeen = false
			case "account":
				session.accountID = "foreign"
			case "channel":
				session.channel.ID++
			case "single":
				b.Single = true
			case "unfinished":
				b.PhaseCompleted = false
			case "timeout":
				b.TimedOut = true
			case "phase":
				b.Phase = 0
			case "group":
				g.state.Members[0].GroupIndex = 0
			case "removed":
				g.state.Members = g.state.Members[1:]
			case "malformed":
				body = []byte{1, 0, 0}
			}
			dispatchAntonCardRequest(t, g, session, 660, body)
			if len(b.RewardCards[1]) != 0 || antonGrantedItemCount(t, g, "19", 10304529) != 0 {
				t.Fatal("foreign/incomplete card request granted assets", name)
			}
		})
	}
}
