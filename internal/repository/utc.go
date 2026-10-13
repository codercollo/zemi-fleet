package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// utcDB makes every time.Time that sqlc scans come back in UTC. pgx decodes timestamptz
// into the process's local zone, so without this a laptop in Nairobi and a server in UTC would
// hand the services different Locations for the same instant. Normalising here, at the edge
// where data enters the app, means nothing above the repository ever sees a local zone.
type utcDB struct{ sqlc.DBTX }

func (d utcDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return utcRow{d.DBTX.QueryRow(ctx, sql, args...)}
}

func (d utcDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := d.DBTX.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return utcRows{rows}, nil
}

type utcRow struct{ pgx.Row }

func (r utcRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	if err == nil {
		normalizeUTC(dest)
	}
	return err
}

type utcRows struct{ pgx.Rows }

func (r utcRows) Scan(dest ...any) error {
	err := r.Rows.Scan(dest...)
	if err == nil {
		normalizeUTC(dest)
	}
	return err
}

// normalizeUTC converts scanned *time.Time and **time.Time destinations to UTC in place.
func normalizeUTC(dest []any) {
	for _, d := range dest {
		switch v := d.(type) {
		case *time.Time:
			*v = v.UTC()
		case **time.Time:
			if *v != nil {
				u := (*v).UTC()
				*v = &u
			}
		}
	}
}
