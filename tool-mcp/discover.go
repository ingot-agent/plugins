package toolmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ingot-agent/sdk/logging"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxToolPages       = 100
	maxListedTools     = 4096
	maxToolNameRetries = 64
)

// discoveredTool is one normalized remote tool before a model name is
// assigned.
type discoveredTool struct {
	wireName    string
	description string
	schema      []byte
}

// discover performs one full discovery pass over the enabled servers and
// returns a snapshot. It never returns nil and never fails as a whole: a
// broken server is recorded in the snapshot so other servers stay usable.
func (p *platform) discover(ctx context.Context, config Config, previous *Snapshot) *Snapshot {
	hash, err := config.configHash()
	if err != nil {
		p.logger.Warn(ctx, "MCP configuration digest failed", logging.String("error", sanitizeMessage(err.Error())))
	}
	snapshot := emptySnapshot(hash)

	taken := make(map[string]bool)
	total := 0
	for _, server := range config.enabledServers() {
		if err := ctx.Err(); err != nil {
			// Cancellation must not delete a working tool set: retain what the
			// previous snapshot exported for this server.
			snapshot.Servers = append(snapshot.Servers, retainPrevious(server, previous,
				serverStatusSkipped, "discovery interrupted by cancellation"))
			continue
		}

		tools, rejected, failure := p.discoverServer(ctx, server, config)
		serverSnapshot := ServerSnapshot{
			Name:      server.Name,
			Transport: server.Transport,
			Status:    serverStatusOK,
			Tools:     []ToolSnapshot{},
			Rejected:  rejected,
		}
		if failure != "" {
			// A transient outage must not silently delete a working tool set.
			// Retain the previous entry for the same server and transport so a
			// later restart keeps the tools the user already approved.
			retained := retainPrevious(server, previous, serverStatusError, failure)
			for _, tool := range retained.Tools {
				taken[tool.ModelName] = true
			}
			total += len(retained.Tools)
			snapshot.Servers = append(snapshot.Servers, retained)
			continue
		}
		if total+len(tools) > config.MaxToolsTotal {
			serverSnapshot.Status = serverStatusSkipped
			serverSnapshot.Error = fmt.Sprintf(
				"skipped: exporting its %d tools would exceed max_tools_total=%d", len(tools), config.MaxToolsTotal)
			snapshot.Servers = append(snapshot.Servers, serverSnapshot)
			continue
		}

		for _, discovered := range tools {
			modelName, ok := assignModelName(config.ToolNamePrefix, server.Name, discovered.wireName, taken)
			if !ok {
				serverSnapshot.Rejected = append(serverSnapshot.Rejected, RejectedTool{
					WireName: discovered.wireName,
					Reason:   "could not assign a unique model-facing name",
				})
				continue
			}
			serverSnapshot.Tools = append(serverSnapshot.Tools, ToolSnapshot{
				ModelName:   modelName,
				WireName:    discovered.wireName,
				Description: discovered.description,
				Schema:      append([]byte(nil), discovered.schema...),
			})
			serverSnapshot.SchemaBytes += schemaBytes(discovered.schema)
		}
		serverSnapshot.ToolCount = len(serverSnapshot.Tools)
		total += len(serverSnapshot.Tools)
		snapshot.Servers = append(snapshot.Servers, serverSnapshot)
	}
	return snapshot
}

// discoverServer connects to one server, lists its tools, and normalizes each
// accepted tool. The connection is closed before returning.
func (p *platform) discoverServer(ctx context.Context, server ServerConfig, config Config) ([]discoveredTool, []RejectedTool, string) {
	connectCtx, cancelConnect := context.WithTimeout(ctx, time.Duration(server.ConnectTimeoutSeconds)*time.Second)
	connection, err := p.dial(connectCtx, server, config.MaxResultBytes)
	cancelConnect()
	if err != nil {
		return nil, nil, sanitizeMessage(err.Error())
	}
	defer connection.Close()

	listTimeout := time.Duration(server.CallTimeoutSeconds) * time.Second
	if listTimeout < time.Minute {
		listTimeout = time.Minute
	}
	listCtx, cancelList := context.WithTimeout(ctx, listTimeout)
	defer cancelList()

	remoteTools, err := listAllTools(listCtx, connection.session)
	if err != nil {
		// Child stderr can echo injected credentials, so it is logged redacted
		// and never written to the snapshot, an Operation result, or the model.
		if stderr := connection.stderr.String(); stderr != "" {
			p.logger.Warn(ctx, "MCP server wrote to stderr during discovery",
				logging.String("server", server.Name),
				logging.String("stderr", sanitizeMessage(redactSecrets(server, stderr))))
		}
		return nil, nil, sanitizeMessage(err.Error())
	}

	tools := make([]discoveredTool, 0, len(remoteTools))
	var rejected []RejectedTool
	seen := make(map[string]bool, len(remoteTools))
	for _, remote := range remoteTools {
		if remote == nil {
			continue
		}
		wireName := strings.TrimSpace(remote.Name)
		if wireName == "" || seen[wireName] {
			continue
		}
		seen[wireName] = true
		if !matchesToolFilter(server, wireName) {
			continue
		}
		schema, err := normalizeInputSchema(rawSchema(remote))
		if err != nil {
			rejected = append(rejected, RejectedTool{WireName: wireName, Reason: sanitizeMessage(err.Error())})
			continue
		}
		modelName := buildToolName(config.ToolNamePrefix, server.Name, wireName)
		if err := compileSchema(modelName, schema); err != nil {
			rejected = append(rejected, RejectedTool{WireName: wireName, Reason: sanitizeMessage(err.Error())})
			continue
		}
		tools = append(tools, discoveredTool{
			wireName:    wireName,
			description: describeTool(server.Name, wireName, remote.Description),
			schema:      schema,
		})
	}
	return tools, rejected, ""
}

// listAllTools follows pagination and rejects an unbounded or repeating
// listing.
func listAllTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var result []*mcp.Tool
	cursor := ""
	seenCursors := make(map[string]bool)
	for page := 0; page < maxToolPages; page++ {
		response, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		result = append(result, response.Tools...)
		if len(result) > maxListedTools {
			return nil, fmt.Errorf("server listed more than %d tools", maxListedTools)
		}
		if response.NextCursor == "" {
			return result, nil
		}
		if seenCursors[response.NextCursor] {
			return nil, errors.New("server repeated a pagination cursor")
		}
		seenCursors[response.NextCursor] = true
		cursor = response.NextCursor
	}
	return nil, fmt.Errorf("server exceeded %d tool listing pages", maxToolPages)
}

// rawSchema extracts the input schema of a remote tool. The SDK exposes it as
// the default JSON marshaling of the server's schema, so it is re-encoded.
func rawSchema(remote *mcp.Tool) []byte {
	if remote == nil || remote.InputSchema == nil {
		return nil
	}
	encoded, err := json.Marshal(remote.InputSchema)
	if err != nil {
		return nil
	}
	return encoded
}

// assignModelName reserves a unique model-facing name.
func assignModelName(prefix, server, wireName string, taken map[string]bool) (string, bool) {
	base := buildToolName(prefix, server, wireName)
	if !taken[base] {
		taken[base] = true
		return base, true
	}
	for attempt := 0; attempt < maxToolNameRetries; attempt++ {
		candidate := disambiguateToolName(base, fmt.Sprintf("%s\x00%s\x00%d", server, wireName, attempt))
		if !taken[candidate] {
			taken[candidate] = true
			return candidate, true
		}
	}
	return "", false
}

// retainPrevious builds a server snapshot that keeps the previous tool set. It
// is used whenever discovery could not produce an authoritative answer, so an
// outage or a cancellation cannot silently delete tools.
func retainPrevious(server ServerConfig, previous *Snapshot, status, message string) ServerSnapshot {
	entry := ServerSnapshot{
		Name:      server.Name,
		Transport: server.Transport,
		Status:    status,
		Error:     message,
		Tools:     []ToolSnapshot{},
	}
	if prior, ok := previous.server(server.Name); ok && prior.Transport == server.Transport {
		entry.Tools = prior.Tools
		entry.Rejected = prior.Rejected
		entry.ToolCount = prior.ToolCount
		entry.SchemaBytes = prior.SchemaBytes
		entry.Error += " (retaining the previous tool set)"
	}
	return entry
}
