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
		case errors.Is(err, certstore.ErrInvalidDisplayName):
			code = "INVALID_CERTIFICATE_NAME"
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

	if err := security.ValidateID(id); err != nil {
		return model.Certificate{}, fail("INVALID_CERTIFICATE_ID", "%v", err)
	}
	token, err := security.NewID()
	if err != nil {
		return model.Certificate{}, err
	}
	var target model.Certificate
	var der []byte
	var wasTrusted bool
	if err := h.store.Update(func(db *model.Database) error {
		for i := range db.Certificates {
			certificate := &db.Certificates[i]
			if certificate.ID != id {
				continue
			}
			if trustOperationPending(*certificate) {
				return fail("CERTIFICATE_TRUST_OPERATION_PENDING", "certificate trust operation is pending")
			}
			var readErr error
			der, readErr = h.certManager.ReadCertificateVerified(id, certificate.SHA256Fingerprint)
			if readErr != nil {
				return fail("CERTIFICATE_READ_FAILED", "Failed to read certificate bytes: %v", readErr)
			}
			var verifyErr error
			wasTrusted, verifyErr = h.certManager.Trust.Verify(der, certificate.SHA256Fingerprint)
			if verifyErr != nil {
				return fail("CERTIFICATE_TRUST_VERIFY_FAILED", "Failed to verify certificate trust: %v", verifyErr)
			}
			target = *certificate
			certificate.TrustState = model.CertificateTrustInstalling
			certificate.TrustOperationID = token
			certificate.TrustOperationFingerprint = certificate.SHA256Fingerprint
			certificate.TrustOperationWasTrusted = wasTrusted
			certificate.TrustError = ""
			certificate.UpdatedAt = h.now()
			return nil
		}
		return fail("NOT_FOUND", "certificate not found during trust preparation")
	}); err != nil {
		return model.Certificate{}, err
	}

	alreadyTrusted, err := h.certManager.Trust.Install(der, target.SHA256Fingerprint)
	if err != nil {
		_ = h.markCertificateTrustError(id, token, err.Error())
		return model.Certificate{}, fail("CERTIFICATE_TRUST_INSTALL_FAILED", "Failed to install certificate trust: %v", err)
	}
	verified, err := h.certManager.Trust.Verify(der, target.SHA256Fingerprint)
	if err != nil || !verified {
		if err == nil {
			err = errors.New("certificate was not verified after installation")
		}
		_ = h.markCertificateTrustError(id, token, err.Error())
		return model.Certificate{}, fail("CERTIFICATE_TRUST_VERIFY_FAILED", "Failed to verify certificate trust: %v", err)
	}

	var updated model.Certificate
	err = h.store.Update(func(db *model.Database) error {
		for i := range db.Certificates {
			if db.Certificates[i].ID == id && db.Certificates[i].TrustOperationID == token {
				db.Certificates[i].Trusted = true
				db.Certificates[i].TrustState = model.CertificateTrustTrusted
				db.Certificates[i].ManualTrustAcknowledgment = nil
				db.Certificates[i].InstalledByScopeNest = target.InstalledByScopeNest || !alreadyTrusted
				db.Certificates[i].TrustOperationID = ""
				db.Certificates[i].TrustOperationFingerprint = ""
				db.Certificates[i].TrustOperationWasTrusted = false
				db.Certificates[i].TrustError = ""
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

	if err := security.ValidateID(id); err != nil {
		return model.Certificate{}, fail("INVALID_CERTIFICATE_ID", "%v", err)
	}
	token, err := security.NewID()
	if err != nil {
		return model.Certificate{}, err
	}
	var target model.Certificate
	var der []byte
	if err := h.store.Update(func(db *model.Database) error {
		for i := range db.Certificates {
			certificate := &db.Certificates[i]
			if certificate.ID != id {
				continue
			}
			if trustOperationPending(*certificate) {
				return fail("CERTIFICATE_TRUST_OPERATION_PENDING", "certificate trust operation is pending")
			}
			if !certificate.InstalledByScopeNest {
				return fail("CERTIFICATE_NOT_INSTALLED_BY_SCOPENEST", "Refusing to remove a certificate that was not installed by ScopeNest")
			}
			if err := certificateReferenceError(*db, id); err != nil {
				return err
			}
			var readErr error
			der, readErr = h.certManager.ReadCertificateVerified(id, certificate.SHA256Fingerprint)
			if readErr != nil {
				return fail("CERTIFICATE_READ_FAILED", "Failed to read certificate bytes: %v", readErr)
			}
			target = *certificate
			certificate.TrustState = model.CertificateTrustRemoving
			certificate.TrustOperationID = token
			certificate.TrustOperationFingerprint = certificate.SHA256Fingerprint
			certificate.TrustOperationWasTrusted = true
			certificate.TrustError = ""
			certificate.UpdatedAt = h.now()
			return nil
		}
		return fail("NOT_FOUND", "certificate not found during trust preparation")
	}); err != nil {
		return model.Certificate{}, err
	}

	err = h.certManager.Trust.Remove(der, target.SHA256Fingerprint)
	if err != nil {
		_ = h.markCertificateTrustError(id, token, err.Error())
		return model.Certificate{}, fail("CERTIFICATE_TRUST_REMOVE_FAILED", "Failed to remove certificate trust: %v", err)
	}
	verified, err := h.certManager.Trust.Verify(der, target.SHA256Fingerprint)
	if err != nil {
		_ = h.markCertificateTrustError(id, token, err.Error())
		return model.Certificate{}, fail("CERTIFICATE_TRUST_VERIFY_FAILED", "Failed to verify certificate trust: %v", err)
	}
	if verified {
		_ = h.markCertificateTrustError(id, token, "certificate remained trusted after removal")
		return model.Certificate{}, fail("CERTIFICATE_TRUST_REMOVE_FAILED", "certificate remained trusted after removal")
	}

	var updated model.Certificate
	err = h.store.Update(func(db *model.Database) error {
		for i := range db.Certificates {
			if db.Certificates[i].ID == id && db.Certificates[i].TrustOperationID == token {
				db.Certificates[i].Trusted = false
				db.Certificates[i].TrustState = model.CertificateTrustUntrusted
				db.Certificates[i].InstalledByScopeNest = false
				db.Certificates[i].TrustOperationID = ""
				db.Certificates[i].TrustOperationFingerprint = ""
				db.Certificates[i].TrustOperationWasTrusted = false
				db.Certificates[i].TrustError = ""
				db.Certificates[i].UpdatedAt = h.now()
				updated = db.Certificates[i]
				return nil
			}
		}
		return fail("NOT_FOUND", "certificate not found during update")
	})

	return updated, err
}

func trustOperationPending(certificate model.Certificate) bool {
	return certificate.TrustOperationID != "" ||
		certificate.TrustState == model.CertificateTrustInstalling ||
		certificate.TrustState == model.CertificateTrustRemoving
}

func (h *Host) markCertificateTrustError(id, token, message string) error {
	return h.store.Update(func(db *model.Database) error {
		for i := range db.Certificates {
			if db.Certificates[i].ID == id && db.Certificates[i].TrustOperationID == token {
				db.Certificates[i].TrustState = model.CertificateTrustError
				db.Certificates[i].TrustError = message
				db.Certificates[i].UpdatedAt = h.now()
				return nil
			}
		}
		return nil
	})
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

	operationID, err := security.NewID()
	if err != nil {
		return nil, err
	}
	certRoot := filepath.Join(h.store.Root(), "resources", "certificates")
	sourcePath, err := security.ResolveWithin(certRoot, filepath.Join(certRoot, id))
	if err != nil {
		return nil, fail("CERTIFICATE_DELETE_FAILED", "certificate path could not be resolved")
	}
	stagedPath, err := security.ResolveWithin(certRoot, filepath.Join(certRoot, ".delete-"+id+"-"+operationID))
	if err != nil {
		return nil, fail("CERTIFICATE_DELETE_FAILED", "certificate staging path could not be resolved")
	}
	now := h.now()
	libraryOnly := false
	err = h.store.Update(func(db *model.Database) error {
		certificate, err := validateCertificateDeletionInDBExcept(*db, id, "")
		if err != nil {
			return err
		}
		libraryOnly = certificate.Trusted && !certificate.InstalledByScopeNest
		db.CertificateDeletionOps = append(db.CertificateDeletionOps, model.CertificateDeletionOperation{
			CertificateID: id, OperationID: operationID, SourceDirectory: sourcePath,
			StagedDirectory: stagedPath, State: "deleting", CreatedAt: now, UpdatedAt: now,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	renamed := false
	if err := os.Rename(sourcePath, stagedPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = h.markCertificateDeletionError(operationID, err.Error())
			return nil, fail("CERTIFICATE_DELETE_FAILED", "certificate resource could not be staged for deletion: %v", err)
		}
	} else {
		renamed = true
		_ = syncParentBestEffort(stagedPath)
	}
	err = h.store.Update(func(db *model.Database) error {
		if _, err := validateCertificateDeletionInDBExcept(*db, id, operationID); err != nil {
			return err
		}
		for i := range db.Certificates {
			if db.Certificates[i].ID == id {
				db.Certificates = append(db.Certificates[:i], db.Certificates[i+1:]...)
				return nil
			}
		}
		return fail("NOT_FOUND", "certificate was not found")
	})
	if err != nil {
		if renamed {
			if restoreErr := os.Rename(stagedPath, sourcePath); restoreErr != nil {
				_ = h.markCertificateDeletionError(operationID, restoreErr.Error())
			} else {
				_ = syncParentBestEffort(sourcePath)
				_ = h.removeCertificateDeletionOp(operationID)
			}
		}
		return nil, err
	}
	if renamed {
		if err := os.RemoveAll(stagedPath); err != nil {
			_ = h.markCertificateDeletionError(operationID, err.Error())
			return nil, fail("CERTIFICATE_DELETE_FAILED", "staged certificate resource could not be finalized: %v", err)
		}
		_ = syncParentBestEffort(stagedPath)
	}
	if err := h.removeCertificateDeletionOp(operationID); err != nil {
		return nil, err
	}

	return map[string]any{"deleted": true, "id": id, "windowsTrustUnchanged": libraryOnly}, nil
}

func (h *Host) validateCertificateDeletion(id string) error {
	db, err := h.store.Load()
	if err != nil {
		return err
	}
	_, err = validateCertificateDeletionInDBExcept(db, id, "")
	return err
}

func validateCertificateDeletionInDB(db model.Database, id string) (model.Certificate, error) {
	return validateCertificateDeletionInDBExcept(db, id, "")
}

func validateCertificateDeletionInDBExcept(db model.Database, id, allowedOperationID string) (model.Certificate, error) {
	var certificate *model.Certificate
	for i := range db.Certificates {
		if db.Certificates[i].ID == id {
			certificate = &db.Certificates[i]
			break
		}
	}
	if certificate == nil {
		return model.Certificate{}, fail("NOT_FOUND", "certificate was not found")
	}
	if certificate.TrustState == model.CertificateTrustInstalling || certificate.TrustState == model.CertificateTrustRemoving {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_MUST_BE_REMOVED_FIRST", "remove certificate trust before deleting it")
	}
	if certificate.InstalledByScopeNest {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_MUST_BE_REMOVED_FIRST", "remove certificate trust before deleting it")
	}
	if certificate.Trusted && certificate.InstalledByScopeNest {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_MUST_BE_REMOVED_FIRST", "remove certificate trust before deleting it")
	}
	if certificate.TrustState == model.CertificateTrustTrusted && certificate.InstalledByScopeNest {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_MUST_BE_REMOVED_FIRST", "remove certificate trust before deleting it")
	}
	if certificate.TrustOperationID != "" {
		return model.Certificate{}, fail("CERTIFICATE_TRUST_OPERATION_PENDING", "certificate trust operation is pending")
	}
	for _, op := range db.CertificateDeletionOps {
		if op.OperationID == allowedOperationID {
			continue
		}
		if op.CertificateID == id {
			return model.Certificate{}, fail("CERTIFICATE_DELETE_OPERATION_PENDING", "certificate deletion operation is pending")
		}
	}
	if err := certificateReferenceError(db, id); err != nil {
		return model.Certificate{}, err
	}
	return *certificate, nil
}

func (h *Host) reconcileTrustOperations() error {
	if h.certManager == nil || !h.certManager.Trust.Supported() {
		return nil
	}
	return h.store.Update(func(db *model.Database) error {
		now := h.now()
		for i := range db.Certificates {
			certificate := &db.Certificates[i]
			switch certificate.TrustState {
			case model.CertificateTrustInstalling, model.CertificateTrustRemoving, model.CertificateTrustError:
			default:
				continue
			}
			fingerprint := certificate.SHA256Fingerprint
			if certificate.TrustOperationFingerprint != "" {
				fingerprint = certificate.TrustOperationFingerprint
			}
			der, err := h.certManager.ReadCertificateVerified(certificate.ID, fingerprint)
			if err != nil {
				certificate.Trusted = false
				certificate.TrustState = model.CertificateTrustError
				certificate.TrustError = "managed certificate bytes are missing or do not match metadata"
				certificate.UpdatedAt = now
				continue
			}
			verified, err := h.certManager.Trust.Verify(der, fingerprint)
			if err != nil {
				certificate.TrustState = model.CertificateTrustError
				certificate.TrustError = err.Error()
				certificate.UpdatedAt = now
				continue
			}
			switch certificate.TrustState {
			case model.CertificateTrustInstalling:
				if verified {
					certificate.Trusted = true
					certificate.TrustState = model.CertificateTrustTrusted
					certificate.InstalledByScopeNest = certificate.InstalledByScopeNest || !certificate.TrustOperationWasTrusted
					certificate.ManualTrustAcknowledgment = nil
					clearTrustOperation(certificate)
				} else {
					certificate.Trusted = false
					certificate.TrustState = model.CertificateTrustUntrusted
					certificate.InstalledByScopeNest = false
					clearTrustOperation(certificate)
				}
			case model.CertificateTrustRemoving:
				if verified {
					certificate.Trusted = true
					certificate.TrustState = model.CertificateTrustTrusted
					clearTrustOperation(certificate)
				} else {
					certificate.Trusted = false
					certificate.TrustState = model.CertificateTrustUntrusted
					certificate.InstalledByScopeNest = false
					clearTrustOperation(certificate)
				}
			case model.CertificateTrustError:
				if verified {
					certificate.Trusted = true
					if certificate.TrustOperationID != "" {
						certificate.TrustState = model.CertificateTrustTrusted
						certificate.InstalledByScopeNest = certificate.InstalledByScopeNest || !certificate.TrustOperationWasTrusted
						certificate.ManualTrustAcknowledgment = nil
						clearTrustOperation(certificate)
					}
				} else {
					certificate.Trusted = false
					if certificate.TrustOperationID != "" {
						certificate.TrustState = model.CertificateTrustUntrusted
						certificate.InstalledByScopeNest = false
						clearTrustOperation(certificate)
					}
				}
			}
			certificate.UpdatedAt = now
		}
		return nil
	})
}

func (h *Host) reconcileCertificateDeletions() error {
	db, err := h.store.Load()
	if err != nil {
		return err
	}
	var reconciliationErr error
	for _, op := range db.CertificateDeletionOps {
		switch op.State {
		case "deleting", "deletion_error":
		default:
			continue
		}
		if err := h.reconcileCertificateDeletion(op); err != nil {
			// One failed tombstone must not prevent recovery of later operations.
			if recordErr := h.markCertificateDeletionError(op.OperationID, err.Error()); recordErr != nil {
				err = errors.Join(err, recordErr)
			}
			reconciliationErr = errors.Join(reconciliationErr, err)
			continue
		}
	}
	return reconciliationErr
}

func (h *Host) reconcileCertificateDeletion(op model.CertificateDeletionOperation) error {
	certRoot := filepath.Join(h.store.Root(), "resources", "certificates")
	source, err := security.ResolveWithin(certRoot, op.SourceDirectory)
	if err != nil {
		return h.markCertificateDeletionError(op.OperationID, "source directory is outside certificate root")
	}
	staged, err := security.ResolveWithin(certRoot, op.StagedDirectory)
	if err != nil {
		return h.markCertificateDeletionError(op.OperationID, "staged directory is outside certificate root")
	}
	db, err := h.store.Load()
	if err != nil {
		return err
	}
	var certificate *model.Certificate
	for i := range db.Certificates {
		if db.Certificates[i].ID == op.CertificateID {
			certificate = &db.Certificates[i]
			break
		}
	}
	sourceExists := directoryExists(source)
	stagedExists := directoryExists(staged)
	switch {
	case certificate != nil && stagedExists && !sourceExists:
		if err := os.Rename(staged, source); err != nil {
			return h.markCertificateDeletionError(op.OperationID, err.Error())
		}
		_ = syncParentBestEffort(source)
		return h.removeCertificateDeletionOp(op.OperationID)
	case certificate == nil && stagedExists:
		if err := os.RemoveAll(staged); err != nil {
			return h.markCertificateDeletionError(op.OperationID, err.Error())
		}
		_ = syncParentBestEffort(staged)
		return h.removeCertificateDeletionOp(op.OperationID)
	case certificate != nil && sourceExists && !stagedExists:
		return h.removeCertificateDeletionOp(op.OperationID)
	case certificate != nil && sourceExists && stagedExists:
		sourceMatches := certificateDirectoryFingerprintMatches(source, certificate.SHA256Fingerprint)
		stagedMatches := certificateDirectoryFingerprintMatches(staged, certificate.SHA256Fingerprint)
		switch {
		case sourceMatches:
			if err := os.RemoveAll(staged); err != nil {
				return h.markCertificateDeletionError(op.OperationID, err.Error())
			}
			_ = syncParentBestEffort(staged)
			return h.removeCertificateDeletionOp(op.OperationID)
		case stagedMatches:
			if err := os.RemoveAll(source); err != nil {
				return h.markCertificateDeletionError(op.OperationID, err.Error())
			}
			if err := os.Rename(staged, source); err != nil {
				return h.markCertificateDeletionError(op.OperationID, err.Error())
			}
			_ = syncParentBestEffort(source)
			return h.removeCertificateDeletionOp(op.OperationID)
		default:
			return h.markCertificateDeletionError(op.OperationID, "neither deletion directory matches certificate fingerprint")
		}
	case certificate == nil && !stagedExists && !sourceExists:
		return h.markCertificateDeletionError(op.OperationID, "certificate metadata and deletion directories are missing")
	default:
		return h.markCertificateDeletionError(op.OperationID, "certificate deletion tombstone could not be reconciled")
	}
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func certificateDirectoryFingerprintMatches(dir, fingerprint string) bool {
	der, err := os.ReadFile(filepath.Join(dir, "certificate.der"))
	if err != nil {
		return false
	}
	actual, err := certstore.FingerprintDER(der)
	return err == nil && actual == fingerprint
}

func (h *Host) markCertificateDeletionError(operationID, message string) error {
	return h.store.Update(func(db *model.Database) error {
		for i := range db.CertificateDeletionOps {
			if db.CertificateDeletionOps[i].OperationID == operationID {
				db.CertificateDeletionOps[i].State = "deletion_error"
				db.CertificateDeletionOps[i].Error = message
				db.CertificateDeletionOps[i].UpdatedAt = h.now()
				return nil
			}
		}
		return nil
	})
}

func (h *Host) removeCertificateDeletionOp(operationID string) error {
	return h.store.Update(func(db *model.Database) error {
		for i := range db.CertificateDeletionOps {
			if db.CertificateDeletionOps[i].OperationID == operationID {
				db.CertificateDeletionOps = append(db.CertificateDeletionOps[:i], db.CertificateDeletionOps[i+1:]...)
				return nil
			}
		}
		return nil
	})
}

func syncParentBestEffort(path string) error {
	dir := filepath.Dir(path)
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func clearTrustOperation(certificate *model.Certificate) {
	certificate.TrustOperationID = ""
	certificate.TrustOperationFingerprint = ""
	certificate.TrustOperationWasTrusted = false
	certificate.TrustError = ""
}
