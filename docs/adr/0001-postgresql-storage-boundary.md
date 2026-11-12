# PostgreSQL owns application persistence

Accepted for the MVP on 2026-11-12. Use PostgreSQL as the only application database, with versioned Millionaire SQL migrations and exact numeric financial columns. SQLite would simplify local installation, but PostgreSQL supports the planned continuous ingestion, transactional account updates, and concurrent readers without a second backend to maintain.

StopLore accepts a PostgreSQL pool but owns a separate document transaction and also initializes a legacy `trades` table. Give it a dedicated schema and pool with an explicit search path; never query or migrate its tables from Millionaire. Its whole-journal JSONB row lock is unsuitable for the application's candle store or financial ledger. See the [verified persistence contract](../stoplore-integration.md#persistence-contract).
