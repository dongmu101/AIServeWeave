package localstate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UserSID returns the current process identity, without reading credentials.
// UserSID 返回当前进程身份，不读取凭据。
func UserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// Descriptor returns a protected DACL for this user and system administrators.
// Descriptor 返回仅当前用户及系统管理主体可访问的受保护 DACL。
func Descriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	sid, err := UserSID()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + sid + ")")
}

// CheckPrivate verifies effective identity ownership and conservative ACL grants.
// CheckPrivate 校验身份文件所有者及保守的 ACL 授权边界。
func CheckPrivate(path string, _ bool) error {
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() {
		return fmt.Errorf("private state is not a regular file")
	}
	return checkACL(path)
}

func checkACL(path string) error {
	sid, err := UserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	trusted := func(s string) bool { return s == sid || s == "S-1-5-18" || s == "S-1-5-32-544" }
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !trusted(owner.String()) {
		return fmt.Errorf("private state has an unexpected owner")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return fmt.Errorf("private state has no restrictive DACL")
	}
	for n := uint32(0); n < uint32(acl.AceCount); n++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, n, &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("private state has an unsupported ACL entry")
		}
		who := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trusted(who.String()) && uint32(ace.Mask)&0xf00d019f != 0 {
			return fmt.Errorf("private state is accessible outside its user")
		}
	}
	return nil
}

func ensureDirectory(dir string) error {
	_, before := os.Stat(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if before == nil {
		return checkACL(dir)
	}
	sd, err := Descriptor()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func createTemp(dir string) (*os.File, error) {
	sd, err := Descriptor()
	if err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	name := filepath.Join(dir, ".aisw-state-"+hex.EncodeToString(random[:]))
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateFile(path, windows.GENERIC_WRITE|windows.GENERIC_READ, 0, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

// Replace uses the Windows replacement operation without deleting the old file.
// Replace 使用 Windows 替换操作，不预先删除旧文件。
func Replace(source, target string) error {
	s, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(s, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
