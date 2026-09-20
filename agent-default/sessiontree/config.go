package sessiontree

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/agent"
	"github.com/pelletier/go-toml/v2"
)

const (
	configFileName       = "subagents.toml"
	configVersion        = 1
	submitToolName       = "submit_agent_result"
	defaultMaxDepth      = 8
	defaultMaxQueue      = 64
	defaultMaxActive     = 128
	defaultMaxTaskBytes  = 256 * 1024
	defaultMaxResultSize = 256 * 1024
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

type fileConfig struct {
	Version          int           `toml:"subagents_config_version"`
	RootAllowedTypes []string      `toml:"root_allowed_types"`
	Agents           []agentConfig `toml:"agents"`
}

type agentConfig struct {
	Name              string   `toml:"name"`
	Description       string   `toml:"description"`
	SystemPrompt      string   `toml:"system_prompt"`
	Tools             []string `toml:"tools"`
	AllowedChildTypes []string `toml:"allowed_child_types"`
}

type definitionEntry struct {
	info       agent.AgentTypeInfo
	definition agent.ChildDefinition
	digest     string
}

type configuration struct {
	enabled     bool
	rootAllowed []string
	definitions map[string]definitionEntry
}

func loadConfiguration(root string) (configuration, error) {
	if root == "" {
		return configuration{}, errors.New("agent.default state scope is empty")
	}
	raw, err := os.ReadFile(filepath.Join(root, configFileName))
	if errors.Is(err, os.ErrNotExist) {
		return configuration{definitions: map[string]definitionEntry{}}, nil
	}
	if err != nil {
		return configuration{}, fmt.Errorf("read %s: %w", configFileName, err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document fileConfig
	if err := decoder.Decode(&document); err != nil {
		return configuration{}, fmt.Errorf("decode %s: %w", configFileName, err)
	}
	if document.Version != configVersion {
		return configuration{}, fmt.Errorf("subagents_config_version must be %d", configVersion)
	}
	definitions := make(map[string]definitionEntry, len(document.Agents))
	for index, configured := range document.Agents {
		if !identifierPattern.MatchString(configured.Name) {
			return configuration{}, fmt.Errorf("agents[%d].name %q is invalid", index, configured.Name)
		}
		if _, duplicate := definitions[configured.Name]; duplicate {
			return configuration{}, fmt.Errorf("duplicate child agent type %q", configured.Name)
		}
		if configured.Description == "" || !utf8.ValidString(configured.Description) || configured.SystemPrompt == "" || !utf8.ValidString(configured.SystemPrompt) {
			return configuration{}, fmt.Errorf("agent type %q requires UTF-8 description and system_prompt", configured.Name)
		}
		if len(configured.Tools) == 0 || !slices.Contains(configured.Tools, submitToolName) {
			return configuration{}, fmt.Errorf("agent type %q must include %s", configured.Name, submitToolName)
		}
		if err := validateNames(configured.Name+" tools", configured.Tools); err != nil {
			return configuration{}, err
		}
		if err := validateNames(configured.Name+" allowed_child_types", configured.AllowedChildTypes); err != nil {
			return configuration{}, err
		}
		definition := agent.ChildDefinition{
			SystemPrompt:      configured.SystemPrompt,
			Tools:             append([]string{}, configured.Tools...),
			AllowedChildTypes: append([]string{}, configured.AllowedChildTypes...),
		}
		digest, err := definitionDigest(configured.Name, definition)
		if err != nil {
			return configuration{}, err
		}
		definitions[configured.Name] = definitionEntry{
			info:       agent.AgentTypeInfo{Name: configured.Name, Description: configured.Description},
			definition: definition,
			digest:     digest,
		}
	}
	if err := validateNames("root_allowed_types", document.RootAllowedTypes); err != nil {
		return configuration{}, err
	}
	for _, name := range document.RootAllowedTypes {
		if _, exists := definitions[name]; !exists {
			return configuration{}, fmt.Errorf("root_allowed_types references unknown type %q", name)
		}
	}
	for name, entry := range definitions {
		for _, childType := range entry.definition.AllowedChildTypes {
			if _, exists := definitions[childType]; !exists {
				return configuration{}, fmt.Errorf("agent type %q references unknown child type %q", name, childType)
			}
		}
	}
	return configuration{
		enabled:     len(definitions) != 0,
		rootAllowed: append([]string{}, document.RootAllowedTypes...),
		definitions: definitions,
	}, nil
}

func validateNames(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("%s contains invalid name %q", field, value)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains duplicate name %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func definitionDigest(name string, definition agent.ChildDefinition) (string, error) {
	raw, err := json.Marshal(struct {
		Name       string                `json:"name"`
		Definition agent.ChildDefinition `json:"definition"`
	}{Name: name, Definition: definition})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
