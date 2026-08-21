//go:build windows

package live

import "os"

// pidAlive is best-effort on Windows: FindProcess succeeds for any pid, so
// this only rejects obviously invalid ones and leans on the staleness check.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}
