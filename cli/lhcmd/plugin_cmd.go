package lhcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	plugins "github.com/yurika0211/luckyagent-plugins"

	"github.com/yurika0211/luckyagent-runtime/config"
)

const pluginRestartNotice = "插件变更已保存；当前运行实例未重载，请重启 LuckyAgent 运行实例后生效。"

type pluginCommandManager interface {
	List() ([]plugins.Installed, error)
	Install(context.Context, string, plugins.InstallOptions) (plugins.Installed, error)
	Update(context.Context, string, plugins.InstallOptions) (plugins.Installed, error)
	Enable(string, bool) error
	Remove(string) error
}

// Keep management commands independent of Agent construction: inspecting a
// bundle must not connect MCP servers or execute any plugin commands/hooks.
type pluginCommandDeps struct {
	runtimeHome func() (string, error)
	manager     func(string) (pluginCommandManager, error)
	read        func(string) (*plugins.Bundle, error)
	search      func(context.Context, string, string) ([]plugins.MarketplaceEntry, error)
}

type pluginChangeResult struct {
	Action                 string             `json:"action"`
	Name                   string             `json:"name"`
	Plugin                 *plugins.Installed `json:"plugin,omitempty"`
	Enabled                *bool              `json:"enabled,omitempty"`
	RuntimeRestartRequired bool               `json:"runtime_restart_required"`
	Message                string             `json:"message"`
}

func newPluginCmd() *cobra.Command {
	return newPluginCmdWithDeps(pluginCommandDeps{
		runtimeHome: func() (string, error) {
			mgr, err := config.NewManager()
			if err != nil {
				return "", err
			}
			return mgr.HomeDir(), nil
		},
		manager: func(root string) (pluginCommandManager, error) { return plugins.NewManager(root) },
		read:    plugins.Read,
		search:  plugins.SearchMarketplace,
	})
}

func newPluginCmdWithDeps(deps pluginCommandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "管理插件包、信任和启用状态",
		Long:  "管理本地插件包。管理命令只读取或保存插件，不执行插件脚本，也不连接 MCP；变更需要重启运行实例后生效。",
	}
	var asJSON bool
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "以 JSON 输出结果")
	open := func() (pluginCommandManager, error) {
		home, err := deps.runtimeHome()
		if err != nil {
			return nil, err
		}
		return deps.manager(filepath.Join(home, "plugins"))
	}
	write := func(cmd *cobra.Command, value any) error {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	changed := func(cmd *cobra.Command, action, name string, installed *plugins.Installed, enabled *bool) error {
		result := pluginChangeResult{Action: action, Name: name, Plugin: installed, Enabled: enabled,
			RuntimeRestartRequired: true, Message: pluginRestartNotice}
		if asJSON {
			return write(cmd, result)
		}
		if installed != nil {
			cmd.Printf("%s %s@%s (enabled=%t, trusted=%t)\n", action, name, installed.Entry.Version, installed.Entry.Enabled, installed.Entry.Trusted)
		} else if enabled != nil {
			cmd.Printf("%s %s (enabled=%t)\n", action, name, *enabled)
		} else {
			cmd.Printf("%s %s\n", action, name)
		}
		cmd.Println(pluginRestartNotice)
		return nil
	}

	listCmd := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "列出已安装插件", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := open()
			if err != nil {
				return err
			}
			installed, err := mgr.List()
			if err != nil {
				return err
			}
			if installed == nil {
				installed = []plugins.Installed{}
			}
			if asJSON {
				return write(cmd, installed)
			}
			if len(installed) == 0 {
				cmd.Println("暂无已安装插件。")
				return nil
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(out, "NAME\tVERSION\tENABLED\tTRUSTED\tERROR")
			for _, item := range installed {
				fmt.Fprintf(out, "%s\t%s\t%t\t%t\t%s\n", item.Entry.Name, item.Entry.Version, item.Entry.Enabled, item.Entry.Trusted, item.Error)
			}
			return out.Flush()
		}}
	showCmd := &cobra.Command{Use: "show <name>", Short: "查看已安装插件及贡献内容", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := open()
			if err != nil {
				return err
			}
			item, err := findInstalledPlugin(mgr, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return write(cmd, item)
			}
			cmd.Printf("%s@%s\nSource: %s\nRef: %s\nRevision: %s\nSubdir: %s\nDigest: %s\nEnabled: %t\nTrusted: %t\n",
				item.Entry.Name, item.Entry.Version, item.Entry.Source, item.Entry.Ref, item.Entry.Revision, item.Entry.Subdir, item.Entry.Digest, item.Entry.Enabled, item.Entry.Trusted)
			if item.Bundle != nil {
				printPluginBundle(cmd, item.Bundle)
			}
			if item.Error != "" {
				cmd.Printf("Error: %s\n", item.Error)
			}
			return nil
		}}
	validateCmd := &cobra.Command{Use: "validate <path>", Short: "只读校验本地插件目录", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := deps.read(args[0])
			if err != nil {
				return fmt.Errorf("validate plugin: %w", err)
			}
			if asJSON {
				return write(cmd, bundle)
			}
			cmd.Printf("Valid: %s@%s\n", bundle.Manifest.Name, bundle.Manifest.Version)
			printPluginBundle(cmd, bundle)
			return nil
		}}

	searchCmd := &cobra.Command{Use: "search [query]", Short: "搜索指定 marketplace 的插件索引", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			marketplace, _ := cmd.Flags().GetString("marketplace")
			query := ""
			if len(args) != 0 {
				query = args[0]
			}
			entries, err := deps.search(cmd.Context(), marketplace, query)
			if err != nil {
				return err
			}
			if entries == nil {
				entries = []plugins.MarketplaceEntry{}
			}
			if asJSON {
				return write(cmd, entries)
			}
			if len(entries) == 0 {
				cmd.Println("没有匹配的插件。")
				return nil
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(out, "NAME\tDESCRIPTION\tSOURCE")
			for _, entry := range entries {
				fmt.Fprintf(out, "%s\t%s\t%s\n", entry.Name, entry.Description, entry.Source)
			}
			return out.Flush()
		}}
	searchCmd.Flags().String("marketplace", "", "marketplace.json 路径或 HTTPS URL（必填）")
	_ = searchCmd.MarkFlagRequired("marketplace")

	installCmd := &cobra.Command{Use: "install <source>", Short: "从本地目录或 Git 安装插件", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := pluginInstallOptions(cmd, false)
			source := args[0]
			marketplace, _ := cmd.Flags().GetString("marketplace")
			if strings.TrimSpace(marketplace) != "" {
				entries, err := deps.search(cmd.Context(), marketplace, "")
				if err != nil {
					return err
				}
				var match *plugins.MarketplaceEntry
				for index := range entries {
					if entries[index].Name == source {
						if match != nil {
							return fmt.Errorf("marketplace contains duplicate plugin name %q", source)
						}
						match = &entries[index]
					}
				}
				if match == nil {
					return fmt.Errorf("plugin %q not found in marketplace", source)
				}
				source = match.Source
				if !cmd.Flags().Changed("ref") {
					opts.Ref = match.Ref
				}
				if !cmd.Flags().Changed("subdir") {
					opts.Subdir = match.Subdir
				}
			}
			mgr, err := open()
			if err != nil {
				return err
			}
			item, err := mgr.Install(cmd.Context(), source, opts)
			if err != nil {
				return err
			}
			return changed(cmd, "install", item.Entry.Name, &item, nil)
		}}
	addPluginSourceFlags(installCmd)
	installCmd.Flags().String("marketplace", "", "从指定 marketplace 按插件名称精确安装")
	updateCmd := &cobra.Command{Use: "update <name>", Short: "从已记录来源重新安装；新版本需重新信任", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := open()
			if err != nil {
				return err
			}
			item, err := mgr.Update(cmd.Context(), args[0], pluginInstallOptions(cmd, true))
			if err != nil {
				return err
			}
			return changed(cmd, "update", item.Entry.Name, &item, nil)
		}}
	addPluginSourceFlags(updateCmd)

	for _, enabled := range []bool{true, false} {
		enabled := enabled
		action := "disable"
		short := "禁用插件"
		if enabled {
			action, short = "enable", "启用已信任或仅含提示词的插件"
		}
		cmd.AddCommand(&cobra.Command{Use: action + " <name>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				mgr, err := open()
				if err != nil {
					return err
				}
				if err := mgr.Enable(args[0], enabled); err != nil {
					return err
				}
				return changed(cmd, action, args[0], nil, &enabled)
			}})
	}
	removeCmd := &cobra.Command{Use: "remove <name>", Aliases: []string{"rm"}, Short: "删除插件及安装记录", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := open()
			if err != nil {
				return err
			}
			if err := mgr.Remove(args[0]); err != nil {
				return err
			}
			return changed(cmd, "remove", args[0], nil, nil)
		}}
	cmd.AddCommand(listCmd, showCmd, validateCmd, searchCmd, installCmd, updateCmd, removeCmd)
	return cmd
}

func addPluginSourceFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("trust", false, "显式信任本次安装版本的 MCP 命令和 hooks；管理命令不会执行它们")
	cmd.Flags().String("ref", "", "Git ref；update 默认沿用已记录 ref")
	cmd.Flags().String("subdir", "", "Git 仓库内插件子目录；update 默认沿用已记录 subdir")
}

func pluginInstallOptions(cmd *cobra.Command, replace bool) plugins.InstallOptions {
	ref, _ := cmd.Flags().GetString("ref")
	subdir, _ := cmd.Flags().GetString("subdir")
	trust, _ := cmd.Flags().GetBool("trust")
	return plugins.InstallOptions{Ref: ref, Subdir: subdir, Trust: trust, Replace: replace}
}

func findInstalledPlugin(mgr pluginCommandManager, name string) (plugins.Installed, error) {
	installed, err := mgr.List()
	if err != nil {
		return plugins.Installed{}, err
	}
	for _, item := range installed {
		if item.Entry.Name == name {
			return item, nil
		}
	}
	return plugins.Installed{}, fmt.Errorf("plugin %q is not installed", name)
}

func printPluginBundle(cmd *cobra.Command, bundle *plugins.Bundle) {
	cmd.Printf("Description: %s\nFormat: %s\nRoot: %s\nSkills: %d\nCommands: %d\nAgents: %d\nMCP servers: %d\nHooks: %d\n",
		bundle.Manifest.Description, bundle.Format, bundle.Root, len(bundle.Skills), len(bundle.Commands), len(bundle.Agents), len(bundle.MCPServers), len(bundle.Hooks.PreToolUse)+len(bundle.Hooks.PostToolUse))
}
