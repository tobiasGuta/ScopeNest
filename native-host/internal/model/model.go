package model

import "time"

const (
	StateStopped   = "stopped"
	StateLaunching = "launching"
	StateRunning   = "running"

	CertificateTrustUntrusted                    = "untrusted"
	CertificateTrustInstalling                   = "installing"
	CertificateTrustTrusted                      = "trusted"
	CertificateTrustRemoving                     = "removing"
	CertificateTrustError                        = "trust_error"
	CertificateTrustManualAcknowledgedUnverified = "manual_trust_acknowledged_unverified"
)

type ProxyLaunchWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

type ProxyHealthCheck struct {
	Enabled   bool `json:"enabled"`
	TimeoutMs int  `json:"timeoutMs"`
}

type ProxyProfile struct {
	ID                  string           `json:"id"`
	Name                string           `json:"name"`
	Enabled             bool             `json:"enabled"`
	Protocol            string           `json:"protocol"`
	Host                string           `json:"host"`
	Port                int              `json:"port"`
	BypassRules         []string         `json:"bypassRules"`
	CertificateIDs      []string         `json:"certificateIds"`
	HealthCheck         ProxyHealthCheck `json:"healthCheck"`
	UnavailableBehavior string           `json:"unavailableBehavior"`
	CreatedAt           time.Time        `json:"createdAt"`
	UpdatedAt           time.Time        `json:"updatedAt"`
}

type Certificate struct {
	ID                        string                     `json:"id"`
	DisplayName               string                     `json:"displayName"`
	SHA256Fingerprint         string                     `json:"sha256Fingerprint"`
	Subject                   string                     `json:"subject"`
	Issuer                    string                     `json:"issuer"`
	SerialNumber              string                     `json:"serialNumber"`
	NotBefore                 time.Time                  `json:"notBefore"`
	NotAfter                  time.Time                  `json:"notAfter"`
	IsCertificateAuthority    bool                       `json:"isCertificateAuthority"`
	KeyUsage                  []string                   `json:"keyUsage"`
	SourceFormat              string                     `json:"sourceFormat"`
	Trusted                   bool                       `json:"trusted"`
	TrustScope                string                     `json:"trustScope"`
	InstalledByScopeNest      bool                       `json:"installedByScopeNest"`
	TrustState                string                     `json:"trustState"`
	TrustOperationID          string                     `json:"trustOperationId,omitempty"`
	TrustOperationFingerprint string                     `json:"trustOperationFingerprint,omitempty"`
	TrustOperationWasTrusted  bool                       `json:"trustOperationWasTrusted,omitempty"`
	TrustError                string                     `json:"trustError,omitempty"`
	ManualTrustAcknowledgment *ManualTrustAcknowledgment `json:"manualTrustAcknowledgment,omitempty"`
	CreatedAt                 time.Time                  `json:"createdAt"`
	UpdatedAt                 time.Time                  `json:"updatedAt"`
}

type ManualTrustAcknowledgment struct {
	CertificateID     string    `json:"certificateId"`
	SHA256Fingerprint string    `json:"sha256Fingerprint"`
	Platform          string    `json:"platform"`
	AcknowledgedAt    time.Time `json:"acknowledgedAt"`
}

type EnvironmentTemplate struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	ProxyProfileID string    `json:"proxyProfileId,omitempty"`
	CertificateIDs []string  `json:"certificateIds"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type Container struct {
	ID                    string              `json:"id"`
	Name                  string              `json:"name"`
	Color                 string              `json:"color"`
	Icon                  string              `json:"icon,omitempty"`
	CreatedAt             time.Time           `json:"createdAt"`
	UpdatedAt             time.Time           `json:"updatedAt"`
	LastLaunchedAt        *time.Time          `json:"lastLaunchedAt,omitempty"`
	Temporary             bool                `json:"temporary"`
	PendingCleanup        bool                `json:"pendingCleanup"`
	ProfilePath           string              `json:"profilePath"`
	BrowserType           string              `json:"browserType"`
	BrowserExecutable     string              `json:"browserExecutable,omitempty"`
	PID                   int                 `json:"pid,omitempty"`
	Running               bool                `json:"running"`
	State                 string              `json:"state"`
	LaunchToken           string              `json:"launchToken,omitempty"`
	LaunchReservedAt      *time.Time          `json:"launchReservedAt,omitempty"`
	EnvironmentTemplateID string              `json:"environmentTemplateId,omitempty"`
	ProxyProfileID        string              `json:"proxyProfileId,omitempty"`
	NetworkMode           string              `json:"networkMode"`
	ProxyWarning          *ProxyLaunchWarning `json:"proxyWarning,omitempty"`
	DirectFallbackUsed    bool                `json:"directFallbackUsed,omitempty"`
}

type Database struct {
	Version              int                   `json:"version"`
	Containers           []Container           `json:"containers"`
	ProxyProfiles        []ProxyProfile        `json:"proxyProfiles"`
	Certificates         []Certificate         `json:"certificates"`
	EnvironmentTemplates []EnvironmentTemplate `json:"environmentTemplates"`
}
