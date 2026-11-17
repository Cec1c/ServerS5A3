package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"strconv"
	"strings"
	"testing"
	"time"
)

func weddingGenderIngressFixture(t *testing.T, female bool) *weddingChurchLoadingFixture {
	t.Helper()
	f := newWeddingChurchLoadingFixture(t)
	f.s.wedding.rules.GenderPolicy = wedding.GenderPolicyDenyMaleMale
	f.s.wedding.rules.Durations = []int64{300}
	f.s.wedding.rules.QuestIDs = []int64{20737, 20738}
	f.s.wedding.guestRules = &wedding.GuestRules{MinimumGold: 10000, MaximumGold: 400000000, Charge: 50, Capacity: map[byte]int{0: 10}}
	source := cargoQuerySource()
	source["map/outside.map"] = "[npc]\n20013 `[left]` 670 330 0\n"
	source["stackable/stackable.lst"] += "\n10002629 `weddinggift.stk`\n"
	source["stackable/weddinggift.stk"] = "[stackable type]\n`[booster]` 0\n[stack limit]\n1\n"
	source["map/church/church.map"] = "[virtual movable area]\n2 233 642 198 650 287 68 89\n"
	var err error
	f.s.pvfItemCatalog, err = newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"29", "30"} {
		c, _, _ := f.repos.Character.Load(context.Background(), id)
		for key := range c.Stats {
			if strings.HasPrefix(key, "wedding_") {
				delete(c.Stats, key)
			}
		}
		c.Job = "0"
		if female {
			c.Job = "11"
		}
		c.Name = "bride" + id
		if err = r.SaveCharacterFields(context.Background(), f.repos.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
			t.Fatal(err)
		}
		if err = f.repos.Quest.Save(context.Background(), r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed"}, 20738: {Status: "completed"}}}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func weddingGenderRequest(t *testing.T, f *weddingChurchLoadingFixture, operation byte) {
	t.Helper()
	body := make([]byte, 20)
	binary.LittleEndian.PutUint32(body[13:], 30)
	body[17] = operation
	if err := f.s.handleCurrentWeddingRequest(f.first, body); err != nil {
		t.Fatal(err)
	}
}

func weddingGenderAccept(t *testing.T, f *weddingChurchLoadingFixture, operation byte) {
	t.Helper()
	body := make([]byte, 21)
	binary.LittleEndian.PutUint32(body[13:], 29)
	body[17] = operation
	if err := f.s.handleCurrentWeddingResponse(f.second, body); err != nil {
		t.Fatal(err)
	}
}

func TestWeddingGenderMaleMaleRejectedBeforeInvite(t *testing.T) {
	f := weddingGenderIngressFixture(t, false)
	weddingGenderRequest(t, f, 1)
	frames := weddingChurchLoadingFrames(t, f.first)
	if len(frames) != 2 || frames[0].op != 1032 || frames[0].body[0] != 0 || !bytes.Contains(frames[1].body, rosterNameBytes("抱歉，当前仅允许男女或女女角色订婚。")) {
		t.Fatalf("missing explicit rejection: %+v", frames)
	}
	if len(f.s.wedding.invitations) != 0 || len(weddingChurchLoadingFrames(t, f.second)) != 0 {
		t.Fatal("male rejection created an invitation or target notification")
	}
	for _, id := range []string{"29", "30"} {
		c, _, _ := f.repos.Character.Load(context.Background(), id)
		state, err := wedding.Read(c)
		if err != nil || state.Phase != wedding.Single {
			t.Fatal(state, err)
		}
	}
}

func TestWeddingGenderFemaleConsentReservationChurchAndRewards(t *testing.T) {
	f := weddingGenderIngressFixture(t, true)
	ctx := context.Background()
	weddingGenderRequest(t, f, 1)
	found := false
	for _, frame := range weddingChurchLoadingFrames(t, f.second) {
		found = found || frame.op == 997
	}
	if !found || len(f.s.wedding.invitations) != 1 {
		t.Fatal("female invitation missing")
	}
	weddingChurchLoadingFrames(t, f.first)
	weddingGenderAccept(t, f, 1)
	for _, id := range []string{"29", "30"} {
		c, _, _ := f.repos.Character.Load(ctx, id)
		state, err := wedding.Read(c)
		if err != nil || state.Phase != wedding.Engaged {
			t.Fatal(state, err)
		}
	}
	weddingChurchLoadingFrames(t, f.first)
	weddingChurchLoadingFrames(t, f.second)
	weddingGenderRequest(t, f, 2)
	weddingChurchLoadingFrames(t, f.first)
	found = false
	for _, frame := range weddingChurchLoadingFrames(t, f.second) {
		found = found || frame.op == 997
	}
	if !found || len(f.s.wedding.invitations) != 1 {
		t.Fatal("female reservation invitation missing")
	}
	weddingGenderAccept(t, f, 2)
	entryTokens := make(map[*gameSession]uint32)
	for _, p := range []*gameSession{f.first, f.second} {
		found = false
		frames := weddingChurchLoadingFrames(t, p)
		for _, frame := range frames {
			if frame.op == 1004 {
				found = true
				entryTokens[p] = binary.LittleEndian.Uint32(frame.body)
				if string(bytes.TrimRight(frame.body[17:47], "\x00")) != "bride29" || string(bytes.TrimRight(frame.body[47:77], "\x00")) != "bride30" {
					t.Fatal("reservation names reversed")
				}
			}
		}
		if !found {
			t.Fatalf("female reservation notice missing: %+v", frames)
		}
	}
	for index, p := range []*gameSession{f.first, f.second} {
		body := make([]byte, 22)
		binary.LittleEndian.PutUint32(body[13:], entryTokens[p])
		binary.LittleEndian.PutUint32(body[17:], 8)
		if err := f.s.handleCurrentWeddingCeremonyEntry(p, body); err != nil {
			t.Fatal(err)
		}
		if ack := guildReply(t, p, 1039); !bytes.Equal(ack, weddingResultACK(0)) {
			t.Fatalf("female Church entry rejected: %x", ack)
		}
		presence, entered := f.s.onlinePlayers.PlayerForCharacter(p.selectedCharacterID)
		if !entered || presence.Session != p || presence.TownID != 39 || presence.AreaID != 8 || presence.Instance != weddingPairInstance(29, 30) {
			t.Fatalf("female Church presence: %+v", presence)
		}
		weddingChurchLoadingFrames(t, p)
		frames := weddingChurchLoadingFinish(t, f.s, p)
		c, _, _ := f.repos.Character.Load(ctx, strconv.Itoa(int(p.selectedCharacterID)))
		position := f.s.wedding.rules.ForceMove[index]
		if c.Stats["pos_x"] != int64(position.X) || c.Stats["pos_y"] != int64(position.Y) {
			t.Fatalf("brides overlapped: %+v", c.Stats)
		}
		found = false
		for _, frame := range frames {
			if frame.op == 999 && frame.body[0] == 0 {
				found = true
				if string(bytes.TrimRight(frame.body[2:32], "\x00")) != "bride29" || string(bytes.TrimRight(frame.body[32:62], "\x00")) != "bride30" {
					t.Fatal("avatar names reversed")
				}
			}
		}
		if !found {
			t.Fatal("bride avatar setup missing")
		}
	}
	key, _ := r.WeddingPairScope([]string{"29", "30"})
	ceremony := f.s.wedding.ceremonies[key]
	if ceremony == nil {
		t.Fatal("ceremony timer absent")
	}
	ceremony.timer.Stop()
	f.now = time.Unix(f.at, 0)
	f.s.runCurrentWeddingCeremony(key, ceremony)
	for _, p := range []*gameSession{f.first, f.second} {
		found = false
		for _, frame := range weddingChurchLoadingFrames(t, p) {
			if frame.op == 1007 {
				found = true
				if string(bytes.TrimRight(frame.body[8:38], "\x00")) != "bride29" || string(bytes.TrimRight(frame.body[38:68], "\x00")) != "bride30" {
					t.Fatal("movie names reversed")
				}
			}
		}
		if !found {
			t.Fatal("female ceremony animation missing")
		}
	}
	ceremony.timer.Stop()
	f.now = time.Unix(f.at+60, 0)
	f.s.runCurrentWeddingCeremony(key, ceremony)
	for _, id := range []string{"29", "30"} {
		c, _, _ := f.repos.Character.Load(ctx, id)
		state, err := wedding.Read(c)
		if err != nil || state.Phase != wedding.Married {
			t.Fatal(state, err)
		}
		bag, _, _ := f.repos.Inventory.Load(ctx, id)
		assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 1)
	}
}

func TestWeddingGenderStableNumericOrderAndReconnect(t *testing.T) {
	pair := weddingCeremonyRecords()
	pair.Actor.CharacterID = "9"
	pair.Partner.CharacterID = "10"
	pair.Actor.Job = "11"
	pair.Actor.Name = "first"
	pair.Partner.Name = "second"
	rules := &wedding.Rules{GenderPolicy: wedding.GenderPolicyDenyMaleMale, ForceMove: [2]wedding.CeremonyPosition{{X: 670, Y: 330}, {X: 685, Y: 360}}}
	for _, p := range []wedding.PairResult{pair, {Actor: pair.Partner, Partner: pair.Actor}} {
		first, second, err := currentWeddingCeremonyNames(p.Actor, p.Partner)
		if err != nil || string(first) != "first" || string(second) != "second" {
			t.Fatal("numeric name order", string(first), string(second), err)
		}
		position, err := currentWeddingCeremonyPairPosition(rules, p.Actor, p.Partner)
		index := 0
		if p.Actor.CharacterID == "10" {
			index = 1
		}
		if err != nil || position != rules.ForceMove[index] {
			t.Fatal(position, err)
		}
	}
	for _, bad := range []string{"9", "invalid"} {
		b := pair.Partner
		b.CharacterID = bad
		if _, _, err := currentWeddingCeremonyNames(pair.Actor, b); err == nil {
			t.Fatal("ambiguous ID accepted")
		}
	}
	s, identity, repos := weddingCeremonyLoginFixture(t, true)
	s.wedding.rules.GenderPolicy = wedding.GenderPolicyDenyMaleMale
	c, _, _ := repos.Character.Load(context.Background(), "29")
	c.Job = "11"
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := s.runCurrentWeddingCeremonyRecovery(identity, weddingCeremonyLoginSnapshot(t, repos)); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(context.Background(), "29")
	if c.Stats["pos_x"] != 670 || c.Stats["pos_y"] != 330 {
		t.Fatal("reconnect moved first bride into second slot")
	}
}
