package host

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

func (h *Host) listProxyProfiles() ([]model.ProxyProfile, error) {
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	return db.ProxyProfiles, nil
}

func validateProxyInput(p *model.ProxyProfile) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 64 {
		return fmt.Errorf("name must be between 1 and 64 characters")
	}
	if p.Protocol != "http" && p.Protocol != "https" && p.Protocol != "socks4" && p.Protocol != "socks5" {
		return fmt.Errorf("invalid protocol")
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("invalid port")
	}
	if p.Host == "" || strings.Contains(p.Host, "--") || strings.Contains(p.Host, " ") || strings.Contains(p.Host, "/") {
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
	if p.HealthCheck.TimeoutMs < 100 || p.HealthCheck.TimeoutMs > 10000 {
		return fmt.Errorf("health check timeout out of range")
	}
	if len(p.BypassRules) > 100 {
		return fmt.Errorf("too many bypass rules")
	}
	for i, rule := range p.BypassRules {
		p.BypassRules[i] = strings.TrimSpace(rule)
		if strings.Contains(p.BypassRules[i], "--") || strings.Contains(p.BypassRules[i], " ") || strings.Contains(p.BypassRules[i], "\x00") {
			return fmt.Errorf("invalid bypass rule format")
		}
	}
	return nil
}

func (h *Host) createProxyProfile(raw json.RawMessage) (model.ProxyProfile, error) {
	var in model.ProxyProfile
	if err := decodeData(raw, &in); err != nil {
		return model.ProxyProfile{}, err
	}
	if err := validateProxyInput(&in); err != nil {
		return model.ProxyProfile{}, fail("INVALID_PROXY_PROFILE", "%v", err)
	}
	if in.CertificateIDs == nil {
		in.CertificateIDs = []string{}
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
	if in.CertificateIDs == nil {
		in.CertificateIDs = []string{}
	}

	var updated model.ProxyProfile
	err := h.store.Update(func(db *model.Database) error {
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
