// Package epicbook owns the PVF recipes for crafting equipment from book pieces.
package epicbook

import (
	"fmt"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const CatalogPath = "etc/epicpieceinfo.etc"

type Material struct {
	ItemID uint32
	Count  uint32
}

type Recipe struct {
	OutputID  uint32
	PieceID   uint32
	Materials []Material
}

// Catalog indexes by output item ID: the pinned native 788 writer sends the
// selected record's first DWORD, populated from [output], not a piece or row ID.
// It deliberately does not perform grants or invent an account debit boundary.
type Catalog struct {
	recipes map[uint32]Recipe
}

func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.recipes)
}

func (c *Catalog) Lookup(outputID uint32) (Recipe, bool) {
	if c == nil {
		return Recipe{}, false
	}
	r, ok := c.recipes[outputID]
	r.Materials = append([]Material(nil), r.Materials...)
	return r, ok
}

func LoadCatalog(source pvf.Source) (*Catalog, error) {
	if source == nil {
		return nil, pvf.ErrSourceRequired
	}
	text, err := source.ReadText(CatalogPath)
	if err != nil {
		return nil, err
	}
	doc, err := pvf.Parse(CatalogPath, text)
	if err != nil {
		return nil, err
	}
	return parseCatalog(doc)
}

func parseCatalog(doc *pvf.Document) (*Catalog, error) {
	invalid := func(reason string) (*Catalog, error) {
		return nil, fmt.Errorf("%s: invalid equipment recipe catalog: %s", CatalogPath, reason)
	}
	pieces, outputs := map[uint32]uint32{}, map[uint32]uint32{}
	c := &Catalog{recipes: map[uint32]Recipe{}}
	var drop, recipes, info, listOpen, recipeOpen, sawDrop, sawRecipes, sawList bool
	var pending Recipe
	finish := func() bool {
		if pending.OutputID == 0 || len(pending.Materials) == 0 {
			return false
		}
		if _, duplicate := c.recipes[pending.OutputID]; duplicate {
			return false
		}
		c.recipes[pending.OutputID] = pending
		return true
	}
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		values := doc.Tokens[section.Start:section.End]
		for _, token := range values {
			if token.Kind != pvf.TokenInt || token.Int <= 0 || token.Int > math.MaxInt32 {
				return invalid("nonpositive, noninteger or out-of-range value in " + name)
			}
		}
		if name != "piece list" && name != "output" && name != "recipe" && len(values) != 0 {
			return invalid("unexpected values in " + name)
		}
		switch name {
		case "equipment piece drop info":
			if drop || recipes || sawDrop {
				return invalid("duplicate or nested drop section")
			}
			drop, sawDrop = true, true
		case "/equipment piece drop info":
			if !drop || !sawList || listOpen {
				return invalid("unmatched or empty drop section")
			}
			drop = false
		case "piece list":
			if !drop || sawList || len(values) == 0 || len(values)%2 != 0 {
				return invalid("piece list scope or pair count")
			}
			sawList, listOpen = true, true
			for i := 0; i < len(values); i += 2 {
				output, piece := uint32(values[i].Int), uint32(values[i+1].Int)
				if outputs[output] != 0 || pieces[piece] != 0 {
					return invalid("duplicate output or piece")
				}
				outputs[output], pieces[piece] = piece, output
			}
		case "/piece list":
			if !drop || !listOpen {
				return invalid("unmatched piece list close")
			}
			listOpen = false
		case "equipment recipe info":
			if drop || recipes || sawRecipes || !sawDrop {
				return invalid("recipe section scope")
			}
			recipes, sawRecipes = true, true
		case "/equipment recipe info":
			if !recipes || info {
				return invalid("unmatched recipe section close")
			}
			recipes = false
		case "info":
			if !recipes || info {
				return invalid("info scope")
			}
			info, pending = true, Recipe{}
		case "/info":
			if !info || recipeOpen || !finish() {
				return invalid("incomplete or duplicate info")
			}
			info = false
		case "output":
			if !info || pending.OutputID != 0 || len(values) != 1 {
				return invalid("output scope or cardinality")
			}
			pending.OutputID = uint32(values[0].Int)
		case "recipe":
			if !info || pending.OutputID == 0 || len(pending.Materials) != 0 || len(values) == 0 || len(values)%2 != 0 {
				return invalid("recipe scope or pair count")
			}
			seen := map[uint32]bool{}
			for i := 0; i < len(values); i += 2 {
				id := uint32(values[i].Int)
				if seen[id] {
					return invalid("duplicate material")
				}
				seen[id] = true
				pending.Materials = append(pending.Materials, Material{ItemID: id, Count: uint32(values[i+1].Int)})
			}
			recipeOpen = true
		case "/recipe":
			if !info || !recipeOpen {
				return invalid("unmatched recipe close")
			}
			recipeOpen = false
		default:
			return invalid("unknown section " + name)
		}
	}
	if drop || recipes || info || !sawDrop || !sawRecipes || len(c.recipes) == 0 || len(c.recipes) != len(outputs) {
		return invalid("incomplete catalog or recipe coverage")
	}
	for output, r := range c.recipes {
		piece := outputs[output]
		if piece == 0 {
			return invalid("recipe output absent from piece list")
		}
		found := false
		for _, m := range r.Materials {
			if pieces[m.ItemID] != 0 && m.ItemID != piece {
				return invalid("recipe consumes another output's piece")
			}
			found = found || m.ItemID == piece
		}
		if !found {
			return invalid("recipe lacks corresponding piece")
		}
		r.PieceID = piece
		c.recipes[output] = r
	}
	return c, nil
}
