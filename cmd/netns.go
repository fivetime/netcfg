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

var netnsCmd = &cobra.Command{
	Use:   "netns",
	Short: "Network namespace management",
	Long:  `Commands for managing network namespaces.`,
}

var netnsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List network namespaces",
	RunE:    runNetnsList,
}

var netnsCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a network namespace",
	Args:  cobra.ExactArgs(1),
	RunE:  runNetnsCreate,
}

var netnsDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"del", "rm"},
	Short:   "Delete a network namespace",
	Args:    cobra.ExactArgs(1),
	RunE:    runNetnsDelete,
}

var netnsExecCmd = &cobra.Command{
	Use:   "exec <namespace> <command> [args...]",
	Short: "Execute command in a network namespace",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runNetnsExec,
}

func init() {
	rootCmd.AddCommand(netnsCmd)
	netnsCmd.AddCommand(netnsListCmd)
	netnsCmd.AddCommand(netnsCreateCmd)
	netnsCmd.AddCommand(netnsDeleteCmd)
	netnsCmd.AddCommand(netnsExecCmd)
}

func runNetnsList(cmd *cobra.Command, args []string) error {
	namespaces, err := nl.ListNetns()
	if err != nil {
		return fmt.Errorf("failed to list netns: %w", err)
	}

	if len(namespaces) == 0 {
		fmt.Println("No network namespaces found")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPATH")

	for _, ns := range namespaces {
		fmt.Fprintf(w, "%s\t%s\n", ns, nl.GetNetnsPath(ns))
	}

	w.Flush()
	return nil
}

func runNetnsCreate(cmd *cobra.Command, args []string) error {
	name := args[0]

	if nl.NetnsExists(name) {
		return fmt.Errorf("netns %s already exists", name)
	}

	if err := nl.CreateNetns(name); err != nil {
		return fmt.Errorf("failed to create netns %s: %w", name, err)
	}

	fmt.Printf("Created network namespace: %s\n", name)
	return nil
}

func runNetnsDelete(cmd *cobra.Command, args []string) error {
	name := args[0]

	if !nl.NetnsExists(name) {
		return fmt.Errorf("netns %s does not exist", name)
	}

	if err := nl.DeleteNetns(name); err != nil {
		return fmt.Errorf("failed to delete netns %s: %w", name, err)
	}

	fmt.Printf("Deleted network namespace: %s\n", name)
	return nil
}

func runNetnsExec(cmd *cobra.Command, args []string) error {
	nsName := args[0]
	execCmd := args[1:]

	if !nl.NetnsExists(nsName) {
		return fmt.Errorf("netns %s does not exist", nsName)
	}

	// 使用 ip netns exec 执行命令
	// 因为直接在 Go 中切换 netns 后执行外部命令会有问题
	return runInNetns(nsName, execCmd[0], execCmd[1:]...)
}

func runInNetns(nsName, command string, args ...string) error {
	cmdArgs := append([]string{"netns", "exec", nsName, command}, args...)

	// 使用 syscall.Exec 替换当前进程
	// 这样可以正确处理信号和 TTY
	proc := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	allArgs := append([]string{"ip"}, cmdArgs...)
	p, err := os.StartProcess("/sbin/ip", allArgs, &proc)
	if err != nil {
		return fmt.Errorf("failed to exec: %w", err)
	}

	state, err := p.Wait()
	if err != nil {
		return err
	}

	if !state.Success() {
		return fmt.Errorf("command exited with status %d", state.ExitCode())
	}

	return nil
}
