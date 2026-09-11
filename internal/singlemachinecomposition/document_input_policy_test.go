package singlemachinecomposition

import (
	"context"
	"testing"

	"bria/internal/turnprocessing"
)

type documentInputPreparer struct{}

func (documentInputPreparer) Prepare(context.Context, turnprocessing.IncomingInput) (string, error) {
	return "prepared", nil
}

func TestProductionAllowsDocumentsOnlyWithConfiguredPreparer(t *testing.T) {
	if allowProductionDocumentInput(nil) {
		t.Fatal("document input allowed without a preparer")
	}
	if !allowProductionDocumentInput(documentInputPreparer{}) {
		t.Fatal("document input remains disabled with the production preparer")
	}
}
