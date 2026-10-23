ALTER TABLE projects ADD COLUMN agent_roles TEXT NOT NULL DEFAULT '{}'; -- JSON {planner,executor,explorer,specsDir} overrides
ALTER TABLE sessions ADD COLUMN spec_path TEXT NOT NULL DEFAULT '';    -- plan spec file written for this session
