package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The hook runs only after the underlying transaction has completed. It changes
// explicit memory-fixture registration between body components, never live assets.
type reviewOwnedSerializationUOW struct {
	repo.CeraShopAssetUnitOfWork
	owned repo.BuffOwnedSnapshotUnitOfWork
	calls int
	fail  error
	after func()
}

func (u *reviewOwnedSerializationUOW) WithinBuffOwnedSnapshot(ctx context.Context, account, character, scope string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository, repo.BuffPetMetadataReader, repo.OwnedLegacyInventoryRepository) error) error {
	u.calls++
	if u.fail != nil {
		return u.fail
	}
	if err := u.owned.WithinBuffOwnedSnapshot(ctx, account, character, scope, apply); err != nil {
		return err
	}
	if after := u.after; after != nil {
		u.after = nil
		after()
	}
	return nil
}

func reviewOwnedSerializationSeed(t *testing.T) (repo.Group, *Service, *gameSession, *reviewOwnedSerializationUOW) {
	t.Helper()
	t.Setenv(buffSwapReapplyEnv, "on")
	base := newSkillSwitchInventoryRepos(t)
	s, session, _, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, base)
	ctx := context.Background()
	owner, found, err := base.Character.Load(ctx, "1")
	if err != nil || !found {
		t.Fatal("owner fixture missing", found, err)
	}
	owner.Name = "scope-old"
	if err := base.Character.Save(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := base.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{
		"0:54": {ItemID: 101040493, Count: 1},
		"0:55": {ItemID: 100350891, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := base.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := base.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1", Skills: map[int64]repo.SkillState{38: {Level: 10}, 39: {Level: 11}}}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.ApplyCurrent(ctx, base, "1", s.accountIDForSession(session), buffswap.Command{Skill: 38, Slot: 12, List: 0, Index: 54}); err != nil {
		t.Fatal(err)
	}
	owned, ok := base.CeraShopAssets.(repo.BuffOwnedSnapshotUnitOfWork)
	if !ok {
		t.Fatal("fixture lacks owned snapshot capability")
	}
	u := &reviewOwnedSerializationUOW{CeraShopAssetUnitOfWork: base.CeraShopAssets, owned: owned}
	wrapped := base
	wrapped.CeraShopAssets = u
	s.repositoryProvider = func() (repo.Group, bool) { return wrapped, true }
	return base, s, session, u
}

func reviewOwnedSerializationMutate(t *testing.T, base repo.Group, s *Service, session *gameSession) {
	t.Helper()
	ctx := context.Background()
	owner, found, err := base.Character.Load(ctx, "1")
	if err != nil || !found {
		t.Fatal("owner fixture missing during mutation", found, err)
	}
	owner.Name = "scope-new"
	if err := base.Character.Save(ctx, owner); err != nil {
		t.Fatal(err)
	}
	// Use the unwrapped repository so the observation hook cannot recurse.
	if err := buffswap.ApplyCurrent(ctx, base, "1", s.accountIDForSession(session), buffswap.Command{Skill: 39, Slot: 12, List: 0, Index: 55}); err != nil {
		t.Fatal(err)
	}
}

func reviewOwnedSerializationText(t *testing.T, body []byte) string {
	t.Helper()
	if len(body) < 16 {
		t.Fatalf("missing text footer: %x", body)
	}
	footer := body[len(body)-16:]
	length := int(binary.LittleEndian.Uint32(footer[12:]))
	if !bytes.Equal(footer[:8], []byte(peerBuffViewMagic)) || binary.LittleEndian.Uint32(footer[8:12]) != 1 || length <= 0 || length > len(body)-16 {
		t.Fatalf("invalid text footer: %x", footer)
	}
	return string(body[len(body)-16-length : len(body)-16])
}

func reviewOwnedSerializationAssertText(t *testing.T, body []byte, newer bool) {
	t.Helper()
	name, level := "scope-old", "基础 Lv10"
	if newer {
		name, level = "scope-new", "基础 Lv11"
	}
	text := reviewOwnedSerializationText(t, body)
	if !strings.Contains(text, name+" 的 Buff 登记") || !strings.Contains(text, level) || !strings.Contains(text, "已登记 1 件装备") {
		t.Fatalf("snapshot text disagrees: %q", text)
	}
}

func reviewOwnedSerializationAssertItem(t *testing.T, body []byte, newer bool) {
	t.Helper()
	prefix, item := []byte{38, 0, 1, 12, 0, 54, 0}, uint32(101040493)
	if newer {
		prefix, item = []byte{39, 0, 1, 12, 0, 55, 0}, 100350891
	}
	if len(body) != 126 || !bytes.Equal(body[:7], prefix) || binary.LittleEndian.Uint32(body[9:13]) != item {
		t.Fatalf("snapshot item disagrees: %x", body)
	}
}

func reviewOwnedSerializationWrite(reader *csharpLegacyUserInfoReader, kind string) []byte {
	var w packetWriter
	switch kind {
	case "items":
		reader.writeNativeBuffSwapUserInfo(&w)
	case "skill":
		reader.writeNativePeerBuffSkillPage(&w)
	case "text":
		reader.writePeerBuffViewText(&w, 1)
	}
	return w.bytes()
}

func reviewOwnedSerializationAssert(t *testing.T, kind string, body []byte, newer bool) {
	t.Helper()
	switch kind {
	case "items":
		reviewOwnedSerializationAssertItem(t, body, newer)
	case "skill":
		want := []byte{255, 1, 38, 0, 10, 1, 38, 0, 10}
		if newer {
			want = []byte{255, 1, 39, 0, 11, 1, 39, 0, 11}
		}
		if !bytes.Equal(body, want) {
			t.Fatalf("snapshot skill disagrees: got=%x want=%x", body, want)
		}
	case "text":
		reviewOwnedSerializationAssertText(t, body, newer)
	}
}

func reviewOwnedSerializationAssertCleared(t *testing.T, reader *csharpLegacyUserInfoReader) {
	t.Helper()
	if reader.buffOwnedDepth != 0 || reader.buffOwnedLoaded || reader.buffOwnedErr != nil || !reflect.DeepEqual(reader.buffOwned, buffswap.OwnedProjection{}) {
		t.Fatal("completed serialization retained detached cache/error")
	}
}

func TestReviewBuffOwnedSerializationCarrierSnapshotConsistent(t *testing.T) {
	base, s, session, u := reviewOwnedSerializationSeed(t)
	mutated := false
	u.after = func() {
		mutated = true
		reviewOwnedSerializationMutate(t, base, s, session)
	}
	// A literal public-prefix fixture isolates serialization, not GUI routing.
	detail := make([]byte, 15+8+currentMode1StatBlobWireSize+1)
	detail[0] = 3
	body := s.buildNativePeerBuffCarrier(context.Background(), session, 1, detail)
	offset := currentMode1CreateRowsOffset + 4
	if !mutated || u.calls != 1 || len(body) < offset+126+8+9 {
		t.Fatalf("carrier did not share one snapshot: mutated=%v reads=%d bytes=%d", mutated, u.calls, len(body))
	}
	reviewOwnedSerializationAssertItem(t, body[offset:offset+126], false)
	reviewOwnedSerializationAssert(t, "skill", body[offset+126+8:offset+126+8+9], false)
	reviewOwnedSerializationAssertText(t, body, false)
	state, found, err := buffswap.Load(context.Background(), base, "1", s.accountIDForSession(session))
	if err != nil || !found || state.Skill != 39 || state.Slots[12].Index != 55 {
		t.Fatal("mid-serialization mutation did not actually persist in fixture", state, found, err)
	}
	body = s.buildNativePeerBuffCarrier(context.Background(), session, 1, detail)
	if len(body) < offset+126+8+9 {
		t.Fatalf("next carrier truncated: %d", len(body))
	}
	if u.calls != 2 {
		t.Fatalf("next carrier reused previous snapshot: reads=%d", u.calls)
	}
	reviewOwnedSerializationAssertItem(t, body[offset:offset+126], true)
	reviewOwnedSerializationAssert(t, "skill", body[offset+126+8:offset+126+8+9], true)
	reviewOwnedSerializationAssertText(t, body, true)
}

func TestReviewBuffOwnedSerializationNextWriteRefresh(t *testing.T) {
	for _, kind := range []string{"items", "skill", "text"} {
		t.Run(kind, func(t *testing.T) {
			base, s, session, u := reviewOwnedSerializationSeed(t)
			reader := csharpLegacyUserInfoReader{ctx: context.Background(), service: s, session: session, characterID: "1"}
			reviewOwnedSerializationAssert(t, kind, reviewOwnedSerializationWrite(&reader, kind), false)
			reviewOwnedSerializationAssertCleared(t, &reader)
			reviewOwnedSerializationMutate(t, base, s, session)
			reviewOwnedSerializationAssert(t, kind, reviewOwnedSerializationWrite(&reader, kind), true)
			reviewOwnedSerializationAssertCleared(t, &reader)
			if u.calls != 2 {
				t.Fatalf("new writer did not load fresh snapshot: reads=%d", u.calls)
			}
		})
	}
}

func TestReviewBuffOwnedSerializationErrorRecovery(t *testing.T) {
	for _, kind := range []string{"items", "skill", "text"} {
		t.Run(kind, func(t *testing.T) {
			_, s, session, u := reviewOwnedSerializationSeed(t)
			reader := csharpLegacyUserInfoReader{ctx: context.Background(), service: s, session: session, characterID: "1"}
			u.fail = errors.New("explicit snapshot fixture failure")
			body := reviewOwnedSerializationWrite(&reader, kind)
			want := []byte(nil)
			if kind == "items" {
				want = []byte{0, 0, 0}
			} else if kind == "skill" {
				want = []byte{255, 0, 0}
			}
			if !bytes.Equal(body, want) || u.calls != 1 {
				t.Fatalf("snapshot error fallback wrong: kind=%s reads=%d bytes=%x", kind, u.calls, body)
			}
			reviewOwnedSerializationAssertCleared(t, &reader)
			u.fail = nil
			reviewOwnedSerializationAssert(t, kind, reviewOwnedSerializationWrite(&reader, kind), false)
			reviewOwnedSerializationAssertCleared(t, &reader)
			if u.calls != 2 {
				t.Fatalf("prior error poisoned later serialization: reads=%d", u.calls)
			}
		})
	}
}
