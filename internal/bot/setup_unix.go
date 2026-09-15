//go:build !windows

package bot

import "os"

func protectCredentials(path string) error {
	return os.Chmod(path, 0600)
}
