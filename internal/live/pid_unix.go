//go:build !windows

package live

import "syscall"

// pidAlive reports whether the process exists. Signal 0 performs the
// permission and existence checks without delivering anything.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
