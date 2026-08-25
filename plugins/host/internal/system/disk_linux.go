//go:build linux

package system

import (
	"syscall"

	"github.com/pkg/errors"
)

func diskUsedPercent(path string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, errors.WithStack(err)
	}

	if stat.Blocks == 0 {
		return 0, errors.Errorf("no blocks reported for %q", path)
	}

	return 100 * (1 - float64(stat.Bavail)/float64(stat.Blocks)), nil
}
