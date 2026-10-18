package lhcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	plugins "github.com/yurika0211/luckyagent-plugins"
)

type pluginManagerCall struct {
	name    string
	source  string
	options plugins.InstallOptions
	enabled bool
}

type fakePluginCommandManager struct {
	installed []plugins.Installed
	calls     []pluginManagerCall
	err       error
}

func (m *fakePluginCommandManager) List() ([]plugins.Installed, error) { return m.installed, m.err }

func (m *fakePluginCommandManager) Install(_ context.Context, source string, options plugins.InstallOptions) (plugins.Installed, error) {
	m.calls = append(m.calls, pluginManagerCall{name: "install", source: source, options: options})
	return plugins.Installed{Entry: plugins.Entry{Name: "demo-plugin", Version: "1.0.0", Enabled: true, Trusted: options.Trust}}, m.err
}

func (m *fakePluginCommandManager) Update(_ context.Context, name string, options plugins.InstallOptions) (plugins.Installed, error) {
	m.calls = append(m.calls, pluginManagerCall{name: "update:" + name, options: options})
	return plugins.Installed{Entry: plugins.Entry{Name: name, Version: "2.0.0", Enabled: options.Trust, Trusted: options.Trust}}, m.err
}

func (m *fakePluginCommandManager) Enable(name string, enabled bool) error {
	m.calls = append(m.calls, pluginManagerCall{name: name, enabled: enabled})
	return m.err
}

func (m *fakePluginCommandManager) Remove(name string) error {
	m.calls = append(m.calls, pluginManagerCall{name: "remove:" + name})
	return m.err
}

func pluginTestDeps(t *testing.T, mgr pluginCommandManager) pluginCommandDeps {
	t.Helper()
	home := t.TempDir()
	return pluginCommandDeps{
		runtimeHome: func() (string, error) { return home, nil },
		manager: func(root string) (pluginCommandManager, error) {
			if root != filepath.Join(home, "plugins") {
				t.Fatalf("manager root = %q", root)
			}
			return mgr, nil
		},
		read: plugins.Read,
		search: func(context.Context, string, string) ([]plugins.MarketplaceEntry, error) {
			t.Fatal("unexpected marketplace read")
			return nil, nil
		},
	}
}

func executePluginTestCommand(deps pluginCommandDeps, args ...string) (string, error) {
	cmd := newPluginCmdWithDeps(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestPluginCommandListAndShowJSON(t *testing.T) {
	mgr := &fakePluginCommandManager{installed: []plugins.Installed{{Entry: plugins.Entry{Name: "demo-plugin", Version: "1.0.0", Enabled: true}}}}
	deps := pluginTestDeps(t, mgr)
	out, err := executePluginTestCommand(deps, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []plugins.Installed
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 1 || listed[0].Entry.Name != "demo-plugin" {
		t.Fatalf("list = %s, error %v", out, err)
	}
	out, err = executePluginTestCommand(deps, "show", "demo-plugin", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var shown plugins.Installed
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.Entry.Version != "1.0.0" {
		t.Fatalf("show = %s, error %v", out, err)
	}
	if _, err := executePluginTestCommand(deps, "show", "missing"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing plugin error = %v", err)
	}
	mgr.installed = nil
	if out, err := executePluginTestCommand(deps, "list", "--json"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list = %q, %v", out, err)
	}
}

func TestPluginValidateReadsBundleWithoutRuntimeOrExecution(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{"name":"demo-plugin","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "commands"), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "executed")
	if err := os.WriteFile(filepath.Join(root, "commands", "review.md"), []byte("# Review\n\n$(touch "+sentinel+")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := pluginCommandDeps{
		runtimeHome: func() (string, error) { t.Fatal("validate must not open runtime home"); return "", nil },
		manager:     func(string) (pluginCommandManager, error) { t.Fatal("validate must not open manager"); return nil, nil },
		read:        plugins.Read,
	}
	out, err := executePluginTestCommand(deps, "validate", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var bundle plugins.Bundle
	if err := json.Unmarshal([]byte(out), &bundle); err != nil || bundle.Manifest.Name != "demo-plugin" || len(bundle.Commands) != 1 {
		t.Fatalf("validate = %s, error %v", out, err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validation executed command contents: %v", err)
	}
	if _, err := executePluginTestCommand(deps, "validate", t.TempDir()); err == nil {
		t.Fatal("invalid bundle accepted")
	}
}

func TestPluginInstallAndUpdateForwardExplicitTrust(t *testing.T) {
	mgr := &fakePluginCommandManager{}
	deps := pluginTestDeps(t, mgr)
	out, err := executePluginTestCommand(deps, "install", "https://example.com/plugin.git", "--trust", "--ref", "v1", "--subdir", "plugins/demo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(mgr.calls) != 1 || mgr.calls[0].source != "https://example.com/plugin.git" || mgr.calls[0].options != (plugins.InstallOptions{Trust: true, Ref: "v1", Subdir: "plugins/demo"}) {
		t.Fatalf("install calls = %#v", mgr.calls)
	}
	var result pluginChangeResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.RuntimeRestartRequired || result.Plugin == nil || !result.Plugin.Entry.Trusted {
		t.Fatalf("install result = %s, error %v", out, err)
	}
	for _, trust := range []bool{false, true} {
		args := []string{"update", "demo-plugin", "--ref", "v2", "--subdir", "plugins/demo"}
		if trust {
			args = append(args, "--trust")
		}
		out, err := executePluginTestCommand(deps, args...)
		if err != nil {
			t.Fatal(err)
		}
		last := mgr.calls[len(mgr.calls)-1]
		if last.name != "update:demo-plugin" || last.options != (plugins.InstallOptions{Trust: trust, Replace: true, Ref: "v2", Subdir: "plugins/demo"}) {
			t.Fatalf("update call = %#v", last)
		}
		if !strings.Contains(out, pluginRestartNotice) {
			t.Fatalf("missing restart notice: %s", out)
		}
	}
	if _, err := executePluginTestCommand(deps, "update", "demo-plugin"); err != nil {
		t.Fatal(err)
	}
	if last := mgr.calls[len(mgr.calls)-1]; last.options.Ref != "" || last.options.Subdir != "" || last.options.Trust {
		t.Fatalf("update must let manager reuse source and must not reuse trust: %#v", last)
	}
}

func TestPluginEnableDisableRemoveAndErrors(t *testing.T) {
	mgr := &fakePluginCommandManager{}
	deps := pluginTestDeps(t, mgr)
	for _, action := range []string{"enable", "disable", "remove"} {
		out, err := executePluginTestCommand(deps, action, "demo-plugin", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var result pluginChangeResult
		if err := json.Unmarshal([]byte(out), &result); err != nil || result.Action != action || result.Name != "demo-plugin" || !result.RuntimeRestartRequired {
			t.Fatalf("%s result = %s, error %v", action, out, err)
		}
		if action != "remove" && (result.Enabled == nil || *result.Enabled != (action == "enable")) {
			t.Fatalf("enabled state = %#v", result)
		}
	}
	if len(mgr.calls) != 3 || !mgr.calls[0].enabled || mgr.calls[1].enabled || mgr.calls[2].name != "remove:demo-plugin" {
		t.Fatalf("mutation calls = %#v", mgr.calls)
	}
	mgr.err = errors.New("plugin is untrusted")
	for _, args := range [][]string{{"list"}, {"show", "demo-plugin"}, {"install", "source"}, {"update", "demo-plugin"}, {"enable", "demo-plugin"}, {"remove", "demo-plugin"}} {
		out, err := executePluginTestCommand(deps, args...)
		if !errors.Is(err, mgr.err) || strings.Contains(out, pluginRestartNotice) {
			t.Fatalf("failure incorrectly reported success: args=%v, output=%s, err=%v", args, out, err)
		}
	}
}

func TestPluginMarketplaceSearchAndExactInstall(t *testing.T) {
	mgr := &fakePluginCommandManager{}
	deps := pluginTestDeps(t, mgr)
	var queries []string
	deps.search = func(_ context.Context, source, query string) ([]plugins.MarketplaceEntry, error) {
		if source != "https://example.com/marketplace.json" {
			t.Fatalf("marketplace = %s", source)
		}
		queries = append(queries, query)
		return []plugins.MarketplaceEntry{{Name: "demo-plugin", Description: "Demo", Source: "https://example.com/demo.git", Ref: "market-ref", Subdir: "market-dir"}}, nil
	}
	out, err := executePluginTestCommand(deps, "search", "demo", "--marketplace", "https://example.com/marketplace.json", "--json")
	if err != nil || !strings.Contains(out, "demo-plugin") || len(queries) != 1 || queries[0] != "demo" {
		t.Fatalf("search = %s, %v, queries=%v", out, err, queries)
	}
	if _, err := executePluginTestCommand(deps, "search"); err == nil {
		t.Fatal("search without marketplace accepted")
	}
	for _, override := range []bool{false, true} {
		args := []string{"install", "demo-plugin", "--marketplace", "https://example.com/marketplace.json"}
		wantRef, wantSubdir := "market-ref", "market-dir"
		if override {
			args = append(args, "--ref", "cli-ref", "--subdir", "cli-dir")
			wantRef, wantSubdir = "cli-ref", "cli-dir"
		}
		if _, err := executePluginTestCommand(deps, args...); err != nil {
			t.Fatal(err)
		}
		last := mgr.calls[len(mgr.calls)-1]
		if last.source != "https://example.com/demo.git" || last.options.Ref != wantRef || last.options.Subdir != wantSubdir || last.options.Trust {
			t.Fatalf("marketplace install = %#v", last)
		}
	}
	before := len(mgr.calls)
	if _, err := executePluginTestCommand(deps, "install", "demo", "--marketplace", "https://example.com/marketplace.json"); err == nil || len(mgr.calls) != before {
		t.Fatalf("partial marketplace match accepted, err=%v", err)
	}
}

func TestPluginCommandRegisteredAndListDoesNotLoadAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".luckyagent"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A malformed provider config must not prevent plugin management.
	if err := os.WriteFile(filepath.Join(home, ".luckyagent", "config.json"), []byte(`{"models":`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	if _, _, err := root.Find([]string{"plugin", "update"}); err != nil {
		t.Fatalf("plugin command missing: %v", err)
	}
	cmd := newPluginCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty list = %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".luckyagent", "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("management command constructed runtime: %v", err)
	}
}

func TestPluginCommandLocalLifecycleWithoutExecutingHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := t.TempDir()
	sentinel := filepath.Join(home, "hook-executed")
	manifest := map[string]any{
		"name": "demo-plugin", "version": "1.0.0",
		"hooks": map[string]any{"pre_tool_use": []map[string]any{{"command": "touch " + sentinel}}},
	}
	writeManifest := func() {
		t.Helper()
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "plugin.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest()
	run := func(args ...string) (pluginChangeResult, error) {
		t.Helper()
		cmd := newPluginCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetArgs(append(args, "--json"))
		err := cmd.Execute()
		var result pluginChangeResult
		if err == nil {
			if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
				t.Fatalf("decode mutation response %s: %v", out.String(), decodeErr)
			}
		}
		return result, err
	}
	installed, err := run("install", source)
	if err != nil || installed.Plugin == nil || installed.Plugin.Entry.Trusted || installed.Plugin.Entry.Enabled {
		t.Fatalf("untrusted install = %#v, %v", installed, err)
	}
	if _, err := run("enable", "demo-plugin"); err == nil {
		t.Fatal("executable plugin enabled without explicit trust")
	}
	manifest["version"] = "2.0.0"
	writeManifest()
	updated, err := run("update", "demo-plugin", "--trust")
	if err != nil || updated.Plugin == nil || updated.Plugin.Entry.Version != "2.0.0" || !updated.Plugin.Entry.Trusted || updated.Plugin.Entry.Enabled {
		t.Fatalf("update must trust new snapshot while retaining disabled state: %#v, %v", updated, err)
	}
	for _, action := range []string{"enable", "disable", "remove"} {
		if _, err := run(action, "demo-plugin"); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	manager, err := plugins.NewManager(filepath.Join(home, ".luckyagent", "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := manager.List()
	if err != nil || len(listed) != 0 {
		t.Fatalf("removed plugin still listed: %#v, %v", listed, err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin management executed a hook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".luckyagent", "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin management initialized Agent: %v", err)
	}
}
