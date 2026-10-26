// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

const (
	serviceName     = "bamboo.service"
	defaultUnitDir  = "/etc/systemd/system"
	defaultEnvDir   = "/etc/bamboo"
	serviceStateDir = "/var/lib/bamboo"
)

var (
	flagUnitDir string
	flagEnvDir  string
	flagNoStart bool
)

func init() {
	installServiceCmd.Flags().StringVar(&flagUnitDir, "unit-dir", defaultUnitDir, "systemd unit directory")
	installServiceCmd.Flags().StringVar(&flagEnvDir, "env-dir", defaultEnvDir, "directory for the environment file that holds the controller URL and pre-auth key")
	installServiceCmd.Flags().BoolVar(&flagNoStart, "no-start", false, "write the files and enable nothing")
	_ = installServiceCmd.Flags().MarkHidden("unit-dir")
	_ = installServiceCmd.Flags().MarkHidden("env-dir")
	uninstallServiceCmd.Flags().StringVar(&flagUnitDir, "unit-dir", defaultUnitDir, "systemd unit directory")
	_ = uninstallServiceCmd.Flags().MarkHidden("unit-dir")
	rootCmd.AddCommand(installServiceCmd)
	rootCmd.AddCommand(uninstallServiceCmd)
}

var installServiceCmd = &cobra.Command{
	Use:   "install-service",
	Short: "Install bamboo as a systemd service that starts at boot (Linux, root)",
	Long: `Write /etc/bamboo/bamboo.env (mode 0600) from --server, --auth-key,
--hostname and the like, install a systemd unit that runs "bamboo up", and
start it. The pre-auth key lives only in the environment file, not on the
unit's command line. The device identity and peer session are kept in
/var/lib/bamboo, so a restart re-registers the same peer without redeeming a
single-use key again.

Example:
  sudo BAMBOO_AUTH_KEY=bka_... bamboo install-service --server https://bamboo.example.com`,
	Args: cobra.NoArgs,
	RunE: runInstallService,
}

var uninstallServiceCmd = &cobra.Command{
	Use:   "uninstall-service",
	Short: "Stop and remove the bamboo systemd service; keeps the key and environment file",
	Args:  cobra.NoArgs,
	RunE:  runUninstallService,
}

func runInstallService(cmd *cobra.Command, _ []string) error {
	custom := cmd.Flags().Changed("unit-dir") || cmd.Flags().Changed("env-dir")
	if !custom {
		if runtime.GOOS != "linux" {
			return fmt.Errorf("install-service needs Linux with systemd; on macOS use the app under clients/apple/")
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("install-service writes under /etc and needs root; re-run with sudo (keep BAMBOO_AUTH_KEY by passing it as: sudo env BAMBOO_AUTH_KEY=... bamboo install-service ...)")
		}
	}
	if flagServer == "" && !cmd.Flags().Changed("addr") {
		return fmt.Errorf("--server (or BAMBOO_SERVER) is required, e.g. --server https://bamboo.example.com")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate bamboo binary: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return fmt.Errorf("resolve bamboo binary: %w", err)
	}

	env, err := serviceEnv()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(flagEnvDir, 0o700); err != nil {
		return err
	}
	envPath := filepath.Join(flagEnvDir, "bamboo.env")
	if err := os.WriteFile(envPath, []byte(renderEnvFile(env)), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(envPath, 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(flagUnitDir, 0o755); err != nil { //nolint:gosec // systemd unit directory
		return err
	}
	unitPath := filepath.Join(flagUnitDir, serviceName)
	if err := os.WriteFile(unitPath, []byte(renderUnit(exe, envPath)), 0o644); err != nil { //nolint:gosec // unit files are world-readable by convention
		return err
	}
	fmt.Printf("wrote %s (0600) and %s\n", envPath, unitPath)
	if flagNoStart || custom {
		return nil
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", serviceName}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed argument lists
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	fmt.Println("bamboo service enabled and started; follow it with: journalctl -u bamboo -f")
	return nil
}

func runUninstallService(cmd *cobra.Command, _ []string) error {
	custom := cmd.Flags().Changed("unit-dir")
	if !custom {
		if runtime.GOOS != "linux" || os.Geteuid() != 0 {
			return fmt.Errorf("uninstall-service needs Linux and root")
		}
		_ = exec.Command("systemctl", "disable", "--now", serviceName).Run() //nolint:gosec // fixed arguments
	}
	if err := os.Remove(filepath.Join(flagUnitDir, serviceName)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if !custom {
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}
	fmt.Println("service removed; the key and environment file in /etc/bamboo and /var/lib/bamboo are kept")
	return nil
}

// serviceEnv collects the settings the service needs. Only values that
// were given are written, so the unit falls back to the same defaults as
// the command line.
func serviceEnv() (map[string]string, error) {
	env := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			env[k] = v
		}
	}
	set("BAMBOO_SERVER", flagServer)
	if flagServer == "" {
		set("BAMBOO_ADDR", flagAddr)
		set("BAMBOO_CONTROLLER_HTTP_URL", os.Getenv("BAMBOO_CONTROLLER_HTTP_URL"))
	}
	set("BAMBOO_AUTH_KEY", flagAuthKey)
	set("BAMBOO_HOSTNAME", flagHostname)
	set("BAMBOO_IFACE", flagIface)
	if flagAuthKey == "" {
		set("BAMBOO_TENANT", flagTenant)
	}
	for k, v := range env {
		if strings.ContainsAny(v, "\n\r\x00") {
			return nil, fmt.Errorf("%s contains a control character", k)
		}
	}
	return env, nil
}

// renderEnvFile formats systemd EnvironmentFile lines, sorted so the file
// is stable across runs.
func renderEnvFile(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Written by 'bamboo install-service'. Holds the pre-auth key: keep mode 0600.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return b.String()
}

// renderUnit returns the systemd unit. XDG_CONFIG_HOME points the device
// key and peer session at /var/lib/bamboo, which systemd creates (0700).
func renderUnit(exe, envPath string) string {
	return fmt.Sprintf(`[Unit]
Description=bamboo mesh VPN client
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=%s
Environment=XDG_CONFIG_HOME=%s
StateDirectory=bamboo
StateDirectoryMode=0700
NoNewPrivileges=true
ExecStart=%s up
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, envPath, serviceStateDir, exe)
}
