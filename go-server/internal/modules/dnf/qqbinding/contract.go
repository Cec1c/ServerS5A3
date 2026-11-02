// Package qqbinding defines the account ownership contract shared with the game.
package qqbinding

import (
	"errors"
	"regexp"
	"sort"
)

type Settings struct {
	Enabled      bool     `json:"enabled"` // Require binding at login; retained for existing clients.
	AllowBinding bool     `json:"allowBinding"`
	Groups       []string `json:"groups"`
	Revision     uint64   `json:"revision"`
}

var Number = regexp.MustCompile(`^[1-9][0-9]{4,11}$`)

func (s Settings) BindingAllowed() bool { return s.Enabled || s.AllowBinding }

func (s *Settings) Normalize() error {
	if len(s.Groups) > 32 || (s.BindingAllowed() && len(s.Groups) == 0) {
		return errors.New("请配置 1 至 32 个允许绑定的群")
	}
	seen := map[string]bool{}
	groups := make([]string, 0, len(s.Groups))
	for _, g := range s.Groups {
		if !Number.MatchString(g) {
			return errors.New("群号格式无效")
		}
		if !seen[g] {
			groups = append(groups, g)
			seen[g] = true
		}
	}
	sort.Strings(groups)
	s.Groups = groups
	return nil
}

type Complete struct {
	Account  string `json:"account,omitempty"` // Legacy commands only; six-digit codes resolve the account.
	Code     string `json:"code"`
	QQ       string `json:"qq"`
	Group    string `json:"group"`
	Revision uint64 `json:"revision"`
}
type Query struct {
	Search string `json:"search"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}
type Binding struct {
	AccountID  string `json:"accountId"`
	Account    string `json:"account"`
	QQ         string `json:"qq"`
	Group      string `json:"group"`
	BoundAt    int64  `json:"boundAt"`
	State      string `json:"state"`
	Revision   uint64 `json:"revision"`
	UpdatedAt  int64  `json:"updatedAt"`
	CheckedAt  int64  `json:"checkedAt"`
	CheckCause string `json:"checkCause"`
}

const MembershipIntervalSeconds = 6 * 60 * 60

// Inactive records retain both unique identities. Only an explicit delete
// releases them. Administrative disable is never undone by player rebinding.
type Change struct {
	AccountID string `json:"accountId"`
	QQ        string `json:"qq"`
	Revision  uint64 `json:"revision"`
	Action    string `json:"action"`
	RequestID string `json:"requestId"`
}

func (c Change) Validate() error {
	if c.AccountID == "" || len(c.AccountID) > 64 || !Number.MatchString(c.QQ) || c.Revision == 0 || !regexp.MustCompile(`^[a-zA-Z0-9-]{16,80}$`).MatchString(c.RequestID) || (c.Action != "enable" && c.Action != "disable" && c.Action != "delete") {
		return errors.New("绑定操作参数无效")
	}
	return nil
}

type Check struct {
	AccountID        string   `json:"accountId"`
	QQ               string   `json:"qq"`
	Revision         uint64   `json:"revision"`
	SettingsRevision uint64   `json:"settingsRevision"`
	Lease            string   `json:"lease"`
	Groups           []string `json:"groups"`
}
type CheckResult struct {
	AccountID        string `json:"accountId"`
	QQ               string `json:"qq"`
	Revision         uint64 `json:"revision"`
	SettingsRevision uint64 `json:"settingsRevision"`
	Lease            string `json:"lease"`
	Outcome          string `json:"outcome"` // member, absent, unknown
	Group            string `json:"group"`
}
type Page struct {
	Rows    []Binding `json:"rows"`
	HasMore bool      `json:"hasMore"`
	Total   int64     `json:"total"`
}
