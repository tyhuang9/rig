package jobs

import (
	"context"

	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/generatedrecovery"
)

// RecoverRuntimeContext performs deployment and job restart recovery in one
// guarded transaction while the caller holds startup effects admission.
// Historical bindings are never created or refreshed by recovery.
func (s *Service) RecoverRuntimeContext(ctx context.Context) error {
	if ctx == nil || s == nil || s.db == nil || s.now == nil || s.deploymentBindings == nil || !s.deploymentBindings.UsesDatabase(s.db) {
		return deploymentbinding.ErrInvalidConfiguration
	}
	result, err := generatedrecovery.RecoverRuntime(ctx, s.db, s.now().UTC(), s.deploymentBindings)
	if err != nil {
		return err
	}
	if result.Jobs.RequeuedGenerated > 0 {
		s.signal()
	}
	return nil
}
