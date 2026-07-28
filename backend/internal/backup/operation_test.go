package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperationCoordinatorPersistsOnlyTokenHashAndSerializesActiveOperation(t *testing.T) {
	stateDir := t.TempDir()
	coordinator, err := NewOperationCoordinator(stateDir)
	if err != nil {
		t.Fatalf("NewOperationCoordinator() error = %v", err)
	}

	grant, err := coordinator.Begin(OperationKindBackup, true)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if grant.OperationID == "" || grant.Token == "" {
		t.Fatalf("grant = %+v, want operation ID and token", grant)
	}
	if _, err := coordinator.Begin(OperationKindRestore, true); !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("second Begin() error = %v, want ErrOperationInProgress", err)
	}
	if _, err := coordinator.Authorize(grant.OperationID, "incorrect-token"); !errors.Is(err, ErrOperationUnauthorized) {
		t.Fatalf("Authorize() error = %v, want ErrOperationUnauthorized", err)
	}

	state, err := os.ReadFile(filepath.Join(stateDir, operationStateFileName))
	if err != nil {
		t.Fatalf("ReadFile(state): %v", err)
	}
	if strings.Contains(string(state), grant.Token) {
		t.Fatal("operation token was persisted in plaintext")
	}

	reloaded, err := NewOperationCoordinator(stateDir)
	if err != nil {
		t.Fatalf("reload coordinator: %v", err)
	}
	view, err := reloaded.Authorize(grant.OperationID, grant.Token)
	if err != nil {
		t.Fatalf("Authorize(reloaded) error = %v", err)
	}
	if view.Kind != OperationKindBackup || view.State != OperationStateQueued {
		t.Fatalf("reloaded view = %+v, want queued backup", view)
	}
}

func TestOperationCoordinatorTransitionsMaintenanceAndTerminalReplacement(t *testing.T) {
	coordinator, err := NewOperationCoordinator(t.TempDir())
	if err != nil {
		t.Fatalf("NewOperationCoordinator() error = %v", err)
	}
	grant, err := coordinator.Begin(OperationKindRestore, true)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	if err := coordinator.Transition(grant.OperationID, OperationTransition{State: OperationStateRunning}); !errors.Is(err, ErrInvalidOperationTransition) {
		t.Fatalf("invalid transition error = %v, want ErrInvalidOperationTransition", err)
	}
	for _, state := range []OperationState{
		OperationStateWaitingForTasks,
		OperationStateValidating,
		OperationStateRunning,
		OperationStateRollingBack,
	} {
		if err := coordinator.Transition(grant.OperationID, OperationTransition{State: state}); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	if err := coordinator.SetMaintenance(grant.OperationID, true); err != nil {
		t.Fatalf("SetMaintenance(true) error = %v", err)
	}
	if !coordinator.InMaintenance() {
		t.Fatal("coordinator should be in maintenance")
	}
	if err := coordinator.Transition(grant.OperationID, OperationTransition{
		State:         OperationStateFailed,
		ErrorCode:     OperationErrorCodeRestoreFailed,
		RollbackState: RollbackStateSucceeded,
	}); err != nil {
		t.Fatalf("failed transition error = %v", err)
	}
	if coordinator.InMaintenance() {
		t.Fatal("terminal operation must leave maintenance")
	}

	terminal := coordinator.LatestTerminal()
	if terminal == nil || terminal.State != OperationStateFailed || terminal.RollbackState != RollbackStateSucceeded {
		t.Fatalf("LatestTerminal() = %+v, want failed restored operation", terminal)
	}
	if _, err := coordinator.Begin(OperationKindBackup, false); err != nil {
		t.Fatalf("Begin() after terminal operation error = %v", err)
	}
}

func TestOperationCoordinatorRejectsInvalidPersistedState(t *testing.T) {
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, operationStateFileName)
	if err := os.WriteFile(statePath, []byte(`{"unexpected":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile(state): %v", err)
	}
	if _, err := NewOperationCoordinator(stateDir); !errors.Is(err, ErrInvalidOperationState) {
		t.Fatalf("NewOperationCoordinator() error = %v, want ErrInvalidOperationState", err)
	}
}

// TestOperationErrorCodeSafeDescriptionNeverEmpty 保证前端拿到的错误码始终有可展示的中文说明，
// 包括未登记的错误码也回落到内部错误说明，不会渲染成空白。
func TestOperationErrorCodeSafeDescriptionNeverEmpty(t *testing.T) {
	codes := []OperationErrorCode{
		OperationErrorCodeBackupFailed,
		OperationErrorCodeRestoreFailed,
		OperationErrorCodeArtifactInvalid,
		OperationErrorCodePasswordOrCorrupt,
		OperationErrorCodeIncompatibleTarget,
		OperationErrorCodeDatabaseUnavailable,
		OperationErrorCodeInsufficientSpace,
		OperationErrorCodeSnapshotFailed,
		OperationErrorCodeTasksNotIdle,
		OperationErrorCodeInternal,
		OperationErrorCode("未登记的错误码"),
	}
	for _, code := range codes {
		t.Run(string(code), func(t *testing.T) {
			if strings.TrimSpace(code.SafeDescription()) == "" {
				t.Fatalf("SafeDescription() 为空，错误码 %q 无法展示", code)
			}
		})
	}
}
