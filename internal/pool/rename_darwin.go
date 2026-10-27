package pool

import "golang.org/x/sys/unix"

// renameat moves from to to, failing with EEXIST when to exists (RENAME_EXCL), on open directories.
func renameat(fd1 int, from string, fd2 int, to string) error {
	return unix.RenameatxNp(fd1, from, fd2, to, unix.RENAME_EXCL)
}
