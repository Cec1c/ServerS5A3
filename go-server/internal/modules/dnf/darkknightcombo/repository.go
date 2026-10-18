package darkknightcombo

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	settingsFormatKey   = "format"
	settingsFormatValue = "dark-knight-combo-pages-v1"
	settingsPagePrefix  = "page_"
)

func SettingsScope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + ":dark_knight_combo"
}

func LoadPages(ctx context.Context, repo dnfrepo.SettingsRepository, characterID string) ([]Page, bool, error) {
	characterID = strings.TrimSpace(characterID)
	if repo == nil || characterID == "" {
		return nil, false, fmt.Errorf("dark knight combo settings repository is unavailable")
	}
	record, found, err := repo.Load(ctx, SettingsScope(characterID))
	if err != nil || !found {
		return nil, found, err
	}
	if record.Values[settingsFormatKey] != settingsFormatValue {
		return nil, false, fmt.Errorf("unsupported dark knight combo settings format")
	}
	pages := make([]Page, 0, MaximumPageCount)
	for pageIndex := 0; pageIndex < MaximumPageCount; pageIndex++ {
		encoded := strings.TrimSpace(record.Values[fmt.Sprintf("%s%d", settingsPagePrefix, pageIndex)])
		if encoded == "" {
			continue
		}
		block, decodeErr := base64.StdEncoding.Strict().DecodeString(encoded)
		if decodeErr != nil {
			return nil, false, fmt.Errorf("decode dark knight combo page %d: %w", pageIndex, decodeErr)
		}
		page, parseErr := ParsePageBlock(block)
		if parseErr != nil {
			return nil, false, fmt.Errorf("decode dark knight combo page %d: %w", pageIndex, parseErr)
		}
		if int(page.Index) != pageIndex {
			return nil, false, fmt.Errorf("decode dark knight combo page %d: page index=%d", pageIndex, page.Index)
		}
		pages = append(pages, page)
	}
	if len(pages) == 0 {
		return nil, false, nil
	}
	return pages, true, nil
}

func SavePages(ctx context.Context, repo dnfrepo.SettingsRepository, characterID string, pages []Page, updatedAt time.Time) error {
	record, err := BuildSettingsRecord(characterID, pages, updatedAt)
	if err != nil {
		return err
	}
	if repo == nil {
		return ErrPageBlockInvalid
	}
	return repo.Save(ctx, record)
}

// BuildSettingsRecord encodes both independent combo pages without writing
// them. Character creation uses this builder so the combo state is committed
// in the same aggregate transaction as the new character and its skill state.
func BuildSettingsRecord(characterID string, pages []Page, updatedAt time.Time) (dnfrepo.SettingsRecord, error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || len(pages) == 0 || len(pages) > MaximumPageCount {
		return dnfrepo.SettingsRecord{}, ErrPageBlockInvalid
	}
	values := map[string]string{settingsFormatKey: settingsFormatValue}
	sorted := append([]Page(nil), pages...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].Index < sorted[right].Index })
	for _, page := range sorted {
		block, err := EncodePageBlock(page)
		if err != nil {
			return dnfrepo.SettingsRecord{}, err
		}
		key := fmt.Sprintf("%s%d", settingsPagePrefix, page.Index)
		if _, exists := values[key]; exists {
			return dnfrepo.SettingsRecord{}, ErrPageBlockInvalid
		}
		values[key] = base64.StdEncoding.EncodeToString(block)
	}
	return dnfrepo.SettingsRecord{
		Scope:     SettingsScope(characterID),
		Values:    values,
		UpdatedAt: updatedAt.UTC(),
	}, nil
}

func SavePage(ctx context.Context, repo dnfrepo.SettingsRepository, characterID string, page Page, updatedAt time.Time) error {
	page, err := NormalizePage(page)
	if err != nil {
		return err
	}
	pages, found, err := LoadPages(ctx, repo, characterID)
	if err != nil {
		return err
	}
	if !found {
		pages = nil
	}
	replaced := false
	for index := range pages {
		if pages[index].Index == page.Index {
			pages[index] = page
			replaced = true
			break
		}
	}
	if !replaced {
		pages = append(pages, page)
	}
	return SavePages(ctx, repo, characterID, pages, updatedAt)
}
