package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingPeerNonMarriedAnimationIndexNeverUsesSentinel(t *testing.T) {
	for phase := wedding.Single; phase < wedding.Married; phase++ {
		for ring := 0; ring <= 255; ring++ {
			body := buildCurrentWeddingPeerInfo(7, 8, phase, byte(ring), 1000)
			if len(body) != 14 || binary.LittleEndian.Uint32(body) != 7 || binary.LittleEndian.Uint32(body[4:]) != 8 ||
				body[8] != phase || body[9] != 0 || binary.LittleEndian.Uint32(body[10:]) != 1000 {
				t.Fatalf("phase=%d ring=%d unsafe or altered peer body %x", phase, ring, body)
			}
		}
	}
	for ring := byte(0); ring <= 5; ring++ {
		body := buildCurrentWeddingPeerInfo(7, 8, wedding.Married, ring, 1000)
		if body[8] != wedding.Married || body[9] != ring {
			t.Fatalf("married ring %d changed: %x", ring, body)
		}
	}
}

func TestWeddingPeerStoredStagesUseSafeNonMarriedAndValidatedMarriedRing(t *testing.T) {
	s, _, owner, req := weddingCancellationProjectionFixture(t)
	ctx := context.Background()
	if _, err := owner.Engage(ctx, req); err != nil {
		t.Fatal(err)
	}
	s.wedding.rules = &wedding.Rules{Rings: make([]wedding.Ring, 6)}
	for phase := wedding.Engaged; phase <= wedding.Married; phase++ {
		for _, ring := range []int64{0, 1, 2, 3, 4, 5, 255} {
			t.Run(fmt.Sprintf("phase=%d/ring=%d", phase, ring), func(t *testing.T) {
				for _, id := range []string{"1", "2"} {
					c, _, err := owner.Repositories.Character.Load(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					c.Stats["wedding_v1_phase"] = int64(phase)
					c.Stats["wedding_v1_ring"] = ring
					c.Stats["wedding_v1_ceremony_channel"] = 10
					c.Stats["wedding_v1_ceremony_area"] = 8
					if err := r.SaveCharacterFields(ctx, owner.Repositories.Character, c, r.CharacterFieldWedding); err != nil {
						t.Fatal(err)
					}
				}
				c, _, _ := owner.Repositories.Character.Load(ctx, "1")
				body, err := s.currentWeddingPeerInfo(ctx, c)
				if phase == wedding.Married && ring == 255 {
					if err == nil || len(body) != 0 {
						t.Fatalf("invalid married stage bypassed rules: %x %v", body, err)
					}
					return
				}
				want := byte(0)
				if phase == wedding.Married {
					want = byte(ring)
				}
				if err != nil || len(body) != 14 || body[8] != phase || body[9] != want {
					t.Fatalf("projected ring body=%x want=%d err=%v", body, want, err)
				}
			})
		}
	}
}

func TestWeddingCancellationSinglePeerUsesZeroWhileOwnInfoKeepsSentinel(t *testing.T) {
	s, peers, owner, _ := weddingCancellationProjectionFixture(t)
	actor, _ := s.boundGameSessionCharacterSnapshot(peers[0])
	if err := s.publishCurrentWeddingCancellationPeerState(actor, s.currentWeddingPeerReceivers(actor)); err != nil {
		t.Fatal(err)
	}
	ops, bodies := cargoReplies(t, peers[1])
	if len(ops) != 1 || ops[0] != 1013 || len(bodies[0]) != 14 || bodies[0][8] != wedding.Single || bodies[0][9] != 0 || binary.LittleEndian.Uint32(bodies[0][4:]) != 0 {
		t.Fatalf("unsafe single peer projection %v %x", ops, bodies)
	}
	c, _, _ := owner.Repositories.Character.Load(context.Background(), "1")
	if err := s.sendCurrentWeddingInfoRecord(peers[0], c); err != nil {
		t.Fatal(err)
	}
	ops, bodies = cargoReplies(t, peers[0])
	if len(ops) != 1 || ops[0] != 1000 || len(bodies[0]) != 15 || bodies[0][4] != wedding.Single || bodies[0][14] != 255 {
		t.Fatalf("own single Info1000 sentinel changed %v %x", ops, bodies)
	}
}

func TestWeddingReengagementPublishesSafePeerAndUnchangedOwnState(t *testing.T) {
	s, peers, owner, req := weddingCancellationProjectionFixture(t)
	result, err := owner.Engage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.publishCurrentWeddingPair(result); err != nil {
		t.Fatal(err)
	}
	for _, peer := range peers[:2] {
		ops, bodies := cargoReplies(t, peer)
		own, other, profile := 0, 0, 0
		var receiverToken uint32
		for i, op := range ops {
			switch op {
			case 1000:
				own++
				if len(bodies[i]) != 15 || bodies[i][4] != wedding.Engaged || bodies[i][14] != 255 {
					t.Fatalf("own engagement state changed %x", bodies[i])
				}
				receiverToken = binary.LittleEndian.Uint32(bodies[i])
			case 1035:
				profile++
				if own != 1 || len(bodies[i]) != 98 || binary.LittleEndian.Uint32(bodies[i][5:]) != receiverToken {
					t.Fatalf("partner profile did not follow matching own state %x", bodies[i])
				}
			case 1013:
				other++
				if len(bodies[i]) != 14 || bodies[i][8] != wedding.Engaged || bodies[i][9] != 0 {
					t.Fatalf("unsafe engagement peer animation %x", bodies[i])
				}
			default:
				t.Fatalf("unexpected engagement projection %d", op)
			}
		}
		if own != 1 || other != 1 || profile != 1 {
			t.Fatalf("owned/peer projection count %v", ops)
		}
	}
}
