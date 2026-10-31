package channelcatalog

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelinfo"
)

func repositoryChannelTable(t *testing.T) ([]byte, *channelinfo.Index, *Catalog) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "services", "dnfbridge", "testdata", "channel_info.etc"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := channelinfo.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := New(index, Options{ServerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	return raw, index, catalog
}

func TestRepositoryChannelTablePVPAndEmpireRoutes(t *testing.T) {
	_, index, catalog := repositoryChannelTable(t)
	if got := len(index.ChannelsForServer(1)); got != 120 {
		t.Fatalf("公开频道表条数=%d，期望120", got)
	}
	want := map[int]struct {
		kind     int
		group    string
		min, max float64
	}{
		75: {8, "pvp", 1, 90}, 191: {8, "pvp", 1, 90},
		192: {24, "pvp", 1, 90}, 193: {24, "pvp", 1, 90},
		194: {24, "pvp", 1, 90}, 195: {24, "pvp", 1, 90},
		196: {24, "pvp", 1, 90}, 197: {24, "pvp", 1, 90},
		187: {39, "tagtournament", 90, 90},
	}
	ports := make(map[int]bool)
	for _, c := range catalog.Channels() {
		if ports[c.Port] || c.Port != 10000+c.ID {
			t.Fatalf("频道端口重复或不符：%+v", c)
		}
		ports[c.Port] = true
		if c.Group != "pvp" && c.Group != "tagtournament" {
			continue
		}
		entry, ok := want[c.ID]
		if !ok || c.ID < 1 || c.ID > 255 || int(c.Type) != entry.kind || c.Group != entry.group {
			t.Fatalf("玩法频道编号、类型或分组不符：%+v", c)
		}
		delete(want, c.ID)
		raw, found := index.ServerChannel(1, c.ID)
		if !found || len(raw.RawFields) < 7 {
			t.Fatalf("缺少频道数字字段：%+v", raw)
		}
		lo, errLo := strconv.ParseFloat(raw.RawFields[5], 64)
		hi, errHi := strconv.ParseFloat(raw.RawFields[6], 64)
		if errLo != nil || errHi != nil || lo != entry.min || hi != entry.max {
			t.Fatalf("频道%d等级窗口=%v/%v，期望%v/%v", c.ID, lo, hi, entry.min, entry.max)
		}
	}
	if len(want) != 0 {
		t.Fatalf("缺少玩法频道：%v", want)
	}
	for id := 501; id <= 507; id++ {
		if _, found := catalog.Channel(id); found {
			t.Fatalf("旧PVP高号%d仍被发布", id)
		}
	}
	area, found := index.Area("tagtournament")
	if !found || !reflect.DeepEqual(area.DungeonIDs, []int{320, 336}) {
		t.Fatalf("帝国区域映射不符：%+v", area)
	}
	empire, _ := catalog.Channel(187)
	if IsResidentChannel(empire) {
		t.Fatal("帝国频道不得成为普通自动落点")
	}
}

func TestRepositoryChannelTableDownloadedProfilesStayAligned(t *testing.T) {
	raw, source, _ := repositoryChannelTable(t)
	for _, official := range []bool{false, true} {
		name := "90CN"
		builder := channelinfo.Build90CNOnlineScript
		if official {
			name, builder = "official249", channelinfo.BuildOfficial249OnlineScript
		}
		t.Run(name, func(t *testing.T) {
			body, err := builder(raw, 1, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			downloaded, err := channelinfo.Parse(body)
			if err != nil {
				t.Fatal(err)
			}
			want := source.ChannelsForServer(1)
			for i := range want {
				want[i].ServerID = 0
				if official && want[i].Type == 42 && want[i].AreaKey == "fiendwar" {
					want[i].Type = 45
				}
			}
			if got := downloaded.ChannelsForServer(0); !reflect.DeepEqual(got, want) {
				t.Fatal("客户端下载表与实际源表的编号、名称、分组、等级或原有投影不一致")
			}
			if area, found := downloaded.Area("tagtournament"); !found || !reflect.DeepEqual(area.DungeonIDs, []int{320, 336}) {
				t.Fatalf("下载表丢失帝国区域映射：%+v", area)
			}
		})
	}
}

func TestRepositoryChannelTableReferenceMatchesRoutes(t *testing.T) {
	_, index, catalog := repositoryChannelTable(t)
	f, err := os.Open(filepath.Join("..", "..", "..", "..", "..", "Docs", "assets", "channel-reference-120.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) != 121 {
		t.Fatalf("核对CSV条数或格式错误：rows=%d err=%v", len(rows), err)
	}
	seen := make(map[int]bool)
	for _, row := range rows[1:] {
		if len(row) != 5 {
			t.Fatalf("核对CSV字段不完整：%v", row)
		}
		id, errID := strconv.Atoi(row[0])
		kind, errType := strconv.Atoi(row[2])
		port, errPort := strconv.Atoi(row[3])
		actual, found := catalog.Channel(id)
		if errID != nil || errType != nil || errPort != nil || seen[id] || !found || actual.Group != row[1] || int(actual.Type) != kind || actual.Port != port {
			t.Fatalf("核对CSV与真实解析目录不一致：row=%v actual=%+v", row, actual)
		}
		if (actual.Group == "pvp" || actual.Group == "tagtournament") && row[4] != "待验证" {
			t.Fatalf("新频道不能提前标记实机或监听通过：%v", row)
		}
		seen[id] = true
	}
	if len(seen) != len(index.ChannelsForServer(1)) {
		t.Fatal("核对CSV遗漏实际频道")
	}
}
