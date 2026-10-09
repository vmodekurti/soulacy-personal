package runtime

import (
	"encoding/json"
	"strings"
)

type managedActionReceipt struct {
	Status   string `json:"status"`
	Verified bool   `json:"verified"`
	Detail   string `json:"detail,omitempty"`
}

// verifiedManagedActionResult turns a successful typed host mutation into the
// structured receipt consumed by the task contract. The caller only invokes
// this after the installer or registration command has completed its own
// persistence and verification checks.
func verifiedManagedActionResult(detail string) string {
	receipt := managedActionReceipt{
		Status:   "committed",
		Verified: true,
		Detail:   strings.TrimSpace(detail),
	}
	b, _ := json.Marshal(receipt)
	return string(b)
}

func managedActionResultDetail(result string) string {
	var receipt managedActionReceipt
	if json.Unmarshal([]byte(result), &receipt) == nil && receipt.Verified {
		return strings.TrimSpace(receipt.Detail)
	}
	return strings.TrimSpace(result)
}
