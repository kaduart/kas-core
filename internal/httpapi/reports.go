package httpapi

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"time"

	"kas-core-pilot/internal/attribution"
	"kas-core-pilot/internal/configstore"
)

// KAS Reports (spec "KAS por módulos", section 8.6): the revenue report ready
// to hand to a company, as a printable HTML page or a CSV file. It reads the
// Attribution data (the module depends on attribution) and the company's own
// branding from configuration keys; no tenant has code of its own.

// Branding keys, all optional and declared as strings in key_schema:
//
//	branding.name   company name shown in the title
//	branding.color  accent color, #RRGGBB
const (
	brandingNameKey  = "branding.name"
	brandingColorKey = "branding.color"
	defaultColor     = "#1f6f5c"
)

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (h *Handler) registerReportsRoutes() {
	h.mux.HandleFunc("GET /internal/v1/reports/revenue", h.reportsRevenue)
}

type branding struct {
	Name  string
	Color string
}

func (h *Handler) brandingOf(tenantID string) branding {
	b := branding{Name: tenantID, Color: defaultColor}
	if raw, err := h.store.Evaluate(tenantID, brandingNameKey, "string"); err == nil {
		var name string
		if json.Unmarshal(raw, &name) == nil && strings.TrimSpace(name) != "" && len(name) <= 80 {
			b.Name = name
		}
	}
	if raw, err := h.store.Evaluate(tenantID, brandingColorKey, "string"); err == nil {
		var color string
		if json.Unmarshal(raw, &color) == nil && colorPattern.MatchString(color) {
			b.Color = color
		}
	}
	return b
}

// reportsRevenue serves GET /internal/v1/reports/revenue?format=html|csv.
// Same parameters as /internal/v1/attribution/revenue; capability reports:read.
func (h *Handler) reportsRevenue(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if h.attribution == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "attribution_unavailable", "attribution is not enabled")
		return
	}
	extendDeadlines(w, attributionDeadline)
	requested, query, format, valid := revenueQuery(r)
	tenantID, err := h.store.ResolveTenant(r.Header.Get("X-KAS-Service"), bearerToken(r.Header.Get("Authorization")),
		requested, configstore.CapabilityReportsRead)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return
	}
	if !valid || (format != "" && format != "html" && format != "csv") {
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
		writeRevenueHTML(w, h.brandingOf(tenantID), query, result)
	}
	h.logger.Info("report_revenue_read", "correlation_id", CorrelationID(r.Context()), "format", format,
		"duration_ms", time.Since(started).Milliseconds())
}

// brl formats integer cents as "R$ 1.234,56".
func brl(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	whole := fmt.Sprintf("%d", cents/100)
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return fmt.Sprintf("%sR$ %s,%02d", sign, b.String(), cents%100)
}

var levelLabels = map[string]string{
	attribution.LevelProven:   "Comprovada",
	attribution.LevelProbable: "Provável",
	attribution.LevelNone:     "Sem vínculo",
}

var reasonLabels = map[string]string{
	"sem_origem_capturada":                      "Sem origem capturada",
	"contato_da_campanha_depois_do_recebimento": "Contato da campanha depois do recebimento",
	"estorno_sem_recebimento_original":          "Estorno sem recebimento original",
	"lancamento_sem_cliente":                    "Lançamento sem cliente",
}

func label(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return key
}

var revenueTemplate = template.Must(template.New("revenue").Funcs(template.FuncMap{
	"brl": brl, "level": func(k string) string { return label(levelLabels, k) },
	"reason": func(k string) string { return label(reasonLabels, k) },
}).Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Receita por campanha · {{.Brand.Name}}</title>
<style>
:root{--accent:{{.Color}};--ink:#1b1f24;--muted:#5b6470;--line:#e3e6ea;--bg:#ffffff}
@media (prefers-color-scheme:dark){:root{--ink:#e8eaed;--muted:#a3abb5;--line:#30363d;--bg:#111418}}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:960px;margin:0 auto;padding:24px 16px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:16px;margin:28px 0 8px}
.sub{color:var(--muted);margin:0 0 20px}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px}
.tile{border:1px solid var(--line);border-top:3px solid var(--accent);border-radius:8px;padding:12px}
.tile b{display:block;font-size:20px;font-variant-numeric:tabular-nums}.tile span{color:var(--muted);font-size:13px}
.wrap{overflow-x:auto}table{width:100%;border-collapse:collapse;font-variant-numeric:tabular-nums}
th,td{text-align:left;padding:8px;border-bottom:1px solid var(--line);white-space:nowrap}td.n,th.n{text-align:right}
th{color:var(--muted);font-weight:600;font-size:13px}
.ok{color:var(--accent);font-weight:600}.bad{color:#b42318;font-weight:600}
footer{color:var(--muted);font-size:12px;margin-top:28px}
@media print{body{background:#fff;color:#000}.tile{break-inside:avoid}}
</style></head><body><main>
<h1>Receita por campanha · {{.Brand.Name}}</h1>
<p class="sub">{{.Period}}{{if .Unit}} · unidade {{.Unit}}{{end}} · valores em reais, recebidos menos estornos</p>
<div class="tiles">
<div class="tile"><b>{{brl .R.NetTotal}}</b><span>Receita líquida</span></div>
{{range .Levels}}<div class="tile"><b>{{brl .Cents}}</b><span>{{level .Key}}</span></div>{{end}}
</div>
<p>{{if .R.Invariant.Closes}}<span class="ok">Conferido:</span> comprovada + provável + sem vínculo = receita líquida.{{else}}<span class="bad">Atenção:</span> os níveis não somam a receita líquida.{{end}}</p>
<h2>Campanhas</h2>
<div class="wrap"><table><thead><tr><th>Nível</th><th>Origem</th><th>Campanha</th><th class="n">Recebido</th><th class="n">Estornos</th><th class="n">Líquido</th><th class="n">Clientes</th></tr></thead><tbody>
{{range .R.Lines}}<tr><td>{{level .Level}}</td><td>{{.Source}}</td><td>{{if .Campaign}}{{.Campaign}}{{else}}{{.AdID}}{{end}}</td><td class="n">{{brl .Received}}</td><td class="n">{{brl .Refunds}}</td><td class="n">{{brl .Net}}</td><td class="n">{{.Customers}}</td></tr>
{{else}}<tr><td colspan="7">Nenhuma receita ligada a campanha no período.</td></tr>{{end}}
</tbody></table></div>
<h2>Sem vínculo</h2>
<div class="wrap"><table><thead><tr><th>Motivo</th><th class="n">Líquido</th></tr></thead><tbody>
{{range .Reasons}}<tr><td>{{reason .Key}}</td><td class="n">{{brl .Cents}}</td></tr>
{{else}}<tr><td colspan="2">Nada sem vínculo no período.</td></tr>{{end}}
</tbody></table></div>
<footer>Regras: {{range $k, $v := .R.Rules}}{{$k}} {{$v}}; {{end}}gerado em {{.Generated}} (UTC).</footer>
</main></body></html>
`))

type centsEntry struct {
	Key   string
	Cents int64
}

func writeRevenueHTML(w http.ResponseWriter, brand branding, q attribution.Query, res attribution.Result) {
	period := "todo o período"
	switch {
	case !q.From.IsZero() && !q.To.IsZero():
		period = q.From.UTC().Format("02/01/2006") + " a " + q.To.UTC().Format("02/01/2006")
	case !q.From.IsZero():
		period = "desde " + q.From.UTC().Format("02/01/2006")
	case !q.To.IsZero():
		period = "até " + q.To.UTC().Format("02/01/2006")
	}
	levels := []centsEntry{}
	for _, k := range []string{attribution.LevelProven, attribution.LevelProbable, attribution.LevelNone} {
		levels = append(levels, centsEntry{k, res.ByLevel[k]})
	}
	reasons := []centsEntry{}
	for _, k := range []string{"sem_origem_capturada", "contato_da_campanha_depois_do_recebimento", "estorno_sem_recebimento_original", "lancamento_sem_cliente"} {
		if v, ok := res.Unlinked.Reasons[k]; ok && v != 0 {
			reasons = append(reasons, centsEntry{k, v})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_ = revenueTemplate.Execute(w, map[string]any{
		"Brand": brand, "Color": template.CSS(brand.Color), "Period": period, "Unit": q.Unit, "R": res,
		"Levels": levels, "Reasons": reasons, "Generated": time.Now().UTC().Format("02/01/2006 15:04"),
	})
}
