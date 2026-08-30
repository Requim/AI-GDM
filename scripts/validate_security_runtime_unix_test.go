//go:build !windows

package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSecurityMainConcurrentRunsIsolateManagedContainers(t *testing.T) {
	shell := securityGateShell(t)
	root := newSecurityGateRepository(t)
	state := newFakeSecurityRuntime(t)
	foreign := seedSecurityForeignContainer(t, state)
	type result struct {
		output string
		err    error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			output, err := runSecurityGate(shell, root, "validate-security.sh", state, nil)
			results <- result{output: output, err: err}
		}()
	}
	group.Wait()
	close(results)
	for item := range results {
		if item.err != nil || !strings.Contains(item.output, "P9.1 安全门禁通过") {
			t.Fatalf("并发主门禁失败: error=%v output=%s", item.err, item.output)
		}
	}
	assertSecurityManagedLifecycle(t, state, 8)
	assertSecurityCIDFilesUnique(t, state.dockerLog, 8)
	assertSecurityForeignContainer(t, foreign)
}

func TestSecurityMainSignalsCleanExactManagedContainers(t *testing.T) {
	for _, item := range []struct {
		name string
		sig  syscall.Signal
	}{{"hup", syscall.SIGHUP}, {"int", syscall.SIGINT}, {"term", syscall.SIGTERM}} {
		for _, delivery := range []struct {
			name         string
			processGroup bool
		}{{"parent-only", false}, {"process-group", true}} {
			t.Run(item.name+"/"+delivery.name, func(t *testing.T) {
				runSecuritySignalScenario(t, item.sig, delivery.processGroup)
			})
		}
	}
}

func runSecuritySignalScenario(t *testing.T, signal syscall.Signal, processGroup bool) {
	t.Helper()
	shell := securityGateShell(t)
	root := newSecurityGateRepository(t)
	state := newFakeSecurityRuntime(t)
	foreign := seedSecurityForeignContainer(t, state)
	marker := filepath.Join(t.TempDir(), "node-running")
	values := map[string]string{
		"FAKE_BLOCK_CONTAINER_PART": "ai-gdm-security-node-", "FAKE_BLOCK_MARKER": marker,
	}
	command := newSecurityGateCommand(shell, root, "validate-security.sh", state, values)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitSecurityMarker(t, marker)
	target := command.Process.Pid
	if processGroup {
		target = -target
	}
	if err := syscall.Kill(target, signal); err != nil {
		t.Fatal(err)
	}
	waitErr := waitSecurityCommand(command, 10*time.Second)
	if errors.Is(waitErr, syscall.ETIMEDOUT) {
		t.Fatalf("信号 %s 后主门禁未退出: %s", signal, output.String())
	}
	if waitErr == nil {
		t.Fatalf("信号 %s 未终止主门禁: %s", signal, output.String())
	}
	if strings.Contains(output.String(), "P9.1 安全门禁通过") {
		t.Fatalf("信号 %s 后仍发布成功: %s", signal, output.String())
	}
	assertSecurityManagedLifecycle(t, state, 2)
	assertSecurityCIDFilesUnique(t, state.dockerLog, 2)
	assertSecurityForeignContainer(t, foreign)
}

func seedSecurityForeignContainer(t *testing.T, state *fakeSecurityRuntime) string {
	t.Helper()
	cid := strings.Repeat("f", 64)
	path := filepath.Join(state.dockerState, "containers", cid)
	securityWriteFile(t, path, "foreign-security-container\n", 0o600)
	return path
}

func assertSecurityForeignContainer(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("主门禁误删其他容器: %v", err)
	}
}

func waitSecurityMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待安全门禁阻塞点超时: %s", path)
}

func waitSecurityCommand(command *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return syscall.ETIMEDOUT
	}
}
