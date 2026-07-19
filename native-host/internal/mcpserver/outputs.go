package mcpserver

import (
	"encoding/json"
	"errors"
	"net"
	"runtime"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scopenest/scopenest/native-host/internal/host"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
)

type successOutput struct {
	Success bool   `json:"success"`
	Command string `json:"command"`
	Data    any    `json:"data"`
}

type cleanupOutput struct {
	State      string     `json:"state"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ErrorCode  string     `json:"errorCode,omitempty"`
}

type pingOutput struct {
	ScopeNestVersion string        `json:"scopeNestVersion"`
	MCPServerVersion string        `json:"mcpServerVersion"`
	ProtocolVersion  int           `json:"protocolVersion"`
	Platform         string        `json:"platform"`
	StartupCleanup   cleanupOutput `json:"startupCleanup"`
}

type detectedBrowserOutput struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type statusOutput struct {
	ScopeNestVersion              string                  `json:"scopeNestVersion"`
	ProtocolVersion               int                     `json:"protocolVersion"`
	Platform                      string                  `json:"platform"`
	ContainerCount                int                     `json:"containerCount"`
	SavedContainerCount           int                     `json:"savedContainerCount"`
	TemporaryContainerCount       int                     `json:"temporaryContainerCount"`
	RunningContainerCount         int                     `json:"runningContainerCount"`
	PendingCleanupCount           int                     `json:"pendingCleanupCount"`
	DetectedBrowsers              []detectedBrowserOutput `json:"detectedBrowsers"`
	StartupCleanup                cleanupOutput           `json:"startupCleanup"`
	BrokenReferenceCounts         map[string]int          `json:"brokenReferenceCounts"`
	TrustCapabilities             trustCapabilitiesOutput `json:"trustCapabilities"`
	CertificateResourceIssueCount int                     `json:"certificateResourceIssueCount"`
}

type trustCapabilitiesOutput struct {
	TrustInstallation         bool `json:"trustInstallation"`
	ManualTrustAcknowledgment bool `json:"manualTrustAcknowledgment"`
}

type proxyWarningOutput struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

type containerOutput struct {
	ID                    string              `json:"id"`
	Name                  string              `json:"name"`
	Color                 string              `json:"color"`
	Icon                  string              `json:"icon,omitempty"`
	Temporary             bool                `json:"temporary"`
	PendingCleanup        bool                `json:"pendingCleanup"`
	BrowserType           string              `json:"browserType"`
	Running               bool                `json:"running"`
	State                 string              `json:"state"`
	CreatedAt             time.Time           `json:"createdAt"`
	UpdatedAt             time.Time           `json:"updatedAt"`
	LastLaunchedAt        *time.Time          `json:"lastLaunchedAt,omitempty"`
	NetworkMode           string              `json:"networkMode"`
	ProxyProfileID        string              `json:"proxyProfileId,omitempty"`
	EnvironmentTemplateID string              `json:"environmentTemplateId,omitempty"`
	ProxyWarning          *proxyWarningOutput `json:"proxyWarning,omitempty"`
	DirectFallbackUsed    bool                `json:"directFallbackUsed"`
}

type proxyProfileOutput struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Enabled             bool              `json:"enabled"`
	Protocol            string            `json:"protocol"`
	Host                string            `json:"host"`
	Port                int               `json:"port"`
	CertificateIDs      []string          `json:"certificateIds"`
	HealthCheck         proxyHealthOutput `json:"healthCheck"`
	UnavailableBehavior string            `json:"unavailableBehavior"`
}

type proxyHealthOutput struct {
	Enabled   bool `json:"enabled"`
	TimeoutMs int  `json:"timeoutMs"`
}

type environmentTemplateOutput struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	ProxyProfileID string    `json:"proxyProfileId,omitempty"`
	CertificateIDs []string  `json:"certificateIds"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type readinessOutput struct {
	Ready        bool                         `json:"ready"`
	Network      readinessNetworkOutput       `json:"network"`
	Listener     readinessListenerOutput      `json:"listener"`
	Certificates []readinessCertificateOutput `json:"certificates"`
	Warnings     []string                     `json:"warnings"`
}

type readinessNetworkOutput struct {
	RequestedMode          string   `json:"requestedMode"`
	EffectiveMode          string   `json:"effectiveMode"`
	TemplateID             string   `json:"templateId,omitempty"`
	ProxyProfileID         string   `json:"proxyProfileId,omitempty"`
	RequiredCertificateIDs []string `json:"requiredCertificateIds"`
	UnavailableBehavior    string   `json:"unavailableBehavior,omitempty"`
}

type readinessListenerOutput struct {
	Checked   bool   `json:"checked"`
	Reachable bool   `json:"reachable"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type readinessCertificateOutput struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func makeToolResult(response protocol.Response) *mcp.CallToolResult {
	if !response.Success {
		return responseError(response)
	}
	data, err := sanitizeData(response.Command, response.Data)
	if err != nil {
		return toolError(response.Command, "INTERNAL_ERROR")
	}
	output := successOutput{Success: true, Command: response.Command, Data: data}
	raw, err := json.Marshal(output)
	if err != nil {
		return toolError(response.Command, "INTERNAL_ERROR")
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: output}
}

func sanitizeData(command string, data any) (any, error) {
	switch command {
	case "ping":
		var raw struct {
			HostVersion     string        `json:"hostVersion"`
			ProtocolVersion int           `json:"protocolVersion"`
			StartupCleanup  cleanupOutput `json:"startupCleanup"`
		}
		if err := remarshal(data, &raw); err != nil {
			return nil, err
		}
		return pingOutput{ScopeNestVersion: raw.HostVersion, MCPServerVersion: host.HostVersion, ProtocolVersion: raw.ProtocolVersion, Platform: runtime.GOOS, StartupCleanup: raw.StartupCleanup}, nil
	case "get_status":
		return sanitizeStatus(data)
	case "list_containers", "get_running_containers":
		var value []containerOutput
		if err := remarshal(data, &value); err != nil {
			return nil, err
		}
		return value, nil
	case "create_container", "create_temporary_container", "launch_container", "close_container":
		var value containerOutput
		if err := remarshal(data, &value); err != nil {
			return nil, err
		}
		return value, nil
	case "list_proxy_profiles":
		var value []proxyProfileOutput
		if err := remarshal(data, &value); err != nil {
			return nil, err
		}
		for _, profile := range value {
			if !isLoopbackHost(profile.Host) {
				return nil, errors.New("proxy profile contains a non-loopback host")
			}
		}
		return value, nil
	case "list_environment_templates":
		var value []environmentTemplateOutput
		if err := remarshal(data, &value); err != nil {
			return nil, err
		}
		return value, nil
	case "get_container_readiness":
		var value readinessOutput
		if err := remarshal(data, &value); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, errors.New("unsupported MCP output command")
	}
}

func isLoopbackHost(value string) bool {
	if value == "localhost" {
		return true
	}
	ip := net.ParseIP(value)
	return ip != nil && ip.IsLoopback()
}

func sanitizeStatus(data any) (statusOutput, error) {
	var raw struct {
		HostVersion             string `json:"hostVersion"`
		ProtocolVersion         int    `json:"protocolVersion"`
		Platform                string `json:"platform"`
		ContainerCount          int    `json:"containerCount"`
		SavedContainerCount     int    `json:"savedContainerCount"`
		TemporaryContainerCount int    `json:"temporaryContainerCount"`
		RunningContainerCount   int    `json:"runningContainerCount"`
		PendingCleanupCount     int    `json:"pendingCleanupCount"`
		DetectedBrowsers        []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"detectedBrowsers"`
		StartupCleanup   cleanupOutput           `json:"startupCleanup"`
		Capabilities     trustCapabilitiesOutput `json:"capabilities"`
		BrokenReferences []struct {
			Kind string `json:"kind"`
		} `json:"brokenReferences"`
		CertificateResourceIssues []json.RawMessage `json:"certificateResourceIssues"`
	}
	if err := remarshal(data, &raw); err != nil {
		return statusOutput{}, err
	}
	browsers := make([]detectedBrowserOutput, 0, len(raw.DetectedBrowsers))
	for _, item := range raw.DetectedBrowsers {
		browsers = append(browsers, detectedBrowserOutput{Type: item.Type, Name: item.Name})
	}
	broken := map[string]int{}
	for _, item := range raw.BrokenReferences {
		broken[item.Kind]++
	}
	return statusOutput{ScopeNestVersion: raw.HostVersion, ProtocolVersion: raw.ProtocolVersion, Platform: raw.Platform, ContainerCount: raw.ContainerCount, SavedContainerCount: raw.SavedContainerCount, TemporaryContainerCount: raw.TemporaryContainerCount, RunningContainerCount: raw.RunningContainerCount, PendingCleanupCount: raw.PendingCleanupCount, DetectedBrowsers: browsers, StartupCleanup: raw.StartupCleanup, BrokenReferenceCounts: broken, TrustCapabilities: raw.Capabilities, CertificateResourceIssueCount: len(raw.CertificateResourceIssues)}, nil
}

func remarshal(value any, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
