package pvf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	headerSize     = 0x30
	fileItemSize   = 0x18
	groupItemSize  = 8
	magicSignature = 0x69706B6E
)

type ArchiveFormat string

const (
	// FormatNKPI 表示旧版 DNF PVF `nkpi` 归档格式。
	FormatNKPI ArchiveFormat = "nkpi"
	// FormatProtectedNKPI 表示 23.4.15.0 之后使用 UTF-16 seed 密钥流的 `nkpi` 归档格式。
	FormatProtectedNKPI ArchiveFormat = "protected_nkpi"
)

var (
	ErrInvalidArchive = errors.New("pvf archive is invalid")
	ErrFileNotFound   = errors.New("pvf file not found")
)

type ArchiveSnapshot struct {
	Format       ArchiveFormat `json:"format"`
	Path         string        `json:"path"`
	Size         int64         `json:"size"`
	Checksum     string        `json:"checksum"`
	LoadedAt     string        `json:"loaded_at"`
	FileCount    int           `json:"file_count"`
	GroupCount   int           `json:"group_count"`
	CachedChunks int           `json:"cached_chunks"`
	CachedTexts  int           `json:"cached_texts"`
}

type PreloadResult struct {
	Groups int `json:"groups"`
	Cached int `json:"cached"`
}

type File struct {
	Index       int    `json:"index"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	ArchivePath string `json:"archive_path"`
	DataType    int    `json:"data_type"`
	Size        int    `json:"size"`
}

type Archive struct {
	snapshot Snapshot
	format   ArchiveFormat
	header   pvfHeader

	// data 是启动期读入的完整 PVF 字节，后续查询不再访问磁盘。
	data   []byte
	files  []File
	items  []fileItem
	groups []groupItem

	// pathIdx 保存归一化路径到文件表下标的映射，查询时避免扫描目录。
	pathIdx map[string]int
	bodyOff int
	strA    []byte
	strW    []byte

	// chunks 缓存已解密解压的 body chunk，texts 缓存已解码的脚本文本。
	chunks sync.Map
	texts  sync.Map
}

func Open(path string) (*Archive, error) {
	return LoadArchive(Options{Path: path})
}

func OpenBytes(data []byte) (*Archive, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty pvf data", ErrInvalidArchive)
	}
	copied := append([]byte(nil), data...)
	sum := sha256.Sum256(copied)
	return OpenArchive(&Bundle{
		snapshot: Snapshot{
			Size:     int64(len(copied)),
			Checksum: hex.EncodeToString(sum[:]),
			LoadedAt: time.Now().UTC(),
		},
		data: copied,
	})
}

func LoadArchive(options Options) (*Archive, error) {
	bundle, err := Load(options)
	if err != nil {
		return nil, err
	}
	archive, err := OpenArchive(bundle)
	if err != nil {
		return nil, err
	}
	return archive, nil
}

func OpenArchive(bundle *Bundle) (*Archive, error) {
	if bundle == nil || len(bundle.data) == 0 {
		return nil, fmt.Errorf("%w: empty pvf data", ErrInvalidArchive)
	}
	archive := &Archive{
		snapshot: bundle.snapshot,
		data:     bundle.data,
		pathIdx:  make(map[string]int),
	}
	if err := archive.parse(); err != nil {
		return nil, err
	}
	return archive, nil
}

func (a *Archive) Snapshot() ArchiveSnapshot {
	if a == nil {
		return ArchiveSnapshot{}
	}
	cachedChunks := 0
	a.chunks.Range(func(_, _ any) bool {
		cachedChunks++
		return true
	})
	cachedTexts := 0
	a.texts.Range(func(_, _ any) bool {
		cachedTexts++
		return true
	})
	return ArchiveSnapshot{
		Format:       a.format,
		Path:         a.snapshot.Path,
		Size:         a.snapshot.Size,
		Checksum:     a.snapshot.Checksum,
		LoadedAt:     a.snapshot.LoadedAt.Format(rfc3339Nano),
		FileCount:    len(a.files),
		GroupCount:   len(a.groups),
		CachedChunks: cachedChunks,
		CachedTexts:  cachedTexts,
	}
}

// Format 返回当前 PVF 归档格式，供 smoke 报告和兼容排查使用。
func (a *Archive) Format() ArchiveFormat {
	if a == nil {
		return ""
	}
	return a.format
}

func (a *Archive) Files() []File {
	if a == nil || len(a.files) == 0 {
		return nil
	}
	out := make([]File, len(a.files))
	copy(out, a.files)
	return out
}

func (a *Archive) FileCount() int {
	if a == nil {
		return 0
	}
	return len(a.files)
}

func (a *Archive) CanReadFileData() bool {
	if a == nil {
		return false
	}
	return a.format == FormatNKPI || a.format == FormatProtectedNKPI
}

func (a *Archive) FindFile(relativePath string) (File, bool) {
	if a == nil {
		return File{}, false
	}
	idx, ok := a.pathIdx[pathKey(relativePath)]
	if !ok {
		return File{}, false
	}
	return a.files[idx], true
}

func (a *Archive) FindFileIndex(relativePath string) int {
	if a == nil {
		return -1
	}
	idx, ok := a.pathIdx[pathKey(relativePath)]
	if !ok {
		return -1
	}
	return idx
}

func (a *Archive) ReadText(relativePath string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("%w: archive is nil", ErrInvalidArchive)
	}
	idx, ok := a.pathIdx[pathKey(relativePath)]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrFileNotFound, relativePath)
	}
	return a.readTextIndex(idx)
}

func (a *Archive) ReadRaw(relativePath string) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: archive is nil", ErrInvalidArchive)
	}
	idx, ok := a.pathIdx[pathKey(relativePath)]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, relativePath)
	}
	return a.readRawIndex(idx)
}

// ResolveString exposes one script token's string-pool value. PVF type-1
// scripts store labels and quoted values as encoded offsets into the archive's
// shared ANSI/UTF-16 pools; callers must keep the encoded offset unchanged
// unless they rebuild every referring script token.
func (a *Archive) ResolveString(magicOffset int) string {
	if a == nil {
		return ""
	}
	return a.resolveString(magicOffset)
}

func (a *Archive) FileText(idx int) (string, error) {
	if a == nil {
		return "", fmt.Errorf("%w: archive is nil", ErrInvalidArchive)
	}
	return a.readTextIndex(idx)
}

func (a *Archive) FileRaw(idx int) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: archive is nil", ErrInvalidArchive)
	}
	return a.readRawIndex(idx)
}

func (a *Archive) PreloadAll() (PreloadResult, error) {
	if a == nil {
		return PreloadResult{}, fmt.Errorf("%w: archive is nil", ErrInvalidArchive)
	}
	result := PreloadResult{Groups: len(a.groups)}
	for idx := range a.groups {
		if _, err := a.chunk(idx); err != nil {
			return result, err
		}
		result.Cached++
	}
	return result, nil
}

// ScanTextPrefix 按 group 顺序遍历归档里所有位于 prefix 之下的脚本文件，
// 逐条解码后调用 visit(archivePath, text)。
//
// 与逐文件 ReadText 的关键差别是**内存有界**：解压出来的 group 与解码出来的
// 文本都不写入 chunks/texts 缓存，同一时刻只持有一个解压后的 group 与一条文本。
// 因此对整棵子树做一次性扫描（例如 `equipment/` 下的 140,862 个 `.equ`）
// 不会把解码结果留到进程结束，也不会让归档缓存在扫描后仍然挂着整棵树：
// 扫描前后 `Snapshot().CachedChunks/CachedTexts` 保持不变。
//
// prefix 用归档路径的同样规则归一化（大小写不敏感、`/` 分隔）；空串表示整档。
// 只回调 dataType 为 1（脚本）或 3（UTF-16 文本）的文件，其余类型没有文本。
// 顺序是确定的：先 groupIndex 升序、同 group 内按文件表下标升序。
// visit 返回错误时立即停止并原样返回该错误；单个 group 只会被解压一次。
func (a *Archive) ScanTextPrefix(prefix string, visit func(archivePath string, text string) error) error {
	if a == nil {
		return fmt.Errorf("%w: archive is nil", ErrInvalidArchive)
	}
	if visit == nil {
		return fmt.Errorf("%w: scan visitor is nil", ErrInvalidArchive)
	}
	key := pathKey(prefix)
	type scanTarget struct {
		index int
		chunk int
	}
	targets := make([]scanTarget, 0, 256)
	chunkSet := make(map[int]struct{})
	for idx := range a.files {
		if !archivePathWithinPrefix(a.files[idx].ArchivePath, key) {
			continue
		}
		item := a.items[idx]
		if item.dataType != 1 && item.dataType != 3 {
			continue
		}
		targets = append(targets, scanTarget{index: idx, chunk: item.chunkIndex})
		chunkSet[item.chunkIndex] = struct{}{}
	}
	order := make([]int, 0, len(chunkSet))
	for chunkIndex := range chunkSet {
		order = append(order, chunkIndex)
	}
	sort.Ints(order)
	for _, chunkIndex := range order {
		chunk, err := a.decodeChunkIndex(chunkIndex)
		if err != nil {
			return err
		}
		for _, target := range targets {
			if target.chunk != chunkIndex {
				continue
			}
			item := a.items[target.index]
			if item.dataOffset < 0 || item.dataSize < 0 || item.dataOffset+item.dataSize > len(chunk) {
				return fmt.Errorf("%w: file %d data range is invalid", ErrInvalidArchive, target.index)
			}
			text := a.decodeText(item.dataType, chunk[item.dataOffset:item.dataOffset+item.dataSize])
			if err := visit(a.files[target.index].ArchivePath, text); err != nil {
				return err
			}
		}
		// 本 group 到此为止：循环体不再引用 chunk，下一次迭代即可覆盖/回收它。
		_ = chunk
	}
	return nil
}

// archivePathWithinPrefix 报告归档路径是否落在归一化前缀之下（含前缀本身）。
func archivePathWithinPrefix(archivePath, prefixKey string) bool {
	if prefixKey == "" {
		return true
	}
	candidate := pathKey(archivePath)
	if candidate == prefixKey {
		return true
	}
	return strings.HasPrefix(candidate, prefixKey+"/")
}

func (a *Archive) Bytes() []byte {
	if a == nil {
		return nil
	}
	out := make([]byte, len(a.data))
	copy(out, a.data)
	return out
}

func (a *Archive) Reader() *bytes.Reader {
	if a == nil {
		return bytes.NewReader(nil)
	}
	return bytes.NewReader(a.data)
}
