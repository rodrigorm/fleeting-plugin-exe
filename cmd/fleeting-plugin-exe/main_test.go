package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"gitlab.com/gitlab-org/fleeting/fleeting"
)

func TestPluginServesFleetingProtocol(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fleeting-plugin-exe")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command("go", "build", "-o", binary, ".")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		t.Fatalf("build plugin: %v", err)
	}

	runner, err := fleeting.RunPlugin(binary, nil)
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	runner.Kill()
}
