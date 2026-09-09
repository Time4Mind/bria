package main

import "testing"

func TestCardPageSelectionIsSharedButTransportNeutral(t *testing.T) {
	policy, ok := packagePolicies["internal/cardpageselection"]
	if !ok || policy.maxProductionLines > 200 || policy.maxProductionLines == 0 {
		t.Fatal("neutral page selection must have its own bounded production policy")
	}
	for _, source := range []string{"internal/telegramcontroller", "internal/telegramui"} {
		if !allowedPackageImport("internal/cardpageselection", source, packagePolicies[source].allowedImports) {
			t.Fatalf("%s cannot share neutral page selection", source)
		}
	}
	for _, forbidden := range []string{"internal/telegramui", "internal/telegramstate", "internal/telegramcontrolport", "internal/storage", "internal/telegram"} {
		if allowedPackageImport(forbidden, "internal/cardpageselection", policy.allowedImports) {
			t.Fatalf("page helper must not hide forbidden dependency %s", forbidden)
		}
	}
}

func TestViewDeliveryContextDoesNotIntroduceControllerDependencyBypass(t *testing.T) {
	policy, ok := packagePolicies["internal/viewdeliverycontext"]
	if !ok || policy.maxProductionLines == 0 || policy.maxProductionLines > 75 || len(policy.allowedImports) != 0 {
		t.Fatal("view delivery context must remain small and independent of application packages")
	}
	if !allowedPackageImport("internal/viewdeliverycontext", "internal/telegramcontroller", telegramControllerAllowedImports) {
		t.Fatal("controller must use the explicit neutral context boundary")
	}
}
