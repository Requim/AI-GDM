package scripts

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const archiveRoot = "ai-gdm-v0.1.1-linux-amd64"

type archiveMember struct {
	name     string
	typeflag byte
	linkname string
	payload  string
}

func TestReleaseArchiveValidatorAcceptsRegularPackage(t *testing.T) {
	archive, sidecar := writeArchiveFixture(t, []archiveMember{
		{name: archiveRoot + "/", typeflag: tar.TypeDir},
		{name: archiveRoot + "/README.md", typeflag: tar.TypeReg, payload: "ok\n"},
	})
	extractRoot := filepath.Join(t.TempDir(), "extracted")
	output, err := runArchiveValidator(t, archive, sidecar, extractRoot)
	if err != nil {
		t.Fatalf("普通发布归档未通过: %v: %s", err, output)
	}
	payload, err := os.ReadFile(filepath.Join(extractRoot, archiveRoot, "README.md"))
	if err != nil || string(payload) != "ok\n" {
		t.Fatalf("安全解包结果无效: error=%v payload=%q", err, payload)
	}
}

func TestReleaseArchiveValidatorRejectsUnboundSidecars(t *testing.T) {
	archive, sidecar := writeArchiveFixture(t, []archiveMember{{name: archiveRoot + "/", typeflag: tar.TypeDir}})
	digest := hashFixtureFile(t, archive)
	tests := map[string]string{
		"错误文件名": digest + "  other.tar.gz\n",
		"多条记录":  digest + "  " + filepath.Base(archive) + "\n" + digest + "  other.tar.gz\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(sidecar, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := runArchiveValidator(t, archive, sidecar, filepath.Join(t.TempDir(), "out")); err == nil {
				t.Fatalf("未拒绝未绑定 sidecar: %s", output)
			}
		})
	}
}

func TestReleaseArchiveValidatorRejectsDangerousMembers(t *testing.T) {
	tests := map[string][]archiveMember{
		"符号链接": {{name: archiveRoot + "/link", typeflag: tar.TypeSymlink, linkname: "../outside"}},
		"硬链接":  {{name: archiveRoot + "/link", typeflag: tar.TypeLink, linkname: archiveRoot + "/README.md"}},
		"字符设备": {{name: archiveRoot + "/device", typeflag: tar.TypeChar}},
		"FIFO": {{name: archiveRoot + "/pipe", typeflag: tar.TypeFifo}},
		"越界路径": {{name: archiveRoot + "/../outside", typeflag: tar.TypeReg, payload: "bad"}},
		"重复成员": {
			{name: archiveRoot + "/same", typeflag: tar.TypeReg, payload: "one"},
			{name: archiveRoot + "/same", typeflag: tar.TypeReg, payload: "two"},
		},
	}
	for name, members := range tests {
		t.Run(name, func(t *testing.T) {
			members = append([]archiveMember{{name: archiveRoot + "/", typeflag: tar.TypeDir}}, members...)
			archive, sidecar := writeArchiveFixture(t, members)
			if output, err := runArchiveValidator(t, archive, sidecar, filepath.Join(t.TempDir(), "out")); err == nil {
				t.Fatalf("未拒绝危险 tar 成员: %s", output)
			}
		})
	}
}

func TestReleaseArchiveValidatorLimitsMemberHeaders(t *testing.T) {
	members := make([]archiveMember, 0, 10_002)
	members = append(members, archiveMember{name: archiveRoot + "/", typeflag: tar.TypeDir})
	for index := 0; index < 10_001; index++ {
		members = append(members, archiveMember{
			name: fmt.Sprintf("%s/files/%05d", archiveRoot, index), typeflag: tar.TypeReg,
		})
	}
	archive, sidecar := writeArchiveFixture(t, members)
	if output, err := runArchiveValidator(t, archive, sidecar, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatalf("未拒绝超量 tar header: %s", output)
	}
}

func writeArchiveFixture(t *testing.T, members []archiveMember) (string, string) {
	t.Helper()
	directory := t.TempDir()
	archive := filepath.Join(directory, archiveRoot+".tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, member := range members {
		writeArchiveMember(t, tarWriter, member)
	}
	if err = tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	sidecar := archive + ".sha256"
	line := fmt.Sprintf("%s  %s\n", hashFixtureFile(t, archive), filepath.Base(archive))
	if err = os.WriteFile(sidecar, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return archive, sidecar
}

func writeArchiveMember(t *testing.T, writer *tar.Writer, member archiveMember) {
	t.Helper()
	header := &tar.Header{
		Name: member.name, Typeflag: member.typeflag, Linkname: member.linkname,
		Mode: 0o644, Size: int64(len(member.payload)),
	}
	if member.typeflag == tar.TypeDir {
		header.Mode, header.Size = 0o755, 0
	}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if member.payload != "" {
		if _, err := writer.Write([]byte(member.payload)); err != nil {
			t.Fatal(err)
		}
	}
}

func runArchiveValidator(t *testing.T, archive, sidecar, extractRoot string) ([]byte, error) {
	t.Helper()
	python := requirePython(t)
	command := exec.Command(python, "validate-release-archive.py",
		"--archive", archive, "--sidecar", sidecar,
		"--expected-root", archiveRoot, "--extract-root", extractRoot)
	command.Dir = "."
	return command.CombinedOutput()
}

func requirePython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "--version").Run() == nil {
			return path
		}
	}
	t.Skip("当前平台缺少 Python 3，腾讯 Ubuntu 必须执行本测试")
	return ""
}

func hashFixtureFile(t *testing.T, filename string) string {
	t.Helper()
	payload, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest)
}

func TestArchiveValidatorMessagesStayChinese(t *testing.T) {
	payload := readPackageFile(t, "validate-release-archive.py")
	if !strings.Contains(payload, "发布归档校验失败") {
		t.Fatal("归档校验器缺少中文失败上下文")
	}
}
