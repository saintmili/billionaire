package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scopeSQL handles only static application-owned SQL. Bound data is never rewritten.
// The private namespace is generated from a random run ID, never caller input.
// Reserved schema qualifiers, DDL/regclass references and owner locks are routed
// together so existing strategy/risk/financial transactions stay fully isolated.
func scopeSQL(sql, schema string) string {
	if schema == "" {
		return sql
	}
	sql = strings.ReplaceAll(sql, "billionaire.", schema+".")
	sql = strings.ReplaceAll(sql, "SCHEMA IF NOT EXISTS billionaire", "SCHEMA IF NOT EXISTS "+schema)
	sql = strings.ReplaceAll(sql, "7341928602", "hashtextextended('"+schema+"',7341928602)")
	return strings.ReplaceAll(sql, "7341928603", "hashtextextended('"+schema+"',7341928603)")
}

type scopedPool struct {
	*pgxpool.Pool
	schema string
}

func (p *scopedPool) Exec(c context.Context, q string, a ...any) (pgconn.CommandTag, error) {
	return p.Pool.Exec(c, scopeSQL(q, p.schema), a...)
}
func (p *scopedPool) Query(c context.Context, q string, a ...any) (pgx.Rows, error) {
	return p.Pool.Query(c, scopeSQL(q, p.schema), a...)
}
func (p *scopedPool) QueryRow(c context.Context, q string, a ...any) pgx.Row {
	return p.Pool.QueryRow(c, scopeSQL(q, p.schema), a...)
}
func (p *scopedPool) Begin(c context.Context) (pgx.Tx, error) { return p.BeginTx(c, pgx.TxOptions{}) }
func (p *scopedPool) BeginTx(c context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	t, e := p.Pool.BeginTx(c, o)
	if e != nil {
		return nil, e
	}
	return &scopedTx{Tx: t, schema: p.schema}, nil
}

type scopedTx struct {
	pgx.Tx
	schema string
}

func (t *scopedTx) Exec(c context.Context, q string, a ...any) (pgconn.CommandTag, error) {
	return t.Tx.Exec(c, scopeSQL(q, t.schema), a...)
}
func (t *scopedTx) Query(c context.Context, q string, a ...any) (pgx.Rows, error) {
	return t.Tx.Query(c, scopeSQL(q, t.schema), a...)
}
func (t *scopedTx) QueryRow(c context.Context, q string, a ...any) pgx.Row {
	return t.Tx.QueryRow(c, scopeSQL(q, t.schema), a...)
}
func (t *scopedTx) Begin(c context.Context) (pgx.Tx, error) {
	v, e := t.Tx.Begin(c)
	if e != nil {
		return nil, e
	}
	return &scopedTx{Tx: v, schema: t.schema}, nil
}

type scopedConn struct {
	*pgx.Conn
	schema string
}

func (c *scopedConn) Begin(ctx context.Context) (pgx.Tx, error) {
	t, e := c.Conn.Begin(ctx)
	if e != nil {
		return nil, e
	}
	return &scopedTx{Tx: t, schema: c.schema}, nil
}
func (c *scopedConn) QueryRow(ctx context.Context, q string, a ...any) pgx.Row {
	return c.Conn.QueryRow(ctx, scopeSQL(q, c.schema), a...)
}
