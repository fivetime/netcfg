package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTakeoverNeutralizeAndRevert 验证：netplan 的 networkd 文件被移走、用户自有文件
// 保留、--revert 能完整还原。用临时目录覆盖 networkdDirs/takeoverBackup，无需 root。
func TestTakeoverNeutralizeAndRevert(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc", "systemd", "network")
	run := filepath.Join(root, "run", "systemd", "network")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}

	netplanFiles := []string{
		filepath.Join(etc, "10-netplan-eth0.network"),
		filepath.Join(etc, "10-netplan-eth0.netdev"),
		filepath.Join(run, "10-netplan-bond0.network"),
	}
	userFile := filepath.Join(etc, "20-myown.network") // 非 netplan，应保留
	for _, f := range append(append([]string{}, netplanFiles...), userFile) {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 覆盖全局，测试后恢复
	oldDirs, oldBackup, oldRevert, oldDry := networkdDirs, takeoverBackup, takeoverRevert, takeoverDryRun
	networkdDirs = []string{etc, run}
	takeoverBackup = filepath.Join(root, "backup")
	takeoverRevert, takeoverDryRun = false, false
	defer func() {
		networkdDirs, takeoverBackup, takeoverRevert, takeoverDryRun = oldDirs, oldBackup, oldRevert, oldDry
	}()

	// 中和
	if err := takeoverNeutralize(); err != nil {
		t.Fatalf("neutralize: %v", err)
	}
	for _, f := range netplanFiles {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("expected netplan file moved away: %s", f)
		}
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Errorf("user file should remain untouched: %v", err)
	}

	// 还原
	if err := takeoverRestore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, f := range netplanFiles {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("expected netplan file restored: %s (%v)", f, err)
		}
	}
}
