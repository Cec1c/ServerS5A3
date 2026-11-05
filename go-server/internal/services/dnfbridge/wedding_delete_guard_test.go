package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingDeleteFixture(t *testing.T) (*Service, *gameSession, *bufferConn, wedding.Owner) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	for i, id := range []string{"1", "2"} {
		account, job := "dnf:10001", "0"
		if i == 1 {
			account, job = "dnf:10002", "11"
		}
		if err := g.Account.Save(ctx, r.AccountRecord{AccountID: account}); err != nil {
			t.Fatal(err)
		}
		if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: account, Name: "wedding-delete" + id, Job: job, Level: 30, Stats: map[string]int64{"gold": 900000}}); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		if err := g.Equipment.Save(ctx, r.EquipmentRecord{CharacterID: id, Entries: map[string]r.EquipmentEntry{}}); err != nil {
			t.Fatal(err)
		}
	}
	s, peer, conn := newGameDeleteCharacterService(g, true)
	return s, peer, conn, wedding.Owner{Repositories: g, Rules: &wedding.Rules{MinimumLevel: 30}}
}

func TestWeddingDeleteNonSingleUsesNativeMarriageErrorAndNoMutation(t *testing.T) {
	for _, phase := range []byte{wedding.Engaged, wedding.Reserved, wedding.InCeremony, wedding.Married} {
		t.Run(string(rune('0'+phase)), func(t *testing.T) {
			s, peer, conn, owner := weddingDeleteFixture(t)
			ctx := context.Background()
			c, _, _ := owner.Repositories.Character.Load(ctx, "1")
			c.Stats["wedding_v1_phase"], c.Stats["wedding_v1_partner"] = int64(phase), 2
			c.Stats["wedding_v1_ceremony_channel"], c.Stats["wedding_v1_ceremony_area"] = 10, 8
			if err := r.SaveCharacterFields(ctx, owner.Repositories.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
				t.Fatal(err)
			}
			before, _, _ := owner.Repositories.Character.Load(ctx, "1")
			if err := s.handleGameDeleteCharacter(peer, gameDeleteCharacterBody(0, c.Name)); err != nil {
				t.Fatal(err)
			}
			ack := gameDeleteCharacterReply(t, conn)
			if !bytes.Equal(ack.Body, []byte{0x24}) {
				t.Fatalf("native marriage error %x", ack.Body)
			}
			after, _, _ := owner.Repositories.Character.Load(ctx, "1")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected deletion mutated relationship")
			}
		})
	}
}

func TestWeddingDeleteFreshTransactionSerializesWithEngageInBothOrders(t *testing.T) {
	for _, engagementFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "engagement-first", false: "deletion-first"}[engagementFirst], func(t *testing.T) {
			s, _, _, owner := weddingDeleteFixture(t)
			ctx := context.Background()
			stale, _, _ := owner.Repositories.Character.Load(ctx, "1")
			req := wedding.PairRequest{Actor: wedding.Actor{AccountID: "dnf:10001", CharacterID: "1"}, Partner: wedding.Actor{AccountID: "dnf:10002", CharacterID: "2"}, Now: time.Date(2026, 11, 3, 18, 0, 0, 0, time.UTC)}
			if engagementFirst {
				if _, err := owner.Engage(ctx, req); err != nil {
					t.Fatal(err)
				}
				if _, _, err := s.softDeleteGameCharacter(ctx, owner.Repositories, stale); !errors.Is(err, errGameDeleteCharacterMarriage) {
					t.Fatalf("stale Single bypassed lock: %v", err)
				}
			} else {
				if _, _, err := s.softDeleteGameCharacter(ctx, owner.Repositories, stale); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.Engage(ctx, req); !errors.Is(err, wedding.ErrOwnership) {
					t.Fatalf("deleted actor engaged: %v", err)
				}
			}
			c, _, _ := owner.Repositories.Character.Load(ctx, "1")
			d, _, _ := owner.Repositories.Character.Load(ctx, "2")
			state, err := wedding.Read(c)
			other, otherErr := wedding.Read(d)
			if err != nil || otherErr != nil {
				t.Fatal(err, otherErr)
			}
			if engagementFirst && (c.Stats["delete_flag"] != 0 || state.Phase != wedding.Engaged || other.Phase != wedding.Engaged) {
				t.Fatal("engagement not protected")
			}
			if !engagementFirst && (c.Stats["delete_flag"] != 1 || c.Slot != -1 || state.Phase != wedding.Single || other.Phase != wedding.Single) {
				t.Fatal("deleted actor or spouse unexpectedly rebound")
			}
		})
	}
}

type weddingDeleteAbortAssets struct{ r.CharacterAssetUnitOfWork }

func (u weddingDeleteAbortAssets) WithinCharacterAssets(ctx context.Context, id string, apply func(r.CharacterRepository, r.InventoryRepository, r.EquipmentRepository) error) error {
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c r.CharacterRepository, i r.InventoryRepository, e r.EquipmentRepository) error {
		if err := apply(c, i, e); err != nil {
			return err
		}
		return errors.New("injected delete rollback")
	})
}

func TestWeddingDeleteTransactionFailureRollsBackAndSingleCooldownCanDelete(t *testing.T) {
	s, peer, conn, owner := weddingDeleteFixture(t)
	ctx := context.Background()
	c, _, _ := owner.Repositories.Character.Load(ctx, "1")
	c.Stats["wedding_v1_cooldown_until"] = time.Now().Add(time.Hour).Unix()
	if err := r.SaveCharacterFields(ctx, owner.Repositories.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	before, _, _ := owner.Repositories.Character.Load(ctx, "1")
	g := owner.Repositories
	g.CharacterAssets = weddingDeleteAbortAssets{g.CharacterAssets}
	s.repositoryProvider = func() (r.Group, bool) { return g, true }
	if err := s.handleGameDeleteCharacter(peer, gameDeleteCharacterBody(0, c.Name)); err != nil {
		t.Fatal(err)
	}
	if ack := gameDeleteCharacterReply(t, conn); !bytes.Equal(ack.Body, []byte{characterDeleteAckBlocked}) {
		t.Fatalf("rollback ack %x", ack.Body)
	}
	after, _, _ := owner.Repositories.Character.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed delete left tombstone")
	}
	s.repositoryProvider = func() (r.Group, bool) { return owner.Repositories, true }
	conn.write.Reset()
	if err := s.handleGameDeleteCharacter(peer, gameDeleteCharacterBody(0, c.Name)); err != nil {
		t.Fatal(err)
	}
	if ack := gameDeleteCharacterReply(t, conn); !bytes.Equal(ack.Body, characterDeleteSuccessAckBody(0)) {
		t.Fatalf("Single cooldown must not block deletion %x", ack.Body)
	}
}
