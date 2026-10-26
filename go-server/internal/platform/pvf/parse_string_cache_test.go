package pvf

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

func TestParseFilesSharedStringsPreserveMetadataAndLookup(t *testing.T) {
	a := &Archive{pathIdx: make(map[string]int)}
	addA := func(value []byte) int {
		off := len(a.strA)
		a.strA = append(a.strA, value...)
		a.strA = append(a.strA, 0)
		return off << 1
	}
	dir := addA([]byte("Dir"))
	name := addA([]byte("item.stk"))
	same := addA([]byte("shared"))
	alternate := addA([]byte("./DIR//"))
	gbName := addA([]byte{0xd6, 0xd0, 0xce, 0xc4})
	a.strW = []byte{0x2d, 0x4e, 0x87, 0x65, 0, 0}
	offsets := [][2]int{{name, dir}, {name, dir}, {name, alternate}, {same, same}, {1, dir}, {gbName, dir}, {-1, -1}, {1 << 25, 1 << 25}}
	for _, v := range offsets {
		for _, n := range []int{v[0], v[1], 0, 0, 0, 1} {
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], uint32(int32(n)))
			a.data = append(a.data, b[:]...)
		}
	}
	if err := a.parseFiles(0, len(offsets)); err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"Dir/item.stk", "Dir/item.stk", "DIR/item.stk", "shared/shared", "Dir/中文", "Dir/中文", "", ""}
	for i, want := range wantPaths {
		if a.files[i].ArchivePath != want {
			t.Fatalf("文件%d路径=%q，预期%q", i, a.files[i].ArchivePath, want)
		}
	}
	for _, check := range []struct {
		path  string
		index int
	}{{"dir/item.stk", 0}, {"./DIR\\ITEM.STK", 0}, {"SHARED/shared", 3}, {"dir/中文", 4}} {
		if got := a.FindFileIndex(check.path); got != check.index {
			t.Fatalf("路径%q下标=%d，预期%d", check.path, got, check.index)
		}
	}
	if a.files[0].Name != a.files[2].Name || a.files[0].Path != a.files[1].Path || a.files[4].Name != a.files[5].Name {
		t.Fatal("重复偏移或不同编码的公开字符串内容发生变化")
	}
	if a.files[6].Name != "" || a.files[7].Path != "" {
		t.Fatal("无效偏移的原有空串语义发生变化")
	}
	a.data = a.data[:len(a.data)-1]
	if err := a.parseFiles(0, len(offsets)); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("截断文件表未拒绝：%v", err)
	}
}

func TestRealPVFSharedFileStringsPreserveEveryIndexEntry(t *testing.T) {
	source := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if source == "" {
		t.Skip("未指定真实PVF")
	}
	a, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	first := make(map[string]int)
	for i, item := range a.items {
		// 使用移植前逐条解码的公开字段构造方式核对，不依据候选缓存构造期望。
		name, dir := a.resolveString(item.nameOffset), a.resolveString(item.pathOffset)
		want := File{Index: i, Path: dir, Name: name, ArchivePath: joinArchivePath(dir, name), DataType: item.dataType, Size: item.dataSize}
		if a.files[i] != want {
			t.Fatalf("文件%d元数据发生变化", i)
		}
		if want.ArchivePath == "" {
			continue
		}
		key := pathKey(want.ArchivePath)
		idx, exists := first[key]
		if !exists {
			idx = i
			first[key] = i
		}
		if got, ok := a.FindFile(want.ArchivePath); !ok || got != a.files[idx] {
			t.Fatalf("文件%d路径查找或重复路径首条优先发生变化", i)
		}
	}
	if len(first) != len(a.pathIdx) {
		t.Fatalf("路径索引条数不符：%d/%d", len(first), len(a.pathIdx))
	}
	t.Logf("全部%d份文件公开元数据及%d条唯一规范路径逐项一致", len(a.files), len(first))
}
