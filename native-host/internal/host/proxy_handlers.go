package host

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

const (
	maxBypassRules       = 100
	maxBypassRuleLength  = 256
	maxBypassRulesLength = 8192
)

var safeProxyToken = regexp.MustCompile(`^[A-Za-z0-9*._~:/?#[\]@!$&'()+,;=%-]+$`)

func (h *Host) listProxyProfiles() ([]model.ProxyProfile, error) {
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	return db.ProxyProfiles, nil
}

func validateProxyInput(p *model.ProxyProfile) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 80 || hasControl(p.Name) {
		return fmt.Errorf("name must be between 1 and 80 characters")
	}
	if p.Protocol != "http" && p.Protocol != "https" && p.Protocol != "socks4" && p.Protocol != "socks5" {
		return fmt.Errorf("invalid protocol")
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("invalid port")
	}
	p.Host = strings.TrimSpace(p.Host)
	if p.Host == "" || hasControl(p.Host) || strings.Contains(p.Host, "--") || strings.ContainsAny(p.Host, " \t\r\n/\\\"'`") {
		return fmt.Errorf("invalid host format")
	}

	// Enforce loopback
	ips, err := net.LookupIP(p.Host)
	if err != nil {
		return fmt.Errorf("host cannot be resolved")
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("host must resolve to a loopback address")
		}
	}
	if p.UnavailableBehavior != "warn" && p.UnavailableBehavior != "block" && p.UnavailableBehavior != "direct" {
		return fmt.Errorf("invalid unavailable behavior")
	}
	if p.HealthCheck.TimeoutMs == 0 {
		p.HealthCheck.TimeoutMs = 1500
	}
	if p.HealthCheck.TimeoutMs < 100 || p.HealthCheck.TimeoutMs > 10000 {
		return fmt.Errorf("health check timeout out of range")
	}
	if len(p.BypassRules) > maxBypassRules {
		return fmt.Errorf("too many bypass rules")
	}
	seenRules := map[string]bool{}
	totalRuleLength := 0
	cleanRules := make([]string, 0, len(p.BypassRules))
	for i, rule := range p.BypassRules {
		trimmed := strings.TrimSpace(rule)
		p.BypassRules[i] = trimmed
		if trimmed == "" {
			continue
		}
		if len(trimmed) > maxBypassRuleLength || hasControl(trimmed) || strings.Contains(trimmed, "--") || strings.ContainsAny(trimmed, " \t\r\n\"'`") || !safeProxyToken.MatchString(trimmed) {
			return fmt.Errorf("invalid bypass rule format")
		}
		if seenRules[trimmed] {
			return fmt.Errorf("duplicate bypass rule")
		}
		seenRules[trimmed] = true
		totalRuleLength += len(trimmed)
		if totalRuleLength > maxBypassRulesLength {
			return fmt.Errorf("bypass rule list is too long")
		}
		cleanRules = append(cleanRules, trimmed)
	}
	p.BypassRules = cleanRules
	if p.CertificateIDs == nil {
		p.CertificateIDs = []string{}
	}
	seenCerts := map[string]bool{}
	certificateIDs := make([]string, 0, len(p.CertificateIDs))
	for _, certificateID := range p.CertificateIDs {
		if err := security.ValidateID(certificateID); err != nil {
			return fmt.Errorf("invalid certificate ID")
		}
		if seenCerts[certificateID] {
			return fmt.Errorf("duplicate certificate ID")
		}
		seenCerts[certificateID] = true
		certificateIDs = append(certificateIDs, certificateID)
	}
	p.CertificateIDs = certificateIDs
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func (h *Host) createProxyProfile(raw json.RawMessage) (model.ProxyProfile, error) {
	var in model.ProxyProfile
	if err := decodeData(raw, &in); err != nil {
		return model.ProxyProfile{}, err
	}
	if err := validateProxyInput(&in); err != nil {
		return model.ProxyProfile{}, fail("INVALID_PROXY_PROFILE", "%v", err)
	}
	id, err := security.NewID()
	if err != nil {
		return model.ProxyProfile{}, err
	}

	now := h.now()
	in.ID = id
	in.CreatedAt = now
	in.UpdatedAt = now

	err = h.store.Update(func(db *model.Database) error {
		if err := validateCertificateIDsExist(db, in.CertificateIDs); err != nil {
			return err
		}
		db.ProxyProfiles = append(db.ProxyProfiles, in)
		return nil
	})

	if err != nil {
		return model.ProxyProfile{}, err
	}
	return in, nil
}

func (h *Host) updateProxyProfile(raw json.RawMessage) (model.ProxyProfile, error) {
	var in model.ProxyProfile
	if err := decodeData(raw, &in); err != nil {
		return model.ProxyProfile{}, err
	}
	if err := security.ValidateID(in.ID); err != nil {
		return model.ProxyProfile{}, fail("INVALID_PROXY_PROFILE", "%v", err)
	}
	if err := validateProxyInput(&in); err != nil {
		return model.ProxyProfile{}, fail("INVALID_PROXY_PROFILE", "%v", err)
	}
	var updated model.ProxyProfile
	err := h.store.Update(func(db *model.Database) error {
		if err := validateCertificateIDsExist(db, in.CertificateIDs); err != nil {
			return err
		}
		for i := range db.ProxyProfiles {
			if db.ProxyProfiles[i].ID == in.ID {
				in.CreatedAt = db.ProxyProfiles[i].CreatedAt
				in.UpdatedAt = h.now()
				db.ProxyProfiles[i] = in
				updated = in
				return nil
			}
		}
		return fail("NOT_FOUND", "proxy profile not found")
	})

	return updated, err
}

func validateCertificateIDsExist(db *model.Database, ids []string) error {
	certificates := make(map[string]bool, len(db.Certificates))
	for _, certificate := range db.Certificates {
		certificates[certificate.ID] = true
	}
	for _, id := range ids {
		if !certificates[id] {
			return fail("CERTIFICATE_NOT_FOUND", "certificate not found")
		}
	}
	return nil
}

func durationFromTimeout(timeoutMs int) time.Duration {
	if timeoutMs <= 0 {
		timeoutMs = 1500
	}
	return time.Duration(timeoutMs) * time.Millisecond
}

func (h *Host) deleteProxyProfile(id string) (map[string]any, error) {
	if err := security.ValidateID(id); err != nil {
		return nil, fail("INVALID_PROXY_PROFILE", "%v", err)
	}

	err := h.store.Update(func(db *model.Database) error {
		idx := -1
		for i, p := range db.ProxyProfiles {
			if p.ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fail("NOT_FOUND", "proxy profile not found")
		}

		// Check references in containers
		for _, c := range db.Containers {
			if c.ProxyProfileID == id {
				return fail("PROXY_REFERENCE_IN_USE", "Proxy profile is used by a container")
			}
		}
		for _, t := range db.EnvironmentTemplates {
			if t.ProxyProfileID == id {
				return fail("PROXY_REFERENCE_IN_USE", "Proxy profile is used by a template")
			}
		}

		db.ProxyProfiles = append(db.ProxyProfiles[:idx], db.ProxyProfiles[idx+1:]...)
		return nil
	})

	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

type testProxyListenerInput struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type proxyListenerResult struct {
	Reachable bool
	ErrorCode string
	LatencyMs int64
}

func (h *Host) checkProxyListener(host string, port int, timeout time.Duration) proxyListenerResult {
	ips, err := net.LookupIP(host)
	if err != nil {
		return proxyListenerResult{ErrorCode: "DNS_FAILED"}
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return proxyListenerResult{ErrorCode: "NON_LOOPBACK_REJECTED"}
		}
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	address := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	start := time.Now()
	conn, err := h.proxyDial("tcp", address, timeout)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		code := "CONNECTION_REFUSED"
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			code = "TIMED_OUT"
		}
		return proxyListenerResult{ErrorCode: code, LatencyMs: latency}
	}
	_ = conn.Close()
	return proxyListenerResult{Reachable: true, LatencyMs: latency}
}

func (h *Host) testProxyListener(raw json.RawMessage) (map[string]any, error) {
	var in testProxyListenerInput
	if err := decodeData(raw, &in); err != nil {
		return nil, err
	}

	if in.Port < 1 || in.Port > 65535 {
		return nil, fail("INVALID_PROXY_PORT", "invalid port")
	}

	result := h.checkProxyListener(in.Host, in.Port, 1500*time.Millisecond)
	if !result.Reachable {
		return map[string]any{
			"reachable": false,
			"host":      in.Host,
			"port":      in.Port,
			"errorCode": result.ErrorCode,
			"checkedAt": h.now(),
		}, nil
	}
	return map[string]any{
		"reachable": true,
		"host":      in.Host,
		"port":      in.Port,
		"latencyMs": result.LatencyMs,
		"checkedAt": h.now(),
	}, nil
}
