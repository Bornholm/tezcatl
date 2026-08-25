//go:build !linux

package system

import "github.com/pkg/errors"

// The collector is Linux only (it also reads /proc); this stub keeps the
// package compiling for the other release targets, where enabling
// metrics.system fails at startup with a clear error.
func diskUsedPercent(_ string) (float64, error) {
	return 0, errors.New("system metrics collection is only supported on linux")
}
