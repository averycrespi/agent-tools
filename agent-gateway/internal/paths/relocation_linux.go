package paths

import "golang.org/x/sys/unix"

func exchangeRelocation(parent int, source, destination string) error {
	return unix.Renameat2(parent, source, parent, destination, unix.RENAME_EXCHANGE)
}
