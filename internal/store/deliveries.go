package store

import "context"

// The replay memory of outside-host webhooks (forge_deliveries): a key for each delivery a connection has received,
// kept for the replay window and bounded in number. The statements that forget keys are here.

// ForgetDelivery removes one key if it was remembered before cutoff (Unix ms): past the replay window, the same
// delivery is new again.
func ForgetDelivery(ctx context.Context, x CtxExecer, connection, key string, cutoff int64) error {
	_, err := x.Exec(ctx, `DELETE FROM forge_deliveries WHERE connection_id = ? AND delivery_id = ? AND at < ?`, connection, key, cutoff)
	return err
}

// CountDeliveries is how many keys a connection remembers.
func CountDeliveries(ctx context.Context, q CtxQueryer, connection string) (int, error) {
	rows, err := q.Query(ctx, `SELECT count(*) FROM forge_deliveries WHERE connection_id = ?`, connection)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// EvictDeliveries removes a connection's keys that are older than its newest keep keys.
func EvictDeliveries(ctx context.Context, x CtxExecer, connection string, keep int) error {
	_, err := x.Exec(ctx, `DELETE FROM forge_deliveries WHERE connection_id = ? AND at <= (
		SELECT at FROM forge_deliveries WHERE connection_id = ? ORDER BY at DESC LIMIT 1 OFFSET ?)`, connection, connection, keep)
	return err
}
