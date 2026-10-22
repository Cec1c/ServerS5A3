package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

var aikenR142PVF struct {
	sync.Once
	archive     *platformpvf.Archive
	catalog     *pvfDungeonDropCatalog
	catalogOnce sync.Once
	err         error
}

func aikenR142Archive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	// Real resources are opt-in; keep ordinary CI independent of private PVF.
	// A selected but missing/wrong resource remains a failure, never a skip.
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_AIKEN_REAL_PVF_SMOKE"))
	if path == "" {
		path = strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	}
	if path == "" {
		t.Skip("set DNFBRIDGE_AIKEN_REAL_PVF_SMOKE to opt into the real-PVF Aiken tests")
	}
	aikenR142PVF.Do(func() {
		data, err := os.ReadFile(path)
		if err != nil {
			aikenR142PVF.err = err
			return
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348" {
			aikenR142PVF.err = errors.New("unexpected PVF digest")
			return
		}
		aikenR142PVF.archive, aikenR142PVF.err = platformpvf.LoadArchive(platformpvf.Options{Path: path})
	})
	if aikenR142PVF.err != nil {
		t.Fatal(aikenR142PVF.err)
	}
	return aikenR142PVF.archive
}

// This proves current-resource identity, not a legal custom-option instance.
func TestAikenR142ResourceIdentity(t *testing.T) {
	a := aikenR142Archive(t)
	for path, fragment := range map[string]string{
		"npc/487_custom_option_machine.npc":                     "[custom ability upgrade]",
		"equipment/character/common/jacket/cloth/100050746.equ": "warriors dungeon",
		"stackable/10161001/10161536.stk":                       "魔纹石",
		"etc/customability/individualcustominfo.etc":            "[upgrade info]",
	} {
		text, err := a.ReadText(path)
		if err != nil || !strings.Contains(text, fragment) {
			t.Fatalf("%s: %v; missing %q", path, err, fragment)
		}
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{100050746, 10161536} {
		item, err := catalog.ResolveItem(id)
		if err != nil || item.ItemID != id {
			t.Fatalf("item=%d: %+v %v", id, item, err)
		}
	}
}

type aikenR142FailSave struct {
	repo.InventoryRepository
	calls *int
}

func (r aikenR142FailSave) Save(context.Context, repo.InventoryRecord) error {
	*r.calls++
	return errors.New("aiken isolated save fault")
}

func aikenR142Fixture(t *testing.T, scenario string) (*Service, *gameSession, repo.Group, *bytes.Buffer, *int) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw, 9)
	binary.LittleEndian.PutUint32(raw[2:], 100050746)
	binary.LittleEndian.PutUint32(raw[6:], 203004656)
	raw[0x0a] = 7
	binary.LittleEndian.PutUint16(raw[0x0b:], 28)
	binary.LittleEndian.PutUint32(raw[0x0e:], 987654)
	copy(raw[0x72:], []byte{1, 25, 0x7e, 0xa5, 0}) // Proven five-byte carrier; third/BF opaque.
	inv := repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{
		"0:9":   {ItemID: 100050746, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "instance_note": "preserve"}},
		"0:121": {ItemID: 10161536, Count: 100},
	}}
	switch scenario {
	case "missing_material":
		delete(inv.Slots, "0:121")
	case "locked":
		s := inv.Slots["0:9"]
		s.Extra["equipment_lock_state"] = "1"
		inv.Slots["0:9"] = s
	case "empty_slot":
		delete(inv.Slots, "0:9")
	}
	for _, err := range []error{
		g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 100000}}),
		g.Inventory.Save(ctx, inv),
		g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "20", Slots: map[string]repo.ItemStack{"0:121": {ItemID: 10161536, Count: 200}}}),
		g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]repo.ItemStack{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := new(int)
	provided := g
	if scenario == "save_fault_unreached" {
		provided.Inventory = aikenR142FailSave{g.Inventory, calls}
	}
	a := aikenR142Archive(t)
	aikenR142PVF.catalogOnce.Do(func() { aikenR142PVF.catalog, aikenR142PVF.err = newPVFDungeonDropCatalog(a) })
	if aikenR142PVF.err != nil {
		t.Fatal(aikenR142PVF.err)
	}
	items := aikenR142PVF.catalog
	logs := new(bytes.Buffer)
	service := &Service{options: options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain, gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain}, pvfItemCatalog: items, repositoryProvider: func() (repo.Group, bool) { return provided, true }, logger: slog.New(slog.NewTextHandler(logs, nil))}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, accountFromToken: true, itemListMigrationsDone: true}
	return service, session, g, logs, calls
}

func aikenR142Dispatch(t *testing.T, s *Service, session *gameSession, transport string) {
	t.Helper()
	body := []byte{9, 0}
	var err error
	if transport == "legacy" {
		err = s.handleGameCommand(session, 1, 1342, body)
	} else {
		var frame []byte
		frame, err = proto.BuildChannelPacket(1342, body, 1, 1)
		if err == nil {
			err = s.handleGameUpper(session, frame)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestAikenR142LegalGrades(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		for grade := byte(0); grade < 3; grade++ {
			t.Run(fmt.Sprintf("%s/%d_to_%d", transport, grade, grade+1), func(t *testing.T) {
				s, session, g, logs, _ := aikenR142Fixture(t, "")
				ctx := context.Background()
				before, _, _ := g.Inventory.Load(ctx, "19")
				source := before.Slots["0:9"]
				source.RawEntry[0x76] = grade
				before.Slots["0:9"] = source
				if err := g.Inventory.Save(ctx, before); err != nil {
					t.Fatal(err)
				}
				aikenR142Dispatch(t, s, session, transport)
				after, _, _ := g.Inventory.Load(ctx, "19")
				updated := after.Slots["0:9"]
				if len(updated.RawEntry) != 119 || updated.RawEntry[0x76] != grade+1 {
					t.Fatalf("grade not advanced: got=%x want=%d log=%s", updated.RawEntry, grade+1, logs.String())
				}
				if got, want := after.Slots["0:121"].Count, int64(100-[]int{11, 18, 34}[grade]); got != want {
					t.Fatalf("materials=%d want=%d", got, want)
				}
				want := append([]byte(nil), source.RawEntry...)
				want[0x76] = grade + 1
				if !bytes.Equal(want, updated.RawEntry) {
					t.Fatal("opaque instance fields changed")
				}
				raw := append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
				first, rest := splitGameServerUpperPacket(t, raw)
				second, rest := splitGameServerUpperPacket(t, rest)
				if len(rest) != 0 || first.Header.MsgID != 14 || first.Header.Classification != 0 || second.Header.MsgID != 1342 || second.Header.Classification != 1 || !bytes.Equal(second.Body, []byte{1}) {
					t.Fatalf("wrong notification/ACK sequence: %+v %+v", first, second)
				}
				if len(first.Body) != 3+2*119 || !bytes.Equal(first.Body[3+0x72:3+0x77], updated.RawEntry[0x72:0x77]) {
					t.Fatal("NOTI14 instance differs")
				}
				reloaded := currentItemListEntryFromStack(0, 9, updated)
				if !bytes.Equal(reloaded.data[0x72:0x77], updated.RawEntry[0x72:0x77]) {
					t.Fatal("reload projection differs")
				}
				if dir := os.Getenv("AIKEN_R142_OUTPUT"); dir != "" {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					b, _ := json.MarshalIndent(map[string]any{"transport": transport, "before_grade": grade, "after_grade": grade + 1, "material_remaining": after.Slots["0:121"].Count, "frame_hex": hex.EncodeToString(raw), "noti14_body_hex": hex.EncodeToString(first.Body), "ack_body_hex": hex.EncodeToString(second.Body)}, "", "  ")
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-grade%d.json", transport, grade)), b, 0644); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
