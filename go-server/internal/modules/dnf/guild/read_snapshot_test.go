package guild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	r "longheng.io/server/internal/modules/dnf/repository"
	"sync"
	"testing"
	"time"
)

type snapshotSettingsFixture struct {
	mu        sync.Mutex
	record    r.SettingsRecord
	namespace *r.GuildSnapshotNamespace
	loads     int
}

func (s *snapshotSettingsFixture) GuildSnapshotNamespace() *r.GuildSnapshotNamespace {
	return s.namespace
}
func (s *snapshotSettingsFixture) LoadGuildSnapshotRevision(ctx context.Context) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha256.Sum256([]byte(s.record.Values["state"]))
	return hex.EncodeToString(h[:]), true, nil
}
func (s *snapshotSettingsFixture) Load(ctx context.Context, scope string) (r.SettingsRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return r.CloneSettings(s.record), true, nil
}
func (s *snapshotSettingsFixture) Save(ctx context.Context, record r.SettingsRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record = r.CloneSettings(record)
	return nil
}
func newSnapshotSettingsFixture(t *testing.T) (*snapshotSettingsFixture, Registry, r.GuildOwner, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	reg := NewRegistry()
	owner := r.GuildOwner{AccountID: "a", CharacterID: "1"}
	g, err := reg.Create(r.CharacterRecord{CharacterID: "1", AccountID: "a", Name: "member", Level: 90, Stats: map[string]int64{"server_group_id": 1}}, "Guild", now)
	if err != nil {
		t.Fatal(err)
	}
	g.Benefits = map[byte]Benefit{1: {Type: 1, Value: 10, ExpiresAt: now.Add(time.Hour)}}
	store := &snapshotSettingsFixture{namespace: &r.GuildSnapshotNamespace{}}
	if err = Save(context.Background(), store, reg, now); err != nil {
		t.Fatal(err)
	}
	return store, reg, owner, now
}
func TestGuildReadSnapshotHitIsolationAndSameTimestampInvalidation(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		v, ok, err := ReadGuildEntitlement(ctx, store, owner)
		if err != nil || !ok || v.ID != 1 {
			t.Fatal(v, ok, err)
		}
		delete(v.Benefits, 1)
	}
	if store.loads != 1 {
		t.Fatalf("same content loaded %d times", store.loads)
	}
	display, err := ReadGuildDisplay(ctx, store, owner)
	if err != nil {
		t.Fatal(err)
	}
	delete(display.Members, "1")
	delete(display.Benefits, 1)
	v, ok, err := ReadGuildEntitlement(ctx, store, owner)
	if err != nil || !ok || len(v.Benefits) != 1 {
		t.Fatal("mutable display poisoned cache", v, ok, err)
	}
	if _, ok, err = ReadGuildEntitlement(ctx, store, r.GuildOwner{AccountID: "foreign", CharacterID: "1"}); err != nil || ok {
		t.Fatal("foreign owner admitted", ok, err)
	}
	reg.Guilds[1].Members["1"] = Member{CharacterID: "1", AccountID: "changed", Name: "member", Grade: 1, Level: 90, ServerGroup: 1, JoinedAt: now}
	encoded, _ := json.Marshal(reg)
	store.mu.Lock()
	store.record.Values["state"] = string(encoded)
	store.mu.Unlock()
	if _, ok, err = ReadGuildEntitlement(ctx, store, owner); err != nil || ok {
		t.Fatal("equal timestamp hid changed ownership", ok, err)
	}
	if store.loads != 2 {
		t.Fatalf("changed source loads=%d", store.loads)
	}
}
func TestGuildReadSnapshotCorruptionCannotUsePriorValue(t *testing.T) {
	store, _, owner, _ := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	if _, _, err := ReadGuildEntitlement(ctx, store, owner); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.record.Values["state"] = "{invalid"
	store.mu.Unlock()
	if _, _, err := ReadGuildEntitlement(ctx, store, owner); err == nil {
		t.Fatal("corruption accepted cached entitlement")
	}
}
func TestGuildReadSnapshotConcurrentMissLoadsOnce(t *testing.T) {
	store, _, owner, _ := newSnapshotSettingsFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, err := ReadGuildEntitlement(context.Background(), store, owner); err != nil || !ok {
				t.Errorf("entitlement %v %v", ok, err)
			}
		}()
	}
	wg.Wait()
	if store.loads != 1 {
		t.Fatalf("concurrent duplicate loads=%d", store.loads)
	}
}
