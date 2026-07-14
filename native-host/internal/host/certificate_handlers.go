package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/scopenest/scopenest/native-host/internal/certstore"
	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

type importCertificateInput struct {
	DisplayName   string `json:"displayName"`
	ContentBase64 string `json:"contentBase64"`
	ExpectedSize  int    `json:"expectedSize"`
}

type acknowledgeManualTrustInput struct {
	ID                string `json:"id"`
	SHA256Fingerprint string `json:"sha256Fingerprint"`
	Platform          string `json:"platform"`
}

func (h *Host) listCertificates() ([]model.Certificate, error) {
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	return db.Certificates, nil
}

func (h *Host) getCertificate(id string) (model.Certificate, error) {
	if err := security.ValidateID(id); err != nil {
		return model.Certificate{}, fail("INVALID_CERTIFICATE_ID", "%v", err)
	}
	db, err := h.store.Load()
	if err != nil {
		return model.Certificate{}, err
	}
	for _, c := range db.Certificates {
		if c.ID == id {
			return c, nil
		}
	}
	return model.Certificate{}, fail("NOT_FOUND", "certificate not found")
}

func (h *Host) importCertificate(raw json.RawMessage) (model.Certificate, error) {
	var in importCertificateInput
	if err := decodeData(raw, &in); err != nil {
		return model.Certificate{}, err
	}

	if h.certManager == nil {
		return model.Certificate{}, fail("CERTIFICATE_MANAGER_UNAVAILABLE", "Certificate manager is not available")
	}

	staged, err := h.certManager.Import(in.DisplayName, in.ContentBase64, in.ExpectedSize)
	if err != nil {
		code := "INVALID_CERTIFICATE"
		switch {
		case errors.Is(err, certstore.ErrTooLarge):
			code = "CERTIFICATE_TOO_LARGE"
		case errors.Is(err, certstore.ErrMultipleCerts):
			code = "CERTIFICATE_MULTIPLE_REJECTED"
		case errors.Is(err, certstore.ErrPrivateKey):
			code = "CERTIFICATE_PRIVATE_KEY_REJECTED"
		case errors.Is(err, certstore.ErrNotCA):
			code = "CERTIFICATE_NOT_CA"
		}
		return model.Certificate{}, fail(code, "%v", err)
	}

	cert, err := h.certManager.CommitImport(staged)
	if err != nil {
		return model.Certificate{}, fail("IMPORT_FAILED", "Failed to commit certificate: %v", err)
	}
	return cert, nil
}

func (h *Host) installCertificateTrust(id string) (model.Certificate, error) {
	if h.certManager == nil {
		return model.Certificate{}, fail("CERTIFICATE_MANAGER_UNAVAILABLE", "Certificate manager is not available")
	}
	if !h.certManager.Trust.Supported() {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_UNSUPPORTED", "Certificate trust installation is not supported on this platform")
	}

	var target model.Certificate
	db, err := h.store.Load()
	if err != nil {
		return model.Certificate{}, err
	}
	found := false
	for _, c := range db.Certificates {
		if c.ID == id {
			target = c
			found = true
			break
		}
	}
	if !found {
		return model.Certificate{}, fail("NOT_FOUND", "certificate not found")
	}

	der, err := h.certManager.ReadCertificateVerified(id, target.SHA256Fingerprint)
	if err != nil {
		return model.Certificate{}, fail("CERTIFICATE_READ_FAILED", "Failed to read certificate bytes: %v", err)
	}

	alreadyTrusted, err := h.certManager.Trust.Install(der, target.SHA256Fingerprint)
	if err != nil {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_INSTALL_FAILED", "Failed to install certificate trust: %v", err)
	}

	var updated model.Certificate
	err = h.store.Update(func(db *model.Database) error {
		for i := range db.Certificates {
			if db.Certificates[i].ID == id {
				db.Certificates[i].Trusted = true
				db.Certificates[i].TrustState = model.CertificateTrustTrusted
				db.Certificates[i].ManualTrustAcknowledgment = nil
				db.Certificates[i].InstalledByScopeNest = !alreadyTrusted
				db.Certificates[i].UpdatedAt = h.now()
				updated = db.Certificates[i]
				return nil
			}
		}
		return fail("NOT_FOUND", "certificate not found during update")
	})

	if err != nil {
		return model.Certificate{}, err
	}

	return updated, nil
}

func (h *Host) removeCertificateTrust(id string) (model.Certificate, error) {
	if h.certManager == nil {
		return model.Certificate{}, fail("CERTIFICATE_MANAGER_UNAVAILABLE", "Certificate manager is not available")
	}
	if !h.certManager.Trust.Supported() {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_UNSUPPORTED", "Certificate trust installation is not supported on this platform")
	}

	var target model.Certificate
	db, err := h.store.Load()
	if err != nil {
		return model.Certificate{}, err
	}
	found := false
	for _, c := range db.Certificates {
		if c.ID == id {
			target = c
			found = true
			break
		}
	}
	if !found {
		return model.Certificate{}, fail("NOT_FOUND", "certificate not found")
	}

	if !target.InstalledByScopeNest {
		return model.Certificate{}, fail("CERTIFICATE_NOT_INSTALLED_BY_SCOPENEST", "Refusing to remove a certificate that was not installed by ScopeNest")
	}
	if err := certificateReferenceError(db, id); err != nil {
		return model.Certificate{}, err
	}

	der, err := h.certManager.ReadCertificateVerified(id, target.SHA256Fingerprint)
	if err != nil {
		return model.Certificate{}, fail("CERTIFICATE_READ_FAILED", "Failed to read certificate bytes: %v", err)
	}

	err = h.certManager.Trust.Remove(der, target.SHA256Fingerprint)
	if err != nil {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_REMOVE_FAILED", "Failed to remove certificate trust: %v", err)
	}

	var updated model.Certificate
	err = h.store.Update(func(db *model.Database) error {
		for i := range db.Certificates {
			if db.Certificates[i].ID == id {
				db.Certificates[i].Trusted = false
				db.Certificates[i].TrustState = model.CertificateTrustUntrusted
				db.Certificates[i].InstalledByScopeNest = false
				db.Certificates[i].UpdatedAt = h.now()
				updated = db.Certificates[i]
				return nil
			}
		}
		return fail("NOT_FOUND", "certificate not found during update")
	})

	return updated, err
}

func certificateReferenceError(db model.Database, id string) error {
	for _, proxy := range db.ProxyProfiles {
		for _, certificateID := range proxy.CertificateIDs {
			if certificateID == id {
				return fail("CERTIFICATE_REFERENCE_IN_USE", "certificate is referenced by proxy profile %s", proxy.ID)
			}
		}
	}
	for _, template := range db.EnvironmentTemplates {
		for _, certificateID := range template.CertificateIDs {
			if certificateID == id {
				return fail("CERTIFICATE_REFERENCE_IN_USE", "certificate is referenced by environment template %s", template.ID)
			}
		}
	}
	return nil
}

func (h *Host) acknowledgeManualCertificateTrust(raw json.RawMessage) (model.Certificate, error) {
	var in acknowledgeManualTrustInput
	if err := decodeData(raw, &in); err != nil {
		return model.Certificate{}, err
	}
	if h.platform != "linux" || in.Platform != "linux" {
		return model.Certificate{}, fail("MANUAL_TRUST_ACKNOWLEDGMENT_UNSUPPORTED", "manual trust acknowledgment is available only on Linux")
	}
	if err := security.ValidateID(in.ID); err != nil {
		return model.Certificate{}, fail("INVALID_CERTIFICATE_ID", "%v", err)
	}
	var updated model.Certificate
	err := h.store.Update(func(db *model.Database) error {
		for i := range db.Certificates {
			certificate := &db.Certificates[i]
			if certificate.ID != in.ID {
				continue
			}
			if certificate.SHA256Fingerprint != in.SHA256Fingerprint {
				return fail("CERTIFICATE_FINGERPRINT_MISMATCH", "acknowledgment fingerprint does not match the managed certificate")
			}
			acknowledgedAt := h.now().UTC()
			certificate.Trusted = false
			certificate.InstalledByScopeNest = false
			certificate.TrustState = model.CertificateTrustManualAcknowledgedUnverified
			certificate.ManualTrustAcknowledgment = &model.ManualTrustAcknowledgment{
				CertificateID: certificate.ID, SHA256Fingerprint: certificate.SHA256Fingerprint,
				Platform: "linux", AcknowledgedAt: acknowledgedAt,
			}
			certificate.UpdatedAt = acknowledgedAt
			updated = *certificate
			return nil
		}
		return fail("NOT_FOUND", "certificate not found")
	})
	return updated, err
}

func (h *Host) deleteCertificate(id string) (map[string]any, error) {
	if err := security.ValidateID(id); err != nil {
		return nil, fail("INVALID_CERTIFICATE_ID", "%v", err)
	}

	var errResult error
	err := h.store.Update(func(db *model.Database) error {
		idx := -1
		for i, c := range db.Certificates {
			if c.ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fail("NOT_FOUND", "certificate was not found")
		}

		// Check references
		for _, p := range db.ProxyProfiles {
			for _, cid := range p.CertificateIDs {
				if cid == id {
					errResult = fail("CERTIFICATE_REFERENCE_IN_USE", "Certificate is referenced by a proxy profile")
					return nil
				}
			}
		}
		for _, t := range db.EnvironmentTemplates {
			for _, cid := range t.CertificateIDs {
				if cid == id {
					errResult = fail("CERTIFICATE_REFERENCE_IN_USE", "Certificate is referenced by an environment template")
					return nil
				}
			}
		}

		// It's safe to delete
		db.Certificates = append(db.Certificates[:idx], db.Certificates[idx+1:]...)
		return nil
	})

	if err != nil {
		return nil, err
	}
	if errResult != nil {
		return nil, errResult
	}

	// Remove dir from filesystem
	if h.certManager != nil {
		certPath := filepath.Join(h.store.Root(), "resources", "certificates", id)
		certPath, err = security.ResolveWithin(filepath.Join(h.store.Root(), "resources", "certificates"), certPath)
		if err == nil {
			_ = os.RemoveAll(certPath)
		}
	}

	return map[string]any{"deleted": true, "id": id}, nil
}
