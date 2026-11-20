package main

import (
	"context"

	"github.com/mlops-ca/notarx/internal/lifecycle"
	"github.com/mlops-ca/notarx/internal/repository"
)

// brokeredStore is the store the worker runtimes of a process use (ADR 0023 section 7): every method is the
// PostgreSQL store's except the queue claims, which the process's one claimer per queue answers, and the
// wake-ups, which tell that claimer a queue changed.
type brokeredStore struct {
	*repository.PostgresStore
	broker *lifecycle.ClaimBroker
}

func newBrokeredStore(store *repository.PostgresStore) *brokeredStore {
	return &brokeredStore{PostgresStore: store, broker: lifecycle.NewClaimBroker(store)}
}

func (s *brokeredStore) ClaimLifecycleJobsByKindAndFormat(ctx context.Context, kind repository.LifecycleJobKind, format repository.Format, limit int) ([]repository.LifecycleJob, error) {
	return s.broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, format, limit)
}

func (s *brokeredStore) ClaimReplicationPlansByFormat(ctx context.Context, format repository.Format, limit int) ([]repository.ReplicationPlan, error) {
	return s.broker.ClaimReplicationPlansByFormat(ctx, format, limit)
}

func (s *brokeredStore) Listen(ctx context.Context, channel string) <-chan struct{} {
	return s.broker.Relay(ctx, s.PostgresStore, channel)
}
