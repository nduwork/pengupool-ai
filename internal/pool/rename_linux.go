package pool

import "golang.org/x/sys/unix"

// renameat moves from to to, failing with EEXIST when to exists (RENAME_NOREPLACE), on open directories.
func renameat(fd1 int, from string, fd2 int, to string) error {
	return unix.Renameat2(fd1, from, fd2, to, unix.RENAME_NOREPLACE)
}
