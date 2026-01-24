/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/netcfg/netcfg/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var getCmd = &cobra.Command{
	Use:   "get [path]",
	Short: "Get a configuration value",
	Long: `Get a specific configuration value by path.

The path uses dot notation to navigate the YAML structure.
Without a path, returns the entire configuration.

Examples:
  netcfg get
  netcfg get network.ethernets.eth0.addresses
  netcfg get network.netns.ns1`,
	RunE: runGet,
}

func init() {
	rootCmd.AddCommand(getCmd)
}

func runGet(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(args) == 0 {
		// 输出整个配置
		data, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
		return nil
	}

	// 将配置转为 map 以便查询
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return err
	}

	// 按路径查询
	path := args[0]
	parts := strings.Split(path, ".")

	var current interface{} = m
	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			var ok bool
			current, ok = v[part]
			if !ok {
				return fmt.Errorf("path not found: %s", path)
			}
		default:
			return fmt.Errorf("cannot navigate into non-map at: %s", part)
		}
	}

	// 输出结果
	result, err := yaml.Marshal(current)
	if err != nil {
		return err
	}
	fmt.Print(string(result))

	return nil
}

var setCmd = &cobra.Command{
	Use:   "set path=value",
	Short: "Set a configuration value",
	Long: `Set a configuration value by path.

This modifies the configuration and saves it to a file.
Use 'netcfg apply' to apply the changes.

Examples:
  netcfg set network.ethernets.eth0.dhcp4=true
  netcfg set network.ethernets.eth0.addresses='["192.168.1.100/24"]'`,
	RunE: runSet,
}

var setOriginHint string

func init() {
	rootCmd.AddCommand(setCmd)
	setCmd.Flags().StringVar(&setOriginHint, "origin-hint", "netcfg", "Origin hint for the configuration file")
}

func runSet(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: netcfg set path=value")
	}

	// 解析 path=value
	parts := strings.SplitN(args[0], "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid format, expected path=value")
	}

	path := parts[0]
	valueStr := parts[1]

	// 解析值
	var value interface{}
	if err := yaml.Unmarshal([]byte(valueStr), &value); err != nil {
		// 如果解析失败，当作字符串处理
		value = valueStr
	}

	// 加载现有配置
	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		// 如果没有配置，创建新的
		cfg = &config.Config{
			Network: config.Network{Version: 2},
		}
	}

	// 转为 map
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return err
	}

	// 设置值
	pathParts := strings.Split(path, ".")
	if err := setNestedValue(m, pathParts, value); err != nil {
		return err
	}

	// 保存配置
	outputFile := fmt.Sprintf("%s/99-%s.yaml", configDir, setOriginHint)

	// 确保目录存在
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	outputData, err := yaml.Marshal(m)
	if err != nil {
		return err
	}

	if err := os.WriteFile(outputFile, outputData, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Printf("Configuration saved to %s\n", outputFile)
	fmt.Println("Run 'netcfg apply' to apply changes")

	return nil
}

func setNestedValue(m map[string]interface{}, path []string, value interface{}) error {
	if len(path) == 0 {
		return fmt.Errorf("empty path")
	}

	if len(path) == 1 {
		m[path[0]] = value
		return nil
	}

	key := path[0]
	rest := path[1:]

	// 获取或创建下一级 map
	next, ok := m[key]
	if !ok {
		next = make(map[string]interface{})
		m[key] = next
	}

	nextMap, ok := next.(map[string]interface{})
	if !ok {
		nextMap = make(map[string]interface{})
		m[key] = nextMap
	}

	return setNestedValue(nextMap, rest, value)
}
