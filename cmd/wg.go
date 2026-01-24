/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	nl "github.com/netcfg/netcfg/netlink"
	"github.com/spf13/cobra"
)

var wgCmd = &cobra.Command{
	Use:   "wg",
	Short: "WireGuard utilities",
	Long:  `WireGuard key generation and device management utilities.`,
}

var wgGenkeyCmd = &cobra.Command{
	Use:   "genkey",
	Short: "Generate a new private key",
	Long:  `Generate a new WireGuard private key and output to stdout.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		privKey, _, err := nl.GenerateKeyPair()
		if err != nil {
			return err
		}
		fmt.Println(privKey)
		return nil
	},
}

var wgPubkeyCmd = &cobra.Command{
	Use:   "pubkey",
	Short: "Calculate public key from private key",
	Long:  `Read a private key from stdin and output the corresponding public key.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var privKey string
		if _, err := fmt.Scanln(&privKey); err != nil {
			return fmt.Errorf("failed to read private key: %w", err)
		}

		pubKey, err := nl.PublicKeyFromPrivate(privKey)
		if err != nil {
			return err
		}
		fmt.Println(pubKey)
		return nil
	},
}

var wgGenpskCmd = &cobra.Command{
	Use:   "genpsk",
	Short: "Generate a new preshared key",
	Long:  `Generate a new WireGuard preshared key and output to stdout.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		psk, err := nl.GeneratePresharedKey()
		if err != nil {
			return err
		}
		fmt.Println(psk)
		return nil
	},
}

var wgShowCmd = &cobra.Command{
	Use:   "show [interface]",
	Short: "Show WireGuard device information",
	Long:  `Show WireGuard device configuration and status.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		wgMgr, err := nl.NewWireGuardManager()
		if err != nil {
			return err
		}
		defer wgMgr.Close()

		if len(args) > 0 {
			// 显示指定设备
			return showWireGuardDevice(wgMgr, args[0])
		}

		// 列出所有设备
		devices, err := wgMgr.ListDevices()
		if err != nil {
			return err
		}

		if len(devices) == 0 {
			fmt.Println("No WireGuard devices found")
			return nil
		}

		for i, dev := range devices {
			if i > 0 {
				fmt.Println()
			}
			if err := showWireGuardDevice(wgMgr, dev.Name); err != nil {
				return err
			}
		}
		return nil
	},
}

func showWireGuardDevice(wgMgr *nl.WireGuardManager, name string) error {
	dev, err := wgMgr.GetDevice(name)
	if err != nil {
		return fmt.Errorf("failed to get device %s: %w", name, err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintf(w, "interface: %s\n", dev.Name)
	fmt.Fprintf(w, "  public key:\t%s\n", dev.PublicKey.String())
	if dev.ListenPort > 0 {
		fmt.Fprintf(w, "  listen port:\t%d\n", dev.ListenPort)
	}
	if dev.FirewallMark > 0 {
		fmt.Fprintf(w, "  fwmark:\t0x%x\n", dev.FirewallMark)
	}

	for _, peer := range dev.Peers {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "peer: %s\n", peer.PublicKey.String())
		if peer.Endpoint != nil {
			fmt.Fprintf(w, "  endpoint:\t%s\n", peer.Endpoint.String())
		}
		if len(peer.AllowedIPs) > 0 {
			fmt.Fprintf(w, "  allowed ips:\t")
			for i, ip := range peer.AllowedIPs {
				if i > 0 {
					fmt.Fprintf(w, ", ")
				}
				fmt.Fprintf(w, "%s", ip.String())
			}
			fmt.Fprintln(w)
		}
		if !peer.LastHandshakeTime.IsZero() {
			fmt.Fprintf(w, "  latest handshake:\t%s\n", peer.LastHandshakeTime.Format("2006-01-02 15:04:05"))
		}
		if peer.ReceiveBytes > 0 || peer.TransmitBytes > 0 {
			fmt.Fprintf(w, "  transfer:\t%s received, %s sent\n",
				formatBytes(peer.ReceiveBytes),
				formatBytes(peer.TransmitBytes))
		}
		if peer.PersistentKeepaliveInterval > 0 {
			fmt.Fprintf(w, "  persistent keepalive:\tevery %s\n", peer.PersistentKeepaliveInterval)
		}
	}

	return nil
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func init() {
	rootCmd.AddCommand(wgCmd)
	wgCmd.AddCommand(wgGenkeyCmd)
	wgCmd.AddCommand(wgPubkeyCmd)
	wgCmd.AddCommand(wgGenpskCmd)
	wgCmd.AddCommand(wgShowCmd)
}
