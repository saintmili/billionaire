package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/testdb"
)

// The subprocess boundary exercises main's signals, not only Execute cancellation.
// Optional systemd mode runs the same journey in an isolated transient user unit.
func TestDeploymentFixtureLifecycle(t *testing.T) {
	dsn := testdb.New(t)
	dir, err := os.MkdirTemp("/tmp", "billionaire-deploy-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, "billionaire")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/billionaire")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(configs.Example+"\nengine:\n  lookback: 1\n  queue_capacity: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "control.sock")
	fixture := filepath.Join(dir, "market.json")
	fixtureData, err := os.ReadFile("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, fixtureData, 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "BILLIONAIRE_") {
			env = append(env, value)
		}
	}
	env = append(env, "BILLIONAIRE_DATABASE_URL="+dsn)
	args := func(cmd ...string) []string {
		return append([]string{"--config", cfg, "--control-socket", socket, "--output", "json"}, cmd...)
	}
	invoke := func(cmd ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		process := exec.CommandContext(ctx, binary, args(cmd...)...)
		process.Env = env
		return process.Output()
	}
	must := func(cmd ...string) []byte {
		t.Helper()
		out, err := invoke(cmd...)
		if err != nil {
			t.Fatalf("%s failed: %v", cmd[0], err)
		}
		return out
	}
	must("config", "validate")
	must("init")
	invalid := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(invalid, []byte("schema_version: invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("start", "--fixture", fixture, "--config", invalid); err == nil {
		t.Fatal("invalid startup accepted")
	}
	validEnv := env
	env = append(append([]string{}, env[:len(env)-1]...), "BILLIONAIRE_DATABASE_URL=postgres://nobody@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1")
	if _, err := invoke("start", "--fixture", fixture); err == nil {
		t.Fatal("unavailable database startup accepted")
	}
	env = validEnv
	type state struct {
		Engine       string              `json:"engine"`
		Connectivity string              `json:"engine_connectivity"`
		Health       *app.HealthSnapshot `json:"health"`
		Paused       bool                `json:"kill_switch"`
	}
	status := func() state {
		t.Helper()
		var s state
		if err := json.Unmarshal(must("status"), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status(); s.Engine != "stopped" {
		t.Fatalf("initial owner %s", s.Engine)
	}
	create := []string{"paper", "account", "create", "--name", "Deployment", "--balance", "1000", "--request-id", "deploy-create"}
	if _, err := invoke(create...); err == nil {
		t.Fatal("missing owner admitted mutation")
	}
	service := os.Getenv("BILLIONAIRE_TEST_SYSTEMD") == "1"
	unit := fmt.Sprintf("billionaire-fixture-%d-%d.service", os.Getpid(), time.Now().UnixNano())
	var session, accountID string
	for attempt := 0; attempt < 2; attempt++ {
		var process *exec.Cmd
		if service {
			// EnvironmentFile keeps the DSN out of argv and process evidence.
			envfile := filepath.Join(dir, "service.env")
			if strings.ContainsAny(dsn, "\n\r\"\\") {
				t.Fatal("test DSN cannot be encoded in systemd environment file")
			}
			if err := os.WriteFile(envfile, []byte("BILLIONAIRE_DATABASE_URL=\""+dsn+"\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			command := []string{"--user", "--wait", "--pipe", "--collect", "--unit", unit, "--property=EnvironmentFile=" + envfile, "--property=WorkingDirectory=" + dir, "--property=UMask=0077", "--property=Restart=on-failure", "--property=RestartSec=10s", "--property=StartLimitIntervalSec=300", "--property=StartLimitBurst=3", "--property=SuccessExitStatus=130", "--property=TimeoutStopSec=30s", "--property=NoNewPrivileges=true", "--property=ProtectSystem=strict", "--property=ProtectHome=true", "--property=ReadWritePaths=" + dir, binary}
			command = append(command, args("start", "--fixture", fixture, "--hold")...)
			process = exec.Command("systemd-run", command...)
		} else {
			process = exec.Command(binary, args("start", "--fixture", fixture, "--hold")...)
			process.Env = env
		}
		logfile, err := os.Create(filepath.Join(dir, fmt.Sprintf("owner-%d.log", attempt)))
		if err != nil {
			t.Fatal(err)
		}
		process.Stdout = logfile
		process.Stderr = logfile
		if err = process.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- process.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			if service {
				if out, err := exec.Command("systemctl", "--user", "stop", unit).CombinedOutput(); err != nil {
					t.Errorf("service stop: %v %s", err, out)
				}
			} else {
				_ = process.Process.Signal(syscall.SIGTERM)
			}
			select {
			case err := <-done:
				if err != nil {
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 130 {
						t.Errorf("shutdown: %v", err)
					}
				}
			case <-time.After(35 * time.Second):
				_ = process.Process.Kill()
				t.Error("shutdown timed out")
			}
			logfile.Close()
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(15 * time.Second)
		var ready state
		for time.Now().Before(deadline) {
			ready = status()
			if ready.Connectivity == "connected" && ready.Health != nil && len(ready.Health.Feeds) == 4 && allDisconnected(ready.Health.Feeds) {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if ready.Engine != "running" || ready.Connectivity != "connected" || ready.Health == nil || ready.Health.Ownership != "held" || ready.Health.Session == session {
			t.Fatalf("owner not recovered: %+v", ready)
		}
		if ready.Health.EntriesReady || !allDisconnected(ready.Health.Feeds) {
			t.Fatal("exhausted fixture claimed entry readiness")
		}
		session = ready.Health.Session
		for _, path := range []string{dir, socket} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			want := os.FileMode(0600)
			if path == dir {
				want = 0700
			}
			if info.Mode().Perm() != want {
				t.Fatalf("permissions %s: %o", path, info.Mode().Perm())
			}
		}
		first := must(create...)
		if string(first) != string(must(create...)) {
			t.Fatal("creation retry changed result")
		}
		var account paper.Account
		if err := json.Unmarshal(first, &account); err != nil || account.ID == "" {
			t.Fatal("missing account")
		}
		if accountID != "" && accountID != account.ID {
			t.Fatal("restart lost account identity")
		}
		accountID = account.ID
		must("control", "outcome", "deploy-create")
		var accounts []paper.Account
		if err := json.Unmarshal(must("paper", "accounts"), &accounts); err != nil || len(accounts) != 1 {
			t.Fatal("restart duplicated account")
		}
		must("kill-switch", "on", "--request-id", "deploy-pause")
		must("kill-switch", "off", "--request-id", "deploy-resume")
		must("kill-switch", "on", "--request-id", "deploy-pause")
		if status().Paused {
			t.Fatal("historical pause receipt reapplied")
		}
		if _, err := invoke(append(create, "--offline")...); err == nil {
			t.Fatal("offline bypassed owner")
		}
		if _, err := invoke("start", "--fixture", fixture, "--control-socket", filepath.Join(dir, "second.sock")); err == nil {
			t.Fatal("second owner admitted")
		}
		if status().Health.Session != session {
			t.Fatal("second owner disturbed first")
		}
		stop()
		if _, err := os.Stat(socket); !os.IsNotExist(err) {
			t.Fatalf("socket survived shutdown: %v", err)
		}
		if status().Engine != "stopped" {
			t.Fatal("ownership survived shutdown")
		}
	}
	if _, err := invoke(create...); err == nil {
		t.Fatal("stopped owner silently replaced")
	}
}

func TestDeploymentUnitVerification(t *testing.T) {
	tool, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze unavailable")
	}
	raw, err := os.ReadFile("../../deploy/billionaire.service")
	if err != nil {
		t.Fatal(err)
	}
	// Validate the exact unit syntax and dependencies using a harmless existing
	// executable. Production installation separately verifies the installed binary.
	raw = []byte(strings.ReplaceAll(string(raw), "/usr/local/bin/billionaire", "/usr/bin/true"))
	path := filepath.Join(t.TempDir(), "billionaire.service")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(tool, "verify", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unit verification: %v %s", err, out)
	}
}

func allDisconnected(feeds []app.FeedHealth) bool {
	for _, feed := range feeds {
		if feed.Status != "disconnected" {
			return false
		}
	}
	return true
}
