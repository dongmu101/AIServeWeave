package tunnel_test

import (
	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
	"golang.org/x/sys/windows"
	"os"
)

func exposeIdentityFile(path string, _ os.FileMode) error {
	sid, err := localstate.UserSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
