package attribution

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// CSV columns accepted per kind (spec 8.4). The header must use these names;
// order is free, unknown or repeated columns are refused, and external_id plus
// the required columns of the kind must be present.
var csvColumns = map[string][]string{
	KindCustomer: {"external_id", "phone", "email", "unit", "voided"},
	KindContact: {"external_id", "customer_ref", "source", "contacted_at", "campaign", "ad_id", "gclid", "fbclid",
		"ttclid", "ctwa_clid", "method", "acquisition", "voided"},
	KindReceipt: {"external_id", "customer_ref", "amount_cents", "received_at", "payment_ref", "unit", "voided"},
	KindRefund:  {"external_id", "amount_cents", "refunded_at", "receipt_ref", "payment_ref", "customer_ref", "unit", "voided"},
}

var csvRequired = map[string][]string{
	KindCustomer: {"external_id"},
	KindContact:  {"external_id", "customer_ref", "source", "contacted_at"},
	KindReceipt:  {"external_id", "customer_ref", "amount_cents", "received_at"},
	KindRefund:   {"external_id", "amount_cents", "refunded_at"},
}

// MaxCSVRows bounds one file (the importer sends it in batches of 500).
const MaxCSVRows = 100_000

// ParseCSV reads a UTF-8, comma-separated file of one kind into items. Values
// are not validated here beyond their type: the API validates every item.
func ParseCSV(kind string, r io.Reader) ([]Item, error) {
	allowed, ok := csvColumns[kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = 0
	header, err := reader.Read()
	if err != nil {
		return nil, errors.New("CSV header is missing")
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\uFEFF")
	}
	index := map[string]int{}
	for i, name := range header {
		name = strings.TrimSpace(name)
		if !contains(allowed, name) {
			return nil, fmt.Errorf("column %q is not accepted for %s", name, kind)
		}
		if _, dup := index[name]; dup {
			return nil, fmt.Errorf("column %q is repeated", name)
		}
		index[name] = i
	}
	for _, need := range csvRequired[kind] {
		if _, ok := index[need]; !ok {
			return nil, fmt.Errorf("column %q is required for %s", need, kind)
		}
	}
	var items []Item
	for line := 2; ; line++ {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(items) >= MaxCSVRows {
			return nil, fmt.Errorf("file exceeds %d rows", MaxCSVRows)
		}
		get := func(name string) string {
			if i, ok := index[name]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		boolOf := func(name string) (bool, error) {
			switch strings.ToLower(get(name)) {
			case "", "false", "0", "nao", "não":
				return false, nil
			case "true", "1", "sim":
				return true, nil
			}
			return false, fmt.Errorf("line %d: %s must be true or false", line, name)
		}
		it := Item{
			ExternalID: get("external_id"), CustomerRef: get("customer_ref"), Phone: get("phone"), Email: get("email"),
			Unit: get("unit"), Source: get("source"), Campaign: get("campaign"), AdID: get("ad_id"), Gclid: get("gclid"),
			Fbclid: get("fbclid"), Ttclid: get("ttclid"), CtwaClid: get("ctwa_clid"), Method: get("method"),
			ContactedAt: get("contacted_at"), PaymentRef: get("payment_ref"), ReceivedAt: get("received_at"),
			ReceiptRef: get("receipt_ref"), RefundedAt: get("refunded_at"),
		}
		if raw := get("amount_cents"); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("line %d: amount_cents must be an integer number of cents", line)
			}
			it.AmountCents = v
		}
		if it.Acquisition, err = boolOf("acquisition"); err != nil {
			return nil, err
		}
		if it.Voided, err = boolOf("voided"); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}
