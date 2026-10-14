package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Post-commit projection regression, developed against cf3943f.
// Existing rental_charge_wallet_test.go covers success, rejection and switches,
// but not a connection error after commit + successful op1000/op985 writes.
// This checks backend projection failure only, not native display acceptance or
// proof of the original incident's cause.
// All accounts/characters are synthetic memory records, no production access.
// The local real PVF supplies catalog values; the real-resource suite requires
// this root to pass rather than counting the ordinary suite's optional skip.
// Do not retry the charge handler: a second purchase is a new economic action.
// Set DNFBRIDGE_REAL_PVF_SMOKE to execute this real-resource regression.
func TestR4RentalWalletSendFailureAfterCommit(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to execute the real-resource regression")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCurrentRentalCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	const startGold int64 = 3_000_000
	const startPoints = 7
	const chargeCount = 3
	cost := int64(chargeCount) * catalog.GoldPerPoint
	if cost <= 0 || cost >= startGold || startPoints+chargeCount > int(catalog.Limit) {
		t.Fatalf("real PVF does not fit fixture: cost=%d limit=%d", cost, catalog.Limit)
	}
	wantGold := startGold - cost
	wantPoints := strconv.Itoa(startPoints + chargeCount)

	for _, partial := range []int{0, 7} {
		t.Run("wallet_prefix_bytes_"+strconv.Itoa(partial), func(t *testing.T) {
			t.Setenv(currentRentalRejectAckEnv, "")
			t.Setenv(currentRentalChargeWalletEnv, "")
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
				AccountID: "r4-send-failure", Metadata: map[string]string{currentRentalPointMetadataKey: strconv.Itoa(startPoints)},
			}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: "19", AccountID: "r4-send-failure", Level: 1, Stats: map[string]int64{"gold": startGold},
			}); err != nil {
				t.Fatal(err)
			}
			uow := &r4RentalSendFailureUOW{RentalAssetUnitOfWork: repos.RentalAssets}
			repos.RentalAssets = uow
			assertCommittedOnce := func() {
				t.Helper()
				if uow.calls != 1 || uow.commits != 1 {
					t.Fatalf("transaction calls=%d commits=%d want 1/1", uow.calls, uow.commits)
				}
				character, found, err := repos.Character.Load(ctx, "19")
				if err != nil || !found || character.Stats["gold"] != wantGold {
					t.Fatalf("committed character: found=%t gold=%d err=%v want=%d", found, character.Stats["gold"], err, wantGold)
				}
				account, found, err := repos.Account.Load(ctx, "r4-send-failure")
				if err != nil || !found || account.Metadata[currentRentalPointMetadataKey] != wantPoints {
					t.Fatalf("committed account: found=%t points=%q err=%v want=%s", found, account.Metadata[currentRentalPointMetadataKey], err, wantPoints)
				}
			}
			writeErr := errors.New("controlled op14 connection write failure after commit")
			conn := &r4RentalSendFailureConn{err: writeErr, partial: partial, beforeFailure: assertCommittedOnce}
			var events bytes.Buffer
			service := Service{
				logger:                  slog.New(slog.NewTextHandler(&events, nil)),
				options:                 options{accountID: "r4-send-failure", gameUpperBodyCodec: gameUpperBodyCodecPlain},
				initialEquipmentArchive: archive,
				dungeonMonsterTable:     monsters,
				repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
			}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			body := make([]byte, currentRentalRequestWireSize)
			binary.LittleEndian.PutUint32(body[13:17], 1)
			binary.LittleEndian.PutUint32(body[17:21], chargeCount)
			err := service.handleCurrentChargeRentalPoint(session, body)
			if !errors.Is(err, writeErr) {
				t.Fatalf("handler error=%v, want propagated connection error %v", err, writeErr)
			}
			assertCommittedOnce()
			if len(conn.attempts) != 3 {
				t.Fatalf("write attempts=%d want exactly ACK, state, failed wallet; no failure ACK/retry", len(conn.attempts))
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			wantAck := upperSuccessBody(buildCurrentRentalChargeSuccessBody(1, chargeCount, startPoints+chargeCount, false))
			if ack.Header.Classification != 1 || ack.Header.MsgID != uint16(dnfenum.CmdPacketChargeRentpoint) || !bytes.Equal(ack.Body, wantAck) {
				t.Fatalf("successful charge ACK mismatch: %+v body=%x", ack.Header, ack.Body)
			}
			state, rest := splitGameServerUpperPacket(t, rest)
			if state.Header.Classification != 0 || state.Header.MsgID != currentRentalStateMsgID || !bytes.Equal(state.Body, buildCurrentRentalPointStateBody(startPoints+chargeCount, nil)) {
				t.Fatalf("successful op985 mismatch: %+v body=%x", state.Header, state.Body)
			}
			wallet, tail := splitGameServerUpperPacket(t, conn.attempts[2])
			if len(tail) != 0 || wallet.Header.Classification != 0 || wallet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) || !bytes.Equal(wallet.Body, buildCurrentGoldStateBody(wantGold)) {
				t.Fatalf("failed write was not committed-gold op14: %+v body=%x", wallet.Header, wallet.Body)
			}
			if !bytes.Equal(rest, conn.attempts[2][:partial]) {
				t.Fatalf("accepted suffix=%x want only %d failed-wallet prefix bytes", rest, partial)
			}
			if strings.Contains(events.String(), "game-upper-rental-point-charge-transaction-blocked") || strings.Contains(events.String(), currentRentalFailureAckEvent) {
				t.Fatalf("post-commit send failure was mislabeled transaction rejection: %s", events.String())
			}

			// Normal read/projection on a fresh backend service/session, not a
			// second purchase and not a full login/client-display acceptance test.
			// No wallet value from the failed request is carried into this step.
			good := &bufferConn{}
			fresh := Service{
				options:            options{accountID: "r4-send-failure", gameUpperBodyCodec: gameUpperBodyCodecPlain},
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			}
			reselected := &gameSession{conn: good, selectedCharacterID: 19}
			_, id, reloaded, err := fresh.currentRentalSelectedCharacter(ctx, reselected)
			if err != nil || id != "19" || reloaded.Stats["gold"] != wantGold {
				t.Fatalf("fresh load id=%q gold=%d err=%v", id, reloaded.Stats["gold"], err)
			}
			if err := fresh.sendSelectedRentalWalletStateWithRefresh(reselected, "r4_post_write_failure_reload", true); err != nil {
				t.Fatal(err)
			}
			if err := fresh.publishCurrentRentalChargeWallet(reselected, reloaded.Stats["gold"]); err != nil {
				t.Fatal(err)
			}
			state, rest = splitGameServerUpperPacket(t, good.write.Bytes())
			if state.Header.Classification != 0 || state.Header.MsgID != currentRentalStateMsgID || !bytes.Equal(state.Body, buildCurrentRentalPointStateBody(startPoints+chargeCount, nil)) {
				t.Fatalf("fresh state not committed points: %+v body=%x", state.Header, state.Body)
			}
			wallet, rest = splitGameServerUpperPacket(t, rest)
			if wallet.Header.Classification != 0 || wallet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) || !bytes.Equal(wallet.Body, buildCurrentGoldStateBody(wantGold)) || len(rest) != 0 {
				t.Fatalf("fresh projection not committed gold: %+v body=%x extra=%x", wallet.Header, wallet.Body, rest)
			}
			assertCommittedOnce()
		})
	}
}

type r4RentalSendFailureUOW struct {
	dnfrepo.RentalAssetUnitOfWork
	calls   int
	commits int
}

func (u *r4RentalSendFailureUOW) WithinRentalAssets(ctx context.Context, accountID, characterID string, apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	u.calls++
	err := u.RentalAssetUnitOfWork.WithinRentalAssets(ctx, accountID, characterID, apply)
	if err == nil {
		u.commits++
	}
	return err
}

type r4RentalSendFailureConn struct {
	bufferConn
	attempts      [][]byte
	err           error
	partial       int
	beforeFailure func()
}

func (c *r4RentalSendFailureConn) Write(data []byte) (int, error) {
	c.attempts = append(c.attempts, append([]byte(nil), data...))
	if len(c.attempts) == 3 {
		c.beforeFailure()
		_, _ = c.write.Write(data[:c.partial])
		return c.partial, c.err
	}
	return c.write.Write(data)
}
