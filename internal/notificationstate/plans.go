package notificationstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"bria/internal/notificationplan"
)

var errNoChange = errors.New("part receipt store unchanged")

type savedPlan struct {
	Version   int      `json:"version"`
	InputHash string   `json:"input_hash"`
	Pages     []string `json:"pages"`
	Checksum  string   `json:"checksum"`
}

func (plan savedPlan) copy() notificationplan.Plan {
	return notificationplan.Plan{Version: plan.Version, InputHash: plan.InputHash, Pages: append([]string(nil), plan.Pages...)}
}

// LoadPlan never computes pages or writes the receipt file.
func (store *FilePartReceiptStore) LoadPlan(ctx context.Context, operationID, inputHash string) (notificationplan.Plan, bool, error) {
	var result notificationplan.Plan
	var exists bool
	if err := validatePlanIdentity(operationID, inputHash); err != nil {
		return result, false, err
	}
	err := store.inspect(func(state partReceiptFileState) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		plan, found := state.Plans[operationID]
		if found && plan.InputHash != inputHash {
			return errors.New("notification plan input identity conflicts")
		}
		if found {
			result, exists = plan.copy(), true
		}
		return nil
	})
	return result, exists, err
}

// GetOrCreatePlan serializes migration with every receipt and claim mutation.
func (store *FilePartReceiptStore) GetOrCreatePlan(ctx context.Context, operationID, inputHash string, richPages, legacyPages []string) (notificationplan.Plan, error) {
	var result notificationplan.Plan
	if err := validatePlanIdentity(operationID, inputHash); err != nil {
		return result, err
	}
	err := store.mutate(ctx, func(state *partReceiptFileState) error {
		if plan, found := state.Plans[operationID]; found {
			if plan.InputHash != inputHash {
				return errors.New("notification plan input identity conflicts")
			}
			result = plan.copy()
			return errNoChange
		}
		plan := savedPlan{Version: 2, InputHash: inputHash, Pages: append([]string(nil), richPages...)}
		if _, legacy := state.Operations[operationID]; legacy {
			plan.Version, plan.Pages = 1, append([]string(nil), legacyPages...)
		}
		plan.Checksum = planChecksum(operationID, plan)
		if err := validateSavedPlan(operationID, plan); err != nil {
			return err
		}
		if state.Plans == nil {
			state.Plans = make(map[string]savedPlan)
		}
		state.Plans[operationID], state.Version = plan, partReceiptFileVersion
		for partID := range state.Operations[operationID] {
			if err := validateStoredBinding(*state, operationID, partID, true); err != nil {
				return err
			}
		}
		result = plan.copy()
		return nil
	})
	if err != nil {
		return notificationplan.Plan{}, err
	}
	return result, nil
}

// ClaimPart fences a send before it can reach the network. A false result must
// never be sent: it is already confirmed or may have reached Telegram.
func (store *FilePartReceiptStore) ClaimPart(ctx context.Context, operationID, partID string) (bool, error) {
	claimed := false
	err := store.mutate(ctx, func(state *partReceiptFileState) error {
		if err := validateStoredBinding(*state, operationID, partID, true); err != nil {
			return err
		}
		if _, known := state.Operations[operationID][partID]; known {
			return errNoChange
		}
		if state.Operations[operationID] == nil {
			state.Operations[operationID] = make(map[string]storedPart)
		}
		state.Operations[operationID][partID] = storedPart{State: DeliveryUnknown}
		claimed = true
		return nil
	})
	return claimed && err == nil, err
}

// ReleasePart is only for a definitive rejection of this caller's claimed send.
// Ambiguous outcomes must retain their fence until explicit reconciliation.
func (store *FilePartReceiptStore) ReleasePart(ctx context.Context, operationID, partID string) error {
	return store.mutate(ctx, func(state *partReceiptFileState) error {
		if err := validateStoredBinding(*state, operationID, partID, true); err != nil {
			return err
		}
		if state.Operations[operationID][partID].State != DeliveryUnknown {
			return errors.New("Telegram part is not unknown")
		}
		delete(state.Operations[operationID], partID)
		return nil
	})
}

func validateOperation(operationID string) error {
	if operationID == "" || strings.TrimSpace(operationID) != operationID || len(operationID) > 512 || !utf8.ValidString(operationID) {
		return errors.New("invalid notification operation identity")
	}
	return nil
}

func validatePlanIdentity(operationID, inputHash string) error {
	if err := validateOperation(operationID); err != nil {
		return err
	}
	digest, err := hex.DecodeString(inputHash)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("invalid notification input digest")
	}
	return nil
}

func validateStoredBinding(state partReceiptFileState, operationID, partID string, requirePlan bool) error {
	if err := validatePartBinding(operationID, partID); err != nil {
		return err
	}
	plan, exists := state.Plans[operationID]
	if !exists && requirePlan {
		return errors.New("notification part requires a saved plan")
	}
	if exists && !strings.HasSuffix(partID, "-of-"+strconv.Itoa(len(plan.Pages))) {
		return errors.New("notification part conflicts with saved plan")
	}
	return nil
}

func validateSavedPlan(operationID string, plan savedPlan) error {
	if validatePlanIdentity(operationID, plan.InputHash) != nil || (plan.Version != 1 && plan.Version != 2) || len(plan.Pages) == 0 || plan.Checksum != planChecksum(operationID, plan) {
		return errors.New("invalid notification saved plan")
	}
	for _, page := range plan.Pages {
		if !utf8.ValidString(page) || strings.TrimSpace(page) == "" || (plan.Version == 2 && len(page) > 4096) {
			return errors.New("invalid notification saved page")
		}
	}
	return nil
}

func planChecksum(operationID string, plan savedPlan) string {
	plan.Checksum = ""
	data, _ := json.Marshal(struct {
		Operation string
		Plan      savedPlan
	}{operationID, plan})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// Cover the complete manifest, including absent plans and retained legacy maps.
func stateChecksum(state partReceiptFileState) string {
	state.Checksum = ""
	data, _ := json.Marshal(state)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
