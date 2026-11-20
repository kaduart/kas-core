package httpapi

import (
	"encoding/csv"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"kas-core-pilot/internal/attribution"
	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
)

// KAS Attribution routes (spec "KAS por módulos", section 8.4). Inputs are
// generic (customers, contacts, receipts, refunds) and sent by the tenant's own
// service; the module gate in configstore decides whether the tenant contracted
// the module. Nothing here knows any tenant's business.

// maxAttributionRequestBytes fits a batch of MaxBatchItems inputs.
const maxAttributionRequestBytes = 1 << 20

// attributionDeadline replaces the server's short read/write timeouts for a
// batch (up to 1 MiB and MaxBatchItems inserts) and for a report.
const attributionDeadline = 30 * time.Second

// extendDeadlines gives this one request more time than the server default.
// Writers that cannot set deadlines (tests) are left as they are.
func extendDeadlines(w http.ResponseWriter, d time.Duration) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(d)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
}

// EnableAttribution turns on the Attribution routes and registers the module
// readiness check. Call it before serving requests. Without it those routes
// answer 503 attribution_unavailable.
func (h *Handler) EnableAttribution(store *attribution.Store) {
	h.attribution = store
	h.EnableModuleReadiness(configstore.ModuleAttribution, store.Missing)
}

func (h *Handler) registerAttributionRoutes() {
	for _, kind := range []string{attribution.KindCustomer, attribution.KindContact, attribution.KindReceipt, attribution.KindRefund} {
		kind := kind
		h.mux.HandleFunc("POST /internal/v1/attribution/"+kind+":batch", func(w http.ResponseWriter, r *http.Request) {
			h.attributionImport(w, r, kind)
		})
	}
	h.mux.HandleFunc("GET /internal/v1/attribution/revenue", h.attributionRevenue)
}

type attributionBatchRequest struct {
	ContractVersion string             `json:"contract_version"`
	TenantID        string             `json:"tenant_id"`
	BatchID         string             `json:"batch_id"`
	Actor           string             `json:"actor"`
	Items           []attribution.Item `json:"items"`
}

type attributionBatchResponse struct {
	ContractVersion string `json:"contract_version"`
	TenantID        string `json:"tenant_id"`
	attribution.BatchResult
	Duplicate bool `json:"duplicate"`
}

func (h *Handler) attributionImport(w http.ResponseWriter, r *http.Request, kind string) {
	started := time.Now()
	if h.attribution == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "attribution_unavailable", "attribution is not enabled")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	extendDeadlines(w, attributionDeadline)
	var req attributionBatchRequest
	if err := decodeStrictBody(r, maxAttributionRequestBytes, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the size limit")
			return
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request JSON is invalid")
		return
	}
	if req.ContractVersion != attribution.ContractVersion {
		h.writeError(w, r, http.StatusBadRequest, "unsupported_contract_version", "contract_version must be v1")
		return
	}
	serviceID := r.Header.Get("X-KAS-Service")
	tenantID, err := h.store.ResolveTenant(serviceID, bearerToken(r.Header.Get("Authorization")), req.TenantID, configstore.CapabilityAttributionImport)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return
	}
	result, duplicate, err := h.attribution.Import(r.Context(), attribution.Batch{
		TenantID: tenantID, ServiceID: serviceID, Kind: kind, BatchID: req.BatchID, Actor: req.Actor, Items: req.Items,
	})
	switch {
	case errors.Is(err, attribution.ErrInvalidBatch):
		h.writeError(w, r, http.StatusBadRequest, "invalid_batch",
			"batch_id, actor and 1 to "+strconv.Itoa(attribution.MaxBatchItems)+" items are required")
		return
	case errors.Is(err, attribution.ErrBatchConflict):
		h.writeError(w, r, http.StatusConflict, "batch_conflict", "batch_id was already used with different content")
		return
	case err != nil:
		h.logger.Error("attribution_import_failed", "correlation_id", CorrelationID(r.Context()), "error", err)
		h.writeError(w, r, http.StatusServiceUnavailable, "attribution_unavailable", "attribution store is unavailable")
		return
	}
	if !duplicate {
		h.auditBatch(r, serviceID, tenantID, req.Actor, kind, req.BatchID)
	}
	status := createdOr(duplicate)
	h.writeJSON(w, status, attributionBatchResponse{
		ContractVersion: attribution.ContractVersion, TenantID: tenantID, BatchResult: result, Duplicate: duplicate,
	})
	h.logger.Info("attribution_batch", "correlation_id", CorrelationID(r.Context()), "status", status, "kind", kind,
		"created", result.Created, "updated", result.Updated, "unchanged", result.Unchanged, "rejected", len(result.Rejected),
		"duration_ms", time.Since(started).Milliseconds())
}

// auditBatch records who imported which batch. The record holds identifiers
// only: no amounts, no contact data (the audit trail stays free of money and
// personal data, decision D2). A failure is logged; the batch is already stored.
func (h *Handler) auditBatch(r *http.Request, serviceID, tenantID, actor, kind, batchID string) {
	if h.audit == nil {
		return
	}
	_, err := h.audit.Submit(r.Context(), serviceID, audit.Submission{
		ContractVersion: audit.ContractVersion,
		EventID:         "attribution-batch-" + newCorrelationID(),
		TenantID:        tenantID,
		ServiceID:       serviceID,
		Actor:           actor,
		Action:          "attribution.batch_imported",
		Resource:        "attribution:" + kind,
		Result:          "success",
		CorrelationID:   CorrelationID(r.Context()),
		OccurredAt:      time.Now().UTC().Format(time.RFC3339Nano),
		EntityType:      "attribution_batch",
		EntityID:        batchID,
	})
	if err != nil {
		h.logger.Warn("attribution_batch_audit_failed", "correlation_id", CorrelationID(r.Context()))
	}
}

type attributionRevenueResponse struct {
	ContractVersion string `json:"contract_version"`
	TenantID        string `json:"tenant_id"`
	From            string `json:"from,omitempty"`
	To              string `json:"to,omitempty"`
	Unit            string `json:"unit,omitempty"`
	attribution.Result
}

// parsePeriodBound accepts an RFC 3339 instant or a plain date (YYYY-MM-DD,
// UTC). A plain "to" date includes the whole day.
func parsePeriodBound(raw string, end bool) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, false
	}
	if end {
		t = t.Add(24*time.Hour - time.Nanosecond)
	}
	return t, true
}

// RevenueQuery reads the report parameters shared by the Attribution and the
// Reports routes. It returns the requested tenant, the query, the format and
// whether the parameters are valid.
func revenueQuery(r *http.Request) (string, attribution.Query, string, bool) {
	q := r.URL.Query()
	if len(r.URL.RawQuery) > 512 {
		return "", attribution.Query{}, "", false
	}
	for name, values := range q {
		switch name {
		case "tenant_id", "from", "to", "unit", "model", "format":
		default:
			return "", attribution.Query{}, "", false
		}
		if len(values) != 1 {
			return "", attribution.Query{}, "", false
		}
	}
	from, ok1 := parsePeriodBound(q.Get("from"), false)
	to, ok2 := parsePeriodBound(q.Get("to"), true)
	unit := q.Get("unit")
	if !ok1 || !ok2 || (!from.IsZero() && !to.IsZero() && to.Before(from)) || (unit != "" && !attribution.ValidRef(unit)) {
		return "", attribution.Query{}, "", false
	}
	return q.Get("tenant_id"), attribution.Query{From: from, To: to, Unit: unit, Model: q.Get("model")}, q.Get("format"), true
}

// attributionRevenue serves GET /internal/v1/attribution/revenue (json or csv).
func (h *Handler) attributionRevenue(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if h.attribution == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "attribution_unavailable", "attribution is not enabled")
		return
	}
	extendDeadlines(w, attributionDeadline)
	requested, query, format, valid := revenueQuery(r)
	tenantID, err := h.store.ResolveTenant(r.Header.Get("X-KAS-Service"), bearerToken(r.Header.Get("Authorization")),
		requested, configstore.CapabilityAttributionRead)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return
	}
	if !valid || (format != "" && format != "json" && format != "csv") {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "query parameters are invalid")
		return
	}
	result, ok := h.computeRevenue(w, r, tenantID, query)
	if !ok {
		return
	}
	if format == "csv" {
		writeRevenueCSV(w, tenantID, result)
	} else {
		resp := attributionRevenueResponse{ContractVersion: attribution.ContractVersion, TenantID: tenantID, Unit: query.Unit, Result: result}
		if !query.From.IsZero() {
			resp.From = query.From.UTC().Format(time.RFC3339)
		}
		if !query.To.IsZero() {
			resp.To = query.To.UTC().Format(time.RFC3339Nano)
		}
		h.writeJSON(w, http.StatusOK, resp)
	}
	h.logger.Info("attribution_revenue_read", "correlation_id", CorrelationID(r.Context()), "format", format,
		"duration_ms", time.Since(started).Milliseconds())
}

// computeRevenue loads the tenant's current inputs and computes the report,
// answering the error itself when it fails.
func (h *Handler) computeRevenue(w http.ResponseWriter, r *http.Request, tenantID string, query attribution.Query) (attribution.Result, bool) {
	data, err := h.attribution.Current(r.Context(), tenantID)
	if err != nil {
		h.logger.Error("attribution_read_failed", "correlation_id", CorrelationID(r.Context()), "error", err)
		h.writeError(w, r, http.StatusServiceUnavailable, "attribution_unavailable", "attribution store is unavailable")
		return attribution.Result{}, false
	}
	result, err := attribution.Compute(data, query)
	if errors.Is(err, attribution.ErrModelUnsupported) {
		h.writeError(w, r, http.StatusBadRequest, "model_unsupported", "attribution model is not supported")
		return attribution.Result{}, false
	}
	if err != nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "attribution_unavailable", "report could not be computed")
		return attribution.Result{}, false
	}
	return result, true
}

// RevenueCSVHeader is the column order of the CSV report.
var revenueCSVHeader = []string{"nivel", "origem", "campanha", "ad_id", "recebido_centavos", "estornos_centavos",
	"liquido_centavos", "clientes", "lancamentos"}

// writeRevenueCSV writes one row per campaign line, one "sem_vinculo" row and
// one "total" row. Every text value was validated on input to start with a
// letter or digit, so no cell can start a spreadsheet formula.
func writeRevenueCSV(w http.ResponseWriter, tenantID string, res attribution.Result) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="receita-por-campanha-`+tenantID+`.csv"`)
	w.WriteHeader(http.StatusOK)
	out := csv.NewWriter(w)
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	_ = out.Write(revenueCSVHeader)
	for _, l := range res.Lines {
		_ = out.Write([]string{l.Level, l.Source, l.Campaign, l.AdID, i(l.Received), i(l.Refunds), i(l.Net),
			strconv.Itoa(l.Customers), strconv.Itoa(l.Entries)})
	}
	u := res.Unlinked
	_ = out.Write([]string{attribution.LevelNone, "", "", "", i(u.Received), i(u.Refunds), i(u.Net),
		strconv.Itoa(u.Customers), strconv.Itoa(u.Entries)})
	_ = out.Write([]string{"total", "", "", "", "", "", i(res.NetTotal), "", ""})
	out.Flush()
}
