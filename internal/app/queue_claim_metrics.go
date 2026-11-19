package app

import (
	"io"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/mlops-ca/notarx/internal/repository"
)

// queueClaimScanBuckets are the upper bounds of notarx_queue_claim_scanned_rows. A claim that nears the
// scan cap (repository.QueueClaimScanCap) means a hot repository heads the queue.
var queueClaimScanBuckets = [...]int{1, 10, 100, 1000}

// queueClaimKinds are the label values: every job kind plus "all" for claims that do not name one.
func queueClaimKinds() []string {
	kinds := []string{"all"}
	for _, kind := range repository.LifecycleJobKinds() {
		kinds = append(kinds, string(kind))
	}
	return kinds
}

type queueClaimHistogram struct {
	buckets [len(queueClaimScanBuckets) + 1]atomic.Uint64
	count   atomic.Uint64
	sum     atomic.Uint64
}

// queueClaimMetrics is usable as a zero value, like the rest of Metrics.
type queueClaimMetrics struct {
	once   sync.Once
	byKind map[string]*queueClaimHistogram
}

func (m *queueClaimMetrics) histograms() map[string]*queueClaimHistogram {
	m.once.Do(func() {
		m.byKind = map[string]*queueClaimHistogram{}
		for _, kind := range queueClaimKinds() {
			m.byKind[kind] = &queueClaimHistogram{}
		}
	})
	return m.byKind
}

// ObserveQueueClaimScannedRows records the candidate rows one lifecycle claim examined.
func (m *Metrics) ObserveQueueClaimScannedRows(kind repository.LifecycleJobKind, scanned int) {
	label := string(kind)
	if label == "" {
		label = "all"
	}
	histogram := m.queueClaims.histograms()[label]
	if histogram == nil || scanned < 0 {
		return
	}
	index := len(queueClaimScanBuckets)
	for i, bound := range queueClaimScanBuckets {
		if scanned <= bound {
			index = i
			break
		}
	}
	histogram.buckets[index].Add(1)
	histogram.count.Add(1)
	histogram.sum.Add(uint64(scanned))
}

func (m *queueClaimMetrics) write(w io.Writer) {
	_, _ = io.WriteString(w, "# TYPE notarx_queue_claim_scanned_rows histogram\n")
	for _, kind := range queueClaimKinds() {
		histogram := m.histograms()[kind]
		var cumulative uint64
		for i, bound := range queueClaimScanBuckets {
			cumulative += histogram.buckets[i].Load()
			_, _ = io.WriteString(w, "notarx_queue_claim_scanned_rows_bucket{kind=\""+kind+"\",le=\""+strconv.Itoa(bound)+"\"} "+utoa(cumulative)+"\n")
		}
		cumulative += histogram.buckets[len(queueClaimScanBuckets)].Load()
		_, _ = io.WriteString(w, "notarx_queue_claim_scanned_rows_bucket{kind=\""+kind+"\",le=\"+Inf\"} "+utoa(cumulative)+"\n")
		_, _ = io.WriteString(w, "notarx_queue_claim_scanned_rows_sum{kind=\""+kind+"\"} "+utoa(histogram.sum.Load())+"\n")
		_, _ = io.WriteString(w, "notarx_queue_claim_scanned_rows_count{kind=\""+kind+"\"} "+utoa(histogram.count.Load())+"\n")
	}
}
