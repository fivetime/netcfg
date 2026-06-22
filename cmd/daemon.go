/*
Copyright © 2024 netcfg authors

daemon command - runs netcfg as a background service managing DHCP leases.
*/

package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/netcfg/netcfg/config"
	"github.com/netcfg/netcfg/netlink"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run as a daemon managing DHCP leases",
	Long: `Run netcfg as a background daemon that:
- Applies network configuration
- Manages DHCP leases with automatic renewal
- Monitors lease expiration

The daemon will keep running until it receives SIGTERM or SIGINT.

Example:
  netcfg daemon              # Run in foreground
  netcfg daemon &            # Run in background
  systemctl start netcfg     # Run via systemd`,
	RunE: runDaemon,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
}

func runDaemon(cmd *cobra.Command, args []string) error {
	slog.Info("netcfg daemon starting", "pid", os.Getpid())

	// 启动 DHCP 守护进程
	daemon := netlink.GetDHCPDaemon()
	if err := daemon.Start(); err != nil {
		return fmt.Errorf("failed to start DHCP daemon: %w", err)
	}

	// 加载并应用配置
	ndpMgr := newNDPManager()

	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		slog.Warn("failed to load config", "error", err)
	} else {
		if err := applyConfigWithDaemon(cfg, daemon); err != nil {
			slog.Error("failed to apply config", "error", err)
		}
		ndpMgr.Reload(cfg) // 启动 NDP 代答器
	}

	// 等待信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	slog.Info("daemon running, waiting for signals")

	for {
		sig := <-sigCh
		switch sig {
		case syscall.SIGHUP:
			// 重新加载配置
			slog.Info("received SIGHUP, reloading configuration")
			cfg, err := config.LoadConfig(configDir)
			if err != nil {
				slog.Error("failed to reload config", "error", err)
				continue
			}
			if err := applyConfigWithDaemon(cfg, daemon); err != nil {
				slog.Error("failed to apply config", "error", err)
			}
			ndpMgr.Reload(cfg) // 重载 NDP 代答器
		case syscall.SIGTERM, syscall.SIGINT:
			slog.Info("received shutdown signal", "signal", sig)
			ndpMgr.Stop()
			daemon.Stop()
			slog.Info("daemon stopped")
			return nil
		}
	}
}

func applyConfigWithDaemon(cfg *config.Config, daemon *netlink.DHCPDaemon) error {
	// 处理需要 DHCP 的接口
	for name, eth := range cfg.Network.Ethernets {
		if eth.DHCP4 || eth.DHCP6 {
			v4ov := dhcpOverridesToNl(eth.DHCP4Overrides)
			v6ov := dhcpOverridesToNl(eth.DHCP6Overrides)
			if err := daemon.RequestLease(name, eth.DHCP4, eth.DHCP6, v4ov, v6ov); err != nil {
				slog.Error("DHCP request failed", "interface", name, "error", err)
			}
		}
	}

	// 其他静态配置使用原有逻辑
	// ...

	return nil
}
