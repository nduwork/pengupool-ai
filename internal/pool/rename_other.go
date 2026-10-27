//go:build !linux && !darwin

package pool

import (
	"errors"
	"syscall"
)

// renameat is not available here: no rename that refuses to replace, so Explorer's rename is refused.
// ponytail: a Lstat-then-rename would race; add the platform's no-replace rename when a host needs it.
func renameat(int, string, int, string) error {
	return errors.New("renaming is supported on macOS and Linux only: " + syscall.ENOSYS.Error())
}
