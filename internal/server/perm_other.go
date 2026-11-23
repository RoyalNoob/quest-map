//go:build !windows

package server

import "os"

// restrictDir makes dir usable by its owner only, so the token in server.json stays private.
func restrictDir(dir string) error { return os.Chmod(dir, 0o700) }
