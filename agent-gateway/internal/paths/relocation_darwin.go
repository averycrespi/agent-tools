package paths

import "golang.org/x/sys/unix"

func exchangeRelocation(parent int, source, destination string) error {
	return unix.RenameatxNp(parent, source, parent, destination, unix.RENAME_SWAP)
}
