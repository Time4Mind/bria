package nativecli

import "bria/internal/domain"

// LaunchPolicy describes existing facts, never permission to forge sandbox
// state or impersonate another operating user.
type LaunchPolicy struct{ Root, ExistingCLISandbox bool }

func BuildWithPolicy(provider domain.Provider, rawCommand []string, workdir, resumeID string, policy LaunchPolicy) (Plan, error) {
	plan, err := Build(provider, rawCommand, workdir, resumeID)
	if err != nil {
		return Plan{}, err
	}
	if provider == domain.ProviderClaude && policy.Root && !policy.ExistingCLISandbox {
		for i, arg := range plan.Command {
			if arg == "--dangerously-skip-permissions" {
				args := append([]string(nil), plan.Command[:i]...)
				args = append(args, "--permission-mode", "auto")
				plan.Command = append(args, plan.Command[i+1:]...)
				break
			}
		}
	}
	return plan, nil
}
