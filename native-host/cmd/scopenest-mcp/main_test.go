package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/certstore"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

type noTrustStore struct{}

func (noTrustStore) Scope() string                        { return "test" }
func (noTrustStore) Supported() bool                      { return false }
func (noTrustStore) Verify([]byte, string) (bool, error)  { return false, nil }
func (noTrustStore) Install([]byte, string) (bool, error) { return false, errors.New("unsupported") }
func (noTrustStore) Remove([]byte, string) error          { return errors.New("unsupported") }

func TestInitializeServerRunsStoreMigrations(t *testing.T) {
	dataDir := t.TempDir()
	server, err := initializeServer(dataDir, browser.ExecLauncher{}, noTrustStore{})
	if err != nil {
		t.Fatal(err)
	}
	if server == nil {
		t.Fatal("server is nil")
	}
	st, err := store.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Version == 0 {
		t.Fatal("migration did not initialize the database version")
	}
}

func TestRunReturnsNonzeroOnInitializationFailure(t *testing.T) {
	code := run(context.Background(), &mcp.StdioTransport{}, func() (*mcp.Server, error) {
		return nil, errors.New("initialization failed")
	})
	if code == 0 {
		t.Fatal("initialization failure returned success")
	}
}

func TestStdioTransportAndCleanTermination(t *testing.T) {
	if os.Getenv("SCOPENEST_MCP_TEST_HELPER") == "1" {
		dataDir := os.Getenv("SCOPENEST_MCP_TEST_DATA")
		server, err := initializeServer(dataDir, browser.ExecLauncher{}, noTrustStore{})
		if err != nil {
			os.Exit(2)
		}
		os.Exit(run(context.Background(), &mcp.StdioTransport{}, func() (*mcp.Server, error) { return server, nil }))
	}

	dataDir := filepath.Join(t.TempDir(), "mcp-data")
	command := exec.Command(os.Args[0], "-test.run=TestStdioTransportAndCleanTermination")
	command.Env = append(os.Environ(), "SCOPENEST_MCP_TEST_HELPER=1", "SCOPENEST_MCP_TEST_DATA="+dataDir)
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "scopenest_ping", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("ping failed: %#v", result.Content)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

var _ certstore.TrustStore = noTrustStore{}
