package multinodecomposition_test

import "testing"

func TestMultiNodeRoleCutoverDurableSyntheticVertical(t *testing.T) {
	t.Run("one address pinned roles and offline replay", runOneAddressRolesReconnectAndReplayOfflineEventExactlyOnce)
	t.Run("manual cutover exact durable state", runManualCutoverPhysicallyReopensExactStateAndFence)
}
