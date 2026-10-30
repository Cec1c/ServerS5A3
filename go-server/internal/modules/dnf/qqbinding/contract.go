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
	Account  string `json:"account"`
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
	AccountID string `json:"accountId"`
	Account   string `json:"account"`
	QQ        string `json:"qq"`
	Group     string `json:"group"`
	BoundAt   int64  `json:"boundAt"`
}
type Page struct {
	Rows    []Binding `json:"rows"`
	HasMore bool      `json:"hasMore"`
	Total   int64     `json:"total"`
}
