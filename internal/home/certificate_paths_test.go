package home

import (
	"path/filepath"
	"testing"
)

func TestDefaultCertificatePathsUseLLMDynamixDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	paths, err := defaultCertificatePaths()
	if err != nil {
		t.Fatalf("defaultCertificatePaths() error = %v", err)
	}

	wantDir := filepath.Join(home, ".llmdynamix")
	if paths.Dir != wantDir {
		t.Fatalf("Dir = %q, want %q", paths.Dir, wantDir)
	}
	if paths.ClientCert != filepath.Join(wantDir, "client-crt.pem") {
		t.Fatalf("ClientCert = %q, want file under %q", paths.ClientCert, wantDir)
	}
	if paths.ClientKey != filepath.Join(wantDir, "client-key.pem") {
		t.Fatalf("ClientKey = %q, want file under %q", paths.ClientKey, wantDir)
	}
	if paths.CACert != filepath.Join(wantDir, "home-ca-crt.pem") {
		t.Fatalf("CACert = %q, want file under %q", paths.CACert, wantDir)
	}
}
