package entitystore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
	"ues/blockstore"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/multiformats/go-multihash"
	"lukechampine.com/blake3"
)

// Entity представляет базовую сущность в UES системе
// Все данные в UES представляются как сущности с метаданными
type Entity struct {
	// CID - Content Identifier, уникальный идентификатор сущности
	// Вычисляется от содержимого сущности (content-addressable)
	CID string `json:"cid"`
	// Type - тип сущности (например: "message", "user", "room", "file")
	Type string `json:"type"`
	// Data - основные данные сущности в формате IPLD
	Data datamodel.Node `json:"data"`
	// Metadata - метаданные для индексирования и поиска
	Metadata map[string]interface{} `json:"metadata"`
	// Signature - цифровая подпись сущности для верификации подлинности
	Signature []byte `json:"signature"`
	// CreatedAt - время создания сущности
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt - время последнего обновления сущности
	UpdatedAt time.Time `json:"updated_at"`
}

// UESStorageLevel1 представляет первый уровень архитектуры UES - уровень хранения данных
// Этот уровень отвечает за:
// 1. Хранение IPLD блоков в блок-сторе (BadgerDB)
// 2. Индексирование метаданных в реляционной БД (SQLite)
// 3. Криптографические операции (подписи, хеширование)
// 4. Базовые CRUD операции для работы с сущностями
type EntityStore struct {
	// blockStore - основное хранилище IPLD блоков, использует BadgerDB
	// Ключ: CID блока, Значение: сериализованные IPLD данные в CBOR
	blockStore blockstore.Blockstore
	// indexDB - база данных индексов для быстрого поиска по метаданным
	// Использует SQLite с оптимизированными индексами
	indexDB *sql.DB
	// privateKey - приватный ключ Ed25519 для подписи операций
	privateKey ed25519.PrivateKey
	// publicKey - публичный ключ Ed25519 для верификации подписей
	publicKey ed25519.PublicKey
}

// NewUESStorageLevel1 создает новый экземпляр уровня хранения данных
// dbPath - путь к директории для BadgerDB
// sqlitePath - путь к файлу SQLite базы данных
func NewEntityStore(bs blockstore.Blockstore, sqlitePath string) (*EntityStore, error) {

	// Открываем SQLite для индексов
	indexDB, err := sql.Open("sqlite3", sqlitePath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000")
	if err != nil {
		bs.Close()
		return nil, fmt.Errorf("failed to open SQLite: %w", err)
	}

	// Генерируем Ed25519 ключевую пару для подписей
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		bs.Close()
		indexDB.Close()
		return nil, fmt.Errorf("failed to generate Ed25519 keys: %w", err)
	}

	storage := &EntityStore{
		blockStore: bs,
		indexDB:    indexDB,
		privateKey: privateKey,
		publicKey:  publicKey,
	}

	// Инициализируем схему базы данных
	if err := storage.initializeSchema(); err != nil {
		storage.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	log.Printf("UES Storage Level 1 initialized successfully")
	log.Printf("Public Key: %x", publicKey)

	return storage, nil
}

// initializeSchema создает необходимые таблицы в SQLite для индексирования
func (s *EntityStore) initializeSchema() error {

	// Таблица сущностей с индексами для быстрого поиска
	entitiesSchema := `
	CREATE TABLE IF NOT EXISTS entities (
		cid TEXT PRIMARY KEY,           -- Content Identifier сущности
		type TEXT NOT NULL,             -- Тип сущности
		metadata TEXT,                  -- JSON метаданные для поиска
		signature BLOB,                 -- Цифровая подпись
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	
	-- Индекс по типу сущности для быстрой фильтрации
	CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(type);
	
	-- Индекс по времени создания для хронологического поиска
	CREATE INDEX IF NOT EXISTS idx_entities_created ON entities(created_at);
	
	-- Составной индекс для комбинированных запросов
	CREATE INDEX IF NOT EXISTS idx_entities_type_created ON entities(type, created_at);
	`

	// Таблица операций для отслеживания изменений
	operationsSchema := `
	CREATE TABLE IF NOT EXISTS operations (
		id TEXT PRIMARY KEY,            -- Уникальный ID операции
		type TEXT NOT NULL,             -- Тип операции
		entity_cid TEXT,                -- CID целевой сущности
		payload TEXT,                   -- JSON данные операции
		author_did TEXT,                -- DID автора операции
		signature BLOB,                 -- Подпись операции
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		
		FOREIGN KEY (entity_cid) REFERENCES entities(cid)
	);
	
	-- Индекс по CID сущности для поиска операций
	CREATE INDEX IF NOT EXISTS idx_operations_entity ON operations(entity_cid);
	
	-- Индекс по автору операций
	CREATE INDEX IF NOT EXISTS idx_operations_author ON operations(author_did);
	
	-- Индекс по времени для хронологии операций
	CREATE INDEX IF NOT EXISTS idx_operations_timestamp ON operations(timestamp);
	`

	// Выполняем создание схемы
	if _, err := s.indexDB.Exec(entitiesSchema); err != nil {
		return fmt.Errorf("failed to create entities schema: %w", err)
	}

	if _, err := s.indexDB.Exec(operationsSchema); err != nil {
		return fmt.Errorf("failed to create operations schema: %w", err)
	}

	return nil
}

// StoreEntity сохраняет сущность в блок-сторе и создает индекс
func (s *EntityStore) StoreEntity(ctx context.Context, entity *Entity) error {


	s.blockStore.PutNode(ctx, entity.Data)


	// Сериализуем данные сущности в CBOR формат для IPLD
	// define a buffer to hold the serialized data
	var data bytes.Buffer
	if err := dagcbor.Encode(entity.Data, bufio.NewWriter(&data)); err != nil {
		return fmt.Errorf("failed to encode entity data: %w", err)
	}

	// Вычисляем BLAKE3 хеш от сериализованных данных
	hasher := blake3.New(32, nil)
	hasher.Write(data.Bytes())
	hash := hasher.Sum(nil)

	// Создаем Multihash для IPLD совместимости
	mh, err := multihash.Encode(hash, multihash.BLAKE3)
	if err != nil {
		return fmt.Errorf("failed to create multihash: %w", err)
	}

	// Генерируем CID v1 с CBOR кодеком
	c := cid.NewCidV1(cid.DagCBOR, mh)
	entity.CID = c.String()

	// Подписываем сущность нашим приватным ключом
	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return fmt.Errorf("failed to marshal entity: %w", err)
	}

	entity.Signature = ed25519.Sign(s.privateKey, entityBytes)

	// Сохраняем в блок-сторе (BadgerDB)
	err = s.blockStore.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(entity.CID), serializedData)
	})
	if err != nil {
		return fmt.Errorf("failed to store in block store: %w", err)
	}

	// Сериализуем метаданные для индекса
	metadataJSON, err := json.Marshal(entity.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Сохраняем индекс в SQLite
	_, err = s.indexDB.ExecContext(ctx,
		`INSERT OR REPLACE INTO entities 
		(cid, type, metadata, signature, created_at, updated_at) 
		VALUES (?, ?, ?, ?, ?, ?)`,
		entity.CID, entity.Type, string(metadataJSON), entity.Signature,
		entity.CreatedAt, entity.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to store in index: %w", err)
	}

	log.Printf("Stored entity %s of type %s", entity.CID, entity.Type)
	return nil
}

// Close закрывает все соединения с базами данных
func (s *EntityStore) Close() error {
	var errors []error

	if err := s.blockStore.Close(); err != nil {
		errors = append(errors, fmt.Errorf("failed to close BadgerDB: %w", err))
	}

	if err := s.indexDB.Close(); err != nil {
		errors = append(errors, fmt.Errorf("failed to close SQLite: %w", err))
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors closing storage: %v", errors)
	}

	log.Printf("UES Storage Level 1 closed successfully")
	return nil
}
