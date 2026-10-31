package localcontrol

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"unsafe"

	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
	"golang.org/x/sys/windows"
)

type pipeListener struct {
	mu     sync.Mutex
	handle windows.Handle
	name   string
	closed bool
}

func address(key string) (string, error) {
	hash, err := endpointKey(key)
	if err != nil {
		return "", err
	}
	sid, err := localstate.UserSID()
	return `\\.\pipe\aisw-agent-` + sid + "-" + hash, err
}

func makePipe(name string, first bool) (windows.Handle, error) {
	sid, err := localstate.UserSID()
	if err != nil {
		return 0, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + sid + ")")
	if err != nil {
		return 0, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(p, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS, 2, 65536, 64, 5000, &sa)
}

func listen(key string) (listener, error) {
	name, err := address(key)
	if err != nil {
		return nil, err
	}
	h, err := makePipe(name, true)
	if err != nil {
		return nil, err
	}
	return &pipeListener{handle: h, name: name}, nil
}

func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	_ = windows.CancelIoEx(l.handle, nil)
	return nil
}

func (l *pipeListener) Accept() (connection, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(event)
	ov := windows.Overlapped{HEvent: event}
	l.mu.Lock()
	h := l.handle
	if l.closed {
		l.mu.Unlock()
		windows.CloseHandle(h)
		return nil, net.ErrClosed
	}
	// Register the pending operation before Close can cancel it.
	// 在 Close 可取消操作前，先登记挂起的连接操作。
	err = windows.ConnectNamedPipe(h, &ov)
	l.mu.Unlock()
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		var n uint32
		err = windows.GetOverlappedResult(h, &ov, &n, true)
	}
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		windows.CloseHandle(h)
		return nil, err
	}
	var pid uint32
	if err = windows.GetNamedPipeClientProcessId(h, &pid); err == nil {
		err = checkPeer(pid)
	}
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		windows.CloseHandle(h)
		return nil, net.ErrClosed
	}
	next, err := makePipe(l.name, false)
	if err == nil {
		l.handle = next
	}
	l.mu.Unlock()
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), l.name), nil
}

func checkPeer(pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	u, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sid, err := localstate.UserSID()
	if err != nil {
		return err
	}
	if u.User.Sid.String() != sid {
		return errors.New("local control peer is not this user")
	}
	return nil
}

func dial(ctx context.Context, key string) (connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := address(key)
	if err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	var pid uint32
	if err = windows.GetNamedPipeServerProcessId(h, &pid); err == nil {
		err = checkPeer(pid)
	}
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}
