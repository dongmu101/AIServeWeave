//go:build !windows

package localcontrol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type unixListener struct{ *net.UnixListener }

func (l unixListener) Accept() (connection, error) { return l.AcceptUnix() }

func address(key string) (string, error) {
	hash, err := endpointKey(key)
	return filepath.Join("/tmp", fmt.Sprintf("aisw-%d", os.Getuid()), hash+".sock"), err
}

func listen(key string) (listener, error) {
	path, err := address(key)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("unsafe local control directory")
	}
	if info, err := os.Lstat(path); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("local control path is occupied")
		}
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			c.Close()
			return nil, errors.New("Agent already running for this configuration")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return unixListener{l}, nil
}

func dial(ctx context.Context, key string) (connection, error) {
	path, err := address(key)
	if err != nil {
		return nil, err
	}
	c, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return c, nil
}
