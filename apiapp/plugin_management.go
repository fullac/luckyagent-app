package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	transport "github.com/yurika0211/luckyagent-api"
	plugins "github.com/yurika0211/luckyagent-plugins"
)

const pluginManagementNotice = "插件变更已保存，重启 LuckyAgent 服务后生效。"

var managedPluginName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Keep prompts, scripts, MCP commands, environment and credentials out of the
// management response. The installed entry and contribution names are enough
// for a client to inspect and manage an installation.
type managedPlugin struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Description   string   `json:"description"`
	Source        string   `json:"source"`
	Ref           string   `json:"ref"`
	Revision      string   `json:"revision"`
	Subdir        string   `json:"subdir"`
	Digest        string   `json:"digest"`
	InstalledAt   string   `json:"installed_at"`
	Enabled       bool     `json:"enabled"`
	Trusted       bool     `json:"trusted"`
	RequiresTrust bool     `json:"requires_trust"`
	Skills        []string `json:"skills"`
	Commands      []string `json:"commands"`
	Agents        []string `json:"agents"`
	MCPServers    []string `json:"mcp_servers"`
	HookCount     int      `json:"hook_count"`
	Error         string   `json:"error,omitempty"`
}

type pluginInstallRequest struct {
	Source string `json:"source"`
	Ref    string `json:"ref"`
	Subdir string `json:"subdir"`
	Trust  bool   `json:"trust"`
}

type pluginUpdateRequest struct {
	Trust bool `json:"trust"`
}

func summarizePlugin(item plugins.Installed) managedPlugin {
	e := item.Entry
	source := e.Source
	if u, err := url.Parse(source); err == nil && u.Host != "" {
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		source = u.String()
	}
	p := managedPlugin{Name: e.Name, Version: e.Version, Source: source, Ref: e.Ref,
		Revision: e.Revision, Subdir: e.Subdir, Digest: e.Digest, InstalledAt: e.InstalledAt,
		Enabled: e.Enabled, Trusted: e.Trusted, Error: item.Error,
		Skills: []string{}, Commands: []string{}, Agents: []string{}, MCPServers: []string{}}
	if b := item.Bundle; b != nil {
		p.Description = b.Manifest.Description
		for _, skill := range b.Skills {
			p.Skills = append(p.Skills, skill.Name)
		}
		for _, command := range b.Commands {
			p.Commands = append(p.Commands, command.Name)
		}
		for _, agent := range b.Agents {
			p.Agents = append(p.Agents, agent.Name)
		}
		for name := range b.MCPServers {
			p.MCPServers = append(p.MCPServers, name)
		}
		p.HookCount = len(b.Hooks.PreToolUse) + len(b.Hooks.PostToolUse)
		p.RequiresTrust = len(p.MCPServers) > 0 || p.HookCount > 0
	}
	sort.Strings(p.Skills)
	sort.Strings(p.Commands)
	sort.Strings(p.Agents)
	sort.Strings(p.MCPServers)
	return p
}

func (s *Server) pluginManager(create bool) (*plugins.Manager, error) {
	if s.agent == nil || s.agent.Config() == nil {
		return nil, errors.New("plugin runtime unavailable")
	}
	root := filepath.Join(s.agent.Config().HomeDir(), "plugins")
	if !create {
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	return plugins.NewManager(root)
}

// registerPluginManagementRoutes is called before the shared authentication,
// admission, rate limiting and logging middleware wrap the HTTP mux.
func (s *Server) registerPluginManagementRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/plugins", s.handlePlugins)
	mux.HandleFunc("/api/v1/plugins/", s.handlePlugin)
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
		return
	}
	if s.agent == nil || s.agent.Config() == nil {
		s.sendError(w, "plugin runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	if r.Method == http.MethodPost {
		var req pluginInstallRequest
		if err := transport.DecodeJSON(w, r, &req, 16<<10); err != nil {
			transport.SendRequestError(w, err)
			return
		}
		req.Source, req.Ref, req.Subdir = strings.TrimSpace(req.Source), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Subdir)
		if req.Source == "" || len(req.Source) > 4096 || len(req.Ref) > 256 || len(req.Subdir) > 1024 {
			s.sendError(w, "source is required; source, ref or subdir is too long", http.StatusBadRequest, "")
			return
		}
		mgr, err := s.pluginManager(true)
		if err != nil {
			s.sendError(w, err.Error(), http.StatusInternalServerError, "")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		item, err := mgr.Install(ctx, req.Source, plugins.InstallOptions{Ref: req.Ref, Subdir: req.Subdir, Trust: req.Trust})
		if err != nil {
			s.sendPluginManagementError(w, err)
			return
		}
		p := summarizePlugin(item)
		s.sendPluginChange(w, http.StatusCreated, "install", p.Name, &p)
		return
	}
	mgr, err := s.pluginManager(false)
	if err != nil {
		s.sendError(w, err.Error(), http.StatusInternalServerError, "")
		return
	}
	items := []managedPlugin{}
	if mgr != nil {
		installed, err := mgr.List()
		if err != nil {
			s.sendError(w, err.Error(), http.StatusInternalServerError, "")
			return
		}
		for _, item := range installed {
			items = append(items, summarizePlugin(item))
		}
	}
	s.sendJSON(w, http.StatusOK, map[string]any{"plugins": items, "count": len(items), "changes_require_restart": true})
}

func (s *Server) handlePlugin(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/plugins/")
	parts := strings.Split(path, "/")
	if len(parts) > 2 || len(parts[0]) > 48 || !managedPluginName.MatchString(parts[0]) {
		s.sendError(w, "invalid plugin name or route", http.StatusBadRequest, "")
		return
	}
	name, action := parts[0], ""
	if len(parts) == 2 {
		action = parts[1]
		if action != "enable" && action != "disable" && action != "update" {
			s.sendError(w, "unknown plugin action", http.StatusNotFound, "")
			return
		}
	}
	allowed := "GET, DELETE"
	validMethod := r.Method == http.MethodGet || r.Method == http.MethodDelete
	if action != "" {
		allowed, validMethod = "POST", r.Method == http.MethodPost
	}
	if !validMethod {
		w.Header().Set("Allow", allowed)
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
		return
	}
	if s.agent == nil || s.agent.Config() == nil {
		s.sendError(w, "plugin runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	mgr, err := s.pluginManager(false)
	if err != nil {
		s.sendError(w, err.Error(), http.StatusInternalServerError, "")
		return
	}
	var selected *managedPlugin
	if mgr != nil {
		items, err := mgr.List()
		if err != nil {
			s.sendError(w, err.Error(), http.StatusInternalServerError, "")
			return
		}
		for _, item := range items {
			if item.Entry.Name == name {
				p := summarizePlugin(item)
				selected = &p
				break
			}
		}
	}
	if selected == nil {
		s.sendError(w, "plugin not installed", http.StatusNotFound, "")
		return
	}
	switch {
	case action == "update":
		var req pluginUpdateRequest
		if err := transport.DecodeJSON(w, r, &req, 4096); err != nil {
			transport.SendRequestError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		item, err := mgr.Update(ctx, name, plugins.InstallOptions{Trust: req.Trust})
		if err != nil {
			s.sendPluginManagementError(w, err)
			return
		}
		p := summarizePlugin(item)
		s.sendPluginChange(w, http.StatusOK, action, name, &p)
	case action == "enable" || action == "disable":
		enabled := action == "enable"
		if err := mgr.Enable(name, enabled); err != nil {
			s.sendPluginManagementError(w, err)
			return
		}
		selected.Enabled = enabled
		s.sendPluginChange(w, http.StatusOK, action, name, selected)
	case r.Method == http.MethodDelete:
		if err := mgr.Remove(name); err != nil {
			s.sendPluginManagementError(w, err)
			return
		}
		s.sendPluginChange(w, http.StatusOK, "remove", name, nil)
	default:
		s.sendJSON(w, http.StatusOK, map[string]any{"plugin": selected})
	}
}

func (s *Server) sendPluginChange(w http.ResponseWriter, status int, action, name string, p *managedPlugin) {
	s.sendJSON(w, status, map[string]any{"action": action, "name": name, "plugin": p,
		"runtime_restart_required": true, "message": pluginManagementNotice})
}

func (s *Server) sendPluginManagementError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	var pathErr *os.PathError
	switch {
	case errors.Is(err, plugins.ErrBusy):
		status = http.StatusConflict
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		status = http.StatusRequestTimeout
	case errors.As(err, &pathErr):
		status = http.StatusInternalServerError
	case strings.Contains(err.Error(), "is not installed"):
		status = http.StatusNotFound
	case strings.Contains(err.Error(), "already installed"), strings.Contains(err.Error(), "trust"), strings.Contains(err.Error(), "digest"):
		status = http.StatusConflict
	}
	s.sendError(w, err.Error(), status, "")
}
