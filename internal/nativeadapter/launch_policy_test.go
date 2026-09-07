package nativeadapter

import "testing"

func TestLaunchPolicyUsesOnlyTruthfulChildEnvironment(t *testing.T) {
	for _, test := range []struct {
		euid          int
		env           []string
		root, sandbox bool
	}{
		{0, nil, true, false}, {1000, nil, false, false}, {0, []string{"IS_SANDBOX=1"}, true, true},
		{0, []string{"IS_SANDBOX=1", "IS_SANDBOX=0"}, true, false}, {0, []string{"BRIA_IS_SANDBOX=1"}, true, false},
	} {
		policy := nativeLaunchPolicy(test.euid, test.env)
		if policy.Root != test.root || policy.ExistingCLISandbox != test.sandbox {
			t.Fatal("launch context inferred or spoofed")
		}
	}
}
