//go:build !windows

package sessions

import "syscall"

// oNoFollow makes an open fail rather than follow a symlink at the final
// path component — the last guard against a tar that plants a link and
// then writes through it. Windows has no equivalent (see the other file).
const oNoFollow = syscall.O_NOFOLLOW
