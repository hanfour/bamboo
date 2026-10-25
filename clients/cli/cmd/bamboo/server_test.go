// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func TestResolveTarget(t *testing.T) {
	cases := []struct {
		name, server, addr, httpEnv string
		want                        target
		wantErr                     bool
	}{
		{name: "https default port", server: "https://bamboo.example.com",
			want: target{grpcAddr: "bamboo.example.com:443", tls: true, httpBase: "https://bamboo.example.com"}},
		{name: "https explicit port", server: "https://bamboo.example.com:8443/",
			want: target{grpcAddr: "bamboo.example.com:8443", tls: true, httpBase: "https://bamboo.example.com:8443"}},
		{name: "http keeps plaintext", server: "http://10.0.0.5:8081",
			want: target{grpcAddr: "10.0.0.5:8081", tls: false, httpBase: "http://10.0.0.5:8081"}},
		{name: "legacy addr", addr: "ctrl:8080",
			want: target{grpcAddr: "ctrl:8080", httpBase: "http://localhost:8081"}},
		{name: "legacy addr with http env", addr: "ctrl:8080", httpEnv: "http://ctrl:8081",
			want: target{grpcAddr: "ctrl:8080", httpBase: "http://ctrl:8081"}},
		{name: "bare host rejected", server: "bamboo.example.com", wantErr: true},
		{name: "ftp rejected", server: "ftp://x", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveTarget(c.server, c.addr, c.httpEnv)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestApplyEnv(t *testing.T) {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	var server, key, host string
	fs.StringVar(&server, "server", "", "")
	fs.StringVar(&key, "auth-key", "", "")
	fs.StringVar(&host, "hostname", "default-host", "")
	t.Setenv("BAMBOO_SERVER", "https://env.example.com")
	t.Setenv("BAMBOO_AUTH_KEY", "bka_x_y")
	t.Setenv("BAMBOO_HOSTNAME", "from-env")
	if err := fs.Parse([]string{"--hostname", "from-flag"}); err != nil {
		t.Fatal(err)
	}
	if err := applyEnv(fs); err != nil {
		t.Fatal(err)
	}
	if server != "https://env.example.com" || key != "bka_x_y" {
		t.Errorf("env not applied: server=%q key=%q", server, key)
	}
	if host != "from-flag" {
		t.Errorf("flag must beat env, got %q", host)
	}
}

func TestPeerSessionPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	a := &peerSession{}
	a.loadFrom(path) // no file yet
	exp := time.Now().Add(time.Hour)
	a.set("tok-1", exp)

	b := &peerSession{}
	b.loadFrom(path)
	if b.Token() != "tok-1" || !b.live(time.Now()) {
		t.Fatalf("restored token = %q live=%v", b.Token(), b.live(time.Now()))
	}
	b.set("tok-2", exp)
	c := &peerSession{}
	c.loadFrom(path)
	if c.Token() != "tok-2" {
		t.Fatalf("renewal not persisted, got %q", c.Token())
	}

	expired := &peerSession{}
	expired.loadFrom(path)
	expired.set("old", time.Now().Add(-time.Minute))
	again := &peerSession{}
	again.loadFrom(path)
	if again.live(time.Now()) {
		t.Fatal("an expired token must not count as live")
	}
}

func TestRenderEnvFileAndUnit(t *testing.T) {
	env := renderEnvFile(map[string]string{"BAMBOO_SERVER": "https://x", "BAMBOO_AUTH_KEY": "bka_a_b"})
	if !strings.Contains(env, "BAMBOO_AUTH_KEY=bka_a_b\nBAMBOO_SERVER=https://x\n") {
		t.Errorf("env file not sorted/complete:\n%s", env)
	}
	unit := renderUnit("/usr/local/bin/bamboo", "/etc/bamboo/bamboo.env")
	for _, want := range []string{
		"EnvironmentFile=/etc/bamboo/bamboo.env",
		"ExecStart=/usr/local/bin/bamboo up\n",
		"XDG_CONFIG_HOME=/var/lib/bamboo",
		"Restart=always",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "bka_") {
		t.Error("the pre-auth key must not appear in the unit file")
	}
}

func TestServiceEnvRejectsControlChars(t *testing.T) {
	old := flagHostname
	defer func() { flagHostname = old }()
	flagHostname = "bad\nBAMBOO_AUTH_KEY=evil"
	if _, err := serviceEnv(); err == nil {
		t.Fatal("a newline in a value could inject another variable")
	}
}

func TestInstallServiceWritesFilesWithSafeModes(t *testing.T) {
	dir := t.TempDir()
	unitDir, envDir := filepath.Join(dir, "units"), filepath.Join(dir, "etc")
	oServer, oKey, oHost := flagServer, flagAuthKey, flagHostname
	defer func() { flagServer, flagAuthKey, flagHostname = oServer, oKey, oHost }()
	flagServer, flagAuthKey, flagHostname = "https://bamboo.example.com", "bka_k_s", "box-1"

	// Call the runner directly: PersistentPreRunE would re-resolve flags.
	if err := installServiceCmd.Flags().Set("unit-dir", unitDir); err != nil {
		t.Fatal(err)
	}
	if err := installServiceCmd.Flags().Set("env-dir", envDir); err != nil {
		t.Fatal(err)
	}
	if err := runInstallService(installServiceCmd, nil); err != nil {
		t.Fatalf("runInstallService: %v", err)
	}
	st, err := osStat(filepath.Join(envDir, "bamboo.env"))
	if err != nil {
		t.Fatal(err)
	}
	if st != 0o600 {
		t.Errorf("env file mode = %o, want 600", st)
	}
	if _, err := osStat(filepath.Join(unitDir, "bamboo.service")); err != nil {
		t.Fatalf("unit not written: %v", err)
	}
}

func osStat(path string) (os.FileMode, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Mode().Perm(), nil
}
