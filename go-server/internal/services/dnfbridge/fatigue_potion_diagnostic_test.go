package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/fatigue"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const fatigueDiagnosticSecret = "PRIVATE_ACCOUNT_TOKEN_ERROR_MARKER"

var fatigueDiagnosticWriteError = errors.New(fatigueDiagnosticSecret)

type fatigueDiagnosticConn struct {
	bufferConn
	writes, failAt, shortAt int
}

func (c *fatigueDiagnosticConn) Write(p []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return 0, fatigueDiagnosticWriteError
	}
	if c.writes == c.shortAt {
		return c.bufferConn.Write(p[:len(p)-1])
	}
	return c.bufferConn.Write(p)
}

type fatigueDiagnosticFailCharacter struct{ repo.CharacterRepository }

func (fatigueDiagnosticFailCharacter) Save(context.Context, repo.CharacterRecord) error {
	return fatigueDiagnosticWriteError
}

type fatigueDiagnosticAssets struct {
	repo.CharacterAssetUnitOfWork
	calls      int
	fail       bool
	failCommit bool
}

func (u *fatigueDiagnosticAssets) WithinCharacterAssets(ctx context.Context, id string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	u.calls++
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if u.fail {
			c = fatigueDiagnosticFailCharacter{c}
		}
		if err := apply(c, i, e); err != nil {
			return err
		}
		if u.failCommit {
			return fatigueDiagnosticWriteError
		}
		return nil
	})
}

// Only the preflight read is stale. The transaction's real memory repository
// still owns Count4; diagnostics must derive before=Remaining+1, never 99.
type fatigueDiagnosticStaleInventory struct {
	repo.InventoryRepository
	reads int
}

type fatigueDiagnosticRefreshFailure struct {
	repo.InventoryRepository
	reads int
}

func (i *fatigueDiagnosticRefreshFailure) Load(ctx context.Context, id string) (repo.InventoryRecord, bool, error) {
	i.reads++
	if i.reads == 2 {
		return repo.InventoryRecord{}, false, fatigueDiagnosticWriteError
	}
	return i.InventoryRepository.Load(ctx, id)
}

func (i *fatigueDiagnosticStaleInventory) Load(ctx context.Context, id string) (repo.InventoryRecord, bool, error) {
	b, ok, err := i.InventoryRepository.Load(ctx, id)
	i.reads++
	if i.reads == 1 && ok && err == nil {
		b = repo.CloneInventory(b)
		x := b.Slots["0:87"]
		x.Count = 99
		b.Slots["0:87"] = x
	}
	return b, ok, err
}

type fatigueDiagnosticFixture struct {
	s      *Service
	p      *gameSession
	conn   *fatigueDiagnosticConn
	assets *fatigueDiagnosticAssets
	repos  repo.Group
	logs   bytes.Buffer
}

func fatigueDiagnosticArchive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real fatigue potion handler")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func newFatigueDiagnosticFixture(t *testing.T, a *platformpvf.Archive, count int64) *fatigueDiagnosticFixture {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	r := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	account := repo.AccountRecord{AccountID: fatigueDiagnosticSecret}
	character := repo.CharacterRecord{CharacterID: "17", AccountID: account.AccountID, Level: 90, Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 156}}
	fatigue.Refresh(&character, account, now)
	ctx := context.Background()
	if err = r.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err = r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw, 87)
	binary.LittleEndian.PutUint32(raw[2:], 10005025)
	binary.LittleEndian.PutUint32(raw[6:], uint32(count))
	bag := repo.InventoryRecord{CharacterID: "17", Slots: map[string]repo.ItemStack{"0:87": {ItemID: 10005025, Count: count, RawEntry: raw, Extra: map[string]string{"item_kind": "stackable"}}}}
	if err = r.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	u := &fatigueDiagnosticAssets{CharacterAssetUnitOfWork: r.CharacterAssets}
	r.CharacterAssets = u
	f := &fatigueDiagnosticFixture{repos: r, assets: u, conn: &fatigueDiagnosticConn{}}
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = now
	f.s = &Service{logger: slog.New(slog.NewJSONHandler(&f.logs, nil)), options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: a, pvfItemCatalog: catalog, gameplayTimers: q, repositoryProvider: func() (repo.Group, bool) { return f.repos, true }}
	f.p = &gameSession{conn: f.conn, selectedCharacterID: 17, accountID: account.AccountID, connID: fatigueDiagnosticSecret, channel: channelcatalog.Channel{ID: 42}}
	if f.s.packetLog != nil {
		t.Fatal("test must exercise packet logging disabled")
	}
	return f
}

func (f *fatigueDiagnosticFixture) use() (bool, error) {
	body := make([]byte, 19)
	binary.LittleEndian.PutUint16(body, 87)
	binary.LittleEndian.PutUint32(body[7:], 58)
	return f.s.handleAlignedGameCommand(f.p, 1, 515, body)
}

func (f *fatigueDiagnosticFixture) records(t *testing.T) []map[string]any {
	t.Helper()
	if strings.Contains(f.logs.String(), fatigueDiagnosticSecret) {
		t.Fatalf("application logger disclosed private account/connection/error: %s", f.logs.String())
	}
	var records []map[string]any
	for _, line := range bytes.Split(f.logs.Bytes(), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r["kind"] == "game-fatigue-potion-committed" {
			records = append(records, r)
		}
	}
	return records
}

func (f *fatigueDiagnosticFixture) assertCommitted(t *testing.T, before int64, writes [3]string) {
	t.Helper()
	records := f.records(t)
	if len(records) != 1 {
		t.Fatalf("committed application records=%d, want exactly1 with packetLog=nil; logs=%s", len(records), f.logs.String())
	}
	want := map[string]any{"kind": "game-fatigue-potion-committed", "char_id": float64(17), "channel_id": float64(42), "slot": float64(87), "item_id": float64(10005025), "action": float64(58), "committed": true, "before": float64(before), "after": float64(before - 1), "ack_call_returned_ok": writes[0] == "returned_ok", "inventory_refresh_call_returned_ok": writes[1] == "returned_ok", "fatigue_call_returned_ok": writes[2] == "returned_ok"}
	stage := "owner_committed"
	for i, outcome := range writes {
		if outcome != "not_attempted" {
			stage = []string{"ack_call_", "inventory_refresh_call_", "fatigue_call_"}[i] + outcome
		}
	}
	want["stage"] = stage
	r := records[0]
	delete(r, "time")
	delete(r, "level")
	delete(r, "msg")
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("unexpected typed fields or write progress: got=%v want=%v", r, want)
	}
	b, _, err := f.repos.Inventory.Load(context.Background(), "17")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := f.repos.Character.Load(context.Background(), "17")
	if err != nil {
		t.Fatal(err)
	}
	if f.assets.calls != 1 || b.Slots["0:87"].Count != before-1 || c.Stats["fatigue"] != 30 || c.Stats["fatigue_potion_used_10005025"] != 1 {
		t.Fatalf("owner must commit exactly one debit, calls=%d bag=%+v stats=%v", f.assets.calls, b, c.Stats)
	}
}

func TestFatiguePotionDiagnosticCommitted(t *testing.T) {
	a := fatigueDiagnosticArchive(t)
	for _, count := range []int64{1, 4} {
		t.Run(fmt.Sprintf("count%d", count), func(t *testing.T) {
			f := newFatigueDiagnosticFixture(t, a, count)
			handled, err := f.use()
			if !handled || err != nil {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			// Assert the existing ACK/op14/fatigue bytes and order, not just logging.
			ack, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
			if ack.Header.MsgID != 515 || !bytes.Equal(ack.Body, []byte{1, 87, 0, 0, 58, 0, 0, 0}) {
				t.Fatal("success ACK changed")
			}
			row, rest := splitGameServerUpperPacket(t, rest)
			if row.Header.MsgID != 14 || len(row.Body) != 122 || int64(binary.LittleEndian.Uint32(row.Body[9:])) != count-1 {
				t.Fatalf("inventory quantity changed: %+v %x", row.Header, row.Body)
			}
			expectedID := uint32(10005025)
			if count == 1 {
				expectedID = ^uint32(0)
			}
			if binary.LittleEndian.Uint32(row.Body[5:]) != expectedID {
				t.Fatal("inventory deletion/item ID changed")
			}
			fp, rest := splitGameServerUpperPacket(t, rest)
			if fp.Header.MsgID != currentFatigueMsgID || len(rest) != 0 || f.conn.writes != 3 {
				t.Fatal("fatigue response/order or write count changed")
			}
			f.assertCommitted(t, count, [3]string{"returned_ok", "returned_ok", "returned_ok"})
		})
	}
	t.Run("transaction_result_not_preflight_count", func(t *testing.T) {
		f := newFatigueDiagnosticFixture(t, a, 4)
		f.repos.Inventory = &fatigueDiagnosticStaleInventory{InventoryRepository: f.repos.Inventory}
		if handled, err := f.use(); !handled || err != nil {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
		f.assertCommitted(t, 4, [3]string{"returned_ok", "returned_ok", "returned_ok"})
	})
}

func TestFatiguePotionDiagnosticWriteFailures(t *testing.T) {
	a := fatigueDiagnosticArchive(t)
	for _, count := range []int64{1, 4} {
		for stage := 1; stage <= 3; stage++ {
			t.Run(fmt.Sprintf("count%d_write%d", count, stage), func(t *testing.T) {
				f := newFatigueDiagnosticFixture(t, a, count)
				f.conn.failAt = stage
				handled, err := f.use()
				if !handled || !errors.Is(err, fatigueDiagnosticWriteError) || f.conn.writes != stage {
					t.Fatalf("write failure return/stop changed: handled=%t err=%v writes=%d", handled, err, f.conn.writes)
				}
				want := [3]string{"not_attempted", "not_attempted", "not_attempted"}
				for i := 0; i < stage-1; i++ {
					want[i] = "returned_ok"
				}
				want[stage-1] = "failed"
				f.assertCommitted(t, count, want)
			})
		}
	}
}

func TestFatiguePotionDiagnosticNoSuccessOnRollback(t *testing.T) {
	a := fatigueDiagnosticArchive(t)
	for _, commitFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_owner_callback_%t", commitFailure), func(t *testing.T) {
			f := newFatigueDiagnosticFixture(t, a, 4)
			f.assets.fail = !commitFailure      // inventory staged, then character Save fails.
			f.assets.failCommit = commitFailure // owner populated result, then rollback.
			beforeC, _, _ := f.repos.Character.Load(context.Background(), "17")
			beforeB, _, _ := f.repos.Inventory.Load(context.Background(), "17")
			handled, err := f.use()
			if !handled || err != nil {
				t.Fatalf("rejection return changed: handled=%t err=%v", handled, err)
			}
			afterC, _, _ := f.repos.Character.Load(context.Background(), "17")
			afterB, _, _ := f.repos.Inventory.Load(context.Background(), "17")
			ack, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
			if ack.Header.MsgID != 515 || !bytes.Equal(ack.Body, []byte{0, 1, 87, 0, 0, 58}) || len(rest) != 0 {
				t.Fatal("rollback rejection response changed")
			}
			if f.assets.calls != 1 || !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeB, afterB) || len(f.records(t)) != 0 {
				t.Fatal("failed transaction changed assets or emitted committed success")
			}
		})
	}
}

func TestFatiguePotionDiagnosticInventoryRefreshCallFailure(t *testing.T) {
	f := newFatigueDiagnosticFixture(t, fatigueDiagnosticArchive(t), 4)
	f.repos.Inventory = &fatigueDiagnosticRefreshFailure{InventoryRepository: f.repos.Inventory}
	handled, err := f.use()
	if !handled || !errors.Is(err, fatigueDiagnosticWriteError) || f.conn.writes != 1 {
		t.Fatalf("post-commit refresh failure return changed: %t %v writes%d", handled, err, f.conn.writes)
	}
	f.assertCommitted(t, 4, [3]string{"returned_ok", "failed", "not_attempted"})
}

func TestFatiguePotionDiagnosticShortWrite(t *testing.T) {
	a := fatigueDiagnosticArchive(t)
	for stage := 1; stage <= 3; stage++ {
		t.Run(fmt.Sprint(stage), func(t *testing.T) {
			f := newFatigueDiagnosticFixture(t, a, 4)
			f.conn.shortAt = stage
			handled, err := f.use()
			if !handled || err != nil || f.conn.writes != 3 {
				t.Fatalf("existing short-write return/continuation changed: %t %v writes%d", handled, err, f.conn.writes)
			}
			want := [3]string{"returned_ok", "returned_ok", "returned_ok"}
			// Existing short write returns nil: this is a call-result log, not delivery.
			f.assertCommitted(t, 4, want)
		})
	}
}

func TestFatiguePotionDiagnosticSuppressed(t *testing.T) {
	a := fatigueDiagnosticArchive(t)
	for _, spec := range []string{"1:515", "0:14", fmt.Sprintf("0:%d", currentFatigueMsgID)} {
		t.Run(spec, func(t *testing.T) {
			t.Setenv(upperSendFilterEnv, spec)
			resetUpperSendFilterCache()
			t.Cleanup(resetUpperSendFilterCache)
			f := newFatigueDiagnosticFixture(t, a, 4)
			handled, err := f.use()
			if !handled || err != nil || f.conn.writes != 2 {
				t.Fatalf("suppression return changed: %t %v writes%d", handled, err, f.conn.writes)
			}
			want := [3]string{"returned_ok", "returned_ok", "returned_ok"}
			// Suppression returns nil, so all three service calls returned OK.
			f.assertCommitted(t, 4, want)
		})
	}
}
