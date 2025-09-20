// Package sqliteindexer - упрощенная версия без FTS5 для совместимости
//
// Эта версия реализует все основные функции SQLiteIndexer, но использует
// обычный LIKE поиск вместо FTS5 для совместимости с системами где
// FTS5 не поддерживается.
package sqliteindexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	_ "github.com/mattn/go-sqlite3"
)

// SimpleSQLiteIndexer представляет упрощенный SQLite-based индексер без FTS5
type SimpleSQLiteIndexer struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewSimpleSQLiteIndexer создает новый простой SQLite индексер без FTS5
func NewSimpleSQLiteIndexer(dbPath string) (*SimpleSQLiteIndexer, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	indexer := &SimpleSQLiteIndexer{
		db: db,
	}

	if err := indexer.initSimpleSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return indexer, nil
}

// initSimpleSchema инициализирует упрощенную схему без FTS5
func (idx *SimpleSQLiteIndexer) initSimpleSchema() error {
	schema := `
	-- Основная таблица записей (без FTS5)
	CREATE TABLE IF NOT EXISTS records (
		cid TEXT PRIMARY KEY,
		collection TEXT NOT NULL,
		rkey TEXT NOT NULL,
		record_type TEXT NOT NULL,
		data TEXT NOT NULL,
		search_text TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(collection, rkey)
	);

	-- Индексы для оптимизации
	CREATE INDEX IF NOT EXISTS idx_records_collection ON records(collection);
	CREATE INDEX IF NOT EXISTS idx_records_type ON records(record_type);
	CREATE INDEX IF NOT EXISTS idx_records_collection_type ON records(collection, record_type);
	CREATE INDEX IF NOT EXISTS idx_records_created_at ON records(created_at);
	CREATE INDEX IF NOT EXISTS idx_records_updated_at ON records(updated_at);
	
	-- Индекс для текстового поиска через LIKE
	CREATE INDEX IF NOT EXISTS idx_records_search_text ON records(search_text);

	-- Таблица атрибутов для структурированного поиска
	CREATE TABLE IF NOT EXISTS record_attributes (
		cid TEXT NOT NULL,
		attribute_name TEXT NOT NULL,
		attribute_value TEXT NOT NULL,
		value_type TEXT NOT NULL,
		PRIMARY KEY (cid, attribute_name),
		FOREIGN KEY (cid) REFERENCES records(cid) ON DELETE CASCADE
	);

	-- Индексы для атрибутов
	CREATE INDEX IF NOT EXISTS idx_attr_name_value ON record_attributes(attribute_name, attribute_value);
	CREATE INDEX IF NOT EXISTS idx_attr_name_type ON record_attributes(attribute_name, value_type);

	-- Триггер для обновления времени
	CREATE TRIGGER IF NOT EXISTS update_records_timestamp 
		AFTER UPDATE ON records
	BEGIN
		UPDATE records SET updated_at = CURRENT_TIMESTAMP WHERE cid = NEW.cid;
	END;

	-- Представление для статистики
	CREATE VIEW IF NOT EXISTS collection_stats AS
	SELECT 
		collection,
		COUNT(*) as record_count,
		COUNT(DISTINCT record_type) as type_count,
		MIN(created_at) as first_record,
		MAX(updated_at) as last_updated
	FROM records 
	GROUP BY collection;
	`

	_, err := idx.db.Exec(schema)
	return err
}

// IndexRecord индексирует запись в SQLite (простая версия)
func (idx *SimpleSQLiteIndexer) IndexRecord(ctx context.Context, recordCID cid.Cid, metadata IndexMetadata) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	dataJSON, err := json.Marshal(metadata.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal record data: %w", err)
	}

	_, err = idx.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO records 
		(cid, collection, rkey, record_type, data, search_text, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, recordCID.String(), metadata.Collection, metadata.RKey, metadata.RecordType,
		string(dataJSON), metadata.SearchText, metadata.CreatedAt, metadata.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to index record: %w", err)
	}

	if err := idx.indexAttributes(ctx, recordCID.String(), metadata.Data); err != nil {
		return fmt.Errorf("failed to index attributes: %w", err)
	}

	return nil
}

// indexAttributes индексирует атрибуты записи
func (idx *SimpleSQLiteIndexer) indexAttributes(ctx context.Context, cidStr string, data map[string]interface{}) error {
	_, err := idx.db.ExecContext(ctx, "DELETE FROM record_attributes WHERE cid = ?", cidStr)
	if err != nil {
		return err
	}

	for key, value := range data {
		valueStr, valueType := getAttributeValue(value)
		_, err = idx.db.ExecContext(ctx, `
			INSERT INTO record_attributes (cid, attribute_name, attribute_value, value_type)
			VALUES (?, ?, ?, ?)
		`, cidStr, key, valueStr, valueType)
		if err != nil {
			return err
		}
	}

	return nil
}

// DeleteRecord удаляет запись из индекса
func (idx *SimpleSQLiteIndexer) DeleteRecord(ctx context.Context, recordCID cid.Cid) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	_, err := idx.db.ExecContext(ctx, "DELETE FROM records WHERE cid = ?", recordCID.String())
	return err
}

// SearchRecords выполняет поиск записей (простая версия)
func (idx *SimpleSQLiteIndexer) SearchRecords(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if query.FullTextQuery != "" {
		return idx.searchSimpleText(ctx, query)
	} else {
		return idx.searchStructured(ctx, query)
	}
}

// searchSimpleText выполняет простой текстовый поиск через LIKE
func (idx *SimpleSQLiteIndexer) searchSimpleText(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	sql := `
		SELECT cid, collection, rkey, record_type, data, created_at, updated_at
		FROM records 
		WHERE search_text LIKE ?
	`
	args := []interface{}{"%" + query.FullTextQuery + "%"}

	if query.Collection != "" {
		sql += " AND collection = ?"
		args = append(args, query.Collection)
	}

	if query.RecordType != "" {
		sql += " AND record_type = ?"
		args = append(args, query.RecordType)
	}

	if query.SortBy != "" {
		order := "ASC"
		if query.SortOrder == "DESC" {
			order = "DESC"
		}
		sql += fmt.Sprintf(" ORDER BY %s %s", query.SortBy, order)
	} else {
		sql += " ORDER BY created_at DESC"
	}

	if query.Limit > 0 {
		sql += " LIMIT ?"
		args = append(args, query.Limit)

		if query.Offset > 0 {
			sql += " OFFSET ?"
			args = append(args, query.Offset)
		}
	}

	return idx.executeSearchQuery(ctx, sql, args...)
}

// searchStructured выполняет структурированный поиск
func (idx *SimpleSQLiteIndexer) searchStructured(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	sql := "SELECT cid, collection, rkey, record_type, data, created_at, updated_at FROM records WHERE 1=1"
	args := []interface{}{}

	if query.Collection != "" {
		sql += " AND collection = ?"
		args = append(args, query.Collection)
	}

	if query.RecordType != "" {
		sql += " AND record_type = ?"
		args = append(args, query.RecordType)
	}

	if len(query.Filters) > 0 {
		for attr, value := range query.Filters {
			sql += " AND cid IN (SELECT cid FROM record_attributes WHERE attribute_name = ? AND attribute_value = ?)"
			args = append(args, attr, fmt.Sprintf("%v", value))
		}
	}

	if query.SortBy != "" {
		order := "ASC"
		if query.SortOrder == "DESC" {
			order = "DESC"
		}
		sql += fmt.Sprintf(" ORDER BY %s %s", query.SortBy, order)
	} else {
		sql += " ORDER BY created_at DESC"
	}

	if query.Limit > 0 {
		sql += " LIMIT ?"
		args = append(args, query.Limit)

		if query.Offset > 0 {
			sql += " OFFSET ?"
			args = append(args, query.Offset)
		}
	}

	return idx.executeSearchQuery(ctx, sql, args...)
}

// executeSearchQuery выполняет SQL запрос и возвращает результаты
func (idx *SimpleSQLiteIndexer) executeSearchQuery(ctx context.Context, sql string, args ...interface{}) ([]SearchResult, error) {
	rows, err := idx.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult

	for rows.Next() {
		var result SearchResult
		var cidStr, dataJSON string

		err = rows.Scan(&cidStr, &result.Collection, &result.RKey, &result.RecordType,
			&dataJSON, &result.CreatedAt, &result.UpdatedAt)

		if err != nil {
			return nil, err
		}

		if result.CID, err = cid.Parse(cidStr); err != nil {
			return nil, fmt.Errorf("invalid CID in search results: %w", err)
		}

		if err = json.Unmarshal([]byte(dataJSON), &result.Data); err != nil {
			return nil, fmt.Errorf("invalid JSON data in search results: %w", err)
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

// GetCollectionStats возвращает статистику по коллекции
func (idx *SimpleSQLiteIndexer) GetCollectionStats(ctx context.Context, collection string) (map[string]interface{}, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	row := idx.db.QueryRowContext(ctx, `
		SELECT record_count, type_count, first_record, last_updated
		FROM collection_stats 
		WHERE collection = ?
	`, collection)

	var recordCount, typeCount int
	var firstRecordStr, lastUpdatedStr string

	err := row.Scan(&recordCount, &typeCount, &firstRecordStr, &lastUpdatedStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return map[string]interface{}{
				"record_count": 0,
				"type_count":   0,
			}, nil
		}
		return nil, err
	}

	result := map[string]interface{}{
		"record_count": recordCount,
		"type_count":   typeCount,
	}

	// Парсим даты если они не пустые
	if firstRecordStr != "" {
		if firstRecord, err := time.Parse("2006-01-02 15:04:05", firstRecordStr); err == nil {
			result["first_record"] = firstRecord
		}
	}

	if lastUpdatedStr != "" {
		if lastUpdated, err := time.Parse("2006-01-02 15:04:05", lastUpdatedStr); err == nil {
			result["last_updated"] = lastUpdated
		}
	}

	return result, nil
}

// Close закрывает подключение к базе данных
func (idx *SimpleSQLiteIndexer) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.db.Close()
}
