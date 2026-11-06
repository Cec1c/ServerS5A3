package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingDivorceFixture(t *testing.T) (*Service, []*gameSession, r.Group) {
	t.Helper()
	s, peers, repos := weddingCancelNPCFixture(t)
	ctx := context.Background()
	for _, peer := range peers {
		c, _, _ := repos.Character.Load(ctx, fmt.Sprint(peer.selectedCharacterID))
		c.Stats["wedding_v1_phase"] = int64(wedding.Married)
		c.Stats["wedding_v1_married_at"] = s.gameplayNow().Unix() - 100
		c.Stats["wedding_v1_love_points"] = 400
		if e := r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
			t.Fatal(e)
		}
	}
	return s, peers, repos
}

func TestWeddingDivorceNativeUnilateralOfflinePartnerAndReplay(t *testing.T) {
	s, peers, repos := weddingDivorceFixture(t)
	ctx := context.Background()
	delete(s.gameSessions, peers[1].selectedCharacterID)
	request := currentWeddingRequest{Operation: 6}
	if e := s.handleCurrentWeddingDivorceRequest(peers[0], request); e != nil {
		t.Fatal(e)
	}
	ops, bodies := cargoReplies(t, peers[0])
	notice, info := -1, -1
	if len(ops) == 0 || ops[0] != 1032 || binary.LittleEndian.Uint32(bodies[0][1:]) != 0 {
		t.Fatalf("ack %v %x", ops, bodies)
	}
	for i, op := range ops {
		if op == 1002 {
			notice = i
			if !reflect.DeepEqual(bodies[i], []byte{6}) {
				t.Fatal("wrong divorce operation")
			}
		}
		if op == 1000 {
			info = i
		}
	}
	if notice < 0 || info <= notice {
		t.Fatalf("clear ordering %v", ops)
	}
	for i, peer := range peers {
		c, _, _ := repos.Character.Load(ctx, fmt.Sprint(peer.selectedCharacterID))
		state, e := wedding.Read(c)
		if e != nil || state.Phase != wedding.Single || state.LovePoints != 0 {
			t.Fatalf("relationship %+v %v", state, e)
		}
		if c.Stats["gold"] != []int64{400000, 1000000}[i] {
			t.Fatal("wrong unilateral payer")
		}
	}
	before := captureWeddingCancellationState(t, repos, peers)
	if e := s.handleCurrentWeddingDivorceRequest(peers[0], request); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, captureWeddingCancellationState(t, repos, peers)) {
		t.Fatal("repeat charged")
	}
}

func TestWeddingDivorceNativeMutualRefusalThenConsent(t *testing.T) {
	s, peers, repos := weddingDivorceFixture(t)
	request := currentWeddingRequest{Operation: 7, Target: uint32(peers[1].selectedCharacterID)}
	before := captureWeddingCancellationState(t, repos, peers)
	if e := s.handleCurrentWeddingDivorceRequest(peers[0], request); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, captureWeddingCancellationState(t, repos, peers)) {
		t.Fatal("invitation mutated assets")
	}
	response := currentWeddingResponse{currentWeddingRequest: currentWeddingRequest{Operation: 7, Target: 0}, Answer: 1}
	if e := s.handleCurrentWeddingDivorceResponse(peers[1], response); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, captureWeddingCancellationState(t, repos, peers)) {
		t.Fatal("refusal mutated assets")
	}
	if e := s.handleCurrentWeddingDivorceRequest(peers[0], request); e != nil {
		t.Fatal(e)
	}
	response.Target, response.Answer = uint32(peers[0].selectedCharacterID), 0
	if e := s.handleCurrentWeddingDivorceResponse(peers[1], response); e != nil {
		t.Fatal(e)
	}
	for _, peer := range peers {
		c, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		state, e := wedding.Read(c)
		if e != nil || state.Phase != wedding.Single {
			t.Fatalf("mutual %+v %v", state, e)
		}
	}
}

func TestWeddingDivorceMutualFailedConsentNotifiesBothWithoutAssets(t *testing.T) {
	s, peers, repos := weddingDivorceFixture(t)
	ctx := context.Background()
	for _, p := range peers {
		c, _, _ := repos.Character.Load(ctx, fmt.Sprint(p.selectedCharacterID))
		c.Stats["gold"] = 0
		if e := r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldStats); e != nil {
			t.Fatal(e)
		}
	}
	before := captureWeddingCancellationState(t, repos, peers)
	req := currentWeddingRequest{Operation: 7, Target: uint32(peers[1].selectedCharacterID)}
	if e := s.handleCurrentWeddingDivorceRequest(peers[0], req); e != nil {
		t.Fatal(e)
	}
	for _, p := range peers {
		cargoReplies(t, p)
	}
	answer := currentWeddingResponse{currentWeddingRequest: currentWeddingRequest{Operation: 7, Target: uint32(peers[0].selectedCharacterID)}, Answer: 0}
	if e := s.handleCurrentWeddingDivorceResponse(peers[1], answer); e != nil {
		t.Fatal(e)
	}
	for i, p := range peers {
		ops, bodies := cargoReplies(t, p)
		op, offset := uint16(998), 0
		if i == 1 {
			op, offset = 1033, 1
		}
		if len(ops) != 1 || ops[0] != op || int32(binary.LittleEndian.Uint32(bodies[0][offset:])) != -7 {
			t.Fatalf("failed mutual divorce party%d: %v %x", i, ops, bodies)
		}
	}
	if len(s.wedding.invitations) != 0 || !reflect.DeepEqual(before, captureWeddingCancellationState(t, repos, peers)) {
		t.Fatal("failed divorce consumed assets or retained pending request")
	}
}
