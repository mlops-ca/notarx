package repository

import (
	"context"
	"encoding/json"
)

// LifecycleJobFormat is the format a job's payload names, the key multi-format claims split by; it is empty
// when the payload names none.
func LifecycleJobFormat(job LifecycleJob) Format {
	format, _ := lifecyclePayloadFormat(job.Payload)
	return format
}

func lifecyclePayloadFormat(payload []byte) (Format, bool) {
	var value struct {
		Format Format `json:"format"`
	}
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", false
	}
	return value.Format, true
}

// formatClaimPhase names the step of a multi-format claim one batch serves.
type formatClaimPhase int

const (
	// formatClaimOpening is the first scan over every format, about half of the claim.
	formatClaimOpening formatClaimPhase = iota
	// formatClaimShare is the equal share of one format the opening scan did not reach.
	formatClaimShare
	// formatClaimRemainder is what is left of the claim, open to every format again.
	formatClaimRemainder
)

// formatClaimBatch claims up to want more items of formats for phase; a batch may also bound its own scan by
// phase, as the lifecycle claim does with claimPhaseBudget. It returns the format of each item it
// claimed, and empty when the queue held fewer candidates than it asked for.
type formatClaimBatch func(ctx context.Context, phase formatClaimPhase, formats []Format, want int) (claimed []Format, empty bool, err error)

// claimFormatShares is the claim of the per-process claimer for several formats of one queue (ADR 0023
// section 7), shared by the lifecycle job and replication plan claims. One scan over every format takes about
// half of the claim; an empty queue ends there, so an idle queue costs one query however many formats a node
// serves, and so does a queue the scan drained (it held fewer candidates than it asked for). When more work
// exists, each format the scan did not reach then gets an equal share, in rotating order,
// and only what is left is open to every format again. A deep, older backlog of one format therefore cannot
// starve the others the way one oldest-first scan would.
func claimFormatShares(ctx context.Context, formats []Format, limit int, rotation uint64, batch formatClaimBatch) error {
	opening, empty, err := batch(ctx, formatClaimOpening, formats, max((limit+1)/2, 1))
	if err != nil || empty {
		return err
	}
	total := len(opening)
	served := make(map[Format]bool, len(formats))
	for _, format := range opening {
		served[format] = true
	}
	share := max(limit/len(formats), 1)
	start := int(rotation % uint64(len(formats)))
	for i := 0; i < len(formats) && total < limit; i++ {
		format := formats[(start+i)%len(formats)]
		if served[format] {
			continue
		}
		claimed, _, err := batch(ctx, formatClaimShare, []Format{format}, min(share, limit-total))
		if err != nil {
			return err
		}
		total += len(claimed)
	}
	if total < limit {
		_, _, err = batch(ctx, formatClaimRemainder, formats, limit-total)
	}
	return err
}

// formatList is the format filter of a single-format claim: an empty format claims every format, as the
// single-format claims did before format lists (their SQL read an empty format as no filter).
func formatList(format Format) []Format {
	if format == "" {
		return nil
	}
	return []Format{format}
}
