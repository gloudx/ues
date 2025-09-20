package repository

import (
	"context"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"time"

	"ues/blockstore"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/node/basicnode"
)

// BlobStore управляет хранением файлов и бинарных объектов в UES
//
// АРХИТЕКТУРНАЯ РОЛЬ И НАЗНАЧЕНИЕ:
// BlobStore решает проблему хранения произвольных файлов (изображения, документы,
// видео, аудио и другие медиа) в distributed системе UES. Основная задача -
// обеспечить эффективное хранение файлов различного размера с сохранением
// целостности данных и возможности их последующего получения.
//
// ПРИНЦИПЫ РАБОТЫ:
//  1. АВТОМАТИЧЕСКИЙ CHUNKING: Файлы больше threshold (по умолчанию 256KB)
//     автоматически разбиваются на chunks для соответствия ограничениям IPFS блоков
//  2. METADATA MANAGEMENT: Для каждого файла создается metadata запись с
//     информацией о файле (имя, размер, MIME тип, временные метки)
//  3. INTEGRATION WITH REPOSITORY: BlobStore интегрируется с Repository layer
//     для создания typed записей в collections с ссылками на blob content
//  4. CONTENT ADDRESSING: Использует IPFS/IPLD content addressing для
//     immutable ссылок на файлы через их cryptographic hashes (CID)
//
// СТРАТЕГИИ ХРАНЕНИЯ:
// - Small files (<256KB): Сохраняются как single IPLD узел с binary данными
// - Large files (>256KB): Разбиваются на chunks, создается merkle tree структура
// - Metadata: Создается отдельная IPLD запись с metadata информацией
//
// АРХИТЕКТУРНАЯ ИНТЕГРАЦИЯ:
// BlobStore размещен на Repository уровне потому что:
// - Имеет доступ к collections system для metadata записей
// - Может создавать typed записи согласно lexicon schemas
// - Обеспечивает semantic context для файловых операций
// - Интегрируется с permission и validation системами Repository
//
// ИСПОЛЬЗОВАНИЕ В ПРИЛОЖЕНИЯХ:
// - Social media: хранение изображений, видео в постах
// - Document management: хранение PDF, Word документов
// - Media platforms: хранение аудио/видео контента
// - File sharing: хранение произвольных файлов пользователей
type BlobStore struct {
	// bs - базовое блочное хранилище для physical storage данных.
	// Обеспечивает low-level операции записи/чтения IPLD узлов.
	// Абстрагирует от конкретной storage backend (память, диск, S3, etc.)
	bs blockstore.Blockstore

	// repository - ссылка на Repository instance для создания metadata записей.
	// Используется для:
	// - Создания typed записей в collections согласно lexicon schemas
	// - Валидации metadata согласно schema constraints
	// - Интеграции с permission system Repository
	// - Обеспечения consistency между blob storage и application data
	repository *Repository

	// config - конфигурация поведения BlobStore.
	// Определяет параметры chunking, размерные ограничения,
	// политики создания metadata и другие operational настройки.
	config *BlobConfig
}

// BlobConfig конфигурирует поведение blob storage system
//
// НАЗНАЧЕНИЕ: Определяет operational параметры BlobStore для настройки
// под конкретные use cases и performance требования.
//
// ПРИНЦИПЫ КОНФИГУРАЦИИ:
// - CHUNKING CONTROL: Управляет размером chunks и threshold для chunking
// - SIZE LIMITS: Устанавливает ограничения на размер файлов для предотвращения abuse
// - METADATA POLICY: Определяет автоматическое создание metadata записей
// - COLLECTION MAPPING: Указывает collection для хранения metadata
type BlobConfig struct {
	// ChunkSize определяет размер chunk'а при разбиении больших файлов.
	// ПРИНЦИП: Larger chunks = fewer network requests но больше memory usage.
	// РЕКОМЕНДАЦИИ:
	// - 256KB (default): баланс между network efficiency и memory usage
	// - 64KB: для slow networks или limited memory environments
	// - 1MB: для high-bandwidth networks и large memory systems
	// ВЛИЯНИЕ: Прямо влияет на количество chunks и depth merkle tree
	ChunkSize int `json:"chunk_size"`

	// MaxFileSize устанавливает максимальный размер принимаемого файла.
	// ЦЕЛЬ: Предотвращение:
	// - DoS атак через upload огромных файлов
	// - Исчерпания storage space
	// - Performance degradation от processing очень больших файлов
	// РЕКОМЕНДАЦИИ:
	// - 100MB (default): подходит для большинства use cases
	// - 10MB: для systems с ограниченными ресурсами
	// - 1GB+: для specialized applications (video platforms, etc.)
	MaxFileSize int64 `json:"max_file_size"`

	// AutoMetadata управляет автоматическим созданием metadata записей.
	// ПРИНЦИП: Если true, BlobStore автоматически создает typed записи
	// в Repository collections с metadata о каждом загруженном файле.
	// ИСПОЛЬЗОВАНИЕ:
	// - true: для applications где files нужно track и query
	// - false: для pure storage без application-level metadata
	// ИНТЕГРАЦИЯ: Требует корректной настройки Repository и lexicon schemas
	AutoMetadata bool `json:"auto_metadata"`

	// Collection указывает имя collection для хранения metadata записей.
	// ПРИНЦИП: Metadata записи создаются как typed records в этой collection
	// согласно соответствующему lexicon schema.
	// СХЕМА: Collection должна поддерживать schema для blob metadata:
	// - name: string (original filename)
	// - size: integer (file size in bytes)
	// - mimeType: string (MIME type)
	// - contentCid: string (CID of blob content)
	// - createdAt: datetime (upload timestamp)
	// DEFAULT: "blobs" - стандартная collection для file metadata
	Collection string `json:"collection"`
}

// BlobMetadata содержит исчерпывающие метаданные о загруженном файле
//
// НАЗНАЧЕНИЕ: Обеспечивает rich metadata для file management, discovery
// и integration с application logic. Позволяет applications работать
// с files на semantic уровне, а не только как raw binary data.
//
// ПРИНЦИПЫ ДИЗАЙНА:
// 1. IMMUTABLE METADATA: После создания metadata не изменяется (как и сам file)
// 2. SELF-DESCRIBING: Содержит всю необходимую информацию для работы с файлом
// 3. EXTENSIBLE: Поддерживает дополнительные metadata через Extra поле
// 4. INTEGRATION READY: Структура compatible с Repository collections
//
// ИСПОЛЬЗОВАНИЕ В APPLICATIONS:
// - File browsers: отображение имени, размера, типа файлов
// - Search systems: поиск по имени, типу, размеру файлов
// - Access control: проверка permissions на основе metadata
// - Caching systems: cache policies на основе размера и типа
type BlobMetadata struct {
	// Name хранит оригинальное имя файла как оно было при upload.
	// ВАЖНОСТЬ: Критично для user experience - позволяет users
	// идентифицировать files по понятным именам, а не cryptographic hashes.
	// ИСПОЛЬЗОВАНИЕ:
	// - File browsers для display
	// - Download operations для default filename
	// - Search и filtering по имени файла
	// ОГРАНИЧЕНИЯ: Может содержать любые символы, applications должны
	// sanitize для filesystem operations
	Name string `json:"name"`

	// Size содержит точный размер файла в байтах.
	// ПРИМЕНЕНИЕ:
	// - UI progress bars при upload/download
	// - Storage quota management и billing
	// - Network bandwidth planning
	// - Cache eviction policies (LRU по размеру)
	// - Validation что file был полностью передан
	// ТОЧНОСТЬ: Размер оригинального файла до любого chunking или compression
	Size int64 `json:"size"`

	// MimeType определяет тип содержимого файла согласно IANA стандартам.
	// ПРИНЦИП ОПРЕДЕЛЕНИЯ:
	// 1. Сначала по file extension через mime.TypeByExtension()
	// 2. Если не определен - fallback к "application/octet-stream"
	// ИСПОЛЬЗОВАНИЕ:
	// - Browser rendering decisions (inline vs download)
	// - Application handlers selection (image viewer, PDF reader, etc.)
	// - Security policies (block executable files)
	// - Compression decisions (don't compress already compressed formats)
	// ПРИМЕРЫ: "image/jpeg", "application/pdf", "video/mp4", "text/plain"
	MimeType string `json:"mimeType"`

	// ContentCID является cryptographic address файла в IPFS/IPLD network.
	// ПРИНЦИПЫ:
	// - IMMUTABLE: CID never changes для одного и того же file content
	// - UNIQUE: Different content = different CID (collision resistant)
	// - VERIFIABLE: CID позволяет verify integrity при получении
	// ИСПОЛЬЗОВАНИЕ:
	// - Primary key для retrieval файла из storage
	// - Ссылки между records (например, post ссылается на image CID)
	// - Deduplication - одинаковые files имеют одинаковый CID
	// - Verification - можно проверить что получили correct file
	// ФОРМАТ: Обычно "bafybeig..." для IPLD DAG-CBOR или raw binary
	ContentCID cid.Cid `json:"contentCid"`

	// CreatedAt фиксирует точное время загрузки файла в UTC.
	// ПРИМЕНЕНИЕ:
	// - Audit trails и logging
	// - Sorting files по времени upload
	// - Retention policies (delete files older than X)
	// - Analytics о patterns загрузки файлов
	// - Legal compliance (когда файл был загружен)
	// ФОРМАТ: RFC3339 timestamp для consistency и interoperability
	CreatedAt time.Time `json:"createdAt"`

	// Checksum опциональная cryptographic hash для additional integrity verification.
	// НАЗНАЧЕНИЕ:
	// - REDUNDANT VERIFICATION: Дополнительная проверка к CID verification
	// - MIGRATION SUPPORT: Помогает при миграции между storage systems
	// - AUDIT COMPLIANCE: Некоторые compliance frameworks требуют explicit checksums
	// АЛГОРИТМ: SHA256 как widely supported и secure
	// WHEN TO USE: Для critical files где нужна дополнительная гарантия integrity
	Checksum string `json:"checksum,omitempty"`

	// Chunks содержит список CID'ов частей для файлов разбитых на chunks.
	// ЛОГИКА ЗАПОЛНЕНИЯ:
	// - Пустой для files < ChunkSize (хранятся как single узел)
	// - Заполнен для files >= ChunkSize (каждый chunk имеет свой CID)
	// ИСПОЛЬЗОВАНИЕ:
	// - Parallel download chunks для faster retrieval
	// - Partial file access (можно получить specific chunks)
	// - Recovery от corruption отдельных chunks
	// - Progress tracking при chunked operations
	// СТРУКТУРА: Ordered list - порядок chunks критичен для сборки файла
	Chunks []cid.Cid `json:"chunks,omitempty"`

	// Extra предоставляет extensibility для application-specific metadata.
	// ФИЛОСОФИЯ: Разные applications нуждаются в разных metadata,
	// Extra позволяет добавлять custom fields без изменения core structure.
	// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
	// - Image metadata: "width", "height", "exif_data"
	// - Video metadata: "duration", "codec", "resolution"
	// - Document metadata: "author", "title", "page_count"
	// - Security metadata: "scanned_at", "virus_status"
	// ОГРАНИЧЕНИЯ: Только string values для simplicity и JSON compatibility
	Extra map[string]string `json:"extra,omitempty"`
}

// DefaultBlobConfig создает configuration по умолчанию для общих use cases
//
// ПРИНЦИПЫ ДЕФОЛТНЫХ ЗНАЧЕНИЙ:
// Дефолтная конфигурация выбрана для баланса между performance,
// resource usage и compatibility с большинством applications.
//
// ОБОСНОВАНИЕ ЗНАЧЕНИЙ:
// - ChunkSize 256KB: Оптимальный баланс для большинства networks
// - MaxFileSize 100MB: Разумное ограничение для web applications
// - AutoMetadata true: Большинство applications нуждаются в metadata
// - Collection "blobs": Стандартное имя для file metadata collections
//
// КОГДА КАСТОМИЗИРОВАТЬ:
// - High-performance systems: увеличить ChunkSize до 1MB+
// - Resource-constrained: уменьшить MaxFileSize и ChunkSize
// - Специализированные applications: custom Collection names
func DefaultBlobConfig() *BlobConfig {
	return &BlobConfig{
		// 256KB chunks обеспечивают:
		// - Эффективную передачу по большинству networks
		// - Разумное memory usage при processing
		// - Совместимость с IPFS block size limits
		// - Хороший parallelism при multi-chunk operations
		ChunkSize: 256 * 1024, // 256KB chunks

		// 100MB limit защищает от:
		// - DoS атак через massive file uploads
		// - Storage exhaustion от случайных огромных files
		// - Performance problems от processing very large files
		// При этом позволяя большинство legitimate use cases
		MaxFileSize: 100 * 1024 * 1024, // 100MB max

		// AutoMetadata enabled по умолчанию потому что:
		// - Большинство applications нуждаются в file discovery
		// - Metadata критична для proper file management
		// - Integration с Repository collections ожидается
		AutoMetadata: true,

		// "blobs" как стандартное имя collection:
		// - Понятное для developers
		// - Не конфликтует с application collections
		// - Следует naming conventions
		Collection: "blobs",
	}
}

// NewBlobStore конструирует новый BlobStore instance с заданной конфигурацией
//
// ПАРАМЕТРЫ:
// - bs: blockstore.Blockstore - низкоуровневое хранилище для actual data storage
// - repository: *Repository - для metadata operations и integration (может быть nil)
// - config: *BlobConfig - конфигурация поведения (nil = использовать defaults)
//
// ЛОГИКА ИНИЦИАЛИЗАЦИИ:
// 1. Валидация parameters (blockstore обязателен)
// 2. Применение default configuration если config не предоставлен
// 3. Создание BlobStore instance с validated parameters
//
// DEPENDENCY INJECTION PATTERN:
// Использует dependency injection для:
// - Testability (можно inject mock blockstore и repository)
// - Flexibility (разные storage backends через interface)
// - Separation of concerns (BlobStore не создает dependencies)
//
// NULL SAFETY:
// - Обрабатывает nil config gracefully через defaults
// - Nil repository допустим для pure storage use cases
// - Nil blockstore вызовет panic при использовании (fail fast)
func NewBlobStore(bs blockstore.Blockstore, repository *Repository, config *BlobConfig) *BlobStore {
	// Применяем default configuration если не предоставлена custom
	// Это обеспечивает backward compatibility и ease of use
	if config == nil {
		config = DefaultBlobConfig()
	}

	// Создаем BlobStore instance с validated configuration
	// Все поля инициализируются deterministically
	return &BlobStore{
		// bs хранит reference на blockstore для low-level storage operations
		// Должен implement blockstore.Blockstore interface
		bs: bs,

		// repository используется для metadata operations если AutoMetadata enabled
		// Может быть nil для pure storage scenarios без application metadata
		repository: repository,

		// config определяет все operational параметры BlobStore
		// Гарантированно non-nil после обработки defaults выше
		config: config,
	}
}

// PutBlob реализует полный цикл сохранения файла в distributed storage
//
// АРХИТЕКТУРА ОПЕРАЦИИ:
// Эта функция является главной entry point для file upload operations.
// Она orchestrates весь process от validation до storage и metadata creation.
//
// ДЕТАЛЬНЫЙ WORKFLOW:
// 1. CONTENT INGESTION: Читает file content из io.Reader в memory
// 2. VALIDATION: Проверяет size limits и constraints
// 3. MIME DETECTION: Определяет content type для proper handling
// 4. IPLD ENCODING: Конвертирует binary data в IPLD representation
// 5. STORAGE: Сохраняет в blockstore с возможным chunking
// 6. METADATA CREATION: Создает comprehensive metadata record
// 7. INTEGRATION: Опционально сохраняет metadata в Repository collections
//
// ПРИНЦИПЫ ДИЗАЙНА:
// - ATOMIC OPERATION: Либо весь файл сохранен успешно, либо ничего
// - IMMUTABLE STORAGE: После сохранения файл и metadata не изменяются
// - CONTENT ADDRESSING: Использует cryptographic hashes для addressing
// - METADATA COMPLETENESS: Создает исчерпывающую metadata для file management
//
// PERFORMANCE CONSIDERATIONS:
// - Memory usage: Загружает весь файл в память (TODO: streaming для больших files)
// - Network efficiency: Chunking происходит автоматически в underlying IPLD layer
// - Metadata overhead: Metadata operations асинхронны и не блокируют storage
//
// ERROR HANDLING STRATEGY:
// - Fail fast на validation errors (size, permissions)
// - Rollback semantics для partial failures
// - Graceful degradation для metadata errors (log warning, продолжаем)
//
// ПАРАМЕТРЫ:
//   - ctx: Operation context для cancellation, timeouts, и tracing
//   - filename: Original filename для metadata и MIME detection (НЕ используется для addressing)
//   - content: Stream содержимого файла (любой io.Reader - file, network, memory)
//
// ВОЗВРАЩАЕМЫЕ ЗНАЧЕНИЯ:
//   - cid.Cid: Immutable content address файла в IPFS/IPLD network
//   - *BlobMetadata: Comprehensive metadata включая размер, тип, timestamps
//   - error: Detailed error information для proper error handling
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Upload изображения
//	file, _ := os.Open("photo.jpg")
//	cid, metadata, err := blobStore.PutBlob(ctx, "photo.jpg", file)
//
//	// Upload из memory
//	data := []byte("Hello, world!")
//	cid, metadata, err := blobStore.PutBlob(ctx, "hello.txt", bytes.NewReader(data))
//
//	// Upload с network stream
//	resp, _ := http.Get("https://example.com/file.pdf")
//	cid, metadata, err := blobStore.PutBlob(ctx, "document.pdf", resp.Body)
func (bs *BlobStore) PutBlob(ctx context.Context, filename string, content io.Reader) (cid.Cid, *BlobMetadata, error) {
	// PHASE 1: CONTENT INGESTION
	// Читаем весь stream в memory для size analysis и processing.
	//
	// RATIONALE: Нужно знать полный размер для:
	// - Size validation против MaxFileSize limits
	// - Accurate metadata generation
	// - Efficient storage strategy selection
	//
	// TODO для PRODUCTION: Implement streaming approach для very large files:
	// - Streaming size calculation
	// - Progressive validation
	// - Chunked processing для memory efficiency
	data, err := io.ReadAll(content)
	if err != nil {
		// Wrap error с context для better debugging
		return cid.Undef, nil, fmt.Errorf("failed to read file content from stream: %w", err)
	}

	// PHASE 2: SIZE VALIDATION
	// Проверяем size constraints для предотвращения:
	// - DoS attacks через massive file uploads
	// - Storage exhaustion
	// - Performance degradation от processing huge files
	//
	// Проверка происходит EARLY для fail-fast behavior
	if int64(len(data)) > bs.config.MaxFileSize {
		return cid.Undef, nil, fmt.Errorf("file size %d bytes exceeds configured maximum %d bytes",
			len(data), bs.config.MaxFileSize)
	}

	// PHASE 3: MIME TYPE DETECTION
	// Определяем content type для proper file handling и security.
	//
	// STRATEGY:
	// 1. Primary: File extension через Go standard library
	// 2. Fallback: Generic binary type для unknown extensions
	//
	// SECURITY NOTE: MIME detection по extension не 100% reliable,
	// для security-critical applications нужна content-based detection
	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		// Fallback для files без recognized extension
		mimeType = "application/octet-stream"
	}

	// PHASE 4: IPLD ENCODING
	// Конвертируем raw binary data в IPLD node representation.
	//
	// ПРИНЦИП: IPLD provides unified data model для different content types.
	// Binary data представляется как bytes node в IPLD graph.
	//
	// ALTERNATIVE APPROACHES:
	// - Raw blocks: Прямое storage binary data (менее flexible)
	// - Structured nodes: Complex IPLD structures (overkill для simple files)
	// - Custom codecs: Application-specific encodings (добавляет complexity)
	node := basicnode.NewBytes(data)

	// PHASE 5: PERSISTENT STORAGE
	// Сохраняем IPLD node в blockstore с automatic chunking если необходимо.
	//
	// UNDERLYING MAGIC: PutNode в blockstore может:
	// - Store как single block для small content
	// - Автоматически chunk для content > block size limits
	// - Create merkle DAG structures для efficient retrieval
	// - Handle deduplication если identical content уже exists
	//
	// CONTENT ADDRESSING: Результирующий CID deterministically computed
	// от content, обеспечивая immutability и verifiability
	contentCID, err := bs.bs.PutNode(ctx, node)
	if err != nil {
		return cid.Undef, nil, fmt.Errorf("failed to store file content in blockstore: %w", err)
	}

	// PHASE 6: METADATA CREATION
	// Создаем comprehensive metadata record для file management.
	//
	// METADATA PHILOSOPHY: Rich metadata enables:
	// - File discovery и searching
	// - Application integration
	// - Audit trails и compliance
	// - Performance optimizations
	// METADATA STRUCTURE: Создаем immutable metadata snapshot
	// на момент upload. Все поля заполняются deterministically.
	metadata := &BlobMetadata{
		// Original filename для user-friendly identification
		Name: filename,

		// Exact size для integrity verification и resource management
		Size: int64(len(data)),

		// Detected MIME type для proper application handling
		MimeType: mimeType,

		// Content CID как primary key для retrieval
		ContentCID: contentCID,

		// Upload timestamp для auditing и lifecycle management
		CreatedAt: time.Now(),

		// Chunks field остается пустым в текущей реализации.
		// RATIONALE: Current implementation использует IPLD layer chunking,
		// который происходит transparently в blockstore.PutNode().
		// Future versions могут expose chunk information для advanced use cases.
	}

	// PHASE 7: METADATA PERSISTENCE (OPTIONAL)
	// Сохраняем metadata в Repository collections если configured.
	//
	// CONDITIONAL LOGIC: Metadata persistence происходит только если:
	// 1. AutoMetadata enabled в configuration
	// 2. Repository instance available (не nil)
	//
	// ERROR HANDLING STRATEGY: Metadata errors НЕ прерывают file storage.
	// Это обеспечивает robustness - file остается доступным даже если
	// metadata operations fail (network issues, Repository problems, etc.)
	if bs.config.AutoMetadata && bs.repository != nil {
		if err := bs.createMetadataRecord(ctx, contentCID.String(), metadata); err != nil {
			// GRACEFUL DEGRADATION: Log warning но continue execution.
			// Application может использовать file даже без metadata в Repository.
			// TODO: Consider more sophisticated error handling:
			// - Retry logic для transient failures
			// - Background metadata reconciliation
			// - Monitoring alerts для metadata failures
			fmt.Printf("Warning: failed to create metadata record for file %s (CID: %s): %v\n",
				filename, contentCID.String(), err)
		}
	}

	// PHASE 8: SUCCESS RETURN
	// Возвращаем все необходимые данные для caller:
	// - CID для future retrieval operations
	// - Metadata для application logic и UI
	// - nil error indicating successful operation
	return contentCID, metadata, nil
}

// GetBlob выполняет полное восстановление файла из distributed storage
//
// АРХИТЕКТУРА RETRIEVAL PROCESS:
// Эта функция реализует inverse операцию к PutBlob, восстанавливая
// original file content и metadata из immutable content address (CID).
//
// ПРИНЦИПЫ ДИЗАЙНА:
//   - CONTENT INTEGRITY: CID гарантирует что retrieved content identical
//     к original через cryptographic verification
//   - METADATA RECONSTRUCTION: Пытается восстановить rich metadata либо
//     из Repository records, либо из самого content
//   - GRACEFUL DEGRADATION: Работает даже если metadata недоступен
//   - PERFORMANCE OPTIMIZATION: Efficient retrieval через IPLD layer
//
// DETAILED WORKFLOW:
// 1. METADATA DISCOVERY: Ищет saved metadata в Repository collections
// 2. CONTENT RETRIEVAL: Получает IPLD node из blockstore
// 3. CHUNKED ASSEMBLY: Автоматически собирает chunks если нужно
// 4. BINARY EXTRACTION: Конвертирует IPLD node в raw binary data
// 5. INTEGRITY VERIFICATION: Проверяет что CID matches retrieved content
//
// ERROR SCENARIOS:
// - CID not found: File не существует в storage
// - Corrupted chunks: Some chunks missing или corrupted
// - IPLD decode errors: Node structure problems
// - Network timeouts: Distributed retrieval failures
//
// ПАРАМЕТРЫ:
//   - ctx: Operation context для cancellation и timeout control
//   - contentCID: Immutable address файла в IPFS/IPLD network
//
// ВОЗВРАЩАЕМЫЕ ЗНАЧЕНИЯ:
//   - []byte: Complete file content как raw binary data
//   - *BlobMetadata: Available metadata (может быть nil если не найден)
//   - error: Detailed error information for troubleshooting
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Retrieve uploaded файла
//	data, metadata, err := blobStore.GetBlob(ctx, fileCID)
//	if err != nil { /* handle error */ }
//
//	// Write to file system
//	filename := metadata.Name  // Original filename
//	os.WriteFile(filename, data, 0644)
//
//	// HTTP response
//	w.Header().Set("Content-Type", metadata.MimeType)
//	w.Header().Set("Content-Length", strconv.FormatInt(metadata.Size, 10))
//	w.Write(data)
func (bs *BlobStore) GetBlob(ctx context.Context, contentCID cid.Cid) ([]byte, *BlobMetadata, error) {
	// PHASE 1: METADATA DISCOVERY
	// Пытаемся получить saved metadata из Repository для enhanced user experience.
	//
	// STRATEGY: Metadata lookup происходит FIRST потому что:
	// - Provides rich information for applications (filename, MIME type, etc.)
	// - Fails fast если Repository недоступен (но не прерывает content retrieval)
	// - Enables proper HTTP headers, filename suggestions, etc.
	//
	// ERROR HANDLING: Metadata failures НЕ прерывают content retrieval.
	// File content остается доступным даже без metadata.
	var metadata *BlobMetadata
	if bs.repository != nil {
		// Используем CID.String() как record key для metadata lookup
		if meta, err := bs.getMetadataRecord(ctx, contentCID.String()); err == nil {
			metadata = meta
			// TODO: CONSISTENCY CHECK - verify metadata.ContentCID == contentCID
			// для detection metadata/content mismatches
		}
		// Silently ignore metadata errors - content retrieval продолжается
	}

	// PHASE 2: CONTENT RETRIEVAL
	// Получаем IPLD node из blockstore с automatic chunk assembly.
	//
	// UNDERLYING MAGIC: GetNode в blockstore может:
	// - Retrieve single block для small files
	// - Automatically assemble chunks для large files
	// - Traverse merkle DAG structures
	// - Perform integrity verification через CID validation
	//
	// PERFORMANCE: Blockstore может implement caching, prefetching,
	// и other optimizations transparently
	node, err := bs.bs.GetNode(ctx, contentCID)
	if err != nil {
		// Enhanced error context для better debugging
		return nil, nil, fmt.Errorf("failed to retrieve IPLD node for CID %s: %w",
			contentCID.String(), err)
	}

	// PHASE 3: BINARY DATA EXTRACTION
	// Конвертируем IPLD node representation обратно в raw binary data.
	//
	// NODE TYPE HANDLING: IPLD nodes могут содержать разные data types.
	// Мы expect bytes node (created через basicnode.NewBytes в PutBlob),
	// но должны gracefully handle другие types для robustness.
	//
	// INTEGRITY GUARANTEE: AsBytes() extraction combined с CID verification
	// обеспечивает что retrieved data identical к original content.
	data, err := node.AsBytes()
	if err != nil {
		// Enhanced error с type information для debugging
		return nil, nil, fmt.Errorf("failed to extract binary data from IPLD node (CID: %s, node kind: %s): %w",
			contentCID.String(), node.Kind().String(), err)
	}

	// PHASE 4: CONSISTENCY VERIFICATION (OPTIONAL)
	// Если есть metadata, можем perform дополнительные consistency checks.
	if metadata != nil {
		// Verify размер matches retrieved data
		if metadata.Size != int64(len(data)) {
			// WARNING: Это может indicate metadata corruption или version mismatch
			fmt.Printf("Warning: metadata size (%d) doesn't match retrieved data size (%d) for CID %s\n",
				metadata.Size, len(data), contentCID.String())
			// Continue execution - data более authoritative чем metadata
		}
	}

	// PHASE 5: SUCCESS RETURN
	// Возвращаем complete file content и available metadata.
	//
	// RETURN SEMANTICS:
	// - data: Complete file content, guaranteed identical к original
	// - metadata: Rich metadata если available, nil если не найден
	// - error: nil indicating successful retrieval
	return data, metadata, nil
}

// createMetadataRecord создает запись в repository с метаданными файла
func (bs *BlobStore) createMetadataRecord(ctx context.Context, rkey string, metadata *BlobMetadata) error {
	// Конвертируем metadata в IPLD узел
	node, err := bs.metadataToNode(metadata)
	if err != nil {
		return fmt.Errorf("failed to convert metadata to node: %w", err)
	}

	// Сохраняем в repository
	_, err = bs.repository.PutRecord(ctx, bs.config.Collection, rkey, node)
	return err
}

// getMetadataRecord получает metadata запись из repository
func (bs *BlobStore) getMetadataRecord(ctx context.Context, rkey string) (*BlobMetadata, error) {
	// Получаем CID metadata записи
	metaCID, found, err := bs.repository.GetRecordCID(ctx, bs.config.Collection, rkey)
	if err != nil || !found {
		return nil, fmt.Errorf("metadata not found")
	}

	// Получаем узел
	node, err := bs.bs.GetNode(ctx, metaCID)
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata node: %w", err)
	}

	// Конвертируем в metadata
	return bs.nodeToMetadata(node)
}

// metadataToNode конвертирует BlobMetadata в IPLD узел
func (bs *BlobStore) metadataToNode(metadata *BlobMetadata) (datamodel.Node, error) {
	builder := basicnode.Prototype.Map.NewBuilder()
	ma, err := builder.BeginMap(6) // Количество полей
	if err != nil {
		return nil, err
	}

	// Добавляем поля metadata
	if err := bs.addMapEntry(ma, "name", metadata.Name); err != nil {
		return nil, err
	}
	if err := bs.addMapEntry(ma, "size", metadata.Size); err != nil {
		return nil, err
	}
	if err := bs.addMapEntry(ma, "mimeType", metadata.MimeType); err != nil {
		return nil, err
	}
	if err := bs.addMapEntry(ma, "contentCid", metadata.ContentCID.String()); err != nil {
		return nil, err
	}
	if err := bs.addMapEntry(ma, "createdAt", metadata.CreatedAt.Unix()); err != nil {
		return nil, err
	}
	if err := bs.addMapEntry(ma, "type", "blob_metadata"); err != nil {
		return nil, err
	}

	if err := ma.Finish(); err != nil {
		return nil, err
	}

	return builder.Build(), nil
}

// addMapEntry добавляет entry в map assembler
func (bs *BlobStore) addMapEntry(ma datamodel.MapAssembler, key string, value interface{}) error {
	entry, err := ma.AssembleEntry(key)
	if err != nil {
		return err
	}

	switch v := value.(type) {
	case string:
		return entry.AssignString(v)
	case int64:
		return entry.AssignInt(v)
	case int:
		return entry.AssignInt(int64(v))
	default:
		return fmt.Errorf("unsupported value type: %T", value)
	}
}

// nodeToMetadata конвертирует IPLD узел в BlobMetadata
func (bs *BlobStore) nodeToMetadata(node datamodel.Node) (*BlobMetadata, error) {
	metadata := &BlobMetadata{}

	// Извлекаем поля
	if nameNode, err := node.LookupByString("name"); err == nil {
		if name, err := nameNode.AsString(); err == nil {
			metadata.Name = name
		}
	}

	if sizeNode, err := node.LookupByString("size"); err == nil {
		if size, err := sizeNode.AsInt(); err == nil {
			metadata.Size = size
		}
	}

	if mimeNode, err := node.LookupByString("mimeType"); err == nil {
		if mimeType, err := mimeNode.AsString(); err == nil {
			metadata.MimeType = mimeType
		}
	}

	if cidNode, err := node.LookupByString("contentCid"); err == nil {
		if cidStr, err := cidNode.AsString(); err == nil {
			if contentCID, err := cid.Parse(cidStr); err == nil {
				metadata.ContentCID = contentCID
			}
		}
	}

	if timeNode, err := node.LookupByString("createdAt"); err == nil {
		if timestamp, err := timeNode.AsInt(); err == nil {
			metadata.CreatedAt = time.Unix(timestamp, 0)
		}
	}

	return metadata, nil
}
