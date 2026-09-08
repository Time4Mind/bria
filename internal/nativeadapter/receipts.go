package nativeadapter

import (
	"os"

	"bria/internal/nativeacceptance"
	"bria/internal/nativereceiptstore"
)

func (a *adapter) receiptPath() (string, error) {
	return nativereceiptstore.Path(a.config.StateDir, a.id)
}

func (a *adapter) loadReceipts() error {
	doc, err := nativereceiptstore.Read(a.config.StateDir, a.id)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil {
		a.receipts, a.turnIDs = doc.Receipts, doc.TurnIDs
	}
	return err
}

func (a *adapter) saveReceipts() error {
	delete(a.receipts, "")
	return nativereceiptstore.Write(a.config.StateDir, nativeacceptance.Document{SessionID: a.id, Receipts: a.receipts, TurnIDs: a.turnIDs})
}

// Correlation commits with its outcome before acknowledgement, never a sidecar.
func (a *adapter) acceptReceipt(messageID, turnID string) error {
	if messageID == "" {
		return a.saveReceipts()
	}
	doc, err := nativeacceptance.Accept(nativeacceptance.Document{SessionID: a.id, Receipts: a.receipts, TurnIDs: a.turnIDs}, messageID, turnID)
	if err != nil {
		return err
	}
	if err = nativereceiptstore.Write(a.config.StateDir, doc); err == nil {
		a.receipts, a.turnIDs = doc.Receipts, doc.TurnIDs
	}
	return err
}
