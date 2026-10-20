package onlineevent

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

const ControlsScope = "server:online-activity-controls:v1"

// ControlDefinition lists implemented activities; it never creates PVF events.
type ControlDefinition struct {
	ID             uint16 `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Source         string `json:"source"`
	DefaultEnabled bool   `json:"defaultEnabled"`
}

type ControlRow struct {
	ControlDefinition
	Enabled          bool `json:"enabled"`
	EffectiveEnabled bool `json:"effectiveEnabled"`
	PendingRestart   bool `json:"pendingRestart"`
}

type ControlSnapshot struct {
	Revision        uint64       `json:"revision"`
	Activities      []ControlRow `json:"activities"`
	RestartRequired bool         `json:"restartRequired"`
}

type ControlChange struct {
	ID      uint16 `json:"id"`
	Enabled bool   `json:"enabled"`
}

// Controls persists selected availability while preserving the running process
// catalog. Native op108 is process-owned, so changes take effect at server start.
type Controls struct {
	mu                  sync.RWMutex
	store               repository.SettingsRepository
	definitions         []ControlDefinition
	configured          map[uint16]bool
	active              map[uint16]bool
	activeOverrides     map[uint16]bool
	configuredOverrides map[uint16]bool
	revision            uint64
}

func NewControls(ctx context.Context, store repository.SettingsRepository, definitions []ControlDefinition) (*Controls, error) {
	if store == nil {
		return nil, fmt.Errorf("activity settings repository unavailable")
	}
	c := &Controls{store: store, definitions: append([]ControlDefinition(nil), definitions...), configured: make(map[uint16]bool), active: make(map[uint16]bool), activeOverrides: make(map[uint16]bool), configuredOverrides: make(map[uint16]bool)}
	for _, d := range definitions {
		if d.ID == 0 || d.Name == "" {
			return nil, fmt.Errorf("invalid activity definition")
		}
		if _, exists := c.configured[d.ID]; exists {
			return nil, fmt.Errorf("duplicate activity %d", d.ID)
		}
		c.configured[d.ID] = d.DefaultEnabled
	}
	r, found, err := store.Load(ctx, ControlsScope)
	if err != nil {
		return nil, fmt.Errorf("load activity settings: %w", err)
	}
	if found {
		for k, v := range r.Values {
			if k == "revision" {
				c.revision, err = strconv.ParseUint(v, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid activity revision")
				}
				continue
			}
			id, parseErr := strconv.ParseUint(k, 10, 16)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid activity setting key")
			}
			if _, known := c.configured[uint16(id)]; !known {
				return nil, fmt.Errorf("unsupported persisted activity %d", id)
			}
			if v != "true" && v != "false" {
				return nil, fmt.Errorf("invalid availability for activity %d", id)
			}
			c.configured[uint16(id)] = v == "true"
			c.activeOverrides[uint16(id)] = true
			c.configuredOverrides[uint16(id)] = true
		}
	}
	for id, enabled := range c.configured {
		c.active[id] = enabled
	}
	return c, nil
}

func (c *Controls) Enabled(id uint16) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.active[id]
}

// Override distinguishes an explicit persisted selection from legacy defaults.
// This preserves independent historical catalog and gameplay environment flags
// until the operator selects an activity policy and restarts the service.
func (c *Controls) Override(id uint16) (bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.active[id], c.activeOverrides[id]
}

func (c *Controls) Snapshot() ControlSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshotLocked()
}

func (c *Controls) snapshotLocked() ControlSnapshot {
	result := ControlSnapshot{Revision: c.revision, Activities: make([]ControlRow, 0, len(c.definitions))}
	for _, d := range c.definitions {
		row := ControlRow{ControlDefinition: d, Enabled: c.configured[d.ID], EffectiveEnabled: c.active[d.ID], PendingRestart: c.configured[d.ID] != c.active[d.ID] || c.configuredOverrides[d.ID] != c.activeOverrides[d.ID]}
		result.RestartRequired = result.RestartRequired || row.PendingRestart
		result.Activities = append(result.Activities, row)
	}
	return result
}

func (c *Controls) Save(ctx context.Context, revision uint64, changes []ControlChange) (ControlSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision != c.revision {
		return ControlSnapshot{}, fmt.Errorf("活动设置已更新，请刷新后重试")
	}
	if len(changes) == 0 || len(changes) > len(c.definitions) {
		return ControlSnapshot{}, fmt.Errorf("请选择受支持的活动")
	}
	if c.revision == math.MaxUint64 {
		return ControlSnapshot{}, fmt.Errorf("activity revision exhausted")
	}
	next := make(map[uint16]bool, len(c.configured))
	for id, value := range c.configured {
		next[id] = value
	}
	seen := make(map[uint16]bool)
	overrides := make(map[uint16]bool)
	for id, owned := range c.configuredOverrides {
		overrides[id] = owned
	}
	for _, change := range changes {
		if _, supported := next[change.ID]; !supported {
			return ControlSnapshot{}, fmt.Errorf("不支持开启活动 %d", change.ID)
		}
		if seen[change.ID] {
			return ControlSnapshot{}, fmt.Errorf("活动 %d 重复", change.ID)
		}
		seen[change.ID] = true
		overrides[change.ID] = true
		next[change.ID] = change.Enabled
	}
	values := map[string]string{"revision": strconv.FormatUint(c.revision+1, 10)}
	for id, value := range next {
		if overrides[id] {
			values[strconv.Itoa(int(id))] = strconv.FormatBool(value)
		}
	}
	if err := c.store.Save(ctx, repository.SettingsRecord{Scope: ControlsScope, Values: values, UpdatedAt: time.Now()}); err != nil {
		return ControlSnapshot{}, fmt.Errorf("保存活动设置失败: %w", err)
	}
	c.configured = next
	c.configuredOverrides = overrides
	c.revision++
	return c.snapshotLocked(), nil
}
