package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scopenest/scopenest/native-host/internal/host"
)

const ServerName = "scopenest-mcp"

func New(handler CommandHandler) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Title: "ScopeNest MCP", Version: host.HostVersion},
		&mcp.ServerOptions{
			Instructions: "Operate local ScopeNest browser containers only through the registered tools. Do not infer page access: this server cannot browse, click, inspect page content, or extract browser data.",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	registerTools(server, NewAdapter(handler))
	return server
}
