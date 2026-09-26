package sessiontree

// Built-in types are used only when subagents.toml is absent and the composed
// tool runtime provides submit_agent_result. The ordered allowlists never grant
// access to a tool that is not actually installed.
func builtinConfiguration(available map[string]struct{}) (configuration, error) {
	pick := func(names ...string) []string {
		selected := make([]string, 0, len(names)+1)
		for _, name := range names {
			if _, ok := available[name]; ok {
				selected = append(selected, name)
			}
		}
		return append(selected, submitToolName)
	}
	return configurationFromDocument(fileConfig{
		Version:          configVersion,
		RootAllowedTypes: []string{"coder", "explorer", "reviewer"},
		Agents: []agentConfig{
			{
				Name: "coder", Description: "Implement a bounded coding task and report the changes.",
				SystemPrompt: "Implement the assigned task in the provided workspace. Inspect relevant files first, make focused changes, and report what changed and what you verified. Do not assume a successful command or test without running it. Finish by calling submit_agent_result alone with the result.",
				Tools:        pick("read_file", "search", "edit_file", "shell_exec"),
			},
			{
				Name: "explorer", Description: "Explore the workspace and report relevant findings without editing.",
				SystemPrompt: "Investigate the assigned question using read-only tools. Do not modify files or run commands. Report relevant paths and evidence, then call submit_agent_result alone with the result.",
				Tools:        pick("read_file", "search"),
			},
			{
				Name: "reviewer", Description: "Review code and report actionable findings without editing.",
				SystemPrompt: "Review the assigned code without modifying files or running commands. Prioritize concrete defects with file locations and explain their impact. If none are found, say so. Finish by calling submit_agent_result alone with the result.",
				Tools:        pick("read_file", "search"),
			},
		},
	})
}
