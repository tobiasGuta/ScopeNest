package host

import (
	"fmt"

	"github.com/scopenest/scopenest/native-host/internal/model"
)

type effectiveEnvironment struct {
	RequestedMode          string              `json:"requestedMode"`
	EffectiveMode          string              `json:"effectiveMode"`
	TemplateID             string              `json:"templateId,omitempty"`
	ProxyProfileID         string              `json:"proxyProfileId,omitempty"`
	RequiredCertificateIDs []string            `json:"requiredCertificateIds"`
	UnavailableBehavior    string              `json:"unavailableBehavior,omitempty"`
	ProxyProfile           *model.ProxyProfile `json:"-"`
}

type certificateReadiness struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type listenerReadiness struct {
	Checked   bool   `json:"checked"`
	Reachable bool   `json:"reachable"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type containerReadiness struct {
	Ready        bool                   `json:"ready"`
	Network      effectiveEnvironment   `json:"network"`
	Listener     listenerReadiness      `json:"listener"`
	Certificates []certificateReadiness `json:"certificates"`
	Warnings     []string               `json:"warnings"`
}

func resolveEffectiveEnvironment(db *model.Database, container model.Container) (effectiveEnvironment, error) {
	requested := container.NetworkMode
	if requested == "" {
		requested = "direct"
	}
	resolved := effectiveEnvironment{RequestedMode: requested, EffectiveMode: "direct", RequiredCertificateIDs: []string{}}

	proxies := make(map[string]model.ProxyProfile, len(db.ProxyProfiles))
	for _, proxy := range db.ProxyProfiles {
		proxies[proxy.ID] = proxy
	}
	certificates := make(map[string]bool, len(db.Certificates))
	for _, certificate := range db.Certificates {
		certificates[certificate.ID] = true
	}

	var template *model.EnvironmentTemplate
	if container.EnvironmentTemplateID != "" {
		for i := range db.EnvironmentTemplates {
			if db.EnvironmentTemplates[i].ID == container.EnvironmentTemplateID {
				template = &db.EnvironmentTemplates[i]
				break
			}
		}
		if template == nil {
			return resolved, fail("ENVIRONMENT_TEMPLATE_NOT_FOUND", "environment template was not found")
		}
		resolved.TemplateID = template.ID
		resolved.RequiredCertificateIDs = appendUnique(resolved.RequiredCertificateIDs, template.CertificateIDs...)
	}

	switch requested {
	case "direct":
		if container.ProxyProfileID != "" || container.EnvironmentTemplateID != "" {
			return resolved, fail("INVALID_NETWORK_CONFIGURATION", "direct mode cannot include proxy or template references")
		}
	case "template":
		if template == nil {
			return resolved, fail("ENVIRONMENT_TEMPLATE_NOT_FOUND", "template mode requires an environment template")
		}
		if container.ProxyProfileID != "" {
			return resolved, fail("INVALID_NETWORK_CONFIGURATION", "template mode cannot include a container proxy override")
		}
		if template.ProxyProfileID != "" {
			proxy, ok := proxies[template.ProxyProfileID]
			if !ok {
				return resolved, fail("PROXY_PROFILE_NOT_FOUND", "template proxy profile was not found")
			}
			if !proxy.Enabled {
				return resolved, fail("PROXY_PROFILE_DISABLED", "template proxy profile is disabled")
			}
			resolved.EffectiveMode = "proxy"
			resolved.ProxyProfileID = proxy.ID
			resolved.UnavailableBehavior = proxy.UnavailableBehavior
			resolved.ProxyProfile = &proxy
			resolved.RequiredCertificateIDs = appendUnique(resolved.RequiredCertificateIDs, proxy.CertificateIDs...)
		}
	case "proxy":
		if container.ProxyProfileID == "" {
			return resolved, fail("INVALID_NETWORK_CONFIGURATION", "proxy mode requires a proxy profile")
		}
		proxy, ok := proxies[container.ProxyProfileID]
		if !ok {
			return resolved, fail("PROXY_PROFILE_NOT_FOUND", "configured proxy profile was not found")
		}
		if !proxy.Enabled {
			return resolved, fail("PROXY_PROFILE_DISABLED", "configured proxy profile is disabled")
		}
		resolved.EffectiveMode = "proxy"
		resolved.ProxyProfileID = proxy.ID
		resolved.UnavailableBehavior = proxy.UnavailableBehavior
		resolved.ProxyProfile = &proxy
		resolved.RequiredCertificateIDs = appendUnique(resolved.RequiredCertificateIDs, proxy.CertificateIDs...)
	default:
		return resolved, fail("INVALID_NETWORK_MODE", "network mode must be direct, template, or proxy")
	}

	for _, certificateID := range resolved.RequiredCertificateIDs {
		if !certificates[certificateID] {
			return resolved, fail("CERTIFICATE_NOT_FOUND", "required certificate was not found")
		}
	}
	return resolved, nil
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	for _, value := range additions {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func validateContainerReferences(db *model.Database, in containerInput) error {
	c := model.Container{NetworkMode: in.NetworkMode, ProxyProfileID: in.ProxyProfileID, EnvironmentTemplateID: in.EnvironmentTemplateID}
	_, err := resolveEffectiveEnvironment(db, c)
	return err
}

func (h *Host) certificateReadiness(db *model.Database, ids []string) ([]certificateReadiness, error) {
	if len(ids) == 0 {
		return []certificateReadiness{}, nil
	}
	if h.certManager == nil {
		return nil, fail("CERTIFICATE_MANAGER_UNAVAILABLE", "certificate manager is not available")
	}
	certificates := make(map[string]model.Certificate, len(db.Certificates))
	for _, certificate := range db.Certificates {
		certificates[certificate.ID] = certificate
	}
	result := make([]certificateReadiness, 0, len(ids))
	for _, id := range ids {
		certificate, ok := certificates[id]
		if !ok {
			return nil, fail("CERTIFICATE_NOT_FOUND", "required certificate was not found")
		}
		der, err := h.certManager.ReadCertificateVerified(certificate.ID, certificate.SHA256Fingerprint)
		if err != nil {
			return nil, fail("CERTIFICATE_DER_MISSING_OR_MISMATCHED", "required certificate bytes are missing or do not match metadata")
		}
		switch {
		case h.certManager.Trust.Supported():
			verified, err := h.certManager.Trust.Verify(der, certificate.SHA256Fingerprint)
			if err != nil {
				return nil, fail("CERTIFICATE_TRUST_VERIFICATION_FAILED", "certificate trust could not be verified")
			}
			if !verified || !certificate.Trusted || certificate.TrustState != model.CertificateTrustTrusted {
				return nil, fail("CERTIFICATE_NOT_READY", "required certificate is not trusted")
			}
			result = append(result, certificateReadiness{ID: id, State: "trusted_verified"})
		case h.platform == "linux" && certificate.TrustState == model.CertificateTrustManualAcknowledgedUnverified && certificate.ManualTrustAcknowledgment != nil && certificate.ManualTrustAcknowledgment.SHA256Fingerprint == certificate.SHA256Fingerprint:
			result = append(result, certificateReadiness{ID: id, State: model.CertificateTrustManualAcknowledgedUnverified})
		default:
			return nil, fail("CERTIFICATE_NOT_READY", "required certificate trust is not ready")
		}
	}
	return result, nil
}

func (h *Host) getContainerReadiness(id string) (containerReadiness, error) {
	db, err := h.store.Load()
	if err != nil {
		return containerReadiness{}, err
	}
	var container *model.Container
	for i := range db.Containers {
		if db.Containers[i].ID == id {
			container = &db.Containers[i]
			break
		}
	}
	if container == nil {
		return containerReadiness{}, fail("NOT_FOUND", "container was not found")
	}
	network, err := resolveEffectiveEnvironment(&db, *container)
	if err != nil {
		return containerReadiness{Ready: false, Network: network, Warnings: []string{ErrorCode(err)}}, nil
	}
	certificates, err := h.certificateReadiness(&db, network.RequiredCertificateIDs)
	if err != nil {
		return containerReadiness{Ready: false, Network: network, Warnings: []string{ErrorCode(err)}}, nil
	}
	readiness := containerReadiness{Ready: true, Network: network, Listener: listenerReadiness{}, Certificates: certificates, Warnings: []string{}}
	if network.ProxyProfile != nil && network.ProxyProfile.HealthCheck.Enabled {
		proxy := network.ProxyProfile
		listener := h.checkProxyListener(proxy.Host, proxy.Port, durationFromTimeout(proxy.HealthCheck.TimeoutMs))
		readiness.Listener = listenerReadiness{Checked: true, Reachable: listener.Reachable, ErrorCode: listener.ErrorCode}
		if !listener.Reachable {
			readiness.Warnings = append(readiness.Warnings, fmt.Sprintf("PROXY_LISTENER_UNAVAILABLE:%s", listener.ErrorCode))
			if proxy.UnavailableBehavior == "block" {
				readiness.Ready = false
			}
		}
	}
	return readiness, nil
}
