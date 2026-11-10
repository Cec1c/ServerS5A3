package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWeddingCurrentNativeSupplementalFixtures(t *testing.T) {
	const token uint32 = 131077
	type fixture struct {
		Name    string         `json:"name"`
		Class   byte           `json:"classification"`
		Opcode  uint16         `json:"opcode"`
		Payload string         `json:"payload_hex"`
		Expect  map[string]any `json:"expect"`
	}
	rows := []fixture{}
	add := func(name string, class byte, op uint16, body []byte, expect map[string]any) {
		rows = append(rows, fixture{name, class, op, hex.EncodeToString(body), expect})
	}
	names := [][]byte{[]byte("groom"), []byte("bride")}
	route := wedding.CeremonyRoute{Channel: 10, Area: 8}
	add("own-info-token", 0, 1000, buildCurrentWeddingInfo(token, wedding.InCeremony, 0, 255, 0, 10), map[string]any{"token": token})
	add("reservation-token", 0, 1004, buildCurrentWeddingCeremony(token, route, 2000000000, 0, names[0], names[1], 10), map[string]any{"token": token})
	profile, e := buildCurrentWeddingCharacter(r.CharacterRecord{CharacterID: "5", Name: "bride", AccountID: "a5", Job: "11", Level: 30}, r.EquipmentRecord{CharacterID: "5"}, token)
	if e != nil {
		t.Fatal(e)
	}
	add("spouse-profile-token", 1, 1035, profile, map[string]any{"token": token})
	add("peer-relation-token", 0, 1013, buildCurrentWeddingPeerInfo(5, token, wedding.Married, 1, 10), map[string]any{"token": token})
	add("ceremony-native-end", 0, 999, buildCurrentWeddingCeremonyEnd(2, route, names[0], names[1]), map[string]any{"success": 1, "kind": 2, "kind2": 2, "area": 8, "channel": 10, "first": "groom", "second": "bride"})
	add("ceremony-native-avatar-setup", 0, 999, buildCurrentWeddingAvatarSetup(2, route, names[0], names[1]), map[string]any{"success": 0, "kind": 2, "kind2": 2, "area": 8, "channel": 10, "first": "groom", "second": "bride"})
	friendService := &Service{}
	friendBody, e := friendService.buildCurrentCharacterFriendListBody([]r.CharacterRecord{{CharacterID: "50002", AccountID: "offline-account", Name: "offlineFriend", Job: "0", Level: 30}})
	if e != nil {
		t.Fatal(e)
	}
	add("offline-friend-invitation-target", 0, 273, friendBody, map[string]any{"rows": []map[string]any{{"target_id": 50002, "channel": 0, "name": "offlineFriend", "level": 30}}})
	tree := wedding.TreeState{Index: 1, Expires: 2000000000, Growth: 20, WaterCount: 2}
	room := wedding.Room{Version: 1, Tree: tree, Placed: map[uint16]r.ItemStack{0: {ItemID: 10002685, Count: 1}, 27: {ItemID: 10002797, Count: 1}}, Bag: map[uint16]r.ItemStack{2: {ItemID: 10002685, Count: 1}, 39: {ItemID: 10002797, Count: 1}}}
	add("room-with-tree", 0, 1008, buildCurrentWeddingRoom(room), map[string]any{"placed": [][2]int64{{0, 10002685}, {27, 10002797}}, "tree": []int64{1, 2000000000, 20, 2}})
	add("room-inventory-reserved-tail", 0, 1009, buildCurrentWeddingRoomInventory(room), map[string]any{"items": [][3]int64{{2, 10002685, 1}, {39, 10002797, 1}}, "reserved": 0})
	rules := &wedding.Rules{Furniture: map[int64]wedding.Furniture{10002685: {Positions: map[uint16]bool{0: true, 5: true}}, 10002797: {Positions: map[uint16]bool{27: true}}}}
	refresh := [][4]int64{{0, 10002685, 0, 1}, {0, 0, 5, 0}, {0, 10002797, 27, 1}}
	for slot := int64(0); slot < 40; slot++ {
		row := [4]int64{1, 0, slot, 0}
		if slot == 2 {
			row[1], row[3] = 10002685, 1
		}
		if slot == 39 {
			row[1], row[3] = 10002797, 1
		}
		refresh = append(refresh, row)
	}
	add("room-explicit-empty-slots", 0, 1011, buildCurrentWeddingRoomRefresh(room, rules), map[string]any{"rows": refresh})
	add("tree-state", 0, 1012, buildCurrentWeddingTree(tree), map[string]any{"tree": []int64{1, 2000000000, 20, 2}})
	add("tree-harvest-display", 0, 1014, buildCurrentWeddingTreeHarvestReward(wedding.TreeReward{ItemID: 10002901, Count: 2}), map[string]any{"items": [][2]int64{{10002901, 2}}})
	capsule := wedding.CapsuleState{Progress: 2, Own: 3, Taken: 4}
	add("capsule-state", 0, 1015, buildCurrentWeddingCapsule(capsule, true), map[string]any{"progress": 2, "own": 3, "taken": 4, "spouse_pending": true})
	add("capsule-progress", 0, 1016, binary.LittleEndian.AppendUint32(nil, 2), map[string]any{"progress": 2})
	add("partner-move-check", 1, 1043, buildCurrentWeddingPartnerMoveACK(0, &currentWeddingPartnerMovePending{channel: 10, town: 39, area: 0, x: 700, y: 250}), map[string]any{"flag": 1, "result": 0, "channel": 10, "reserved": 0, "town": 39, "area": 0, "x": 700, "y": 250})
	add("partner-move-check-failure", 1, 1043, buildCurrentWeddingPartnerMoveACK(-1, nil), map[string]any{"flag": 1, "result": -1})
	add("partner-move-confirm", 1, 1044, weddingResultACK(0), map[string]any{"flag": 1, "result": 0})
	add("partner-move-confirm-failure", 1, 1044, weddingResultACK(-1), map[string]any{"flag": 1, "result": -1})
	for op := uint16(1047); op <= 1054; op++ {
		expect := map[string]any{"flag": 1, "result": 0}
		if op == 1052 {
			expect["progress"], expect["own"] = 2, 3
		}
		if op == 1053 {
			expect["own"] = 3
		}
		if op == 1054 {
			expect["taken"] = 4
		}
		add("extension-success-"+strconv.Itoa(int(op)), 1, op, currentWeddingExtensionACK(op, 0, capsule), expect)
		add("extension-failure-"+strconv.Itoa(int(op)), 1, op, currentWeddingExtensionACK(op, -1, capsule), map[string]any{"flag": 1, "result": -1})
	}
	if path := os.Getenv("DNFWEDDING_WIRE_SUPPLEMENT"); path != "" {
		clean := filepath.Clean(path)
		if !strings.HasPrefix(strings.ToLower(clean), `d:\90cn\servers5a3\runtime\fixes\`) {
			t.Fatal("native fixtures must remain in workspace fixes")
		}
		data, e := json.MarshalIndent(struct {
			Kind     string    `json:"kind"`
			Fixtures []fixture `json:"fixtures"`
		}{"wedding-current-wire-fixtures-v1", rows}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		out, e := os.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = out.Write(data); e != nil {
			_ = out.Close()
			t.Fatal(e)
		}
		if e = out.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
