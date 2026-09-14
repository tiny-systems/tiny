//go:build windows

package sessions

// oNoFollow is a no-op on Windows, which has no O_NOFOLLOW. The path
// checks in untarInto are the protection there.
const oNoFollow = 0
