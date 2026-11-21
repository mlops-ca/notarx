package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

const webhookSubscriptionColumns = `id::text,name,endpoint_url,secret_ciphertext,event_types,enabled,version::text,created_at,updated_at`
const webhookDeliveryColumns = `d.id::text,d.event_id::text,e.type,d.subscription_id::text,d.state,d.attempts,d.next_attempt_at,d.lease_owner,d.lease_token::text,d.lease_expires_at,d.last_status,d.last_error,d.created_at,d.updated_at,d.delivered_at`

func encodeWebhookEventTypes(values []WebhookEventType) ([]byte, error) {
	if values == nil {
		values = []WebhookEventType{}
	}
	return json.Marshal(values)
}

func scanWebhookSubscription(scanner interface{ Scan(...any) error }) (WebhookSubscription, error) {
	var value WebhookSubscription
	var eventTypes []byte
	if err := scanner.Scan(&value.ID, &value.Name, &value.EndpointURL, &value.SecretCiphertext, &eventTypes, &value.Enabled, &value.Version, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return WebhookSubscription{}, err
	}
	if err := json.Unmarshal(eventTypes, &value.EventTypes); err != nil {
		return WebhookSubscription{}, err
	}
	return value, nil
}

func (s *PostgresStore) CreateWebhookSubscription(ctx context.Context, value WebhookSubscription, audit ...AuditRecord) (WebhookSubscription, error) {
	eventTypes, err := encodeWebhookEventTypes(value.EventTypes)
	if err != nil {
		return WebhookSubscription{}, err
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (WebhookSubscription, error) {
		created, err := scanWebhookSubscription(q.QueryRowContext(ctx, `INSERT INTO webhook_subscriptions (id,name,endpoint_url,secret_ciphertext,event_types,enabled)
VALUES ($1,$2,$3,$4,$5::jsonb,$6) RETURNING `+webhookSubscriptionColumns, value.ID, value.Name, value.EndpointURL, value.SecretCiphertext, eventTypes, value.Enabled))
		if isUnique(err) {
			return WebhookSubscription{}, ErrWebhookSubscriptionNameExists
		}
		return created, err
	})
}

const webhookSubscriptionsFilter = "webhook-subscriptions"

// PageWebhookSubscriptions pages subscriptions by name in byte order with a keyset on the unique name,
// served by webhook_subscriptions_name_bytes_idx (migration 000005) whatever the database collation.
func (s *PostgresStore) PageWebhookSubscriptions(ctx context.Context, query WebhookSubscriptionQuery) (WebhookSubscriptionPage, error) {
	limit := pageLimit(query.Limit, 500, 500)
	after, present, err := decodeNameToken(query.PageToken, webhookSubscriptionsFilter)
	if err != nil {
		return WebhookSubscriptionPage{}, err
	}
	statement, args := `SELECT `+webhookSubscriptionColumns+` FROM webhook_subscriptions ORDER BY name COLLATE "C" LIMIT $1`, []any{limit + 1}
	if present {
		statement = `SELECT ` + webhookSubscriptionColumns + ` FROM webhook_subscriptions WHERE name COLLATE "C">$1 ORDER BY name COLLATE "C" LIMIT $2`
		args = []any{after, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return WebhookSubscriptionPage{}, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]WebhookSubscription, 0, limit+1)
	for rows.Next() {
		value, scanErr := scanWebhookSubscription(rows)
		if scanErr != nil {
			return WebhookSubscriptionPage{}, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return WebhookSubscriptionPage{}, err
	}
	page := WebhookSubscriptionPage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		page.NextPageToken = EncodeKeysetToken(webhookSubscriptionsFilter, page.Items[limit-1].Name)
	}
	return page, nil
}

func (s *PostgresStore) GetWebhookSubscription(ctx context.Context, id string) (WebhookSubscription, error) {
	value, err := scanWebhookSubscription(s.db.QueryRowContext(ctx, `SELECT `+webhookSubscriptionColumns+` FROM webhook_subscriptions WHERE id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookSubscription{}, ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) UpdateWebhookSubscription(ctx context.Context, value WebhookSubscription, expectedVersion string, audit ...AuditRecord) (WebhookSubscription, error) {
	eventTypes, err := encodeWebhookEventTypes(value.EventTypes)
	if err != nil {
		return WebhookSubscription{}, err
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (WebhookSubscription, error) {
		return updateWebhookSubscription(ctx, q, value, expectedVersion, eventTypes)
	})
}

func updateWebhookSubscription(ctx context.Context, q sqlRunner, value WebhookSubscription, expectedVersion string, eventTypes []byte) (WebhookSubscription, error) {
	updated, err := scanWebhookSubscription(q.QueryRowContext(ctx, `UPDATE webhook_subscriptions
SET name=$2,endpoint_url=$3,secret_ciphertext=$4,event_types=$5::jsonb,enabled=$6,version=version+1,updated_at=now()
WHERE id=$1::uuid AND version=$7::text::bigint RETURNING `+webhookSubscriptionColumns,
		value.ID, value.Name, value.EndpointURL, value.SecretCiphertext, eventTypes, value.Enabled, expectedVersion))
	if isUnique(err) {
		return WebhookSubscription{}, ErrWebhookSubscriptionNameExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return updated, err
	}
	var exists bool
	if queryErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM webhook_subscriptions WHERE id=$1::uuid)`, value.ID).Scan(&exists); queryErr != nil {
		return WebhookSubscription{}, queryErr
	}
	if exists {
		return WebhookSubscription{}, ErrVersionConflict
	}
	return WebhookSubscription{}, ErrNotFound
}

func (s *PostgresStore) EnqueueWebhookEvent(ctx context.Context, event WebhookEvent) (WebhookEvent, error) {
	if event.ID == "" {
		event.ID = ids.New()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WebhookEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = enqueueWebhookEventPostgres(ctx, tx, event); err != nil {
		return WebhookEvent{}, err
	}
	if err = tx.Commit(); err != nil {
		return WebhookEvent{}, err
	}
	return event, nil
}

func scanWebhookDelivery(scanner interface{ Scan(...any) error }) (WebhookDelivery, error) {
	var value WebhookDelivery
	var nextAttemptAt, leaseExpiresAt, deliveredAt sql.NullTime
	var leaseOwner, leaseToken sql.NullString
	var lastStatus sql.NullInt64
	if err := scanner.Scan(&value.ID, &value.EventID, &value.EventType, &value.SubscriptionID, &value.State, &value.Attempts, &nextAttemptAt, &leaseOwner, &leaseToken, &leaseExpiresAt, &lastStatus, &value.LastError, &value.CreatedAt, &value.UpdatedAt, &deliveredAt); err != nil {
		return WebhookDelivery{}, err
	}
	if nextAttemptAt.Valid {
		value.NextAttemptAt = nextAttemptAt.Time
	}
	if leaseOwner.Valid {
		value.LeaseOwner = leaseOwner.String
	}
	if leaseToken.Valid {
		value.LeaseToken = leaseToken.String
	}
	if leaseExpiresAt.Valid {
		value.LeaseExpiresAt = leaseExpiresAt.Time
	}
	if lastStatus.Valid {
		value.LastStatus = int(lastStatus.Int64)
	}
	if deliveredAt.Valid {
		value.DeliveredAt = deliveredAt.Time
	}
	return value, nil
}

func webhookDeliveriesFilter(query WebhookDeliveryQuery) string {
	return "webhook-deliveries\x00" + query.SubscriptionID + "\x00" + string(query.State)
}

// PageWebhookDeliveries pages deliveries newest first by keyset on (created_at, id).
func (s *PostgresStore) PageWebhookDeliveries(ctx context.Context, query WebhookDeliveryQuery) (WebhookDeliveryPage, error) {
	limit := pageLimit(query.Limit, 100, 500)
	cursorAt, cursorID, after, err := decodeCreatedAtToken(query.PageToken, webhookDeliveriesFilter(query))
	if err != nil {
		return WebhookDeliveryPage{}, err
	}
	const base = `SELECT ` + webhookDeliveryColumns + `,s.name FROM webhook_deliveries d JOIN domain_events e ON e.id=d.event_id AND e.occurred_at=d.event_occurred_at JOIN webhook_subscriptions s ON s.id=d.subscription_id
WHERE ($1='' OR d.subscription_id=$1::uuid) AND ($2='' OR d.state=$2)`
	statement, args := base+` ORDER BY d.created_at DESC,d.id DESC LIMIT $3`, []any{query.SubscriptionID, query.State, limit + 1}
	if after {
		statement = base + ` AND (d.created_at,d.id)<($3::timestamptz,$4::uuid) ORDER BY d.created_at DESC,d.id DESC LIMIT $5`
		args = []any{query.SubscriptionID, query.State, cursorAt, cursorID, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return WebhookDeliveryPage{}, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]WebhookDelivery, 0, limit+1)
	for rows.Next() {
		var name string
		value, scanErr := scanWebhookDelivery(extraColumns{rows, []any{&name}})
		value.SubscriptionName = name
		if scanErr != nil {
			return WebhookDeliveryPage{}, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return WebhookDeliveryPage{}, err
	}
	page := WebhookDeliveryPage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		last := page.Items[limit-1]
		page.NextPageToken = encodeCreatedAtToken(webhookDeliveriesFilter(query), last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *PostgresStore) GetWebhookDelivery(ctx context.Context, id string) (WebhookDelivery, error) {
	value, err := scanWebhookDelivery(s.db.QueryRowContext(ctx, getWebhookDeliveryQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookDelivery{}, ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) ClaimWebhookDeliveries(ctx context.Context, owner string, _ time.Time, lease time.Duration, limit int) ([]WebhookDeliveryClaim, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, claimWebhookDeliveriesQuery, owner, lease.String(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	claims := make([]WebhookDeliveryClaim, 0, limit)
	for rows.Next() {
		var delivery WebhookDelivery
		var event WebhookEvent
		var subscription WebhookSubscription
		var eventData, eventTypes []byte
		var nextAttemptAt, leaseExpiresAt, deliveredAt sql.NullTime
		var leaseOwner sql.NullString
		var lastStatus sql.NullInt64
		if err = rows.Scan(
			&delivery.ID, &delivery.EventID, &delivery.EventType, &delivery.SubscriptionID, &delivery.State, &delivery.Attempts, &nextAttemptAt, &leaseOwner, &delivery.LeaseToken, &leaseExpiresAt, &lastStatus, &delivery.LastError, &delivery.CreatedAt, &delivery.UpdatedAt, &deliveredAt,
			&event.ID, &event.Type, &event.RepositoryID, &event.Format, &event.Coordinate, &event.Digest, &event.OccurredAt, &eventData,
			&subscription.ID, &subscription.Name, &subscription.EndpointURL, &subscription.SecretCiphertext, &eventTypes, &subscription.Enabled, &subscription.Version, &subscription.CreatedAt, &subscription.UpdatedAt,
		); err != nil {
			return nil, err
		}
		delivery.LeaseOwner, delivery.LeaseExpiresAt = leaseOwner.String, leaseExpiresAt.Time
		if lastStatus.Valid {
			delivery.LastStatus = int(lastStatus.Int64)
		}
		if deliveredAt.Valid {
			delivery.DeliveredAt = deliveredAt.Time
		}
		event.Data = append([]byte(nil), eventData...)
		if err = json.Unmarshal(eventTypes, &subscription.EventTypes); err != nil {
			return nil, err
		}
		claims = append(claims, WebhookDeliveryClaim{Delivery: delivery, Event: event, Subscription: subscription})
	}
	return claims, rows.Err()
}

func (s *PostgresStore) CompleteWebhookDelivery(ctx context.Context, id, leaseToken string, status int, _ time.Time) error {
	result, err := s.db.ExecContext(ctx, `WITH clock AS MATERIALIZED (SELECT clock_timestamp() AS now)
UPDATE webhook_deliveries SET state='succeeded',next_attempt_at=NULL,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,last_status=$3,last_error='',delivered_at=clock.now,updated_at=clock.now
FROM clock WHERE id=$1::uuid AND state='delivering' AND lease_token=$2::uuid AND lease_expires_at>clock.now`, id, leaseToken, status)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrVersionConflict
	}
	return nil
}

func (s *PostgresStore) FailWebhookDelivery(ctx context.Context, id, leaseToken string, failedAt, retryAt time.Time, status int, message string, dead bool) error {
	retryDelay := retryAt.Sub(failedAt)
	if retryAt.IsZero() || retryDelay < 0 {
		retryDelay = 0
	}
	result, err := s.db.ExecContext(ctx, `WITH clock AS MATERIALIZED (SELECT clock_timestamp() AS now)
UPDATE webhook_deliveries SET state=CASE WHEN $3 OR attempts>=8 THEN 'dead' ELSE 'retrying' END,
    next_attempt_at=CASE WHEN $3 OR attempts>=8 THEN NULL ELSE clock.now+$4::interval END,
    lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,last_status=NULLIF($5,0),last_error=$6,updated_at=clock.now
FROM clock WHERE id=$1::uuid AND state='delivering' AND lease_token=$2::uuid AND lease_expires_at>clock.now`, id, leaseToken, dead, retryDelay.String(), status, message)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrVersionConflict
	}
	return nil
}

func (s *PostgresStore) ReplayWebhookDelivery(ctx context.Context, id string, _ time.Time, audit ...AuditRecord) (WebhookDelivery, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (WebhookDelivery, error) {
		return replayWebhookDelivery(ctx, q, id)
	})
}

func replayWebhookDelivery(ctx context.Context, q sqlRunner, id string) (WebhookDelivery, error) {
	value, err := scanWebhookDelivery(q.QueryRowContext(ctx, `WITH clock AS MATERIALIZED (SELECT clock_timestamp() AS now), replayed AS (
UPDATE webhook_deliveries SET state='pending',attempts=0,next_attempt_at=clock.now,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,last_status=NULL,last_error='',delivered_at=NULL,updated_at=clock.now
FROM clock WHERE id=$1::uuid AND state='dead' RETURNING webhook_deliveries.*)
SELECT d.id::text,d.event_id::text,e.type,d.subscription_id::text,d.state,d.attempts,d.next_attempt_at,d.lease_owner,d.lease_token::text,d.lease_expires_at,d.last_status,d.last_error,d.created_at,d.updated_at,d.delivered_at
FROM replayed d JOIN domain_events e ON e.id=d.event_id AND e.occurred_at=d.event_occurred_at`, id))
	if !errors.Is(err, sql.ErrNoRows) {
		return value, err
	}
	var exists bool
	if queryErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM webhook_deliveries WHERE id=$1::uuid)`, id).Scan(&exists); queryErr != nil {
		return WebhookDelivery{}, queryErr
	}
	if exists {
		return WebhookDelivery{}, ErrInvalidWebhookDeliveryState
	}
	return WebhookDelivery{}, ErrNotFound
}

var _ WebhookStore = (*PostgresStore)(nil)
var _ WebhookStore = (*MemoryStore)(nil)

// getWebhookDeliveryQuery loads one delivery with its event.
const getWebhookDeliveryQuery = `SELECT ` + webhookDeliveryColumns + ` FROM webhook_deliveries d JOIN domain_events e ON e.id=d.event_id AND e.occurred_at=d.event_occurred_at WHERE d.id=$1::uuid`

// claimWebhookDeliveriesQuery takes due deliveries (owner, lease interval, limit); the hot-query registry plans it.
const claimWebhookDeliveriesQuery = `WITH clock AS MATERIALIZED (SELECT clock_timestamp() AS now), exhausted AS (
    UPDATE webhook_deliveries SET state='dead',next_attempt_at=NULL,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=clock.now
    FROM clock WHERE state='delivering' AND lease_expires_at<=clock.now AND attempts>=8
), orphaned AS (
    -- The event log keeps events for NOTARX_EVENTS_RETENTION only; a delivery whose event is gone can never be sent.
    UPDATE webhook_deliveries AS delivery SET state='dead',next_attempt_at=NULL,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
        last_error='event is no longer retained',updated_at=clock.now
    FROM clock WHERE delivery.state IN ('pending','retrying','delivering') AND COALESCE(delivery.next_attempt_at,delivery.lease_expires_at)<=clock.now
      AND NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.id=delivery.event_id AND e.occurred_at=delivery.event_occurred_at)
), candidates AS (
    SELECT delivery.id FROM webhook_deliveries AS delivery CROSS JOIN clock
    WHERE ((state IN ('pending','retrying') AND next_attempt_at<=clock.now) OR (state='delivering' AND lease_expires_at<=clock.now))
      AND attempts<8
      AND EXISTS (SELECT 1 FROM domain_events e WHERE e.id=delivery.event_id AND e.occurred_at=delivery.event_occurred_at)
    ORDER BY COALESCE(next_attempt_at,lease_expires_at),created_at,id FOR UPDATE OF delivery SKIP LOCKED LIMIT $3
), claimed AS (
    UPDATE webhook_deliveries AS delivery SET state='delivering',attempts=delivery.attempts+1,next_attempt_at=NULL,
        lease_owner=$1,lease_token=gen_random_uuid(),lease_expires_at=clock.now+$2::interval,updated_at=clock.now
    FROM candidates,clock WHERE delivery.id=candidates.id RETURNING delivery.*
)
SELECT d.id::text,d.event_id::text,e.type,d.subscription_id::text,d.state,d.attempts,d.next_attempt_at,d.lease_owner,d.lease_token::text,d.lease_expires_at,d.last_status,d.last_error,d.created_at,d.updated_at,d.delivered_at,
       e.id::text,e.type,COALESCE(e.repository_id::text,''),COALESCE(e.format,''),COALESCE(e.coordinate,''),COALESCE(e.digest,''),e.occurred_at,e.payload,
       s.id::text,s.name,s.endpoint_url,s.secret_ciphertext,s.event_types,s.enabled,s.version::text,s.created_at,s.updated_at
FROM claimed d JOIN domain_events e ON e.id=d.event_id AND e.occurred_at=d.event_occurred_at JOIN webhook_subscriptions s ON s.id=d.subscription_id
ORDER BY d.created_at,d.id`

// extraColumns scans the shared columns of a row into the destinations of a scan function and the columns a
// query adds after them into extra.
type extraColumns struct {
	scanner interface{ Scan(...any) error }
	extra   []any
}

func (e extraColumns) Scan(dest ...any) error {
	return e.scanner.Scan(append(dest, e.extra...)...)
}
