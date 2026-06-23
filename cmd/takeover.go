/*
Copyright © 2024 netcfg authors

takeover command - neutralize netplan's systemd-networkd backend files so that
netcfg becomes the sole network configurator after replacing netplan.
*/

package cmd

import (
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// netplanBackupDir 是被中和的 netplan systemd-networkd 后端文件的默认备份根目录。
const netplanBackupDir = "/var/lib/netcfg/netplan-networkd-backup"

// networkdDirs 是 netplan generate 可能写入 systemd-networkd 后端文件的目录。
// 设为包级变量便于单测覆盖。
var networkdDirs = []string{"/etc/systemd/network", "/run/systemd/network"}

// netplanGlobs 匹配 netplan 生成的 networkd 文件名（10-netplan-<id>.{network,netdev,link}）。
var netplanGlobs = []string{"10-netplan-*"}

var (
	takeoverBackup string
	takeoverRevert bool
	takeoverDryRun bool
)

var takeoverCmd = &cobra.Command{
	Use:   "takeover",
	Short: "Neutralize netplan's systemd-networkd backend files so netcfg is the sole configurator",
	Long: `替换 netplan 时，'netplan generate' 写入 systemd-networkd 后端的文件
(/etc/systemd/network/10-netplan-*、/run/systemd/network/10-netplan-*) 会残留，
重启时仍被 systemd-networkd 应用，与 netcfg 冲突（典型表现：bond/vlan 实际由
networkd 而非 netcfg 拉起）。

'netcfg takeover' 把这些文件移到备份目录，让 netcfg 成为唯一的网络配置器；
--revert 可一键还原。安装 deb/rpm/apk 时 postinstall 会自动执行（netcfg
Conflicts/Replaces netplan.io，安装即视为接管）；无 netplan 残留时为 no-op。

  netcfg takeover            # 移走 netplan 的 networkd 文件到备份
  netcfg takeover --dry-run  # 仅显示将处理的文件，不改动
  netcfg takeover --revert   # 还原之前移走的文件`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if takeoverRevert {
			return takeoverRestore()
		}
		return takeoverNeutralize()
	},
}

func init() {
	rootCmd.AddCommand(takeoverCmd)
	takeoverCmd.Flags().StringVar(&takeoverBackup, "backup-dir", netplanBackupDir, "Backup directory for moved files")
	takeoverCmd.Flags().BoolVar(&takeoverRevert, "revert", false, "Restore previously backed-up netplan networkd files")
	takeoverCmd.Flags().BoolVar(&takeoverDryRun, "dry-run", false, "Show what would change without doing it")
}

// takeoverNeutralize 把 netplan 的 networkd 后端文件移到备份目录（路径镜像，便于还原）。
func takeoverNeutralize() error {
	seen := map[string]bool{}
	var matches []string
	for _, dir := range networkdDirs {
		for _, g := range netplanGlobs {
			files, _ := filepath.Glob(filepath.Join(dir, g))
			for _, f := range files {
				if seen[f] {
					continue
				}
				if fi, err := os.Stat(f); err != nil || fi.IsDir() {
					continue
				}
				seen[f] = true
				matches = append(matches, f)
			}
		}
	}
	if len(matches) == 0 {
		slog.Info("no netplan systemd-networkd files found; nothing to take over")
		return nil
	}

	moved := 0
	for _, src := range matches {
		// 备份路径镜像源的绝对路径：/etc/systemd/network/x -> <backup>/etc/systemd/network/x
		dst := filepath.Join(takeoverBackup, strings.TrimPrefix(filepath.Clean(src), string(os.PathSeparator)))
		if takeoverDryRun {
			slog.Info("would move netplan networkd file", "from", src, "to", dst)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			slog.Warn("failed to create backup dir", "dir", filepath.Dir(dst), "error", err)
			continue
		}
		if err := moveFile(src, dst); err != nil {
			slog.Warn("failed to move netplan networkd file", "file", src, "error", err)
			continue
		}
		slog.Info("moved netplan networkd file to backup", "from", src, "to", dst)
		moved++
	}
	if !takeoverDryRun {
		slog.Info("netplan systemd-networkd backend neutralized; netcfg is the sole configurator",
			"moved", moved, "backup", takeoverBackup, "revert_with", "netcfg takeover --revert")
	}
	return nil
}

// takeoverRestore 把备份目录里的文件按镜像路径还原到原始位置。
func takeoverRestore() error {
	if _, err := os.Stat(takeoverBackup); os.IsNotExist(err) {
		slog.Info("no takeover backup found; nothing to restore", "backup", takeoverBackup)
		return nil
	}
	restored := 0
	err := filepath.WalkDir(takeoverBackup, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// path = <backup>/etc/systemd/network/x -> 原始绝对路径 /etc/systemd/network/x
		rel := strings.TrimPrefix(path, takeoverBackup)
		dst := filepath.Clean(string(os.PathSeparator) + rel)
		if takeoverDryRun {
			slog.Info("would restore netplan networkd file", "from", path, "to", dst)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			slog.Warn("failed to create dir", "dir", filepath.Dir(dst), "error", err)
			return nil
		}
		if err := moveFile(path, dst); err != nil {
			slog.Warn("failed to restore netplan networkd file", "file", path, "error", err)
			return nil
		}
		slog.Info("restored netplan networkd file", "to", dst)
		restored++
		return nil
	})
	if err != nil {
		return err
	}
	slog.Info("restore complete", "restored", restored, "from", takeoverBackup)
	return nil
}

// moveFile 移动文件：先尝试 rename，跨文件系统(/run tmpfs → /var)失败时退回 copy+remove。
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}
