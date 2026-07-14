package host

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

func (h *Host) listEnvironmentTemplates() ([]model.EnvironmentTemplate, error) {
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	return db.EnvironmentTemplates, nil
}

func validateTemplateInput(t *model.EnvironmentTemplate) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || len(t.Name) > 64 {
		return fmt.Errorf("name must be between 1 and 64 characters")
	}
	if t.ProxyProfileID != "" {
		if err := security.ValidateID(t.ProxyProfileID); err != nil {
			return fmt.Errorf("invalid proxy profile ID")
		}
	}
	if len(t.CertificateIDs) > 50 {
		return fmt.Errorf("too many certificates")
	}
	for _, cid := range t.CertificateIDs {
		if err := security.ValidateID(cid); err != nil {
			return fmt.Errorf("invalid certificate ID")
		}
	}
	return nil
}

func (h *Host) createEnvironmentTemplate(raw json.RawMessage) (model.EnvironmentTemplate, error) {
	var in model.EnvironmentTemplate
	if err := decodeData(raw, &in); err != nil {
		return model.EnvironmentTemplate{}, err
	}
	if err := validateTemplateInput(&in); err != nil {
		return model.EnvironmentTemplate{}, fail("INVALID_TEMPLATE", "%v", err)
	}
	if in.CertificateIDs == nil {
		in.CertificateIDs = []string{}
	}

	id, err := security.NewID()
	if err != nil {
		return model.EnvironmentTemplate{}, err
	}

	now := h.now()
	in.ID = id
	in.CreatedAt = now
	in.UpdatedAt = now

	err = h.store.Update(func(db *model.Database) error {
		// Validate references exist
		if in.ProxyProfileID != "" {
			found := false
			for _, p := range db.ProxyProfiles {
				if p.ID == in.ProxyProfileID {
					found = true
					break
				}
			}
			if !found {
				return fail("REFERENCE_ERROR", "proxy profile not found")
			}
		}
		for _, cid := range in.CertificateIDs {
			found := false
			for _, c := range db.Certificates {
				if c.ID == cid {
					found = true
					break
				}
			}
			if !found {
				return fail("REFERENCE_ERROR", "certificate not found: %s", cid)
			}
		}

		db.EnvironmentTemplates = append(db.EnvironmentTemplates, in)
		return nil
	})

	if err != nil {
		return model.EnvironmentTemplate{}, err
	}
	return in, nil
}

func (h *Host) updateEnvironmentTemplate(raw json.RawMessage) (model.EnvironmentTemplate, error) {
	var in model.EnvironmentTemplate
	if err := decodeData(raw, &in); err != nil {
		return model.EnvironmentTemplate{}, err
	}
	if err := security.ValidateID(in.ID); err != nil {
		return model.EnvironmentTemplate{}, fail("INVALID_TEMPLATE", "%v", err)
	}
	if err := validateTemplateInput(&in); err != nil {
		return model.EnvironmentTemplate{}, fail("INVALID_TEMPLATE", "%v", err)
	}
	if in.CertificateIDs == nil {
		in.CertificateIDs = []string{}
	}

	var updated model.EnvironmentTemplate
	err := h.store.Update(func(db *model.Database) error {
		// Validate references exist
		if in.ProxyProfileID != "" {
			found := false
			for _, p := range db.ProxyProfiles {
				if p.ID == in.ProxyProfileID {
					found = true
					break
				}
			}
			if !found {
				return fail("REFERENCE_ERROR", "proxy profile not found")
			}
		}
		for _, cid := range in.CertificateIDs {
			found := false
			for _, c := range db.Certificates {
				if c.ID == cid {
					found = true
					break
				}
			}
			if !found {
				return fail("REFERENCE_ERROR", "certificate not found: %s", cid)
			}
		}

		for i := range db.EnvironmentTemplates {
			if db.EnvironmentTemplates[i].ID == in.ID {
				in.CreatedAt = db.EnvironmentTemplates[i].CreatedAt
				in.UpdatedAt = h.now()
				db.EnvironmentTemplates[i] = in
				updated = in
				return nil
			}
		}
		return fail("NOT_FOUND", "environment template not found")
	})

	return updated, err
}

func (h *Host) deleteEnvironmentTemplate(id string) (map[string]any, error) {
	if err := security.ValidateID(id); err != nil {
		return nil, fail("INVALID_TEMPLATE", "%v", err)
	}

	err := h.store.Update(func(db *model.Database) error {
		idx := -1
		for i, t := range db.EnvironmentTemplates {
			if t.ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fail("NOT_FOUND", "environment template not found")
		}

		// Check references in containers
		for _, c := range db.Containers {
			if c.EnvironmentTemplateID == id {
				return fail("TEMPLATE_REFERENCE_IN_USE", "Template is used by a container")
			}
		}

		db.EnvironmentTemplates = append(db.EnvironmentTemplates[:idx], db.EnvironmentTemplates[idx+1:]...)
		return nil
	})

	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}
