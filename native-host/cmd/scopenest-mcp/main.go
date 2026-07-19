package main

import (
	"context"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/certstore"
	"github.com/scopenest/scopenest/native-host/internal/host"
	"github.com/scopenest/scopenest/native-host/internal/mcpserver"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

func main() {
	os.Exit(run(context.Background(), &mcp.StdioTransport{}, initializeDefaultServer))
}

func run(ctx context.Context, transport mcp.Transport, initialize func() (*mcp.Server, error)) int {
	server, err := initialize()
	if err != nil {
		return 1
	}
	if err := server.Run(ctx, transport); err != nil {
		return 1
	}
	return 0
}

func initializeDefaultServer() (*mcp.Server, error) {
	dataDir, err := host.DefaultDataDir()
	if err != nil {
		return nil, err
	}
	return initializeServer(dataDir, browser.ExecLauncher{}, certstore.NewTrustStore())
}

func initializeServer(dataDir string, launcher browser.Launcher, trust certstore.TrustStore) (*mcp.Server, error) {
	st, err := store.New(dataDir)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(); err != nil {
		return nil, err
	}
	certificateManager := certstore.NewManager(st, trust)
	scopeNestHost := host.New(st, launcher, certificateManager)
	return mcpserver.New(scopeNestHost), nil
}
