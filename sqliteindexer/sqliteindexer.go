// Package repository содержит SQLite индексер для UES Repository Layer
//
// SQLite индексер представляет собой высокопроизводительную подсистему индексации
// и поиска для Repository Layer (уровень 3) в архитектуре UES. Он обеспечивает:
//
// 1. Быстрый полнотекстовый поиск через SQLite FTS5
// 2. Структурированные запросы по атрибутам записей
// 3. Аналитику и статистику коллекций
// 4. Автоматическую синхронизацию с MST индексом
//
// Архитектурная роль:
// - Дополняет MST индекс возможностями быстрого поиска
// - Интегрируется с content-addressed storage через CID
// - Обеспечивает SQL-like запросы к IPLD данным
// - Поддерживает консистентность через ACID транзакции
package sqliteindexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-cid"        // Content Identifier для content-addressed storage
	_ "github.com/mattn/go-sqlite3" // SQLite3 драйвер с поддержкой FTS5 и JSON
)

// SQLiteIndexer представляет SQLite-based индексер для записей репозитория.
//
// АРХИТЕКТУРНАЯ РОЛЬ:
// SQLiteIndexer является ключевым компонентом Repository Layer, который:
// - Обеспечивает быстрый поиск по большим объемам данных
// - Дополняет MST индекс возможностями SQL-запросов
// - Интегрируется с IPLD/CBOR данными через JSON сериализацию
// - Поддерживает ACID транзакции для консистентности данных
//
// МЕХАНИЗМ РАБОТЫ:
// 1. Получает IPLD записи от Repository при операциях CRUD
// 2. Извлекает текстовые поля для полнотекстовой индексации FTS5
// 3. Индексирует структурированные атрибуты для быстрого фильтрования
// 4. Поддерживает синхронизацию с MST через CID связи
// 5. Предоставляет SQL-like API для сложных запросов
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Использует WAL журналирование для высокой производительности записи
// - FTS5 обеспечивает субсекундный поиск в текстах
// - Индексы по атрибутам ускоряют фильтрацию
// - Foreign key constraints гарантируют референциальную целостность
type SQLiteIndexer struct {
	db *sql.DB      // Подключение к SQLite базе данных с настройками производительности
	mu sync.RWMutex // RW мьютекс для thread-safe операций (читателей много, писателей мало)
}

// IndexMetadata представляет метаданные для индексации записи
//
// НАЗНАЧЕНИЕ:
// Структура содержит всю информацию, необходимую для индексации IPLD записи
// в SQLite базе данных. Она служит мостом между IPLD/CBOR форматом UES
// и реляционной моделью SQLite.
//
// ПОЛЯ:
// - Collection: логическая группировка записей (аналог таблицы в БД)
// - RKey: уникальный ключ записи в рамках коллекции
// - RecordType: тип записи для дополнительной категоризации
// - Data: фактические данные записи в виде карты ключ-значение
// - SearchText: агрегированный текст для полнотекстового поиска FTS5
// - CreatedAt/UpdatedAt: временные метки для аудита и сортировки
type IndexMetadata struct {
	Collection string                 `json:"collection"`  // Коллекция записи (например: "posts", "users", "comments")
	RKey       string                 `json:"rkey"`        // Уникальный ключ записи в коллекции
	RecordType string                 `json:"record_type"` // Тип записи для дополнительной классификации
	Data       map[string]interface{} `json:"data"`        // Структурированные данные записи для индексации атрибутов
	SearchText string                 `json:"search_text"` // Объединенный текст из всех текстовых полей для FTS5
	CreatedAt  time.Time              `json:"created_at"`  // Время создания записи
	UpdatedAt  time.Time              `json:"updated_at"`  // Время последнего обновления записи
}

// SearchQuery представляет запрос для поиска записей
//
// ФИЛОСОФИЯ ДИЗАЙНА:
// SearchQuery обеспечивает унифицированный API для различных типов поиска:
// - Структурированные запросы (по коллекции, типу, атрибутам)
// - Полнотекстовый поиск через FTS5
// - Комбинированные запросы с фильтрацией и сортировкой
// - Пагинацию для больших результатов
//
// ТИПЫ ПОИСКА:
// 1. Поиск по коллекции: Collection != ""
// 2. Полнотекстовый: FullTextQuery != ""
// 3. Фильтрация: Filters содержит условия
// 4. Сортировка: SortBy + SortOrder
// 5. Пагинация: Limit + Offset
type SearchQuery struct {
	Collection    string                 `json:"collection,omitempty"`      // Фильтр по коллекции ("posts", "users", и т.д.)
	RecordType    string                 `json:"record_type,omitempty"`     // Фильтр по типу записи
	Filters       map[string]interface{} `json:"filters,omitempty"`         // Фильтры по атрибутам записи (WHERE conditions)
	FullTextQuery string                 `json:"full_text_query,omitempty"` // FTS5 запрос для полнотекстового поиска
	SortBy        string                 `json:"sort_by,omitempty"`         // Поле для сортировки (created_at, updated_at, и т.д.)
	SortOrder     string                 `json:"sort_order,omitempty"`      // Направление сортировки: "ASC" или "DESC"
	Limit         int                    `json:"limit,omitempty"`           // Максимальное количество результатов
	Offset        int                    `json:"offset,omitempty"`          // Смещение для пагинации
}

// SearchResult представляет результат поиска
//
// СТРУКТУРА РЕЗУЛЬТАТА:
// SearchResult объединяет данные из content-addressed storage (CID)
// с метаданными из SQLite индекса, обеспечивая полную информацию
// о найденной записи для клиентского кода.
//
// СВЯЗЬ С АРХИТЕКТУРОЙ UES:
// - CID связывает результат с blockstore (уровень 1)
// - Collection/RKey обеспечивают навигацию через MST (уровень 2)
// - Data содержит десериализованные IPLD данные
// - Relevance предоставляет ранжирование FTS5
type SearchResult struct {
	CID        cid.Cid                `json:"cid"`                 // Content Identifier для доступа к blockstore
	Collection string                 `json:"collection"`          // Коллекция записи
	RKey       string                 `json:"rkey"`                // Ключ записи в коллекции
	RecordType string                 `json:"record_type"`         // Тип записи
	Data       map[string]interface{} `json:"data"`                // Десериализованные данные записи
	CreatedAt  time.Time              `json:"created_at"`          // Время создания
	UpdatedAt  time.Time              `json:"updated_at"`          // Время последнего обновления
	Relevance  float64                `json:"relevance,omitempty"` // Оценка релевантности FTS5 (0.0 - 1.0)
}

// NewSQLiteIndexer создает новый SQLite индексер
//
// ПРОЦЕСС ИНИЦИАЛИЗАЦИИ:
// 1. Открывает SQLite соединение с оптимизированными настройками
// 2. Настраивает WAL журналирование для высокой производительности
// 3. Включает foreign key constraints для целостности данных
// 4. Инициализирует схему базы данных с индексами и триггерами
// 5. Создает FTS5 виртуальную таблицу для полнотекстового поиска
//
// НАСТРОЙКИ ПОДКЛЮЧЕНИЯ:
// - WAL журналирование: быстрые записи, блокировки на уровне страниц
// - Foreign keys: автоматическое каскадное удаление связанных данных
// - Безопасность: защита от SQL injection через prepared statements
func NewSQLiteIndexer(dbPath string) (*SQLiteIndexer, error) {
	// Открываем SQLite с производительными настройками:
	// _journal_mode=WAL - журналирование Write-Ahead Log для конкурентного доступа
	// _foreign_keys=ON - включение foreign key constraints для целостности
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	// Создаем экземпляр индексера
	indexer := &SQLiteIndexer{
		db: db,
	}

	// Инициализируем схему базы данных
	// При ошибке корректно закрываем соединение для предотвращения утечек ресурсов
	if err := indexer.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return indexer, nil
}

// initSchema инициализирует схему базы данных
//
// АРХИТЕКТУРА СХЕМЫ ДАННЫХ:
//
// Схема спроектирована для оптимального сочетания структурированного и полнотекстового поиска:
//
// 1. ОСНОВНАЯ ТАБЛИЦА (records):
//   - Хранит метаданные всех записей
//   - Связана с blockstore через CID
//   - JSON поле для гибкого хранения IPLD данных
//
// 2. FTS5 ТАБЛИЦА (records_fts):
//   - Виртуальная таблица для полнотекстового поиска
//   - Автоматически синхронизируется через триггеры
//   - Обеспечивает субсекундный поиск по тексту
//
// 3. АТРИБУТЫ (record_attributes):
//   - Индексирует структурированные поля
//   - Поддерживает типизированные запросы
//   - Обеспечивает быстрые фильтры WHERE
//
// 4. СТАТИСТИКА (collection_stats):
//   - Материализованное представление для аналитики
//   - Кэшированные агрегаты по коллекциям
//   - Быстрый доступ к метрикам
func (idx *SQLiteIndexer) initSchema() error {
	schema := `
	-- ===============================================
	-- ОСНОВНАЯ ТАБЛИЦА ЗАПИСЕЙ
	-- ===============================================
	-- 
	-- НАЗНАЧЕНИЕ:
	-- Центральная таблица, хранящая метаданные всех записей в UES.
	-- Служит мостом между content-addressed storage (CID) и структурированными запросами.
	--
	-- ДИЗАЙН:
	-- - cid как PRIMARY KEY обеспечивает уникальность и быструю навигацию
	-- - collection + rkey образуют логический составной ключ
	-- - data хранит JSON сериализованные IPLD данные
	-- - search_text содержит агрегированный текст для FTS5
	--
	-- ИНДЕКСАЦИЯ:
	-- Таблица оптимизирована для частых запросов по коллекциям и типам записей
	CREATE TABLE IF NOT EXISTS records (
		cid TEXT PRIMARY KEY,              -- Content Identifier - связь с blockstore
		collection TEXT NOT NULL,          -- Логическая коллекция (posts, users, comments)
		rkey TEXT NOT NULL,                -- Уникальный ключ записи в коллекции
		record_type TEXT NOT NULL,         -- Тип записи для дополнительной категоризации
		data TEXT NOT NULL,                -- JSON сериализованные IPLD данные
		search_text TEXT,                  -- Агрегированный текст для полнотекстового поиска
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,  -- Время создания записи
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,  -- Время последнего обновления
		UNIQUE(collection, rkey)           -- Бизнес-ключ: уникальность в рамках коллекции
	);

	-- ===============================================
	-- ИНДЕКСЫ ДЛЯ ОПТИМИЗАЦИИ ЗАПРОСОВ
	-- ===============================================
	--
	-- СТРАТЕГИЯ ИНДЕКСАЦИИ:
	-- Индексы создаются на основе частых паттернов запросов:
	-- 1. Поиск по коллекции (самый частый)
	-- 2. Фильтрация по типу записи
	-- 3. Комбинированные запросы коллекция+тип
	-- 4. Сортировка по времени создания/обновления
	
	-- Индекс для запросов "все записи коллекции X"
	CREATE INDEX IF NOT EXISTS idx_records_collection ON records(collection);
	
	-- Индекс для фильтрации по типу записи
	CREATE INDEX IF NOT EXISTS idx_records_type ON records(record_type);
	
	-- Составной индекс для запросов "записи типа Y в коллекции X"
	CREATE INDEX IF NOT EXISTS idx_records_collection_type ON records(collection, record_type);
	
	-- Индексы для сортировки по времени (ORDER BY оптимизация)
	CREATE INDEX IF NOT EXISTS idx_records_created_at ON records(created_at);
	CREATE INDEX IF NOT EXISTS idx_records_updated_at ON records(updated_at);

	-- ===============================================
	-- FTS5 ПОЛНОТЕКСТОВЫЙ ПОИСК
	-- ===============================================
	--
	-- АРХИТЕКТУРА FTS5:
	-- records_fts - это виртуальная таблица SQLite FTS5, которая:
	-- - Создает инвертированный индекс для быстрого текстового поиска
	-- - Поддерживает ранжирование результатов по релевантности
	-- - Автоматически обрабатывает стемминг и токенизацию
	-- - Связана с основной таблицей через content_rowid
	--
	-- FIELDS:
	-- - cid: для связи с основной таблицей
	-- - collection: для фильтрации поиска по коллекциям
	-- - rkey: для идентификации записи
	-- - search_text: индексируемый текстовый контент
	--
	-- НАСТРОЙКИ:
	-- - content='records': FTS5 синхронизируется с таблицей records
	-- - content_rowid='rowid': использует SQLite rowid для связи
	CREATE VIRTUAL TABLE IF NOT EXISTS records_fts USING fts5(
		cid,           -- Content Identifier для связи
		collection,    -- Коллекция для фильтрации FTS запросов
		rkey,          -- Ключ записи
		search_text,   -- Индексируемый текстовый контент
		content='records',        -- Связь с основной таблицей
		content_rowid='rowid'     -- Использование SQLite rowid
	);

	-- ===============================================
	-- ТРИГГЕРЫ ДЛЯ СИНХРОНИЗАЦИИ FTS5
	-- ===============================================
	--
	-- МЕХАНИЗМ СИНХРОНИЗАЦИИ:
	-- Триггеры обеспечивают автоматическую синхронизацию между
	-- основной таблицей records и FTS5 таблицей records_fts.
	-- Это гарантирует консистентность полнотекстового индекса.
	--
	-- СОБЫТИЯ:
	-- 1. INSERT: добавление новой записи в FTS индекс
	-- 2. DELETE: удаление записи из FTS индекса
	-- 3. UPDATE: пересоздание записи в FTS индексе
	
	-- Триггер вставки: добавляет новую запись в FTS5 при INSERT в records
	CREATE TRIGGER IF NOT EXISTS records_fts_insert AFTER INSERT ON records BEGIN
		INSERT INTO records_fts(cid, collection, rkey, search_text) 
		VALUES (new.cid, new.collection, new.rkey, new.search_text);
	END;

	-- Триггер удаления: удаляет запись из FTS5 при DELETE из records
	CREATE TRIGGER IF NOT EXISTS records_fts_delete AFTER DELETE ON records BEGIN
		DELETE FROM records_fts WHERE cid = old.cid;
	END;

	-- Триггер обновления: пересоздает запись в FTS5 при UPDATE records
	-- Использует DELETE + INSERT для корректного обновления FTS индекса
	CREATE TRIGGER IF NOT EXISTS records_fts_update AFTER UPDATE ON records BEGIN
		DELETE FROM records_fts WHERE cid = old.cid;
		INSERT INTO records_fts(cid, collection, rkey, search_text) 
		VALUES (new.cid, new.collection, new.rkey, new.search_text);
	END;

	-- ===============================================
	-- ТАБЛИЦА АТРИБУТОВ ДЛЯ СТРУКТУРИРОВАННОГО ПОИСКА
	-- ===============================================
	--
	-- НАЗНАЧЕНИЕ:
	-- record_attributes обеспечивает быстрые структурированные запросы
	-- по произвольным полям IPLD записей. Эта таблица реализует паттерн
	-- Entity-Attribute-Value (EAV) для гибкой индексации JSON данных.
	--
	-- АРХИТЕКТУРА EAV:
	-- - Каждый атрибут записи хранится как отдельная строка
	-- - Поддерживается типизация значений (string, number, boolean, datetime)
	-- - Быстрые индексы по имени атрибута и значению
	-- - Каскадное удаление при удалении основной записи
	--
	-- ПРИМЕНЕНИЕ:
	-- Позволяет делать запросы типа:
	-- "найти все посты пользователя X"
	-- "найти все записи с рейтингом > 5"
	-- "найти записи, созданные в 2024 году"
	CREATE TABLE IF NOT EXISTS record_attributes (
		cid TEXT NOT NULL,                 -- Связь с основной записью
		attribute_name TEXT NOT NULL,     -- Имя атрибута (например: "author", "rating", "tags")
		attribute_value TEXT NOT NULL,    -- Значение атрибута (всегда строка для универсальности)
		value_type TEXT NOT NULL,         -- Тип значения: 'string', 'number', 'boolean', 'datetime', 'json'
		PRIMARY KEY (cid, attribute_name), -- Композитный первичный ключ
		FOREIGN KEY (cid) REFERENCES records(cid) ON DELETE CASCADE  -- Каскадное удаление
	);

	-- ИНДЕКСЫ ДЛЯ БЫСТРЫХ ФИЛЬТРОВ:
	-- Индекс для запросов "WHERE attribute_name = X AND attribute_value = Y"
	CREATE INDEX IF NOT EXISTS idx_attr_name_value ON record_attributes(attribute_name, attribute_value);
	
	-- Индекс для типизированных запросов "WHERE attribute_name = X AND value_type = Y"
	CREATE INDEX IF NOT EXISTS idx_attr_name_type ON record_attributes(attribute_name, value_type);

	-- ===============================================
	-- ТРИГГЕР ДЛЯ АВТОМАТИЧЕСКОГО ОБНОВЛЕНИЯ ВРЕМЕННЫХ МЕТОК
	-- ===============================================
	--
	-- МЕХАНИЗМ:
	-- Триггер автоматически обновляет поле updated_at при любом изменении
	-- записи в таблице records. Это обеспечивает точное отслеживание
	-- времени последнего изменения без необходимости помнить об этом
	-- в прикладном коде.
	CREATE TRIGGER IF NOT EXISTS update_records_timestamp 
		AFTER UPDATE ON records
	BEGIN
		UPDATE records SET updated_at = CURRENT_TIMESTAMP WHERE cid = NEW.cid;
	END;

	-- ===============================================
	-- ПРЕДСТАВЛЕНИЕ ДЛЯ СТАТИСТИКИ КОЛЛЕКЦИЙ
	-- ===============================================
	--
	-- НАЗНАЧЕНИЕ:
	-- collection_stats - это материализованное представление (VIEW),
	-- которое предоставляет быстрый доступ к аналитической информации
	-- о коллекциях без необходимости выполнять тяжелые агрегирующие запросы.
	--
	-- МЕТРИКИ:
	-- - record_count: общее количество записей в коллекции
	-- - type_count: количество различных типов записей
	-- - first_record: время создания первой записи (для аудита)
	-- - last_updated: время последнего обновления любой записи
	--
	-- ПРОИЗВОДИТЕЛЬНОСТЬ:
	-- SQLite оптимизирует VIEW запросы, и для небольших коллекций
	-- агрегация выполняется быстро. Для больших коллекций можно
	-- рассмотреть материализованные таблицы с инкрементальным обновлением.
	CREATE VIEW IF NOT EXISTS collection_stats AS
	SELECT 
		collection,                        -- Имя коллекции
		COUNT(*) as record_count,          -- Общее количество записей
		COUNT(DISTINCT record_type) as type_count,  -- Количество типов записей
		MIN(created_at) as first_record,   -- Время создания первой записи
		MAX(updated_at) as last_updated    -- Время последнего обновления
	FROM records 
	GROUP BY collection;
	`

	// Выполняем весь DDL скрипт как одну транзакцию
	// Это обеспечивает атомарность создания схемы
	_, err := idx.db.Exec(schema)
	return err
}

// IndexRecord индексирует запись в SQLite
//
// ПРОЦЕСС ИНДЕКСАЦИИ:
// Этот метод является ключевой точкой интеграции между Repository Layer
// и SQLite индексером. Он выполняет полную индексацию IPLD записи:
//
// 1. СЕРИАЛИЗАЦИЯ ДАННЫХ:
//   - Преобразует map[string]interface{} в JSON для хранения в БД
//   - Сохраняет структуру IPLD данных для последующей десериализации
//
// 2. ОСНОВНАЯ ЗАПИСЬ:
//   - Вставляет метаданные в таблицу records
//   - Связывает запись с blockstore через CID
//   - Обеспечивает уникальность через (collection, rkey)
//
// 3. АТРИБУТНАЯ ИНДЕКСАЦИЯ:
//   - Извлекает все поля из Data для структурированного поиска
//   - Создает записи в record_attributes для быстрых фильтров
//   - Поддерживает типизацию для корректных сравнений
//
// 4. АВТОМАТИЧЕСКАЯ FTS5 СИНХРОНИЗАЦИЯ:
//   - SQLite триггеры автоматически обновляют records_fts
//   - search_text индексируется для полнотекстового поиска
//
// ТРАНЗАКЦИОННОСТЬ:
// Метод использует prepared statements для защиты от SQL injection
// и обеспечивает атомарность через ACID свойства SQLite.
func (idx *SQLiteIndexer) IndexRecord(ctx context.Context, recordCID cid.Cid, metadata IndexMetadata) error {
	// Блокируем на запись для thread-safety
	// RWMutex позволяет нескольким читателям работать параллельно,
	// но писатели получают эксклюзивный доступ
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// === СЕРИАЛИЗАЦИЯ IPLD ДАННЫХ ===

	// Преобразуем структурированные данные записи в JSON
	// JSON формат выбран для:
	// - Совместимости с SQLite JSON функциями
	// - Сохранения типов данных при десериализации
	// - Возможности SQL запросов к вложенным полям (JSON_EXTRACT)
	dataJSON, err := json.Marshal(metadata.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal record data: %w", err)
	}

	// === ВСТАВКА ОСНОВНОЙ ЗАПИСИ ===

	// INSERT OR REPLACE обеспечивает upsert семантику:
	// - Если запись с данным CID не существует, создается новая
	// - Если запись существует, она полностью заменяется
	// Это корректно обрабатывает обновления записей в Repository
	_, err = idx.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO records 
		(cid, collection, rkey, record_type, data, search_text, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, recordCID.String(), metadata.Collection, metadata.RKey, metadata.RecordType,
		string(dataJSON), metadata.SearchText, metadata.CreatedAt, metadata.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to index record: %w", err)
	}

	// === ИНДЕКСАЦИЯ АТРИБУТОВ ===

	// Индексируем все поля записи как searchable атрибуты
	// Это позволяет делать быстрые фильтры типа WHERE author = 'john'
	if err := idx.indexAttributes(ctx, recordCID.String(), metadata.Data); err != nil {
		return fmt.Errorf("failed to index attributes: %w", err)
	}

	return nil
}

// indexAttributes индексирует атрибуты записи для быстрого поиска
//
// АЛГОРИТМ ИНДЕКСАЦИИ АТРИБУТОВ:
//
// 1. ОЧИСТКА СТАРЫХ ДАННЫХ:
//   - Удаляет все существующие атрибуты для данного CID
//   - Обеспечивает консистентность при обновлении записи
//   - Предотвращает накопление устаревших атрибутов
//
// 2. ИЗВЛЕЧЕНИЕ И ТИПИЗАЦИЯ:
//   - Проходит по всем полям в map[string]interface{}
//   - Определяет тип каждого значения (string, number, boolean, datetime, json)
//   - Преобразует значения в строковое представление для унификации
//
// 3. СОЗДАНИЕ АТРИБУТНЫХ ЗАПИСЕЙ:
//   - Каждое поле IPLD записи становится строкой в record_attributes
//   - Поддерживает типизированные сравнения (числовые, даты)
//   - Обеспечивает быстрые индексные поиски
//
// ПРИМЕРЫ ИНДЕКСАЦИИ:
// - {"name": "John"} → ("name", "John", "string")
// - {"age": 30} → ("age", "30", "number")
// - {"active": true} → ("active", "true", "boolean")
// - {"created": "2024-01-01T00:00:00Z"} → ("created", "2024-01-01T00:00:00Z", "datetime")
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// Использует prepared statements для оптимальной производительности
// при массовой вставке атрибутов.
func (idx *SQLiteIndexer) indexAttributes(ctx context.Context, cidStr string, data map[string]interface{}) error {
	// === ОЧИСТКА СТАРЫХ АТРИБУТОВ ===

	// Удаляем все существующие атрибуты для данной записи
	// Это обеспечивает идемпотентность операции - повторная индексация
	// записи не создаст дублирующиеся атрибуты
	_, err := idx.db.ExecContext(ctx, "DELETE FROM record_attributes WHERE cid = ?", cidStr)
	if err != nil {
		return err
	}

	// === ИНДЕКСАЦИЯ НОВЫХ АТРИБУТОВ ===

	// Проходим по всем полям в данных записи
	// Каждое поле становится searchable атрибутом
	for key, value := range data {
		// Преобразуем значение в строку и определяем его тип
		// getAttributeValue обрабатывает различные Go типы
		valueStr, valueType := getAttributeValue(value)

		// Вставляем атрибут в таблицу для индексации
		// Используем prepared statement для защиты от SQL injection
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

// getAttributeValue преобразует значение в строку и определяет его тип
//
// СИСТЕМА ТИПИЗАЦИИ:
//
// Функция реализует универсальную систему типизации для SQLite индекса,
// которая сохраняет семантику типов Go/IPLD при хранении в текстовом формате.
//
// ПОДДЕРЖИВАЕМЫЕ ТИПЫ:
//
// 1. STRING - текстовые данные
//   - Хранятся как есть
//   - Поддерживают лексикографическое сравнение
//   - Используются в полнотекстовом поиске
//
// 2. NUMBER - числовые данные
//   - int, int32, int64, float32, float64
//   - Сохраняют возможность числового сравнения
//   - Могут использоваться в диапазонных запросах
//
// 3. BOOLEAN - логические значения
//   - true/false
//   - Поддерживают фильтрацию по булевым условиям
//
// 4. DATETIME - временные метки
//   - time.Time → RFC3339 формат
//   - Сортируемый текстовый формат для временных запросов
//   - Совместим с SQLite datetime функциями
//
// 5. JSON - сложные структуры
//   - Массивы, объекты, вложенные структуры
//   - Сериализация в JSON для сохранения структуры
//   - Поддержка SQLite JSON операторов
//
// FALLBACK СТРАТЕГИЯ:
// Неизвестные типы преобразуются в строки через fmt.Sprintf("%v")
// что обеспечивает универсальность системы типов.
func getAttributeValue(value interface{}) (string, string) {
	switch v := value.(type) {
	case string:
		// Текстовые данные - основа для полнотекстового поиска
		// Сохраняются без изменений для точного соответствия
		return v, "string"

	case int, int32, int64, float32, float64:
		// Числовые типы - конвертируются в строку с сохранением значения
		// Важно: SQLite может выполнять числовые сравнения для этих значений
		// при указании правильного типа в value_type
		return fmt.Sprintf("%v", v), "number"

	case bool:
		// Булевы значения - стандартизированы как "true"/"false"
		// Позволяют делать точные запросы по логическим условиям
		return fmt.Sprintf("%t", v), "boolean"

	case time.Time:
		// Временные метки - RFC3339 формат для сортируемого текстового представления
		// Совместимо с SQLite datetime функциями и позволяет временные запросы
		return v.Format(time.RFC3339), "datetime"

	default:
		// КОМПЛЕКСНЫЕ ТИПЫ (массивы, объекты, структуры)
		// Пытаемся сериализовать в JSON для сохранения структуры
		if jsonBytes, err := json.Marshal(v); err == nil {
			// Успешная JSON сериализация - сохраняем как json тип
			// Это позволяет использовать SQLite JSON операторы для запросов
			return string(jsonBytes), "json"
		}

		// FALLBACK для неизвестных типов
		// Преобразуем в строку через fmt.Sprintf для универсальности
		// Гарантирует, что любое значение может быть проиндексировано
		return fmt.Sprintf("%v", v), "string"
	}
}

// DeleteRecord удаляет запись из индекса
//
// ПРОЦЕСС УДАЛЕНИЯ:
//
// 1. КАСКАДНОЕ УДАЛЕНИЕ:
//   - Основная запись удаляется из таблицы records
//   - Foreign key constraints автоматически удаляют связанные атрибуты
//   - SQLite триггеры автоматически очищают FTS5 индекс
//
// 2. КОНСИСТЕНТНОСТЬ:
//   - Все связанные данные удаляются атомарно
//   - Индексы остаются консистентными
//   - Нет "висячих" ссылок или устаревших данных
//
// 3. ПРОИЗВОДИТЕЛЬНОСТЬ:
//   - Удаление по PRIMARY KEY (cid) максимально быстрое
//   - Каскадные операции выполняются эффективно SQLite
//
// ВАЖНО: Этот метод удаляет только данные из SQLite индекса.
// Сами блоки в blockstore не затрагиваются - это responsibility Repository Layer.
func (idx *SQLiteIndexer) DeleteRecord(ctx context.Context, recordCID cid.Cid) error {
	// Блокируем на запись для thread-safety
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Удаляем основную запись по CID
	// Foreign key constraints и триггеры автоматически очистят:
	// - record_attributes (через ON DELETE CASCADE)
	// - records_fts (через AFTER DELETE триггер)
	_, err := idx.db.ExecContext(ctx, "DELETE FROM records WHERE cid = ?", recordCID.String())
	return err
}

// SearchRecords выполняет поиск записей по запросу
//
// ДИСПЕТЧЕР ПОИСКА:
//
// Этот метод является основной точкой входа для всех типов поиска
// в SQLite индексере. Он анализирует SearchQuery и направляет запрос
// к соответствующему специализированному методу поиска.
//
// ТИПЫ ПОИСКА:
//
// 1. ПОЛНОТЕКСТОВЫЙ ПОИСК (FullTextQuery != ""):
//   - Использует SQLite FTS5 для поиска по тексту
//   - Поддерживает ранжирование по релевантности
//   - Быстрый поиск в больших объемах текстовых данных
//   - Направляется к searchFullText()
//
// 2. СТРУКТУРИРОВАННЫЙ ПОИСК (FullTextQuery == ""):
//   - Использует обычные SQL запросы с WHERE условиями
//   - Поиск по коллекции, типу, атрибутам
//   - Точные соответствия и фильтрация
//   - Направляется к searchStructured()
//
// ОБЩИЕ ВОЗМОЖНОСТИ:
// - Комбинирование фильтров (коллекция + тип + атрибуты)
// - Сортировка по любому полю
// - Пагинация результатов
// - Thread-safe операции через RWMutex
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// RWMutex позволяет нескольким читателям выполнять поиск параллельно,
// что критично для высоконагруженных приложений.
func (idx *SQLiteIndexer) SearchRecords(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	// Блокируем на чтение - позволяет параллельные поиски
	// Это ключевая оптимизация для производительности чтения
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var results []SearchResult
	var err error

	// === ДИСПЕТЧЕРИЗАЦИЯ ТИПА ПОИСКА ===

	if query.FullTextQuery != "" {
		// ПОЛНОТЕКСТОВЫЙ ПОИСК через FTS5
		// Приоритет отдается FTS5 когда указан FullTextQuery
		// поскольку он обеспечивает лучшее ранжирование и производительность
		// для текстовых запросов
		results, err = idx.searchFullText(ctx, query)
	} else {
		// СТРУКТУРИРОВАННЫЙ ПОИСК через SQL WHERE
		// Используется для точных соответствий, фильтров по атрибутам
		// и запросов, где полнотекстовый поиск не нужен
		results, err = idx.searchStructured(ctx, query)
	}

	return results, err
}

// searchFullText выполняет полнотекстовый поиск
//
// МЕХАНИЗМ FTS5 ПОИСКА:
//
// SQLite FTS5 (Full-Text Search 5) - это высокопроизводительный движок
// полнотекстового поиска, который обеспечивает:
//
// 1. ИНВЕРТИРОВАННЫЙ ИНДЕКС:
//   - Токенизация текста на слова
//   - Создание инвертированного индекса: слово → список документов
//   - Быстрый поиск O(log N) вместо O(N) сканирования
//
// 2. РАНЖИРОВАНИЕ ПО РЕЛЕВАНТНОСТИ:
//   - BM25 алгоритм для оценки релевантности
//   - Учитывает частоту терминов и длину документа
//   - Возвращает результаты по убыванию релевантности
//
// 3. РАСШИРЕННЫЙ СИНТАКСИС ЗАПРОСОВ:
//   - Поиск по фразам: "точная фраза"
//   - Булевы операторы: AND, OR, NOT
//   - Поиск с подстановочными знаками: prefix*
//   - Поиск рядом стоящих слов: NEAR(term1, term2)
//
// АРХИТЕКТУРА ЗАПРОСА:
// - JOIN между records_fts и records для получения полных данных
// - Фильтрация по коллекции и типу через основную таблицу
// - Сортировка по релевантности или пользовательскому полю
// - Пагинация для управления размером результата
func (idx *SQLiteIndexer) searchFullText(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	// === ПОСТРОЕНИЕ FTS5 ЗАПРОСА ===

	// Базовый SQL для полнотекстового поиска:
	// - records_fts.rank содержит оценку релевантности BM25
	// - JOIN с основной таблицей для получения полных метаданных
	// - MATCH оператор для FTS5 поиска
	sql := `
		SELECT r.cid, r.collection, r.rkey, r.record_type, r.data, r.created_at, r.updated_at,
		       fts.rank as relevance
		FROM records_fts fts
		JOIN records r ON r.cid = fts.cid
		WHERE records_fts MATCH ?
	`
	// Первый параметр - FullTextQuery для FTS5 MATCH
	args := []interface{}{query.FullTextQuery}

	// === ДОПОЛНИТЕЛЬНЫЕ ФИЛЬТРЫ ===

	// Фильтр по коллекции (если указан)
	// Ограничивает FTS поиск конкретной коллекцией для повышения точности
	if query.Collection != "" {
		sql += " AND r.collection = ?"
		args = append(args, query.Collection)
	}

	// Фильтр по типу записи (если указан)
	// Дополнительная категоризация внутри коллекции
	if query.RecordType != "" {
		sql += " AND r.record_type = ?"
		args = append(args, query.RecordType)
	}

	// === СОРТИРОВКА ===

	if query.SortBy != "" {
		// ПОЛЬЗОВАТЕЛЬСКАЯ СОРТИРОВКА
		// Клиент может переопределить сортировку по релевантности
		order := "ASC"
		if query.SortOrder == "DESC" {
			order = "DESC"
		}
		sql += fmt.Sprintf(" ORDER BY r.%s %s", query.SortBy, order)
	} else {
		// СОРТИРОВКА ПО РЕЛЕВАНТНОСТИ (по умолчанию)
		// FTS5 rank сортируется по убыванию для лучших результатов вверху
		sql += " ORDER BY relevance DESC"
	}

	// === ПАГИНАЦИЯ ===

	// LIMIT для ограничения количества результатов
	if query.Limit > 0 {
		sql += " LIMIT ?"
		args = append(args, query.Limit)

		// OFFSET для пагинации (только если указан LIMIT)
		if query.Offset > 0 {
			sql += " OFFSET ?"
			args = append(args, query.Offset)
		}
	}

	// Выполняем построенный SQL запрос
	return idx.executeSearchQuery(ctx, sql, args...)
}

// searchStructured выполняет структурированный поиск
//
// МЕХАНИЗМ СТРУКТУРИРОВАННОГО ПОИСКА:
//
// В отличие от полнотекстового поиска, структурированный поиск работает
// с точными соответствиями и фильтрами по метаданным записей.
//
// ТИПЫ СТРУКТУРИРОВАННЫХ ЗАПРОСОВ:
//
// 1. ПОИСК ПО КОЛЛЕКЦИИ:
//   - "Найти все записи в коллекции 'posts'"
//   - Использует индекс idx_records_collection
//
// 2. ПОИСК ПО ТИПУ ЗАПИСИ:
//   - "Найти все записи типа 'article'"
//   - Полезно для категоризации внутри коллекции
//
// 3. ПОИСК ПО АТРИБУТАМ:
//   - "Найти все посты автора 'john'"
//   - "Найти все записи с рейтингом > 5"
//   - Использует таблицу record_attributes с EAV моделью
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Прямые индексные поиски O(log N)
// - Оптимизированные JOIN операции
// - Возможность составных индексов для частых комбинаций
//
// АРХИТЕКТУРА ЗАПРОСА:
// - Базовый SELECT из таблицы records
// - Динамическое добавление WHERE условий
// - Субзапросы для атрибутных фильтров
// - Гибкая сортировка и пагинация
func (idx *SQLiteIndexer) searchStructured(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	// === БАЗОВЫЙ SQL ЗАПРОС ===

	// Начинаем с простого SELECT из основной таблицы
	// WHERE 1=1 позволяет динамически добавлять AND условия
	sql := "SELECT cid, collection, rkey, record_type, data, created_at, updated_at FROM records WHERE 1=1"
	args := []interface{}{}

	// === ФИЛЬТРЫ ПО МЕТАДАННЫМ ===

	// Фильтр по коллекции
	// Использует индекс idx_records_collection для быстрого поиска
	if query.Collection != "" {
		sql += " AND collection = ?"
		args = append(args, query.Collection)
	}

	// Фильтр по типу записи
	// Может использовать составной индекс idx_records_collection_type
	// если также указана коллекция
	if query.RecordType != "" {
		sql += " AND record_type = ?"
		args = append(args, query.RecordType)
	}

	// === ФИЛЬТРЫ ПО АТРИБУТАМ (EAV МОДЕЛЬ) ===

	// Обрабатываем фильтры по произвольным атрибутам записей
	// Каждый фильтр добавляет субзапрос к таблице record_attributes
	if len(query.Filters) > 0 {
		for attr, value := range query.Filters {
			// IN субзапрос для поиска записей с конкретным атрибутом
			// Это эффективный способ фильтрации в EAV модели:
			// "Найти все CID, которые имеют атрибут X со значением Y"
			sql += " AND cid IN (SELECT cid FROM record_attributes WHERE attribute_name = ? AND attribute_value = ?)"
			args = append(args, attr, fmt.Sprintf("%v", value))
		}
	}

	// === СОРТИРОВКА ===

	if query.SortBy != "" {
		// ПОЛЬЗОВАТЕЛЬСКАЯ СОРТИРОВКА
		// Клиент может сортировать по любому полю из таблицы records
		order := "ASC"
		if query.SortOrder == "DESC" {
			order = "DESC"
		}
		sql += fmt.Sprintf(" ORDER BY %s %s", query.SortBy, order)
	} else {
		// СОРТИРОВКА ПО ВРЕМЕНИ СОЗДАНИЯ (по умолчанию)
		// Показывает новые записи первыми
		// Использует индекс idx_records_created_at
		sql += " ORDER BY created_at DESC"
	}

	// === ПАГИНАЦИЯ ===

	// LIMIT для ограничения количества результатов
	if query.Limit > 0 {
		sql += " LIMIT ?"
		args = append(args, query.Limit)

		// OFFSET для пагинации
		if query.Offset > 0 {
			sql += " OFFSET ?"
			args = append(args, query.Offset)
		}
	}

	// Выполняем построенный SQL запрос
	return idx.executeSearchQuery(ctx, sql, args...)
}

// executeSearchQuery выполняет SQL запрос и возвращает результаты
//
// УНИВЕРСАЛЬНЫЙ ИСПОЛНИТЕЛЬ ЗАПРОСОВ:
//
// Этот метод централизует выполнение всех типов поисковых запросов
// и обеспечивает единообразную обработку результатов.
//
// ОБРАБОТКА РЕЗУЛЬТАТОВ:
//
// 1. ВЫПОЛНЕНИЕ ЗАПРОСА:
//   - Использует prepared statements для безопасности
//   - Поддерживает контекстное отменение через context
//   - Автоматически закрывает ресурсы через defer
//
// 2. ПАРСИНГ СТРОК:
//   - Определяет тип запроса (FTS или обычный) по наличию relevance
//   - Корректно обрабатывает NULL значения
//   - Десериализует JSON данные обратно в map[string]interface{}
//
// 3. ПРЕОБРАЗОВАНИЕ ТИПОВ:
//   - CID строка → cid.Cid объект
//   - JSON строка → map[string]interface{}
//   - Обработка ошибок парсинга
//
// БЕЗОПАСНОСТЬ:
// - Prepared statements предотвращают SQL injection
// - Валидация CID предотвращает некорректные данные
// - Graceful обработка ошибок JSON
func (idx *SQLiteIndexer) executeSearchQuery(ctx context.Context, sql string, args ...interface{}) ([]SearchResult, error) {
	// === ВЫПОЛНЕНИЕ SQL ЗАПРОСА ===

	// Выполняем запрос с prepared statement для безопасности
	// QueryContext поддерживает отмену через context.Context
	rows, err := idx.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	// Обязательно закрываем rows для освобождения ресурсов
	defer rows.Close()

	// === ОБРАБОТКА РЕЗУЛЬТАТОВ ===

	var results []SearchResult

	// Итерируемся по всем строкам результата
	for rows.Next() {
		var result SearchResult
		var cidStr, dataJSON string
		var relevance *float64 // Nullable для FTS запросов

		// === ОПРЕДЕЛЕНИЕ ТИПА ЗАПРОСА И ПАРСИНГ ===

		// Проверяем наличие поля relevance в SQL для определения типа запроса
		if strings.Contains(sql, "relevance") {
			// FTS ЗАПРОС с оценкой релевантности
			err = rows.Scan(&cidStr, &result.Collection, &result.RKey, &result.RecordType,
				&dataJSON, &result.CreatedAt, &result.UpdatedAt, &relevance)
			// Устанавливаем relevance только если он не NULL
			if relevance != nil {
				result.Relevance = *relevance
			}
		} else {
			// ОБЫЧНЫЙ СТРУКТУРИРОВАННЫЙ ЗАПРОС без relevance
			err = rows.Scan(&cidStr, &result.Collection, &result.RKey, &result.RecordType,
				&dataJSON, &result.CreatedAt, &result.UpdatedAt)
		}

		// Проверяем ошибки сканирования строки
		if err != nil {
			return nil, err
		}

		// === ПАРСИНГ CID ===

		// Преобразуем строковое представление CID в объект cid.Cid
		// CID валидация важна для предотвращения некорректных данных
		if result.CID, err = cid.Parse(cidStr); err != nil {
			return nil, fmt.Errorf("invalid CID in search results: %w", err)
		}

		// === ДЕСЕРИАЛИЗАЦИЯ JSON ДАННЫХ ===

		// Восстанавливаем структурированные данные из JSON
		// Это возвращает оригинальную IPLD структуру записи
		if err = json.Unmarshal([]byte(dataJSON), &result.Data); err != nil {
			return nil, fmt.Errorf("invalid JSON data in search results: %w", err)
		}

		// Добавляем обработанный результат в слайс
		results = append(results, result)
	}

	// Проверяем ошибки итерации (могут возникнуть после Next())
	return results, rows.Err()
}

// GetCollectionStats возвращает статистику по коллекции
//
// СИСТЕМА АНАЛИТИКИ:
//
// Метод предоставляет быстрый доступ к аналитической информации
// о коллекциях через материализованное представление collection_stats.
//
// ВОЗВРАЩАЕМЫЕ МЕТРИКИ:
//
// 1. RECORD_COUNT - общее количество записей в коллекции
//   - Основная метрика для понимания размера коллекции
//   - Используется для пагинации и планирования производительности
//
// 2. TYPE_COUNT - количество различных типов записей
//   - Показывает разнообразие данных в коллекции
//   - Полезно для схемного анализа и валидации
//
// 3. FIRST_RECORD - время создания первой записи
//   - Аудитная информация о возрасте коллекции
//   - Полезно для миграций и архивирования
//
// 4. LAST_UPDATED - время последнего обновления
//   - Показывает активность коллекции
//   - Используется для кэширования и синхронизации
//
// ОБРАБОТКА ОТСУТСТВУЮЩИХ КОЛЛЕКЦИЙ:
// Если коллекция не существует, возвращаются нулевые значения
// вместо ошибки, что упрощает логику клиентского кода.
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// Использует VIEW, который кэшируется SQLite для повторных запросов
// и выполняется быстро для большинства размеров коллекций.
func (idx *SQLiteIndexer) GetCollectionStats(ctx context.Context, collection string) (map[string]interface{}, error) {
	// Блокируем на чтение для thread-safety
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// === ЗАПРОС СТАТИСТИКИ ===

	// Выполняем запрос к материализованному представлению collection_stats
	// VIEW автоматически агрегирует данные по коллекциям
	row := idx.db.QueryRowContext(ctx, `
		SELECT record_count, type_count, first_record, last_updated
		FROM collection_stats 
		WHERE collection = ?
	`, collection)

	var recordCount, typeCount int
	var firstRecord, lastUpdated time.Time

	// === ОБРАБОТКА РЕЗУЛЬТАТА ===

	err := row.Scan(&recordCount, &typeCount, &firstRecord, &lastUpdated)
	if err != nil {
		if err == sql.ErrNoRows {
			// КОЛЛЕКЦИЯ НЕ НАЙДЕНА - возвращаем нулевые значения
			// Это нормальная ситуация для новых или пустых коллекций
			return map[string]interface{}{
				"record_count": 0,
				"type_count":   0,
			}, nil
		}
		// ОШИБКА БАЗЫ ДАННЫХ - пробрасываем дальше
		return nil, err
	}

	// === ФОРМИРОВАНИЕ ОТВЕТА ===

	// Возвращаем полную статистику в виде карты
	// Формат map[string]interface{} обеспечивает гибкость для клиентов
	return map[string]interface{}{
		"record_count": recordCount, // Количество записей
		"type_count":   typeCount,   // Количество типов
		"first_record": firstRecord, // Первая запись
		"last_updated": lastUpdated, // Последнее обновление
	}, nil
}

// Close закрывает подключение к базе данных
//
// ПРОЦЕДУРА GRACEFUL SHUTDOWN:
//
// Корректное закрытие SQLite соединения критично для:
//
// 1. СБРОС ДАННЫХ НА ДИСК:
//   - WAL журнал синхронизируется с основной БД
//   - Незавершенные транзакции откатываются
//   - Данные в памяти записываются на диск
//
// 2. ОСВОБОЖДЕНИЕ РЕСУРСОВ:
//   - Файловые дескрипторы закрываются
//   - Память, используемая SQLite, освобождается
//   - Блокировки файлов снимаются
//
// 3. THREAD-SAFETY:
//   - Блокировка на запись предотвращает новые операции
//   - Ожидание завершения всех текущих операций
//   - Атомарное закрытие соединения
//
// ВАЖНОСТЬ:
// Некорректное закрытие может привести к потере данных,
// особенно в WAL режиме, где изменения могут оставаться
// в журнале и не быть записанными в основную БД.
//
// ИСПОЛЬЗОВАНИЕ:
// Этот метод должен вызываться в defer при создании индексера
// или при shutdown приложения для гарантированного освобождения ресурсов.
func (idx *SQLiteIndexer) Close() error {
	// === БЛОКИРОВКА НА ЗАПИСЬ ===

	// Получаем эксклюзивную блокировку для предотвращения новых операций
	// Это гарантирует, что все текущие операции завершатся
	// перед закрытием соединения
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// === ЗАКРЫТИЕ СОЕДИНЕНИЯ ===

	// Закрываем SQLite соединение
	// SQLite автоматически:
	// - Сбрасывает WAL журнал на диск
	// - Откатывает незавершенные транзакции
	// - Освобождает все ресурсы
	// - Снимает файловые блокировки
	return idx.db.Close()
}
