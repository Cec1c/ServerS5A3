package pet

import (
	"errors"
	"fmt"
	"math"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

var ErrPetFeedInvalid = errors.New("pet feed is not supported by the current PVF")

// FeedDefinition is produced only by the runtime catalog. The ordinary item
// success callback applies its native skill-charge effect; the server owns
// the durable satiety increase and the inventory debit.
type FeedDefinition struct {
	itemID          int64
	path            string
	durationSeconds int64
	chargePercent   int64
	satiety         int
}

func (d FeedDefinition) ItemID() int64          { return d.itemID }
func (d FeedDefinition) Satiety() int           { return d.satiety }
func (d FeedDefinition) DurationSeconds() int64 { return d.durationSeconds }
func (d FeedDefinition) ChargePercent() int64   { return d.chargePercent }

func (c *PVFCatalog) ResolveFeed(itemID int64) (FeedDefinition, error) {
	if c == nil || c.source == nil {
		return FeedDefinition{}, ErrPetPVFSourceRequired
	}
	c.feedOnce.Do(func() {
		text, err := c.source.ReadText("stackable/stackable.lst")
		if err != nil {
			c.feedErr = err
			return
		}
		doc, err := dnfpvf.Parse("stackable/stackable.lst", text)
		if err != nil {
			c.feedErr = err
			return
		}
		c.feedPaths = make(map[int64]string)
		for _, entry := range dnfpvf.ParseList(doc) {
			if entry.ID <= 0 || entry.ID > math.MaxUint32 {
				continue
			}
			p := cleanPetPVFPath(entry.Path)
			if previous, found := c.feedPaths[entry.ID]; found && previous != p {
				c.feedErr = ErrPetFeedInvalid
				return
			}
			c.feedPaths[entry.ID] = p
		}
	})
	if c.feedErr != nil {
		return FeedDefinition{}, fmt.Errorf("feed catalog: %w", c.feedErr)
	}
	p, ok := c.feedPaths[itemID]
	if !ok || p == "" {
		return FeedDefinition{}, ErrPetFeedInvalid
	}
	p, text, err := readPetPVFReference(c.source, "stackable", p)
	if err != nil {
		return FeedDefinition{}, err
	}
	doc, err := dnfpvf.Parse(p, text)
	if err != nil {
		return FeedDefinition{}, err
	}
	kind, ok := doc.Text("stackable type")
	if !ok || normalizePetPVFType(kind) != "[feed]" {
		return FeedDefinition{}, ErrPetFeedInvalid
	}
	values := doc.Ints("int data")
	if len(values) != 3 || values[0] < 0 || values[0] > 86400 || values[1] < -100 || values[1] > 0 || values[2] <= 0 || values[2] > 100 {
		return FeedDefinition{}, fmt.Errorf("%w: item=%d int data=%v", ErrPetFeedInvalid, itemID, values)
	}
	return FeedDefinition{itemID: itemID, path: p, durationSeconds: values[0], chargePercent: values[1], satiety: int(values[2])}, nil
}
