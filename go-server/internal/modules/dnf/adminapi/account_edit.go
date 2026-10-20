package adminapi

const AccountEditProtocol = "account-row-v1"

type AccountEditStatus struct {
	Protocol  string `json:"protocol"`
	AccountID string `json:"accountId"`
	Busy      bool   `json:"busy"`
}
