// Package chainledger applies the shared raw name-encoding rule (see
// name_encoding.go) to an adjustment plan.
//
// Every name that takes part in the lineage adjustment — the name of each
// change record, every upstreams entry, and every removals entry — is
// checked against its RAW string literal before the plan is accepted, even
// when a removals name would only have matched a dataset that does not
// exist. A literal carrying bytes that are not valid UTF-8, or a \u escape
// forming an unpaired surrogate (a lone high or low surrogate, or a low
// surrogate written before its high surrogate), rejects the whole plan.
//
// The change records reuse the same "name" + "upstreams" record checker as a
// graph's datasets, and field recognition, record reading, unknown-field
// skipping, locations, and the byte-vs-surrogate error wording are all the
// shared machinery in name_encoding.go and duplicate_fields.go, so the plan
// reader can never judge one name or record shape differently from the graph
// readers.
package chainledger

import (
	"encoding/json"
)

// checkPlanNameEncoding scans the raw plan JSON and rejects the whole plan
// when any name that participates in the adjustment — changes[i].name, any
// changes[i].upstreams[j], or any removals[i] — is written with a string
// literal the decoder cannot read faithfully (invalid UTF-8 bytes or an
// unpaired surrogate escape). The error names the field and the zero-based
// record or array position, and says which of the two corruptions occurred.
func checkPlanNameEncoding(data []byte) error {
	return runFieldScan(data, func(dec *json.Decoder) error {
		return scanObjectFields(dec, []knownField{
			{name: "changes", nested: func(dec *json.Decoder) error {
				return scanNameUpstreamsRecords(dec, "changes", "change record")
			}},
			{name: "removals", nested: scanRemovalsNames},
		})
	})
}
