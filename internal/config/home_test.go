package config

import "testing"

func TestLLMDynamixBackendDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`api-keys: ["real-key"]`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	if DefaultAuthDir != "~/.llmdynamix" {
		t.Fatalf("DefaultAuthDir = %q, want ~/.llmdynamix", DefaultAuthDir)
	}
	if cfg.RemoteManagement.PanelGitHubRepository != "https://github.com/jecruz/llmdynamix" {
		t.Fatalf("PanelGitHubRepository = %q, want LLM Dynamix repository", cfg.RemoteManagement.PanelGitHubRepository)
	}
}

func TestParseConfigBytesIgnoresHomeConfig(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
home:
  enabled: true
  host: home.example.com
  port: 444
  disable-cluster-discovery: true
  tls:
    enable: true
    server-name: home.example.com
    ca-cert: C:/certs/ca.pem
    insecure-skip-verify: true
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	if cfg.Home.Enabled {
		t.Fatal("Home.Enabled = true, want false")
	}
	if cfg.Home.Host != "" {
		t.Fatalf("Home.Host = %q, want empty", cfg.Home.Host)
	}
	if cfg.Home.Port != 0 {
		t.Fatalf("Home.Port = %d, want 0", cfg.Home.Port)
	}
	if cfg.Home.DisableClusterDiscovery {
		t.Fatal("Home.DisableClusterDiscovery = true, want false")
	}
	if cfg.Home.TLS.Enable {
		t.Fatal("Home.TLS.Enable = true, want false")
	}
	if cfg.Home.TLS.ServerName != "" {
		t.Fatalf("Home.TLS.ServerName = %q, want empty", cfg.Home.TLS.ServerName)
	}
	if cfg.Home.TLS.CACert != "" {
		t.Fatalf("Home.TLS.CACert = %q, want empty", cfg.Home.TLS.CACert)
	}
	if cfg.Home.TLS.InsecureSkipVerify {
		t.Fatal("Home.TLS.InsecureSkipVerify = true, want false")
	}
}
