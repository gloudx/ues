package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Options описывает базовые настройки подключения на уровне хранения.
type Options struct {
	// DriverName позволяет указать зарегистрированный драйвер (по умолчанию "sqlite").
	DriverName string
	// JournalMode задаёт режим журнала (обычно WAL). Если пусто — используется WAL.
	JournalMode string
	// Synchronous задаёт уровень синхронизации (обычно NORMAL). Если пусто — NORMAL.
	Synchronous string
	// BusyTimeout — длительность ожидания перед ошибкой SQLITE_BUSY. Если 0 — 5с.
	BusyTimeout time.Duration
	// ForeignKeys включает/выключает проверку внешних ключей (по умолчанию true).
	ForeignKeys *bool
	// CacheSize задаёт размер кеша в страницах (отрицательное значение = KiB). 0 — без изменения.
	CacheSize int
	// MaxOpenConns ограничивает количество открытых соединений. 0 — оставляем значение по умолчанию.
	MaxOpenConns int
	// MaxIdleConns ограничивает пул idle соединений. 0 — используем значение по умолчанию.
	MaxIdleConns int
	// ConnMaxLifetime ограничивает время жизни соединения.
	ConnMaxLifetime time.Duration
}

// Database — thin-wrapper вокруг *sql.DB без знания о структурах индексации.
type Database struct {
	db *sql.DB
}

// Open создаёт подключение к SQLite с заданными опциями и прогоняет нужные PRAGMA.
func Open(path string, opts Options) (*Database, error) {
	if path == "" {
		return nil, errors.New("sqlite: empty path")
	}

	driver := opts.DriverName
	if driver == "" {
		driver = "sqlite"
	}

	journal := opts.JournalMode
	if journal == "" {
		journal = "WAL"
	}
	syncMode := opts.Synchronous
	if syncMode == "" {
		syncMode = "NORMAL"
	}
	busy := opts.BusyTimeout
	if busy <= 0 {
		busy = 5 * time.Second
	}

	db, err := sql.Open(driver, path)
	if err != nil {
		return nil, err
	}

	if opts.MaxOpenConns > 0 {
		db.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns > 0 {
		db.SetMaxIdleConns(opts.MaxIdleConns)
	}
	if opts.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pragmas := []string{
		fmt.Sprintf("PRAGMA journal_mode=%s", journal),
		fmt.Sprintf("PRAGMA synchronous=%s", syncMode),
		fmt.Sprintf("PRAGMA busy_timeout=%d", busy.Milliseconds()),
	}

	if opts.ForeignKeys != nil {
		if *opts.ForeignKeys {
			pragmas = append(pragmas, "PRAGMA foreign_keys=ON")
		} else {
			pragmas = append(pragmas, "PRAGMA foreign_keys=OFF")
		}
	} else {
		pragmas = append(pragmas, "PRAGMA foreign_keys=ON")
	}

	if opts.CacheSize != 0 {
		pragmas = append(pragmas, fmt.Sprintf("PRAGMA cache_size=%d", opts.CacheSize))
	}

	for _, pragma := range pragmas {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite: apply %s: %w", pragma, err)
		}
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return &Database{db: db}, nil
}

// Close закрывает базовое соединение.
func (d *Database) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// Exec выполняет запрос без возвращаемых строк.
func (d *Database) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.db.ExecContext(ctx, query, args...)
}

// Query выполняет запрос и возвращает строки вызывающему коду.
func (d *Database) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, query, args...)
}

// Prepare подготавливает выражение для повторного использования.
func (d *Database) Prepare(ctx context.Context, query string) (*sql.Stmt, error) {
	return d.db.PrepareContext(ctx, query)
}

// BeginTx открывает транзакцию; вызывающий код решает, как с ней работать.
func (d *Database) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := d.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx}, nil
}

// Tx — thin-wrapper над *sql.Tx без бизнес-логики индексов.
type Tx struct {
	tx *sql.Tx
}

// Exec выполняет инструкцию в транзакции.
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

// Query выполняет запрос в транзакции.
func (t *Tx) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

// Prepare подготавливает выражение в рамках транзакции.
func (t *Tx) Prepare(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.tx.PrepareContext(ctx, query)
}

// Commit завершает транзакцию.
func (t *Tx) Commit() error {
	return t.tx.Commit()
}

// Rollback откатывает транзакцию.
func (t *Tx) Rollback() error {
	return t.tx.Rollback()
}

// Underlying позволяет получить *sql.DB для низкоуровневого доступа.
func (d *Database) Underlying() *sql.DB {
	return d.db
}

// UnderlyingTx возвращает оригинальную *sql.Tx.
func (t *Tx) UnderlyingTx() *sql.Tx {
	return t.tx
}
