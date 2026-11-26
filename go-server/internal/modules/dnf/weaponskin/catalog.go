package weaponskin

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

// Appearance pairs a weapon with its cosmetic EQU and the two consumable forms.
// The indexes in this directory are shared with the companion client.
type Appearance struct {
	SourceID, ID uint32
	SourcePath   string
	Path         string
	OwnerJobs    uint16
	Products     [2]uint32
}

//go:embed catalog.tsv
var catalogTSV string

var appearances = loadCatalog()

func loadCatalog() []Appearance {
	rows := make([]Appearance, 0, strings.Count(catalogTSV, "\n"))
	for line := range strings.SplitSeq(strings.TrimSuffix(catalogTSV, "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			panic("武器外观目录的列数不正确")
		}
		var numbers [5]uint32
		for i := range numbers {
			id, err := strconv.ParseUint(fields[i], 10, 32)
			if err != nil || id == 0 {
				panic("武器外观目录编号或职业掩码无效")
			}
			numbers[i] = uint32(id)
		}
		if numbers[4] > 0xffff || fields[5] == "" || fields[6] == "" {
			panic("武器外观目录职业掩码或资源路径无效")
		}
		rows = append(rows, Appearance{
			SourceID: numbers[0], ID: numbers[1], Products: [2]uint32{numbers[2], numbers[3]},
			OwnerJobs: uint16(numbers[4]), SourcePath: fields[5], Path: fields[6],
		})
	}
	return rows
}

func Catalog() []Appearance { return appearances }

var CatalogChecksum = func() uint32 {
	ids := make([]byte, 0, 20*len(appearances))
	for _, a := range appearances {
		ids = binary.LittleEndian.AppendUint32(ids, a.SourceID)
		ids = binary.LittleEndian.AppendUint32(ids, a.ID)
		ids = binary.LittleEndian.AppendUint32(ids, a.Products[0])
		ids = binary.LittleEndian.AppendUint32(ids, a.Products[1])
		ids = binary.LittleEndian.AppendUint32(ids, uint32(a.OwnerJobs))
	}
	return crc32.ChecksumIEEE(ids)
}()

var bySource, byAppearance, byProduct, catalogPosition = indexAppearances()

func indexAppearances() (map[uint32]Appearance, map[uint32]Appearance, map[uint32]Appearance, map[uint32]int) {
	source := make(map[uint32]Appearance, len(appearances))
	appearance := make(map[uint32]Appearance, len(appearances))
	product := make(map[uint32]Appearance, 2*len(appearances))
	positions := make(map[uint32]int, len(appearances))
	for i, row := range appearances {
		if source[row.SourceID].ID != 0 || appearance[row.ID].ID != 0 {
			panic("武器外观目录原武器或外观编号重复")
		}
		source[row.SourceID] = row
		appearance[row.ID] = row
		positions[row.ID] = i
		for _, id := range row.Products {
			if product[id].ID != 0 {
				panic("武器外观目录成品编号重复")
			}
			product[id] = row
		}
	}
	return source, appearance, product, positions
}

func FromSource(id uint32) (Appearance, bool) {
	row, ok := bySource[id]
	return row, ok
}

func FromAppearance(id uint32) (Appearance, bool) {
	row, ok := byAppearance[id]
	return row, ok
}

func FromProduct(id uint64, recipe BeiyunRecipe) (Appearance, bool) {
	row, ok := byProduct[uint32(id)]
	return row, ok && recipe <= SecretRecipe && id == uint64(row.Products[recipe])
}

// ProductRecipe identifies the transfer rules of an actual catalog product.
func ProductRecipe(itemID int64) (BeiyunRecipe, bool) {
	row, ok := byProduct[uint32(itemID)]
	if ok {
		switch itemID {
		case int64(row.Products[MagicRecipe]):
			return MagicRecipe, true
		case int64(row.Products[SecretRecipe]):
			return SecretRecipe, true
		}
	}
	return MagicRecipe, false
}

func (a Appearance) Allows(job string) bool {
	number, err := strconv.ParseUint(job, 10, 4)
	return err == nil && a.ID != 0 && a.OwnerJobs&(1<<number) != 0
}

func (a Appearance) Items(recipe BeiyunRecipe) BeiyunRecipeItems {
	if recipe > SecretRecipe || a.ID == 0 {
		return BeiyunRecipeItems{}
	}
	items := recipe.Items()
	if a.ID == SampleID {
		return items
	}
	items.ProductID = a.Products[recipe]
	items.ProductPath = fmt.Sprintf("stackable/weaponskinforge/%d.stk", items.ProductID)
	return items
}
