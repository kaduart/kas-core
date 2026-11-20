package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/process"
)

// Process Studio routes (spec v2, section 6). The engine carries no business
// rule: definitions, roles and business statuses are data sent by the tenant's
// own service. occurred_at always comes from the authenticated service (the
// CRM server), never from a browser.

const (
	maxProcessRequestBytes    = 8 << 10
	maxDefinitionRequestBytes = 72 << 10
	processContractVersion    = "v1"
)

// EnableProcess turns on the Process Studio routes. Call it before serving
// requests. Without it those routes answer 503 process_unavailable.
func (h *Handler) EnableProcess(engine *process.Engine) { h.process = engine }

func (h *Handler) registerProcessRoutes() {
	h.mux.HandleFunc("POST /internal/v1/process/definitions:validate", h.processValidateDefinition)
	h.mux.HandleFunc("POST /internal/v1/process/definitions", h.processPublishDefinition)
	h.mux.HandleFunc("GET /internal/v1/process/definitions", h.processListDefinitions)
	h.mux.HandleFunc("GET /internal/v1/process/definitions/{key}", h.processGetDefinition)
	h.mux.HandleFunc("GET /internal/v1/process/instances", h.processListInstances)
	h.mux.HandleFunc("PUT /internal/v1/process/roles/{role}/members", h.processSetRoleMembers)
	h.mux.HandleFunc("POST /internal/v1/process/instances", h.processStartInstance)
	h.mux.HandleFunc("GET /internal/v1/process/instances/{id}", h.processGetInstance)
	h.mux.HandleFunc("GET /internal/v1/process/tasks", h.processListTasks)
	h.mux.HandleFunc("POST /internal/v1/process/tasks/{id}/resolve", h.processResolveTask)
	h.mux.HandleFunc("POST /internal/v1/process/commands/{key}/result", h.processCommandResult)
	h.mux.HandleFunc("POST /internal/v1/process/commands/{key}/query", h.processQueryCommand)
	h.mux.HandleFunc("POST /internal/v1/process/events", h.processIngestEvent)
}

type processDefinitionRequest struct {
	ContractVersion string          `json:"contract_version"`
	TenantID        string          `json:"tenant_id"`
	PublishID       string          `json:"publish_id"`
	Definition      json.RawMessage `json:"definition"`
}

type processRoleMembersRequest struct {
	ContractVersion string    `json:"contract_version"`
	TenantID        string    `json:"tenant_id"`
	ChangeID        string    `json:"change_id"`
	Members         []string  `json:"members"`
	ValidFrom       time.Time `json:"valid_from"`
}

type processStartRequest struct {
	ContractVersion string    `json:"contract_version"`
	TenantID        string    `json:"tenant_id"`
	ProcessKey      string    `json:"process_key"`
	BusinessRef     string    `json:"business_ref"`
	StartKey        string    `json:"start_key"`
	ActorID         string    `json:"actor_id"`
	OccurredAt      time.Time `json:"occurred_at"`
}

type processResolveRequest struct {
	ContractVersion string    `json:"contract_version"`
	TenantID        string    `json:"tenant_id"`
	SubmissionKey   string    `json:"submission_key"`
	Outcome         string    `json:"outcome"`
	ActorID         string    `json:"actor_id"`
	Role            string    `json:"role"`
	OccurredAt      time.Time `json:"occurred_at"`
}

type processCommandResultRequest struct {
	ContractVersion string    `json:"contract_version"`
	TenantID        string    `json:"tenant_id"`
	Status          string    `json:"status"`
	ReasonCode      string    `json:"reason_code,omitempty"`
	ResultRef       string    `json:"result_ref,omitempty"`
	OccurredAt      time.Time `json:"occurred_at"`
}

type processQueryRequest struct {
	ContractVersion string `json:"contract_version"`
	TenantID        string `json:"tenant_id"`
	ActorID         string `json:"actor_id"`
}

type processEventRequest struct {
	ContractVersion string    `json:"contract_version"`
	TenantID        string    `json:"tenant_id"`
	EventID         string    `json:"event_id"`
	EventType       string    `json:"event_type"`
	BusinessRef     string    `json:"business_ref"`
	WaitKey         string    `json:"wait_key,omitempty"`
	OccurredAt      time.Time `json:"occurred_at"`
}

// processPrelude checks availability, media type and decodes the body.
func (h *Handler) processPrelude(w http.ResponseWriter, r *http.Request, limit int, body any) bool {
	if h.process == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "process_unavailable", "process engine is not enabled")
		return false
	}
	if body == nil {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return false
	}
	if err := decodeStrictBody(r, limit, body); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the size limit")
			return false
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request JSON is invalid")
		return false
	}
	return true
}

// processAuth authenticates the service and resolves the tenant for a
// capability. It returns the service id and tenant id.
func (h *Handler) processAuth(w http.ResponseWriter, r *http.Request, contractVersion, requestedTenant, capability string) (string, string, bool) {
	if contractVersion != processContractVersion {
		h.writeError(w, r, http.StatusBadRequest, "unsupported_contract_version", "contract_version must be v1")
		return "", "", false
	}
	serviceID := r.Header.Get("X-KAS-Service")
	token := bearerToken(r.Header.Get("Authorization"))
	tenantID, err := h.store.ResolveTenant(serviceID, token, requestedTenant, capability)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return "", "", false
	}
	return serviceID, tenantID, true
}

func (h *Handler) writeProcessError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, process.ErrInvalidDefinition):
		h.writeError(w, r, http.StatusUnprocessableEntity, "invalid_definition", err.Error())
	case errors.Is(err, process.ErrInvalidInput):
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request fields are invalid")
	case errors.Is(err, process.ErrClockSkew):
		h.writeError(w, r, http.StatusBadRequest, "clock_skew", "occurred_at is too far from the server clock")
	case errors.Is(err, process.ErrNotFound):
		h.writeError(w, r, http.StatusNotFound, "not_found", "resource not found for this tenant")
	case errors.Is(err, process.ErrRoleNotAllowed):
		h.writeError(w, r, http.StatusForbidden, "role_not_allowed", "role may not resolve this task")
	case errors.Is(err, process.ErrActorNotInRole):
		h.writeError(w, r, http.StatusForbidden, "actor_not_in_role", "actor did not hold the role at occurred_at")
	case errors.Is(err, process.ErrTaskClosed):
		h.writeError(w, r, http.StatusConflict, "task_closed", "task is no longer open")
	case errors.Is(err, process.ErrCommandUnknown):
		h.writeError(w, r, http.StatusConflict, "command_unknown", "a command result is still unknown; consult the external system first")
	case errors.Is(err, process.ErrCommandNotPending):
		h.writeError(w, r, http.StatusConflict, "command_not_pending", "command is not requested or unknown")
	case errors.Is(err, process.ErrResultRefRequired):
		h.writeError(w, r, http.StatusUnprocessableEntity, "result_ref_required", "this step needs result_ref with a confirmed result")
	case errors.Is(err, process.ErrInstanceActive):
		h.writeError(w, r, http.StatusConflict, "instance_active", "a running instance already exists for this business_ref")
	case errors.Is(err, process.ErrWaitTypeMismatch):
		h.writeError(w, r, http.StatusConflict, "wait_type_mismatch", "event type does not match the wait")
	case errors.Is(err, process.ErrConflict):
		h.writeError(w, r, http.StatusConflict, "idempotency_conflict", "key was already used with different content")
	default:
		h.logger.Error("process_request_failed", "correlation_id", CorrelationID(r.Context()), "error", err)
		h.writeError(w, r, http.StatusServiceUnavailable, "process_unavailable", "process store is unavailable")
	}
}

func createdOr(duplicate bool) int {
	if duplicate {
		return http.StatusOK
	}
	return http.StatusCreated
}

func (h *Handler) processValidateDefinition(w http.ResponseWriter, r *http.Request) {
	var req processDefinitionRequest
	if !h.processPrelude(w, r, maxDefinitionRequestBytes, &req) {
		return
	}
	if _, _, ok := h.processAuth(w, r, req.ContractVersion, req.TenantID, configstore.CapabilityProcessDefine); !ok {
		return
	}
	def, _, checksum, err := process.ParseDefinition(req.Definition)
	if err != nil {
		h.writeJSON(w, http.StatusOK, map[string]any{"contract_version": processContractVersion, "valid": false, "errors": []string{err.Error()}})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"contract_version": processContractVersion, "valid": true, "process_key": def.ProcessKey, "checksum": checksum})
}

func (h *Handler) processPublishDefinition(w http.ResponseWriter, r *http.Request) {
	var req processDefinitionRequest
	if !h.processPrelude(w, r, maxDefinitionRequestBytes, &req) {
		return
	}
	serviceID, tenantID, ok := h.processAuth(w, r, req.ContractVersion, req.TenantID, configstore.CapabilityProcessDefine)
	if !ok {
		return
	}
	version, duplicate, err := h.process.PublishDefinition(r.Context(), tenantID, serviceID, req.PublishID, req.Definition)
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, createdOr(duplicate), map[string]any{"contract_version": processContractVersion, "tenant_id": tenantID, "version": version, "duplicate": duplicate})
}

func (h *Handler) processSetRoleMembers(w http.ResponseWriter, r *http.Request) {
	var req processRoleMembersRequest
	if !h.processPrelude(w, r, maxProcessRequestBytes, &req) {
		return
	}
	serviceID, tenantID, ok := h.processAuth(w, r, req.ContractVersion, req.TenantID, configstore.CapabilityProcessDefine)
	if !ok {
		return
	}
	assignmentVersion, duplicate, err := h.process.SetRoleMembers(r.Context(), tenantID, serviceID, r.PathValue("role"), req.ChangeID, req.Members, req.ValidFrom)
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, createdOr(duplicate), map[string]any{"contract_version": processContractVersion, "assignment_version": assignmentVersion, "duplicate": duplicate})
}

func (h *Handler) processStartInstance(w http.ResponseWriter, r *http.Request) {
	var req processStartRequest
	if !h.processPrelude(w, r, maxProcessRequestBytes, &req) {
		return
	}
	serviceID, tenantID, ok := h.processAuth(w, r, req.ContractVersion, req.TenantID, configstore.CapabilityProcessRun)
	if !ok {
		return
	}
	id, duplicate, err := h.process.StartInstance(r.Context(), process.StartRequest{
		TenantID: tenantID, ServiceID: serviceID, ProcessKey: req.ProcessKey, BusinessRef: req.BusinessRef,
		StartKey: req.StartKey, ActorID: req.ActorID, OccurredAt: req.OccurredAt,
	})
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, createdOr(duplicate), map[string]any{"contract_version": processContractVersion, "instance_id": id, "duplicate": duplicate})
}

func (h *Handler) processGetInstance(w http.ResponseWriter, r *http.Request) {
	if !h.processPrelude(w, r, 0, nil) {
		return
	}
	_, tenantID, ok := h.processAuth(w, r, processContractVersion, r.URL.Query().Get("tenant_id"), configstore.CapabilityProcessRun)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "instance id is invalid")
		return
	}
	view, err := h.process.GetInstance(r.Context(), tenantID, id)
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"contract_version": processContractVersion, "instance": view})
}

func (h *Handler) processListTasks(w http.ResponseWriter, r *http.Request) {
	if !h.processPrelude(w, r, 0, nil) {
		return
	}
	q := r.URL.Query()
	_, tenantID, ok := h.processAuth(w, r, processContractVersion, q.Get("tenant_id"), configstore.CapabilityProcessTask)
	if !ok {
		return
	}
	tasks, err := h.process.ListTasks(r.Context(), tenantID, q.Get("role"), q.Get("status"))
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"contract_version": processContractVersion, "tasks": tasks})
}

func (h *Handler) processResolveTask(w http.ResponseWriter, r *http.Request) {
	var req processResolveRequest
	if !h.processPrelude(w, r, maxProcessRequestBytes, &req) {
		return
	}
	serviceID, tenantID, ok := h.processAuth(w, r, req.ContractVersion, req.TenantID, configstore.CapabilityProcessTask)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "task id is invalid")
		return
	}
	res, err := h.process.SubmitTask(r.Context(), process.SubmitRequest{
		TenantID: tenantID, ServiceID: serviceID, TaskID: id, Outcome: req.Outcome, ActorID: req.ActorID,
		Role: req.Role, OccurredAt: req.OccurredAt, SubmissionKey: req.SubmissionKey,
	})
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"contract_version": processContractVersion, "status": res.Status, "instance_id": res.InstanceID,
		"next_step": res.NextStep, "duplicate": res.Duplicate,
	})
}

func (h *Handler) processCommandResult(w http.ResponseWriter, r *http.Request) {
	var req processCommandResultRequest
	if !h.processPrelude(w, r, maxProcessRequestBytes, &req) {
		return
	}
	serviceID, tenantID, ok := h.processAuth(w, r, req.ContractVersion, req.TenantID, configstore.CapabilityProcessCommand)
	if !ok {
		return
	}
	duplicate, err := h.process.RecordCommandResult(r.Context(), process.CommandResult{
		TenantID: tenantID, ServiceID: serviceID, CommandKey: r.PathValue("key"), Status: req.Status,
		ReasonCode: req.ReasonCode, ResultRef: req.ResultRef, OccurredAt: req.OccurredAt,
	})
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	// 200 means durably recorded: only now may the CRM ack its delivery.
	out := map[string]any{"contract_version": processContractVersion, "recorded": true, "duplicate": duplicate}
	if req.ResultRef != "" {
		out["result_ref"] = req.ResultRef
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *Handler) processQueryCommand(w http.ResponseWriter, r *http.Request) {
	var req processQueryRequest
	if !h.processPrelude(w, r, maxProcessRequestBytes, &req) {
		return
	}
	serviceID, tenantID, ok := h.processAuth(w, r, req.ContractVersion, req.TenantID, configstore.CapabilityProcessCommand)
	if !ok {
		return
	}
	if err := h.process.QueryCommand(r.Context(), tenantID, serviceID, req.ActorID, r.PathValue("key")); err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusAccepted, map[string]any{"contract_version": processContractVersion, "queried": true})
}

func (h *Handler) processIngestEvent(w http.ResponseWriter, r *http.Request) {
	var req processEventRequest
	if !h.processPrelude(w, r, maxProcessRequestBytes, &req) {
		return
	}
	serviceID, tenantID, ok := h.processAuth(w, r, req.ContractVersion, req.TenantID, configstore.CapabilityProcessEvent)
	if !ok {
		return
	}
	res, err := h.process.IngestEvent(r.Context(), process.EventRequest{
		TenantID: tenantID, ServiceID: serviceID, EventID: req.EventID, EventType: req.EventType,
		BusinessRef: req.BusinessRef, WaitKey: req.WaitKey, OccurredAt: req.OccurredAt,
	})
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, createdOr(res.Duplicate), map[string]any{
		"contract_version": processContractVersion, "outcome": res.Outcome, "matched": res.Matched,
		"instance_id": res.InstanceID, "duplicate": res.Duplicate,
	})
}

func (h *Handler) processReady(ctx context.Context) error {
	if h.process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return h.process.Ready(ctx)
}

func (h *Handler) processListDefinitions(w http.ResponseWriter, r *http.Request) {
	if !h.processPrelude(w, r, 0, nil) {
		return
	}
	_, tenantID, ok := h.processAuth(w, r, processContractVersion, r.URL.Query().Get("tenant_id"), configstore.CapabilityProcessDefine)
	if !ok {
		return
	}
	list, err := h.process.ListDefinitions(r.Context(), tenantID)
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"contract_version": processContractVersion, "tenant_id": tenantID, "definitions": list})
}

func (h *Handler) processGetDefinition(w http.ResponseWriter, r *http.Request) {
	if !h.processPrelude(w, r, 0, nil) {
		return
	}
	q := r.URL.Query()
	_, tenantID, ok := h.processAuth(w, r, processContractVersion, q.Get("tenant_id"), configstore.CapabilityProcessDefine)
	if !ok {
		return
	}
	version := 0
	if raw := q.Get("version"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			h.writeError(w, r, http.StatusBadRequest, "invalid_request", "version is invalid")
			return
		}
		version = n
	}
	def, err := h.process.GetDefinition(r.Context(), tenantID, r.PathValue("key"), version)
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"contract_version": processContractVersion, "tenant_id": tenantID, "definition": def})
}

func (h *Handler) processListInstances(w http.ResponseWriter, r *http.Request) {
	if !h.processPrelude(w, r, 0, nil) {
		return
	}
	q := r.URL.Query()
	_, tenantID, ok := h.processAuth(w, r, processContractVersion, q.Get("tenant_id"), configstore.CapabilityProcessRun)
	if !ok {
		return
	}
	list, err := h.process.ListInstances(r.Context(), tenantID, q.Get("process_key"), q.Get("status"))
	if err != nil {
		h.writeProcessError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"contract_version": processContractVersion, "tenant_id": tenantID, "instances": list})
}
