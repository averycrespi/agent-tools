package paths

import (
	"fmt"
	"os"
	"syscall"
)

// CheckOwnerAndMode verifies that path exists, is owned by the current user,
// and has permissions no more permissive than maxMode. It returns a nil error
// only when all three hold; otherwise it returns a descriptive error whose
// message names the offending path, its current mode, and the required mode
// so the operator can apply a targeted chmod.
//
// Startup self-check invariant: os.MkdirAll does NOT narrow permissions on an
// existing directory — if a prior install created the XDG dirs at 0o750 and a
// later version bumps the MkdirAll mode to 0o700, the on-disk mode silently
// stays at 0o750 for every upgraded installation. Without this check at
// startup, tightening the MkdirAll mode is a no-op for existing users and the
// secrets/CA key/state.db directory would continue to be group-readable by
// whatever users share the primary group on the host (often wide on shared
// systems). Failing fast with an actionable chmod command is the only way to
// force the narrowing to actually take effect on upgrade.
//
// The permission comparison uses `info.Mode().Perm() & ^maxMode != 0` — i.e.
// the check fails if any bit outside maxMode is set. This is a strict subset
// comparison that only makes sense for "tight" maxMode values like 0o700
// (callers pass 0o700 throughout agent-gateway); passing a sparse mode like
// 0o705 would reject 0o700 even though 0o700 is arguably "tighter", so keep
// maxMode to the canonical tight values.
func CheckOwnerAndMode(path string, maxMode os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	mode := info.Mode().Perm()
	if mode&^maxMode.Perm() != 0 {
		return fmt.Errorf(
			"insecure permissions on %s: current mode %#o, required mode %#o or tighter; run: chmod %o %s",
			path, mode, maxMode.Perm(), maxMode.Perm(), path,
		)
	}

	// info.Sys() is documented as platform-specific. On Linux (the only target
	// platform per agent-gateway/CLAUDE.md) this is always *syscall.Stat_t; use
	// a comma-ok assertion so a non-Linux build path falls through with a
	// descriptive error instead of a nil-deref / type-assertion panic.
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read owner of %s: unsupported platform", path)
	}
	if stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf(
			"ownership mismatch on %s: owner uid %d, expected uid %d (current user); run: chown %d %s",
			path, stat.Uid, os.Getuid(), os.Getuid(), path,
		)
	}
	return nil
}
