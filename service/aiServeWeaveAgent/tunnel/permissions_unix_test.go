//go:build !windows

package tunnel_test

import "os"

func exposeIdentityFile(path string, mode os.FileMode) error { return os.Chmod(path, mode) }
