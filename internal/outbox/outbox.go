// Package outbox queues WhatsApp messages. Nothing is sent here: a worker reads the rows later.
package outbox

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"mohisab-api/internal/db"
)

const (
	KindRatesBroadcast = "rates_broadcast"
	KindDaySummary     = "day_summary"
)

func Enqueue(ctx context.Context, q db.Querier, shopID uuid.UUID, kind string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `insert into outbox (shop_id, kind, payload) values ($1, $2, $3::jsonb)`, shopID, kind, string(b))
	return err
}
