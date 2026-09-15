package bot

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCredentialWindowsACL(t *testing.T) {
	dir := credentialTestEnv(t)
	if err := saveCredentials(credentials{BotToken: "test-secret"}); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(filepath.Join(dir, "credentials.json"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	want := "D:P(A;;FA;;;" + user.User.Sid.String() + ")"
	if strings.Replace(sd.String(), "D:PAI", "D:P", 1) != want {
		t.Fatalf("unexpected credential DACL: %s", sd.String())
	}
}
