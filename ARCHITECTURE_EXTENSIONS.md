# UES Architecture Extensions

Данный документ описывает три новых архитектурных компонента, добавленных в UES для решения выявленных ограничений.

## Проблемы и Решения

### 1. Отсутствие хранения файлов и бинарных объектов
**Проблема**: В репозитории не было возможности загружать и хранить файлы (изображения, документы, медиа).

**Решение**: `repository/blob_store.go` - BlobStore с автоматическим chunking

### 2. Обработка больших IPLD узлов
**Проблема**: IPFS блоки ограничены размером (~256KB), но IPLD узлы могут быть больше.

**Решение**: `blockstore/chunked_node.go` - ChunkedNodeStore с Merkle DAG структурой

### 3. Отсутствие operation log
**Проблема**: Нужна система записи всех операций для аудита и репликации.

**Решение**: `repository/operation_log.go` - OperationLog на уровне Repository

## Компоненты

### BlobStore - Хранение файлов

**Файл**: `repository/blob_store.go`

**Возможности**:
- Хранение файлов любого размера с автоматическим chunking
- Определение MIME типов
- Создание metadata записей в repository
- Интеграция с существующей системой collections

**Основные методы**:
```go
// Сохранение файла
func (bs *BlobStore) PutBlob(ctx context.Context, filename string, content io.Reader) (cid.Cid, *BlobMetadata, error)

// Получение файла
func (bs *BlobStore) GetBlob(ctx context.Context, contentCID cid.Cid) ([]byte, *BlobMetadata, error)
```

**Конфигурация**:
```go
type BlobConfig struct {
    ChunkSize    int    // Размер chunk'а (default: 256KB)
    MaxFileSize  int64  // Максимальный размер файла (default: 100MB)
    AutoMetadata bool   // Автоматическое создание metadata записей
    Collection   string // Коллекция для metadata (default: "blobs")
}
```

**Стратегии хранения**:
- Файлы < 256KB: сохраняются как single IPLD узел
- Файлы > 256KB: автоматический chunking с metadata о структуре

### ChunkedNodeStore - Большие IPLD узлы

**Файл**: `blockstore/chunked_node.go`

**Проблема**: IPFS блоки имеют ограничение на размер (обычно 256KB). Когда IPLD узел превышает этот размер, его нужно разбить на части.

**Решение**: Автоматический chunking с созданием Merkle DAG структуры:

```
Root Node:
{
  "type": "chunked_node",
  "original_size": 1048576,
  "chunks": [<link1>, <link2>, <link3>],
  "chunk_size": 262144,
  "content_type": "ipld" | "binary"
}
```

**Основные методы**:
```go
// Сохранение IPLD узла с автоматическим chunking
func (cns *ChunkedNodeStore) PutNode(ctx context.Context, node datamodel.Node) (cid.Cid, error)

// Сохранение больших binary данных
func (cns *ChunkedNodeStore) PutLargeBytes(ctx context.Context, data []byte) (cid.Cid, error)

// Получение узла с автоматической сборкой chunks
func (cns *ChunkedNodeStore) GetNode(ctx context.Context, c cid.Cid) (datamodel.Node, error)

// Проверка, является ли CID ссылкой на chunked узел
func (cns *ChunkedNodeStore) IsChunked(ctx context.Context, c cid.Cid) (bool, error)

// Информация о chunks
func (cns *ChunkedNodeStore) GetChunkInfo(ctx context.Context, c cid.Cid) (*ChunkInfo, error)
```

**Процесс работы**:
1. Автоматическое определение размера узла
2. Если размер < threshold: обычное сохранение
3. Если размер >= threshold: chunking + создание root узла
4. Прозрачная сборка при чтении

### OperationLog - Запись операций

**Файл**: `repository/operation_log.go`

**Архитектурное размещение**: Repository уровень - оптимальный баланс между semantic контекстом и performance.

**Альтернативы рассмотрены**:
- Datastore уровень: слишком низкий, нет semantic контекста
- Отдельный сервис: избыточно для single-node deployment
- Application уровень: может пропустить internal операции

**Структура записи**:
```go
type LogEntry struct {
    OperationID   string                 // Уникальный ID операции
    Timestamp     time.Time              // Время операции
    OperationType OperationType          // Тип операции
    Actor         string                 // DID пользователя
    Collection    string                 // Коллекция
    RKey          string                 // Record key
    RecordCID     *cid.Cid              // CID записи
    Metadata      map[string]interface{} // Дополнительные данные
    Result        string                 // "success" | "error"
    Error         string                 // Сообщение об ошибке
    Duration      time.Duration          // Продолжительность операции
}
```

**Типы операций**:
- `OpPutRecord` - создание/обновление записи
- `OpDeleteRecord` - удаление записи
- `OpPutCollection` - создание коллекции
- `OpDeleteCollection` - удаление коллекции
- `OpUpdateSchema` - обновление схемы
- `OpReindex` - переиндексация
- `OpMaintenance` - техническое обслуживание
- `OpReplication` - репликация

**Основные методы**:
```go
// Запись операции в log
func (ol *OperationLog) LogOperation(ctx context.Context, entry *LogEntry) error

// Поиск операций (TODO)
func (ol *OperationLog) QueryOperations(ctx context.Context, query *LogQuery) ([]*LogEntry, error)

// Streaming операций для real-time monitoring (TODO)
func (ol *OperationLog) GetOperationsStream(ctx context.Context) (<-chan *LogEntry, error)

// Cleanup старых записей (TODO)
func (ol *OperationLog) Cleanup(ctx context.Context) error
```

## Интеграция с Repository

Для полной интеграции OperationLog с Repository нужно добавить вызовы логирования в основные методы:

```go
func (r *Repository) PutRecord(ctx context.Context, collection, rkey string, node datamodel.Node) (cid.Cid, error) {
    start := time.Now()
    opID := generateOperationID()
    
    // Выполняем основную операцию
    recordCID, err := r.datastore.Put(ctx, ...)
    
    // Логируем операцию
    entry := &LogEntry{
        OperationID:   opID,
        Timestamp:     start,
        OperationType: OpPutRecord,
        Actor:         r.getCurrentActor(ctx),
        Collection:    collection,
        RKey:          rkey,
        RecordCID:     &recordCID,
        Duration:      time.Since(start),
        Result:        "success",
    }
    
    if err != nil {
        entry.Result = "error"
        entry.Error = err.Error()
    }
    
    if logErr := r.operationLog.LogOperation(ctx, entry); logErr != nil {
        // Логируем ошибку, но не прерываем основную операцию
        fmt.Printf("Failed to log operation: %v\n", logErr)
    }
    
    return recordCID, err
}
```

## Примеры использования

### 1. Загрузка файла
```go
// Создание BlobStore
blobStore := repository.NewBlobStore(blockstore, repository, nil)

// Загрузка файла
file := strings.NewReader("File content")
contentCID, metadata, err := blobStore.PutBlob(ctx, "document.txt", file)

// Получение файла
data, metadata, err := blobStore.GetBlob(ctx, contentCID)
```

### 2. Работа с большими IPLD узлами
```go
// Создание ChunkedNodeStore
chunkedStore := blockstore.NewChunkedNodeStore(blockstore, nil)

// Сохранение больших данных
largeBinaryData := make([]byte, 500*1024) // 500KB
contentCID, err := chunkedStore.PutLargeBytes(ctx, largeBinaryData)

// Получение с автоматической сборкой
node, err := chunkedStore.GetNode(ctx, contentCID)
retrievedData, err := node.AsBytes()
```

### 3. Настройка Operation Log
```go
// Создание с конфигурацией
config := &OperationLogConfig{
    Collection:      "audit_log",
    RetentionPeriod: 90 * 24 * time.Hour, // 90 days
    EnabledOps:      []string{"put_record", "delete_record"},
    StreamEnabled:   true,
}

opLog := repository.NewOperationLog(blockstore, config)

// Логирование операции
entry := &LogEntry{
    OperationID:   uuid.New().String(),
    Timestamp:     time.Now(),
    OperationType: OpPutRecord,
    Actor:         "did:plc:user123",
    Collection:    "app.bsky.feed.post",
    RKey:          "post_123",
    Result:        "success",
}

err := opLog.LogOperation(ctx, entry)
```

## Интегрированный сценарий

Пример полного workflow с использованием всех компонентов:

1. **Пользователь загружает изображение в пост**
2. **BlobStore**: сохраняет изображение с chunking (если нужно)
3. **Repository**: создает пост с ссылкой на изображение
4. **ChunkedNodeStore**: обрабатывает большой пост (если много изображений)
5. **OperationLog**: записывает всю операцию для аудита

## Статус реализации

✅ **BlobStore** - реализован, компилируется
✅ **ChunkedNodeStore** - реализован, компилируется  
✅ **OperationLog** - реализован, компилируется

### TODO для production версии:

**BlobStore**:
- Streaming upload для очень больших файлов
- Compression support
- Thumbnail generation для изображений
- Duplicate detection по хэшам

**ChunkedNodeStore**:
- Proper CBOR/DAG-CBOR serialization вместо временной реализации
- Optimization chunking алгоритмов
- Support для streaming assembly больших узлов

**OperationLog**:
- Query interface для поиска операций
- Real-time streaming для monitoring
- Async batch processing для performance
- Automatic cleanup по retention policies
- Export/import для архивирования

**Общее**:
- Integration tests для всех компонентов
- Performance benchmarks
- Monitoring и metrics
- Documentation и API specs

## Заключение

Все три выявленные архитектурные проблемы UES решены:

1. ✅ **Файлы и бинарные объекты**: BlobStore с chunking и metadata
2. ✅ **Большие IPLD узлы**: ChunkedNodeStore с Merkle DAG
3. ✅ **Operation log**: OperationLog на Repository уровне

Компоненты спроектированы для интеграции с существующей архитектурой UES и готовы к использованию в production после завершения TODO пунктов.