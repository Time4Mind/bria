package providerattachport_test

import (
	"bria/internal/domain"
	"bria/internal/providerattachport"
	"testing"
)

func TestExactResumeValidationPreservesPriorIdentityRequirements(t *testing.T) {
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			request := providerattachport.StartSessionRequest{SessionID: "logical", ComputerID: "local", Provider: provider, Workdir: "/public-fixture",
				Mode: providerattachport.SessionStartResume, PriorBinding: &domain.ProviderBinding{Provider: provider, SessionID: "exact-native", Generation: 7}}
			if err := request.Validate(); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*providerattachport.StartSessionRequest){
				func(r *providerattachport.StartSessionRequest) { r.PriorBinding = nil },
				func(r *providerattachport.StartSessionRequest) { r.PriorBinding.Generation = 0 },
				func(r *providerattachport.StartSessionRequest) { r.PriorBinding.SessionID = " " },
				func(r *providerattachport.StartSessionRequest) { r.PriorBinding.Provider = "different" },
				func(r *providerattachport.StartSessionRequest) { r.Mode = providerattachport.SessionStartNew },
			} {
				invalid := request
				prior := *request.PriorBinding
				invalid.PriorBinding = &prior
				mutate(&invalid)
				if err := invalid.Validate(); err == nil {
					t.Fatal("invalid exact identity was accepted")
				}
			}
		})
	}
}
