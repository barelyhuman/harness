package app

import (
	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/workspace"
)

// DefaultSpecsDir is where plan specs go (relative to the project root) when
// neither the project nor settings.json names a directory.
const DefaultSpecsDir = "docs-spec/plans"

// Agent roles accepted by SetAgentRole.
const (
	RolePlanner  = "planner"
	RoleExecutor = "executor"
	RoleExplorer = "explorer"
	RoleSpecsDir = "specsDir"
)

// AgentRolesInfo is the AgentRoles result: the effective roles, the
// settings.json defaults and the project's own overrides.
type AgentRolesInfo struct {
	Roles    workspace.AgentRoles `json:"roles"`
	Defaults workspace.AgentRoles `json:"defaults"`
	Project  workspace.AgentRoles `json:"project"`
}

// settingString reads key under the "agents" object, nested
// ({"agents":{"planner":..}}) or dotted ({"agents.planner":..}).
func settingString(v map[string]any, key string) string {
	if m, ok := v["agents"].(map[string]any); ok {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	s, _ := v["agents."+key].(string)
	return s
}

func (s *SessionService) roleDefaults() workspace.AgentRoles {
	var r workspace.AgentRoles
	if s.settings != nil {
		v := s.settings.GetSettings().Values
		r = workspace.AgentRoles{Planner: settingString(v, RolePlanner), Executor: settingString(v, RoleExecutor),
			Explorer: settingString(v, RoleExplorer), SpecsDir: settingString(v, RoleSpecsDir)}
	}
	if r.SpecsDir == "" {
		r.SpecsDir = DefaultSpecsDir
	}
	return r
}

func (s *SessionService) rolesInfo(projectID string) (AgentRolesInfo, error) {
	p, err := s.projects.Get(projectID)
	if err != nil {
		return AgentRolesInfo{}, err
	}
	def := s.roleDefaults()
	eff := def
	pick := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	pick(&eff.Planner, p.AgentRoles.Planner)
	pick(&eff.Executor, p.AgentRoles.Executor)
	pick(&eff.Explorer, p.AgentRoles.Explorer)
	pick(&eff.SpecsDir, p.AgentRoles.SpecsDir)
	return AgentRolesInfo{Roles: eff, Defaults: def, Project: p.AgentRoles}, nil
}

// agentRoles returns the effective roles of a project (zero value on error).
func (s *SessionService) agentRoles(projectID string) workspace.AgentRoles {
	info, _ := s.rolesInfo(projectID)
	return info.Roles
}

// AgentRoles returns the planner/executor/explorer models and specs dir of a
// project: project overrides win over settings.json agents.* keys.
func (s *SessionService) AgentRoles(projectID string) (AgentRolesInfo, error) {
	info, err := s.rolesInfo(projectID)
	return info, wrapErr(err)
}

// SetAgentRole sets a project override for role (planner | executor |
// explorer: a model key <provider>/<id>; specsDir: a path). An empty value
// clears the override so the settings.json default applies again.
func (s *SessionService) SetAgentRole(projectID, role, value string) (AgentRolesInfo, error) {
	p, err := s.projects.Get(projectID)
	if err != nil {
		return AgentRolesInfo{}, wrapErr(err)
	}
	r := p.AgentRoles
	switch role {
	case RolePlanner, RoleExecutor, RoleExplorer:
		if value != "" && agent.ModelProvider(value) == "" {
			return AgentRolesInfo{}, badArg("model key must be <provider>/<id>")
		}
		switch role {
		case RolePlanner:
			r.Planner = value
		case RoleExecutor:
			r.Executor = value
		default:
			r.Explorer = value
		}
	case RoleSpecsDir:
		r.SpecsDir = value
	default:
		return AgentRolesInfo{}, badArg("role must be planner, executor, explorer or specsDir")
	}
	if err := s.projects.SetAgentRoles(projectID, r); err != nil {
		return AgentRolesInfo{}, wrapErr(err)
	}
	return s.AgentRoles(projectID)
}
