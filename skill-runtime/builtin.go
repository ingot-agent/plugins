package skillruntime

import (
	_ "embed"
	"fmt"
)

const (
	skillSourceBuiltin = "builtin"
	skillSourceState   = "state"
)

//go:embed skill-create.md
var skillCreateDocument []byte

func loadBuiltinSkills() (map[string]*skill, error) {
	metadata, instructions, err := parseSkill(skillCreateDocument, "skill-create")
	if err != nil {
		return nil, fmt.Errorf("load built-in skill-create: %w", err)
	}
	item := &skill{
		Name: metadata.Name, Description: metadata.Description, Instructions: instructions,
		License: metadata.License, Compatibility: metadata.Compatibility, AllowedTools: metadata.AllowedTools,
		References: map[string]string{}, Source: skillSourceBuiltin,
	}
	item.Digest = skillDigest(skillCreateDocument, item.References)
	return map[string]*skill{item.Name: item}, nil
}
