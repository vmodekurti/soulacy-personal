//go:build !darwin && !linux

package safeundo

import (
	"fmt"
	"os"
)

func lockLedger(string) (*os.File, error) {
	return nil, fmt.Errorf("safe undo requires Linux or macOS ledger locking")
}
