package attribution

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// Rule versions. They match the CRM's revenueAttribution.js (PR #99) so the
// same sample gives the same numbers in both (conciliation, spec 8.6).
const (
	Model             = "primeiro-contato-por-recebimento-v1"
	TouchRuleVersion  = "touch-v2-precedencia"
	RefundRuleVersion = "estorno-v3-saldo-por-recebimento"
)

var ErrModelUnsupported = errors.New("model_unsupported")

// Levels of evidence.
const (
	LevelProven   = "comprovada"
	LevelProbable = "provavel"
	LevelNone     = "sem_vinculo"
)

// Dataset is the current (latest, not voided) state of a tenant's inputs.
type Dataset struct {
	Customers []Record
	Contacts  []Record
	Receipts  []Record
	Refunds   []Record
}

// Query selects the report period (by cash date) and optional unit.
type Query struct {
	From, To time.Time // zero = open
	Unit     string
	Model    string
}

// Line is one campaign line of a level.
type Line struct {
	Level       string         `json:"nivel"`
	Source      string         `json:"origem"`
	Campaign    string         `json:"campanha,omitempty"`
	AdID        string         `json:"ad_id,omitempty"`
	Received    int64          `json:"recebido_centavos"`
	Refunds     int64          `json:"estornos_centavos"`
	Net         int64          `json:"liquido_centavos"`
	Customers   int            `json:"clientes"`
	Entries     int            `json:"lancamentos"`
	ReasonCount map[string]int `json:"motivos_por_lancamento"`
	customers   map[string]struct{}
}

// Unlinked is the "sem vínculo" bucket, with net cents by reason.
type Unlinked struct {
	Received  int64            `json:"recebido_centavos"`
	Refunds   int64            `json:"estornos_centavos"`
	Net       int64            `json:"liquido_centavos"`
	Customers int              `json:"clientes"`
	Entries   int              `json:"lancamentos"`
	Reasons   map[string]int64 `json:"motivos_centavos"`
	customers map[string]struct{}
}

// Result is the report. Values are integer cents, currency BRL.
type Result struct {
	Currency  string           `json:"moeda"`
	NetTotal  int64            `json:"total_liquido_centavos"`
	ByLevel   map[string]int64 `json:"por_nivel_centavos"`
	Lines     []Line           `json:"linhas"`
	Unlinked  Unlinked         `json:"sem_vinculo"`
	Invariant struct {
		SumLevels int64 `json:"soma_niveis_centavos"`
		NetTotal  int64 `json:"total_liquido_centavos"`
		Closes    bool  `json:"fecha"`
	} `json:"invariante"`
	Rules map[string]string `json:"regras"`
}

// SameTouch: same advertising contact? Precedence from strongest to weakest
// identifier: click id (gclid, ttclid, ctwa_clid, fbclid) → ad → campaign. The
// first level where both sides have a value decides; a different source is
// never the same contact. Same rule as the CRM (touch-v2-precedencia).
func SameTouch(a, b Record) bool {
	if a.Source == "" || a.Source != b.Source {
		return false
	}
	clicks := [][2]string{{a.Gclid, b.Gclid}, {a.Ttclid, b.Ttclid}, {a.CtwaClid, b.CtwaClid}, {a.Fbclid, b.Fbclid}}
	present := 0
	for _, p := range clicks {
		if p[0] != "" && p[1] != "" {
			present++
			if p[0] != p[1] {
				return false
			}
		}
	}
	if present > 0 {
		return true
	}
	for _, p := range [][2]string{{a.AdID, b.AdID}, {a.Campaign, b.Campaign}} {
		if p[0] != "" && p[1] != "" {
			return p[0] == p[1]
		}
	}
	return false
}

func strong(c Record) bool {
	return c.Gclid != "" || c.Fbclid != "" || c.Ttclid != "" || c.CtwaClid != "" || contains(StrongMethods, c.Method)
}

type candidate struct {
	contact   Record
	contactAt time.Time
	strong    bool
}

type origin struct {
	level, reason, source, campaign, adID string
}

func (o origin) key() string {
	k := o.campaign
	if k == "" {
		k = o.adID
	}
	return o.level + "|" + o.source + "|" + k + "|" + o.reason
}

// Compute builds the report. Pure: no I/O.
func Compute(d Dataset, q Query) (Result, error) {
	if q.Model != "" && q.Model != Model {
		return Result{}, ErrModelUnsupported
	}
	// 1. Contact points and who shares them.
	keyOf := map[string]string{}
	owners := map[string]map[string]struct{}{}
	for _, c := range d.Customers {
		if c.ContactKey == "" {
			continue
		}
		keyOf[c.ExternalID] = c.ContactKey
		if owners[c.ContactKey] == nil {
			owners[c.ContactKey] = map[string]struct{}{}
		}
		owners[c.ContactKey][c.ExternalID] = struct{}{}
	}
	household := func(customer string) []string {
		if k := keyOf[customer]; k != "" {
			out := make([]string, 0, len(owners[k]))
			for o := range owners[k] {
				out = append(out, o)
			}
			sort.Strings(out)
			return out
		}
		return []string{customer}
	}
	contactsOf := map[string][]Record{}
	for _, c := range d.Contacts {
		contactsOf[c.CustomerRef] = append(contactsOf[c.CustomerRef], c)
	}
	for k := range contactsOf {
		list := contactsOf[k]
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].At.Equal(list[j].At) {
				return list[i].At.Before(list[j].At)
			}
			return list[i].ExternalID < list[j].ExternalID
		})
	}

	// 2. Candidates of a customer, in priority order: its acquisition contact
	// (date anticipated only by an earlier contact of the SAME touch), then every
	// contact of its household by date.
	cache := map[string][]candidate{}
	candidatesOf := func(customer string) []candidate {
		if c, ok := cache[customer]; ok {
			return c
		}
		var pool []Record
		for _, member := range household(customer) {
			pool = append(pool, contactsOf[member]...)
		}
		sort.SliceStable(pool, func(i, j int) bool { return pool[i].At.Before(pool[j].At) })
		var list []candidate
		var acq *Record
		for i := range contactsOf[customer] {
			c := contactsOf[customer][i]
			if c.Acquisition && (acq == nil || c.At.Before(acq.At)) {
				cc := c
				acq = &cc
			}
		}
		if acq != nil {
			at := acq.At
			for _, c := range pool {
				if c.ExternalID != acq.ExternalID && SameTouch(*acq, c) && c.At.Before(at) {
					at = c.At
				}
			}
			list = append(list, candidate{contact: *acq, contactAt: at, strong: strong(*acq)})
		}
		for _, c := range pool {
			if acq != nil && c.ExternalID == acq.ExternalID && c.CustomerRef == acq.CustomerRef {
				continue
			}
			list = append(list, candidate{contact: c, contactAt: c.At, strong: strong(c)})
		}
		cache[customer] = list
		return list
	}
	shared := func(customer string) bool {
		k := keyOf[customer]
		return k != "" && len(owners[k]) > 1
	}
	resolve := func(customer string, at time.Time) origin {
		if customer == "" {
			return origin{level: LevelNone, reason: "lancamento_sem_cliente"}
		}
		list := candidatesOf(customer)
		for _, c := range list {
			if !c.contactAt.After(at) {
				o := origin{source: c.contact.Source, campaign: c.contact.Campaign, adID: c.contact.AdID}
				switch {
				case c.strong && !shared(customer):
					o.level, o.reason = LevelProven, "sinal_forte_contato_exclusivo"
				case c.strong:
					o.level, o.reason = LevelProbable, "sinal_forte_em_contato_compartilhado"
				default:
					o.level, o.reason = LevelProbable, "so_sinal_fraco"
				}
				return o
			}
		}
		if len(list) > 0 {
			return origin{level: LevelNone, reason: "contato_da_campanha_depois_do_recebimento"}
		}
		return origin{level: LevelNone, reason: "sem_origem_capturada"}
	}

	// 3. Receipts by id and by payment (for refunds).
	receiptByID := map[string]Record{}
	byPayment := map[string][]Record{}
	for _, r := range d.Receipts {
		receiptByID[r.ExternalID] = r
		if r.PaymentRef != "" {
			byPayment[r.PaymentRef] = append(byPayment[r.PaymentRef], r)
		}
	}
	for k := range byPayment {
		list := byPayment[k]
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].At.Equal(list[j].At) {
				return list[i].At.Before(list[j].At)
			}
			return list[i].ExternalID < list[j].ExternalID
		})
	}

	inPeriod := func(r Record) bool {
		if !q.From.IsZero() && r.At.Before(q.From) {
			return false
		}
		if !q.To.IsZero() && r.At.After(q.To) {
			return false
		}
		return q.Unit == "" || r.Unit == q.Unit
	}

	type part struct {
		customer string
		o        origin
		cents    int64
	}
	// Refunds consume the balance of the receipts they undo (estorno-v3-saldo-
	// por-recebimento). Every refund of the history is applied in date order,
	// including those before the period or outside the unit, because an earlier
	// refund lowers what is still refundable on a receipt:
	//   - with receipt_ref: up to that receipt's balance;
	//   - with only payment_ref: split among that payment's receipts received up
	//     to the refund date that still have balance, by balance, in cents, the
	//     remainder on the oldest receipt with room;
	//   - what exceeds the balance, or a refund with no receipt up to its date,
	//     is kept in the total as "sem vínculo" with the reason.
	balance := map[string]int64{}
	for _, r := range d.Receipts {
		balance[r.ExternalID] = r.AmountCents
	}
	none := func(customer, reason string, cents int64) part {
		return part{customer: customer, o: origin{level: LevelNone, reason: reason}, cents: cents}
	}
	refundParts := func(f Record) []part {
		amount := f.AmountCents
		var eligible []Record
		explicit := false
		if f.ReceiptRef != "" {
			if r, ok := receiptByID[f.ReceiptRef]; ok {
				eligible, explicit = []Record{r}, true
			}
		}
		if !explicit && f.PaymentRef != "" {
			all := byPayment[f.PaymentRef]
			for _, r := range all {
				if !r.At.After(f.At) {
					eligible = append(eligible, r)
				}
			}
			if len(eligible) == 0 {
				if len(all) > 0 {
					return []part{none(f.CustomerRef, "estorno_antes_do_recebimento", -amount)}
				}
				return []part{none(f.CustomerRef, "estorno_sem_recebimento_original", -amount)}
			}
		}
		if len(eligible) == 0 {
			return []part{none(f.CustomerRef, "estorno_sem_recebimento_original", -amount)}
		}
		var room int64
		weights := make([]int64, len(eligible))
		for i, r := range eligible {
			if b := balance[r.ExternalID]; b > 0 {
				weights[i] = b
				room += b
			}
		}
		take := amount
		if take > room {
			take = room
		}
		alloc := make([]int64, len(eligible))
		var assigned int64
		if take > 0 {
			for i := range eligible {
				alloc[i] = take * weights[i] / room
				assigned += alloc[i]
			}
			for rest := take - assigned; rest > 0; {
				for i := range eligible {
					if rest > 0 && alloc[i] < weights[i] {
						alloc[i]++
						rest--
					}
				}
			}
		}
		var out []part
		idx := map[string]int{}
		for i, r := range eligible {
			if alloc[i] == 0 {
				continue
			}
			balance[r.ExternalID] -= alloc[i]
			o := resolve(r.CustomerRef, r.At)
			if j, ok := idx[o.key()]; ok {
				out[j].cents -= alloc[i]
				continue
			}
			idx[o.key()] = len(out)
			out = append(out, part{customer: r.CustomerRef, o: o, cents: -alloc[i]})
		}
		if over := amount - take; over > 0 {
			out = append(out, none(f.CustomerRef, "estorno_acima_do_saldo", -over))
		}
		return out
	}

	// 4. Sum.
	res := Result{Currency: "BRL", ByLevel: map[string]int64{LevelProven: 0, LevelProbable: 0, LevelNone: 0},
		Unlinked: Unlinked{Reasons: map[string]int64{}, customers: map[string]struct{}{}},
		Rules:    map[string]string{"modelo": Model, "contato": TouchRuleVersion, "estorno": RefundRuleVersion}}
	lines := map[string]*Line{}
	add := func(p part) {
		res.ByLevel[p.o.level] += p.cents
		credit, debit := int64(0), int64(0)
		if p.cents > 0 {
			credit = p.cents
		} else {
			debit = -p.cents
		}
		if p.o.level == LevelNone {
			u := &res.Unlinked
			u.Received += credit
			u.Refunds += debit
			u.Net += p.cents
			u.Entries++
			if p.customer != "" {
				u.customers[p.customer] = struct{}{}
			}
			u.Reasons[p.o.reason] += p.cents
			return
		}
		ck := p.o.campaign
		if ck == "" {
			ck = p.o.adID
		}
		k := p.o.level + "|" + p.o.source + "|" + ck
		l := lines[k]
		if l == nil {
			l = &Line{Level: p.o.level, Source: p.o.source, Campaign: p.o.campaign, AdID: p.o.adID,
				ReasonCount: map[string]int{}, customers: map[string]struct{}{}}
			lines[k] = l
		}
		l.Received += credit
		l.Refunds += debit
		l.Net += p.cents
		l.Entries++
		if p.customer != "" {
			l.customers[p.customer] = struct{}{}
		}
		l.ReasonCount[p.o.reason]++
	}
	for _, r := range d.Receipts {
		if !inPeriod(r) {
			continue
		}
		res.NetTotal += r.AmountCents
		add(part{customer: r.CustomerRef, o: resolve(r.CustomerRef, r.At), cents: r.AmountCents})
	}
	refunds := append([]Record(nil), d.Refunds...)
	sort.SliceStable(refunds, func(i, j int) bool {
		if !refunds[i].At.Equal(refunds[j].At) {
			return refunds[i].At.Before(refunds[j].At)
		}
		return refunds[i].ExternalID < refunds[j].ExternalID
	})
	for _, f := range refunds {
		if f.Unit == "" && f.ReceiptRef != "" {
			// A refund without its own unit belongs to the unit of its receipt.
			f.Unit = receiptByID[f.ReceiptRef].Unit
		}
		if f.Unit == "" && f.PaymentRef != "" && len(byPayment[f.PaymentRef]) > 0 {
			f.Unit = byPayment[f.PaymentRef][0].Unit
		}
		parts := refundParts(f) // always: it consumes balance even outside the period
		if !inPeriod(f) {
			continue
		}
		res.NetTotal -= f.AmountCents
		for _, p := range parts {
			add(p)
		}
	}

	rank := map[string]int{LevelProven: 0, LevelProbable: 1}
	for _, l := range lines {
		l.Customers = len(l.customers)
		res.Lines = append(res.Lines, *l)
	}
	sort.Slice(res.Lines, func(i, j int) bool {
		a, b := res.Lines[i], res.Lines[j]
		if rank[a.Level] != rank[b.Level] {
			return rank[a.Level] < rank[b.Level]
		}
		if a.Net != b.Net {
			return a.Net > b.Net
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return strings.Compare(a.Campaign+a.AdID, b.Campaign+b.AdID) < 0
	})
	if res.Lines == nil {
		res.Lines = []Line{}
	}
	res.Unlinked.Customers = len(res.Unlinked.customers)
	sum := res.ByLevel[LevelProven] + res.ByLevel[LevelProbable] + res.ByLevel[LevelNone]
	res.Invariant.SumLevels, res.Invariant.NetTotal, res.Invariant.Closes = sum, res.NetTotal, sum == res.NetTotal
	return res, nil
}
