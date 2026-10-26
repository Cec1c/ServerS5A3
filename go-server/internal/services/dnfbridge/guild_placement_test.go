package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func guildPlacementFixture(t *testing.T) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, p, repos, source := guildMerchantFixture(t)
	merchantPurchaseSource(t, source)
	source["etc/agitwar.etc"] += "\n[building construct time]\n180"
	source["stackable/merchant.stk"] += "\n[action type]\n`[deploy in isometric cell map]` 71021 15 30\n[/action type]"
	source["passiveobject/passiveobject.lst"] = "71021 `test.obj`"
	source["passiveobject/test.obj"] = "[isometric cell map info]\n`guild agit` `3 X 3` 1 1 1 1 1 1 1 1 1\n[/isometric cell map info]"
	source["map/guildagit/guildagit_w.map"] += "\n[isometric cell map]\n`guild agit` 750 210 500 160 290 370 900 450\n[agitwar area index]\n0"
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = items
	inv := r.InventoryRecord{CharacterID: "29", Slots: map[string]r.ItemStack{"0:42": {ItemID: 10156091, Count: 3, ExpireAt: s.gameplayNow().Add(time.Hour)}}}
	if err = repos.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	return s, p, repos
}
func guildPlacementBody() []byte {
	body, _ := hex.DecodeString("0c00000017000000240000000c45f89a002a000000860300001401000000000000030000000300000009000000000000000f000000010000000100000001000000010000000100000001000000010000000100000001000000")
	binary.LittleEndian.PutUint32(body[13:], 10156091)
	return body
}

func TestGuildPlacementNativeRequestLayout(t *testing.T) {
	body := guildPlacementBody()
	q, err := parseCurrentGuildPlacementRequest(body)
	if err != nil || q.slot != 42 || q.x != 902 || q.y != 276 || q.width != 3 || len(q.mask) != 9 {
		t.Fatalf("decode: %+v %v", q, err)
	}
	for i := 0; i < 13; i++ {
		body[i] = 0xa5
	}
	q2, err := parseCurrentGuildPlacementRequest(body)
	if err != nil || !reflect.DeepEqual(q, q2) {
		t.Fatal("unused prefix changed request")
	}
	for _, size := range []int{0, 40, 52, 53, 88, 90} {
		x := append([]byte(nil), body...)
		if size > len(x) {
			x = append(x, 0)
		} else {
			x = x[:size]
		}
		if _, err = parseCurrentGuildPlacementRequest(x); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
}

func TestGuildPlacementProductionCommitReplayAndReconnect(t *testing.T) {
	s, p, repos := guildPlacementFixture(t)
	if err := s.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	p.conn.(*bufferConn).write.Reset()
	sendMerchantRequest(t, s, p, 837, guildPlacementBody(), 77)
	if got := guildReply(t, p, 837); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("ACK %x", got)
	}
	notice := guildReply(t, p, 777)
	if len(notice) != 39 || binary.LittleEndian.Uint32(notice) != 4 || binary.LittleEndian.Uint32(notice[20:]) != 71021 {
		t.Fatalf("notice %x", notice)
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Guilds[1].Placements.Rows) != 1 {
		t.Fatal("missing committed placement")
	}
	if binary.LittleEndian.Uint32(guildReply(t, p, 1297)[0x852:]) != 15 {
		t.Fatal("maintenance projection")
	}
	exportGuildPlacement(t, "notice-777.bin", notice)
	exportGuildPlacement(t, "ack-837.bin", guildReply(t, p, 837))
	p.conn.(*bufferConn).write.Reset()
	replay := guildPlacementBody()
	for i := 0; i < 13; i++ {
		replay[i] = 0x5a
	}
	sendMerchantRequest(t, s, p, 837, replay, 77)
	if got := guildReply(t, p, 837); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("replay %x", got)
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), "29")
	if inv.Slots["0:42"].Count != 2 {
		t.Fatal("duplicate consumption")
	}
	// The cache is per generation. A fresh client receives the durable table.
	p.guild.placement.identity = boundGameSessionCharacter{}
	p.conn.(*bufferConn).write.Reset()
	if err = s.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	full := guildReply(t, p, 191)
	if len(full) != 1+0x369a || full[0] != 1 || binary.LittleEndian.Uint32(full[6:]) != 1 || binary.LittleEndian.Uint32(full[10:]) != 71021 {
		t.Fatalf("full snapshot %d", len(full))
	}
	exportGuildPlacement(t, "reconnect-191.bin", full)
}

func TestGuildPlacementProductionRejectsForgery(t *testing.T) {
	for _, mode := range []string{"item", "slot", "x", "area", "shape", "mask", "expired", "scene"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildPlacementFixture(t)
			body := guildPlacementBody()
			ctx := context.Background()
			switch mode {
			case "item":
				binary.LittleEndian.PutUint32(body[13:], 3227)
			case "slot":
				binary.LittleEndian.PutUint32(body[17:], 43)
			case "x":
				binary.LittleEndian.PutUint32(body[21:], 903)
			case "area":
				binary.LittleEndian.PutUint32(body[45:], 1)
			case "shape":
				binary.LittleEndian.PutUint32(body[33:], 1)
				binary.LittleEndian.PutUint32(body[37:], 9)
			case "mask":
				binary.LittleEndian.PutUint32(body[53:], 0)
			case "expired":
				inv, _, _ := repos.Inventory.Load(ctx, "29")
				item := inv.Slots["0:42"]
				item.ExpireAt = s.gameplayNow()
				inv.Slots["0:42"] = item
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			case "scene":
				p.townSceneReadyCharacterID = 0
			}
			before, _, _ := repos.Inventory.Load(ctx, "29")
			regBefore, _ := guild.Load(ctx, repos.Settings)
			sendMerchantRequest(t, s, p, 837, body, 77)
			if got := guildReply(t, p, 837); !bytes.Equal(got, []byte{0, 3}) {
				t.Fatalf("reject ACK %x", got)
			}
			after, _, _ := repos.Inventory.Load(ctx, "29")
			regAfter, _ := guild.Load(ctx, repos.Settings)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(regBefore, regAfter) {
				t.Fatal("rejection changed persistent state")
			}
		})
	}
}

func exportGuildPlacement(t *testing.T, name string, body []byte) {
	t.Helper()
	dir := os.Getenv("GUILD_PLACEMENT_NATIVE_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestGuildPlacementRealExtractedCatalog(t *testing.T) {
	root := os.Getenv("GUILD_PLACEMENT_EXTRACTED_ROOT")
	if root == "" {
		t.Skip("set GUILD_PLACEMENT_EXTRACTED_ROOT")
	}
	_, _, _, source := guildMerchantFixture(t)
	for _, folder := range filepath.SplitList(root) {
		err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(folder, path)
			if err != nil {
				return err
			}
			source[strings.ToLower(filepath.ToSlash(relative))] = strings.TrimPrefix(string(b), "\ufeff")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	items, err := newPVFDungeonDropCatalog(merchantExtractedSource(source))
	if err != nil {
		t.Fatal(err)
	}
	merchants, err := loadGuildMerchantPurchaseCatalog(items)
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadGuildPlacementCatalog(items, merchants)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.books) != 132 || c.buildSeconds != 180 {
		t.Fatalf("books=%d seconds=%d", len(c.books), c.buildSeconds)
	}
	b, ok := c.books[10156101]
	if !ok || b.ObjectID != 71021 || b.Width != 3 || b.Maintenance != 15 {
		t.Fatalf("warrior %+v", b)
	}
	m, err := loadCurrentGuildPlacementMap(source, "guildagit/guildagit_3.map")
	if err != nil {
		t.Fatal(err)
	}
	for id, b := range c.books {
		x := 290 + 20*23 + (b.Width*36+b.Height*23)/2
		y := 210 + 20*12 + b.Height*12/2
		if _, err = m.CellsAt(b, x, y); err != nil {
			t.Fatalf("book %d placement: %v", id, err)
		}
	}
}

func TestGuildPlacementProjectionClearsFormerGuild(t *testing.T) {
	for _, leave := range []bool{false, true} {
		t.Run(fmt.Sprint(leave), func(t *testing.T) {
			s, p, repos := guildPlacementFixture(t)
			sendMerchantRequest(t, s, p, 837, guildPlacementBody(), 77)
			reg, err := guild.Load(context.Background(), repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			g := reg.Guilds[1]
			identity, ok := s.boundGameSessionCharacterSnapshot(p)
			if !ok {
				t.Fatal("no bound fixture")
			}
			if leave {
				g = nil
			} else {
				copy := *g
				copy.ID = 2
				copy.Placements = nil
				g = &copy
			}
			p.conn.(*bufferConn).write.Reset()
			p.guild.placement.mu.Lock()
			err = s.writeCurrentGuildPlacementProjectionLocked(p, g, identity)
			p.guild.placement.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			body := guildReply(t, p, 191)
			if len(body) != 1+0x369a || !bytes.Equal(body[1+5:1+0x2e05], make([]byte, 8*64*23)) {
				t.Fatal("old guild placements survived")
			}
			exportGuildPlacement(t, "cleared-191.bin", body)
		})
	}
}

func TestGuildPlacementPeerRefreshUsesCurrentGuild(t *testing.T) {
	source, actor, sourceRepos := guildPlacementFixture(t)
	sendMerchantRequest(t, source, actor, 837, guildPlacementBody(), 77)
	placed, err := guild.Load(context.Background(), sourceRepos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	s, peers, repos := guildHallFixture(t)
	s.options.guildEssenceEnabled = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []uint16{30, 31, 32, 33} {
		p := peers[id]
		if err := s.writeCurrentGuildEssenceSnapshot(p); err != nil {
			t.Fatal(err)
		}
		p.conn.(*bufferConn).write.Reset()
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.stopGameSessionEvents(stop, p); err != nil {
				t.Error(err)
			}
		})
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Placements = placed.Guilds[1].Placements
	if err = guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	s.queueCurrentGuildEssenceRefresh(1, 29)
	for _, id := range []uint16{30, 31, 32, 33} {
		p := peers[id]
		if err := s.callGameSession(ctx, p, "drain-placement-refresh", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if id == 30 || id == 33 {
			if body := guildReply(t, p, 777); len(body) != 39 || binary.LittleEndian.Uint32(body[20:]) != 71021 {
				t.Fatalf("peer %d missing placement: %x", id, body)
			}
		} else if p.conn.(*bufferConn).write.Len() != 0 {
			t.Fatalf("cross-guild placement sent to %d", id)
		}
	}
}
