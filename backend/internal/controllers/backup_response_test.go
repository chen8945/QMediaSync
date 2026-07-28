package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/backup"

	"github.com/gin-gonic/gin"
)

func TestRespondAcceptedBackupOperationPreservesAcceptanceContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	grant := backup.OperationGrant{OperationID: "operation-id", Token: "one-time-token"}

	respondAcceptedBackupOperation(context, grant, "恢复任务已受理")

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusAccepted)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}

	var payload APIResponse[BackupOperationAccepted]
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal accepted response: %v", err)
	}
	if payload.Code != Success || payload.Message != "恢复任务已受理" || payload.Data.OperationID != grant.OperationID || payload.Data.Token != grant.Token {
		t.Fatalf("accepted response = %+v, want accepted operation contract", payload)
	}
}
