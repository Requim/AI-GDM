package gdal

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const maxCommandOutput = 64 << 10

// CommandRunner 执行无 shell 拼接的 GDAL 参数列表。
type CommandRunner interface {
	// Run 执行一次 GDAL 子命令并返回标准输出。
	Run(ctx context.Context, workingDir string, arguments ...string) ([]byte, error)
}

type execRunner struct {
	binary string
}

func (r execRunner) Run(ctx context.Context, workingDir string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, r.binary, arguments...)
	command.Dir = workingDir
	command.Env = append(os.Environ(), "PROJ_NETWORK=OFF", "GDAL_DISABLE_READDIR_ON_OPEN=EMPTY_DIR", "CPL_TMPDIR="+workingDir)
	var stdout, stderr cappedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	output := stdout.Bytes()
	if err == nil {
		return output, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("执行 GDAL %s: %w", strings.Join(arguments, " "), ctx.Err())
	}
	message := strings.TrimSpace(stderr.String())
	return nil, fmt.Errorf("执行 GDAL %s: %w: %s", strings.Join(arguments, " "), err, message)
}

type cappedBuffer struct {
	buffer bytes.Buffer
}

func (b *cappedBuffer) Write(payload []byte) (int, error) {
	remaining := maxCommandOutput - b.buffer.Len()
	if remaining > 0 {
		_, _ = b.buffer.Write(payload[:min(len(payload), remaining)])
	}
	return len(payload), nil
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func (b *cappedBuffer) String() string {
	return b.buffer.String()
}
