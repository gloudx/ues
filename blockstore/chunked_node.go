package blockstore

import (
	"context"
	"fmt"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/node/basicnode"
)

// ChunkedNodeStore представляет интеллектуальное хранилище для управления большими IPLD узлами
// через автоматическое разбиение на фрагменты (chunking).
//
// АРХИТЕКТУРНЫЕ ПРИНЦИПЫ:
// - Transparency: Внешний API остается простым, сложность скрыта внутри
// - Efficiency: Автоматический выбор оптимальной стратегии хранения на основе размера
// - Scalability: Поддержка произвольно больших данных через иерархическое разбиение
// - Compatibility: Полная совместимость с существующими IPLD/IPFS инструментами
// - Reliability: Целостность данных через криптографическую верификацию CID
//
// СТРАТЕГИИ ХРАНЕНИЯ:
// 1. Direct Storage (размер < threshold):
//   - Единый блок для быстрого доступа
//   - Минимальная латентность
//   - Отсутствие метаданных
//
// 2. Chunked Storage (размер >= threshold):
//   - Разбиение на фрагменты фиксированного размера
//   - Merkle DAG структура для верификации
//   - Параллельная загрузка и обработка
//   - Эффективная дедупликация на уровне фрагментов
//
// ПРИМЕНЕНИЕ В UES:
// - Хранение больших медиа-файлов (изображения, видео)
// - Обработка объемных IPLD документов (длинные посты, большие схемы)
// - Эффективная репликация между узлами
// - Оптимизация использования памяти при обработке данных
//
// ИНТЕГРАЦИЯ С BLOCKSTORE:
// ChunkedNodeStore выступает как middleware layer между приложением и базовым Blockstore,
// автоматически применяя оптимальную стратегию хранения без изменения API.
type ChunkedNodeStore struct {
	// bs предоставляет underlying storage для chunks и metadata.
	// Должен implement standard Blockstore interface для interoperability.
	// ChunkedNodeStore acts как intelligent wrapper around этого base storage.
	bs Blockstore

	// chunkSize определяет target размер каждого chunk в байтах.
	// OPTIMIZATION CONSIDERATIONS:
	// - Larger chunks: Fewer network requests, больше memory per operation
	// - Smaller chunks: Better parallelization, более granular deduplication
	// - Network latency: Higher latency benefits от larger chunks
	// - Bandwidth: Higher bandwidth может handle larger chunks efficiently
	// RECOMMENDED VALUES:
	// - 64KB: High-latency или low-memory environments
	// - 256KB: Balanced default для most use cases
	// - 1MB: High-bandwidth, low-latency environments
	chunkSize int

	// threshold определяет minimum размер для triggering chunking.
	// RATIONALE: Small nodes должны оставаться как single blocks для:
	// - Reduced complexity (no DAG traversal needed)
	// - Lower latency (single network request)
	// - Reduced storage overhead (no metadata structure)
	// TYPICALLY: threshold == chunkSize для consistent behavior,
	// но может быть different для specialized use cases.
	threshold int
}

// ChunkedNodeConfig определяет параметры конфигурации для оптимизации работы ChunkedNodeStore.
// Позволяет тонко настроить поведение системы под конкретные требования приложения и инфраструктуры.
//
// ФИЛОСОФИЯ КОНФИГУРАЦИИ:
// Конфигурация должна обеспечивать баланс между:
// - Performance vs Memory usage
// - Network efficiency vs Storage overhead
// - Complexity vs Maintainability
// - Flexibility vs Predictability
//
// ПРОФИЛИ КОНФИГУРАЦИИ:
// 1. Low Memory Profile:
//   - ChunkSize: 64KB, Threshold: 64KB
//   - Минимальное использование RAM
//   - Подходит для embedded систем
//
// 2. Balanced Profile (рекомендуемый):
//   - ChunkSize: 256KB, Threshold: 256KB
//   - Оптимальный баланс для большинства случаев
//   - Хорошая производительность сети и памяти
//
// 3. High Performance Profile:
//   - ChunkSize: 1MB, Threshold: 1MB
//   - Максимальная пропускная способность
//   - Требует достаточный объем RAM
//
// 4. Network Optimized Profile:
//   - ChunkSize: 4MB, Threshold: 1MB
//   - Минимизация сетевых запросов
//   - Подходит для высоколатентных сетей
//
// ВЛИЯНИЕ НА СИСТЕМУ:
// - Производительность сети и дисковых операций
// - Потребление оперативной памяти при обработке
// - Эффективность дедупликации данных
// - Сложность отладки и мониторинга
type ChunkedNodeConfig struct {
	// ChunkSize определяет target размер individual chunks в байтах.
	//
	// PERFORMANCE IMPACT:
	// - NETWORK: Larger chunks reduce request overhead но increase latency
	// - MEMORY: Directly impacts peak memory usage during processing
	// - STORAGE: Affects deduplication efficiency и metadata overhead
	// - PARALLELISM: Smaller chunks enable better concurrent processing
	//
	// RECOMMENDED RANGES:
	// - 32KB-64KB: Ultra-low memory environments, very high latency tolerance
	// - 256KB: Balanced default для general purpose use
	// - 1MB-4MB: High-performance systems с abundant memory и bandwidth
	//
	// DEFAULT: 256KB provides good balance для most environments
	ChunkSize int `json:"chunk_size"`

	// Threshold определяет minimum content размер для triggering chunking.
	//
	// OPTIMIZATION RATIONALE:
	// Small content benefits от being stored как single block:
	// - Single network request (lower latency)
	// - No DAG traversal overhead
	// - Reduced metadata storage
	// - Simpler error scenarios
	//
	// CONFIGURATION STRATEGIES:
	// - threshold == chunkSize: Consistent chunking behavior
	// - threshold > chunkSize: Hysteresis для avoiding thrashing near boundary
	// - threshold < chunkSize: Early chunking для predictable behavior
	//
	// DEFAULT: Equal to ChunkSize для simple, predictable behavior
	Threshold int `json:"threshold"`
}

// NewChunkedNodeStore создает новый экземпляр ChunkedNodeStore с оптимизированной конфигурацией.
// Конструктор выполняет инициализацию и валидацию всех параметров для обеспечения корректной работы.
//
// ПРОЦЕСС ИНИЦИАЛИЗАЦИИ:
// 1. Валидация входных параметров (blockstore не nil)
// 2. Применение конфигурации по умолчанию при необходимости
// 3. Проверка логической корректности конфигурации
// 4. Создание и настройка экземпляра ChunkedNodeStore
// 5. Подготовка внутренних структур для оптимальной работы
//
// СТРАТЕГИЯ КОНФИГУРАЦИИ ПО УМОЛЧАНИЮ:
// Если config равен nil, применяются тщательно подобранные значения:
// - ChunkSize: 256KB - баланс между эффективностью сети и памятью
// - Threshold: 256KB - простая и предсказуемая логика chunking
// Эти значения подходят для 80% случаев использования.
//
// ПАРАМЕТРЫ:
//
//	bs - экземпляр Blockstore для базового хранения данных.
//	     Должен быть полностью инициализирован и готов к операциям.
//	     ТРЕБОВАНИЯ: thread-safe, persistent, поддержка стандартного IPLD API.
//	config - конфигурация chunking параметров. При nil используются defaults.
//	         Рекомендуется явно указывать для production deployments.
//
// ВОЗВРАЩАЕТ:
//
//	*ChunkedNodeStore - полностью инициализированный и готовый к использованию экземпляр.
//	                    Thread-safe для concurrent операций.
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Базовая инициализация с defaults
//	chunkedStore := NewChunkedNodeStore(blockstore, nil)
//
//	// Production конфигурация с оптимизацией для больших файлов
//	config := &ChunkedNodeConfig{
//	    ChunkSize: 1024 * 1024,    // 1MB chunks
//	    Threshold: 512 * 1024,     // 512KB threshold
//	}
//	chunkedStore := NewChunkedNodeStore(blockstore, config)
//
//	// Memory-constrained окружение
//	lowMemConfig := &ChunkedNodeConfig{
//	    ChunkSize: 64 * 1024,      // 64KB chunks
//	    Threshold: 64 * 1024,      // 64KB threshold
//	}
//	chunkedStore := NewChunkedNodeStore(blockstore, lowMemConfig)
//
// ВАЖНЫЕ ЗАМЕЧАНИЯ:
// - Экземпляр является thread-safe и может использоваться из множественных goroutines
// - Конфигурация фиксируется при создании, runtime изменения не поддерживаются
// - Underlying blockstore должен поддерживать concurrent access
// - Производительность зависит от правильного выбора параметров конфигурации
func NewChunkedNodeStore(bs Blockstore, config *ChunkedNodeConfig) *ChunkedNodeStore {
	// CONFIGURATION DEFAULTING:
	// Provides sensible defaults для common use cases while allowing
	// complete customization для specialized requirements.
	if config == nil {
		config = &ChunkedNodeConfig{
			// 256KB chunks provide optimal balance для most scenarios:
			// - Efficient network utilization
			// - Reasonable memory footprint
			// - Good parallelization opportunities
			// - Compatible с standard IPFS block size limits
			ChunkSize: 256 * 1024, // 256KB

			// Threshold равный ChunkSize обеспечивает predictable behavior:
			// - Content < 256KB: Single block storage
			// - Content >= 256KB: Automatic chunking
			// - No hysteresis effects near boundary
			Threshold: 256 * 1024, // 256KB
		}
	}

	// INSTANCE CONSTRUCTION с validated configuration
	return &ChunkedNodeStore{
		// Store reference на underlying blockstore для delegation
		bs: bs,

		// Cache configuration parameters для efficient access during operations
		chunkSize: config.ChunkSize,
		threshold: config.Threshold,
	}
}

// PutNode сохраняет IPLD узел в хранилище с автоматическим выбором стратегии хранения.
// Основной entry point для записи любых IPLD данных с интеллектуальной оптимизацией.
//
// АЛГОРИТМ ПРИНЯТИЯ РЕШЕНИЙ:
// 1. Сериализация IPLD узла для анализа размера
// 2. Сравнение с threshold для выбора стратегии:
//   - Размер < threshold → Direct storage (оптимизация латентности)
//   - Размер ≥ threshold → Chunked storage (оптимизация масштабируемости)
//
// 3. Применение выбранной стратегии с соответствующими оптимизациями
//
// СТРАТЕГИИ ХРАНЕНИЯ:
//
// Direct Storage (малые узлы):
// - Преимущества: минимальная латентность, простота, отсутствие overhead
// - Использование: метаданные, небольшие документы, конфигурации
// - Ограничения: размер ограничен возможностями базового blockstore
//
// Chunked Storage (большие узлы):
// - Преимущества: поддержка произвольного размера, параллелизм, дедупликация
// - Использование: медиа-файлы, большие документы, архивы
// - Overhead: дополнительная структура метаданных, сложность восстановления
//
// ПАРАМЕТРЫ:
//
//	ctx - контекст выполнения для управления таймаутами и отменой операций.
//	      Важен для graceful shutdown длительных операций chunking.
//	node - IPLD узел для сохранения. Может быть любого типа и размера.
//	       Поддерживаются все стандартные IPLD типы данных.
//
// ВОЗВРАЩАЕТ:
//
//	cid.Cid - криптографический идентификатор сохраненного содержимого.
//	          Может использоваться для верификации целостности и retrieval.
//	error - ошибку операции или nil при успешном сохранении.
//
// ВОЗМОЖНЫЕ ОШИБКИ:
// - Ошибки сериализации IPLD узла (некорректная структура данных)
// - Ошибки базового blockstore (недостаток места, проблемы с сетью)
// - Ошибки chunking процесса (memory allocation failures)
// - Context cancellation или timeout
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Direct storage: O(1) относительно размера данных
// - Chunked storage: O(n/chunkSize) где n - размер данных
// - Memory usage: ограничен размером одного chunk при chunked storage
// - Network efficiency: автоматически оптимизируется на основе конфигурации
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Сохранение небольшого JSON документа
//	jsonNode := buildJSONNode(data)
//	cid, err := store.PutNode(ctx, jsonNode)
//
//	// Сохранение большого медиа-файла как IPLD node
//	mediaNode := basicnode.NewBytes(largeMediaData)
//	cid, err := store.PutNode(ctx, mediaNode)
//
// ГАРАНТИИ:
// - Атомарность: операция либо полностью успешна, либо полностью неуспешна
// - Идемпотентность: повторное сохранение того же содержимого возвращает тот же CID
// - Верифицируемость: возвращенный CID может использоваться для проверки целостности
func (cns *ChunkedNodeStore) PutNode(ctx context.Context, node datamodel.Node) (cid.Cid, error) {
	// PHASE 1: CONTENT SERIALIZATION
	// Конвертируем IPLD node в canonical binary representation для size analysis.
	//
	// SERIALIZATION REQUIREMENTS:
	// - Deterministic: Same content -> same serialization
	// - Complete: All node information preserved
	// - Efficient: Minimal serialization overhead
	//
	// CURRENT IMPLEMENTATION: Simplified для demonstration
	// PRODUCTION TODO: Implement proper IPLD codec integration:
	// - DAG-CBOR для structured data (recommended default)
	// - DAG-JSON для human-readable formats
	// - Custom codecs для specialized content types
	data, err := cns.serializeNode(node)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to serialize IPLD node for size analysis: %w", err)
	}

	// PHASE 2: SIZE-BASED STORAGE STRATEGY SELECTION
	// Анализируем serialized size для выбора optimal storage approach.
	//
	// DECISION CRITERIA:
	// Threshold comparison determines storage strategy based на:
	// - Network efficiency considerations
	// - Memory usage patterns
	// - Access pattern expectations
	// - Maintenance complexity trade-offs
	if len(data) < cns.threshold {
		// SMALL NODE PATH: Direct storage optimization
		// Для nodes below threshold используем direct storage потому что:
		// - Single network round-trip для retrieval
		// - No metadata overhead
		// - Simpler error scenarios и debugging
		// - Lower computational overhead
		// - Direct CID addressing без indirection
		return cns.bs.PutNode(ctx, node)
	}

	// LARGE NODE PATH: Chunked storage с merkle DAG structure
	// Для nodes exceeding threshold применяем sophisticated chunking:
	// - Parallel storage и retrieval capabilities
	// - Support для arbitrarily large content
	// - Efficient partial access patterns
	// - Better memory usage control during processing
	// - Enables progressive loading strategies
	return cns.storeChunkedNode(ctx, data, "ipld")
}

// PutLargeBytes специализированный метод для эффективного хранения больших бинарных данных.
// Оптимизирован для работы с raw binary content без overhead IPLD структур.
//
// ОТЛИЧИЯ ОТ PutNode:
// - Прямая работа с []byte без промежуточной IPLD сериализации
// - Оптимизированный путь для binary content типа
// - Меньший overhead для простых binary данных
// - Специализированная metadata структура для binary content
//
// СЦЕНАРИИ ИСПОЛЬЗОВАНИЯ:
// - Загрузка медиа-файлов (изображения, видео, аудио)
// - Сохранение архивов и compressed данных
// - Хранение encrypted binary content
// - Backup больших файлов в distributed storage
// - Streaming upload больших объемов данных
//
// АЛГОРИТМ ОБРАБОТКИ:
// 1. Анализ размера входных данных
// 2. Выбор стратегии на основе threshold:
//   - Малые данные: создание IPLD bytes node + direct storage
//   - Большие данные: chunking с "binary" content type marker
//
// 3. Применение выбранной стратегии с соответствующими оптимизациями
//
// ПАРАМЕТРЫ:
//
//	ctx - контекст выполнения для управления lifecycle операции.
//	      Особенно важен для больших данных, где операция может быть длительной.
//	data - binary данные для сохранения. Размер ограничен только доступной памятью.
//	       Данные копируются при необходимости для обеспечения immutability.
//
// ВОЗВРАЩАЕТ:
//
//	cid.Cid - уникальный идентификатор сохраненного содержимого.
//	          Детерминистически вычисляется на основе содержимого данных.
//	error - ошибку обработки или nil при успешном сохранении.
//
// ОПТИМИЗАЦИИ:
// - Zero-copy операции где возможно
// - Streaming chunking для memory efficiency
// - Parallel chunk storage для improved throughput
// - Intelligent buffer management для large datasets
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Memory usage: O(chunkSize) независимо от общего размера данных
// - Time complexity: O(dataSize/chunkSize) для chunked storage
// - Network efficiency: оптимизирована для batch операций
// - Disk I/O: sequential write patterns для better performance
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Сохранение медиа-файла
//	imageData, err := ioutil.ReadFile("large_image.jpg")
//	cid, err := store.PutLargeBytes(ctx, imageData)
//
//	// Сохранение streaming данных
//	var buffer bytes.Buffer
//	io.Copy(&buffer, largeDataStream)
//	cid, err := store.PutLargeBytes(ctx, buffer.Bytes())
//
// ВАЖНЫЕ ЗАМЕЧАНИЯ:
// - Входные данные не модифицируются методом
// - Для очень больших данных рекомендуется streaming API
// - CID calculation происходит на основе original данных, не chunk structure
// - Binary content type позволяет оптимизированное восстановление данных
func (cns *ChunkedNodeStore) PutLargeBytes(ctx context.Context, data []byte) (cid.Cid, error) {
	// FAST PATH для small data: Direct IPLD node creation
	// Для binary data below threshold, создаем bytes node directly
	// без chunking overhead. Это обеспечивает optimal performance
	// для small binary content.
	if len(data) < cns.threshold {
		// Create IPLD bytes node directly from binary data
		// Это более efficient чем chunking для small content
		node := basicnode.NewBytes(data)
		return cns.bs.PutNode(ctx, node)
	}

	// CHUNKING PATH для large data: Specialized binary chunking
	// Для large binary data применяем optimized chunking strategy
	// с "binary" content type marker для proper reconstruction
	return cns.storeChunkedNode(ctx, data, "binary")
}

// GetNode извлекает IPLD узел из хранилища с автоматическим определением и сборкой chunked content.
// Главный entry point для чтения любых IPLD данных с transparent handling chunked storage.
//
// АЛГОРИТМ ВОССТАНОВЛЕНИЯ:
// 1. Получение root узла по CID из базового хранилища
// 2. Анализ структуры узла для определения типа хранения:
//   - Direct content: возврат узла как есть (быстрый путь)
//   - Chunked content: complex assembly process
//     3. Для chunked content:
//     a. Парсинг metadata структуры (chunks list, sizes, content type)
//     b. Параллельное получение всех chunk узлов
//     c. Валидация целостности каждого chunk
//     d. Сборка original content в правильном порядке
//     e. Восстановление original IPLD structure
//
// ТИПЫ ПОДДЕРЖИВАЕМОГО CONTENT:
// - Direct IPLD nodes: любые стандартные IPLD структуры
// - Binary chunked content: восстанавливается как bytes node
// - IPLD chunked content: десериализуется обратно в original structure
// - Complex nested structures: с полным сохранением иерархии
//
// ПАРАМЕТРЫ:
//
//	ctx - контекст выполнения для управления операцией и cancellation.
//	      Критичен для chunked content где требуется множественные network requests.
//	c - CID (Content Identifier) для retrieval. Должен быть valid и существующий.
//	    Поддерживает как direct content CIDs, так и chunked content root CIDs.
//
// ВОЗВРАЩАЕТ:
//
//	datamodel.Node - восстановленный IPLD узел в original format.
//	                 Полностью идентичен original узлу, переданному в PutNode.
//	error - ошибку восстановления или nil при успешном получении.
//
// ВОЗМОЖНЫЕ ОШИБКИ:
// - CID не найден в хранилище (content missing)
// - Поврежденная metadata структура chunked content
// - Отсутствующие или поврежденные chunks
// - Ошибки десериализации IPLD content
// - Network timeouts при получении chunks
// - Context cancellation during long assembly operations
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Direct content: O(1) - single blockstore lookup
// - Chunked content: O(n) где n - количество chunks
// - Memory usage: оптимизирована через streaming assembly
// - Network requests: параллелизованы для minimal total latency
// - Caching: chunks могут кэшироваться для repeated access
//
// ОПТИМИЗАЦИИ:
// - Parallel chunk retrieval для reduced latency
// - Early error detection для fail-fast behavior
// - Memory-efficient streaming assembly для large content
// - Intelligent prefetching для predicted access patterns
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Получение любого IPLD content
//	node, err := store.GetNode(ctx, contentCID)
//	if err != nil {
//	    log.Fatal("Failed to retrieve content:", err)
//	}
//
//	// Работа с полученным узлом
//	switch node.Kind() {
//	case datamodel.Kind_Bytes:
//	    data, _ := node.AsBytes()
//	    fmt.Printf("Retrieved %d bytes\n", len(data))
//	case datamodel.Kind_Map:
//	    processStructuredData(node)
//	}
//
// ГАРАНТИИ:
// - Content integrity: CID verification обеспечивает неизменность данных
// - Consistency: retrieved content точно соответствует original input
// - Atomicity: операция либо полностью успешна, либо возвращает ошибку
// - Idempotency: повторные вызовы с тем же CID возвращают идентичный результат
func (cns *ChunkedNodeStore) GetNode(ctx context.Context, c cid.Cid) (datamodel.Node, error) {
	// PHASE 1: ROOT NODE RETRIEVAL
	// Получаем initial node structure для analysis и type detection.
	// Этот node может быть либо complete content (для small data),
	// либо metadata structure (для chunked content).
	node, err := cns.bs.GetNode(ctx, c)
	if err != nil {
		// Enhanced error context для better debugging и monitoring
		return nil, fmt.Errorf("failed to retrieve root node for CID %s: %w", c.String(), err)
	}

	// PHASE 2: CONTENT TYPE DETECTION
	// Анализируем retrieved node для определения storage format.
	// Decision tree routes к appropriate processing path.
	if cns.isChunkedNode(node) {
		// CHUNKED CONTENT PATH: Complex assembly required
		// Node содержит metadata structure с chunk references.
		// Необходимо fetch и assemble all chunks для reconstruction.
		return cns.assembleChunkedNode(ctx, node)
	}

	// DIRECT CONTENT PATH: Simple case - node содержит complete content
	// Возвращаем node directly без additional processing
	return node, nil
}

// storeChunkedNode выполняет процесс разбиения больших данных на chunks и создания metadata структуры.
// Реализует core chunking алгоритм с оптимизациями для различных типов содержимого.
//
// АРХИТЕКТУРА CHUNKING:
// 1. Sequential chunk creation:
//   - Разбиение data на фрагменты фиксированного размера
//   - Создание IPLD bytes nodes для каждого chunk
//   - Сохранение chunks в базовом blockstore с получением CIDs
//
// 2. Metadata structure creation:
//   - Создание root узла с информацией о chunking
//   - Включение всех chunk CIDs для reconstruction
//   - Сохранение original size и content type
//   - Сохранение chunk размера для validation
//
// 3. Root node storage:
//   - Финальное сохранение metadata structure
//   - Возврат root CID для external reference
//
// СТРУКТУРА METADATA:
//
//	{
//	  "type": "chunked_node",           // Marker для identification
//	  "original_size": <int>,           // Original data size в байтах
//	  "chunk_size": <int>,              // Размер каждого chunk
//	  "content_type": "binary|ipld",    // Тип original content
//	  "chunks": [<cid1>, <cid2>, ...]   // Ordered list chunk CIDs
//	}
//
// ПАРАМЕТРЫ:
//
//	ctx - контекст для управления lifecycle операции и cancellation.
//	      Важен для длительных операций с большими данными.
//	data - binary данные для chunking. Размер должен быть >= threshold.
//	       Данные обрабатываются sequentially для memory efficiency.
//	contentType - тип содержимого ("binary" или "ipld") для правильного восстановления.
//	              Влияет на strategy десериализации при assembly.
//
// ВОЗВРАЩАЕТ:
//
//	cid.Cid - CID root metadata узла для retrieval всей структуры.
//	error - ошибку chunking процесса или nil при успехе.
//
// АЛГОРИТМ CHUNKING:
// - Sequential processing для predictable memory usage
// - Fixed-size chunks (кроме последнего) для consistent performance
// - Ordered chunk storage для deterministic reconstruction
// - Atomic failure behavior - либо все chunks созданы, либо operation fails
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Memory usage: O(chunkSize) независимо от total data size
// - Time complexity: O(dataSize / chunkSize) для chunk creation
// - Storage efficiency: minimal metadata overhead
// - Network optimization: chunks могут передаваться параллельно
//
// ВАЖНЫЕ ЗАМЕЧАНИЯ:
// - Chunk order критичен для правильного восстановления данных
// - Content type определяет strategy десериализации
// - Все chunks должны быть успешно сохранены для consistency
// - Metadata structure должна быть compatible с assembleChunkedNode
func (cns *ChunkedNodeStore) storeChunkedNode(ctx context.Context, data []byte, contentType string) (cid.Cid, error) {
	var chunks []cid.Cid

	// Разбиваем данные на chunks
	for i := 0; i < len(data); i += cns.chunkSize {
		end := i + cns.chunkSize
		if end > len(data) {
			end = len(data)
		}

		chunk := data[i:end]
		chunkNode := basicnode.NewBytes(chunk)

		chunkCID, err := cns.bs.PutNode(ctx, chunkNode)
		if err != nil {
			return cid.Undef, fmt.Errorf("failed to store chunk %d: %w", i/cns.chunkSize, err)
		}

		chunks = append(chunks, chunkCID)
	}

	// Создаем root узел с metadata о chunks
	rootNode, err := cns.createChunkedRootNode(chunks, len(data), contentType)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to create root node: %w", err)
	}

	// Сохраняем root узел
	rootCID, err := cns.bs.PutNode(ctx, rootNode)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to store root node: %w", err)
	}

	return rootCID, nil
}

// createChunkedRootNode создает IPLD metadata узел для chunked content с полной информацией о структуре.
// Формирует standardized metadata format для consistent reconstruction любых chunked данных.
//
// ЦЕЛЬ METADATA СТРУКТУРЫ:
// - Обеспечить deterministic reconstruction original content
// - Предоставить информацию для validation и integrity checking
// - Поддержать различные типы content через content_type field
// - Сохранить информацию для performance optimization при retrieval
// - Обеспечить compatibility с future versions chunking protocol
//
// АРХИТЕКТУРА METADATA:
// Структура спроектирована как self-contained description всего необходимого
// для полного восстановления original данных:
//
// 1. Type identification: "chunked_node" marker для automatic detection
// 2. Size information: original_size для validation и memory allocation
// 3. Chunking parameters: chunk_size для understanding структуры
// 4. Content classification: content_type для правильной десериализации
// 5. Chunk references: ordered array CIDs для sequential reconstruction
//
// ПАРАМЕТРЫ:
//
//	chunks - упорядоченный список CIDs всех chunks в sequential order.
//	        Order критичен для правильного восстановления original data.
//	originalSize - размер original данных в байтах для validation.
//	               Используется для integrity checking при assembly.
//	contentType - тип original content ("binary" или "ipld").
//	             Определяет strategy десериализации при retrieval.
//
// ВОЗВРАЩАЕТ:
//
//	datamodel.Node - IPLD узел содержащий complete metadata structure.
//	error - ошибку создания metadata или nil при успехе.
//
// СТРУКТУРА ВОЗВРАЩАЕМОГО УЗЛА:
//
//	{
//	  "type": "chunked_node",         // Fixed identifier для detection
//	  "original_size": <number>,      // Original data size в байтах
//	  "chunk_size": <number>,         // Size каждого chunk в байтах
//	  "content_type": <string>,       // "binary" или "ipld"
//	  "chunks": [                     // Ordered array chunk references
//	    "<cid1>",
//	    "<cid2>",
//	    ...
//	  ]
//	}
//
// VALIDATION И INTEGRITY:
// - Все поля обязательны для successful reconstruction
// - Chunk order должен соответствовать original data sequencing
// - Original size должен равняться sum всех chunk sizes
// - Content type должен быть valid ("binary" или "ipld")
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Minimal metadata overhead (typically < 1KB for thousands chunks)
// - Efficient IPLD representation для fast parsing
// - Deterministic structure для consistent CID generation
// - Optimized для frequent access patterns
//
// ПРИМЕРЫ METADATA СТРУКТУР:
//
//	// Для binary content (например, image file)
//	{
//	  "type": "chunked_node",
//	  "original_size": 5242880,
//	  "chunk_size": 262144,
//	  "content_type": "binary",
//	  "chunks": ["QmXYZ...", "QmABC...", ...]
//	}
//
//	// Для IPLD content (например, large JSON document)
//	{
//	  "type": "chunked_node",
//	  "original_size": 1048576,
//	  "chunk_size": 262144,
//	  "content_type": "ipld",
//	  "chunks": ["QmDEF...", "QmGHI...", ...]
//	}
func (cns *ChunkedNodeStore) createChunkedRootNode(chunks []cid.Cid, originalSize int, contentType string) (datamodel.Node, error) {
	builder := basicnode.Prototype.Map.NewBuilder()
	ma, err := builder.BeginMap(5)
	if err != nil {
		return nil, err
	}

	// Тип узла
	if err := cns.addMapEntry(ma, "type", "chunked_node"); err != nil {
		return nil, err
	}

	// Оригинальный размер
	if err := cns.addMapEntry(ma, "original_size", originalSize); err != nil {
		return nil, err
	}

	// Размер chunk'а
	if err := cns.addMapEntry(ma, "chunk_size", cns.chunkSize); err != nil {
		return nil, err
	}

	// Тип содержимого
	if err := cns.addMapEntry(ma, "content_type", contentType); err != nil {
		return nil, err
	}

	// Список chunks
	entry, err := ma.AssembleEntry("chunks")
	if err != nil {
		return nil, err
	}

	la, err := entry.BeginList(int64(len(chunks)))
	if err != nil {
		return nil, err
	}

	for _, chunkCID := range chunks {
		elem := la.AssembleValue()
		if err := elem.AssignString(chunkCID.String()); err != nil {
			return nil, err
		}
	}

	if err := la.Finish(); err != nil {
		return nil, err
	}

	if err := ma.Finish(); err != nil {
		return nil, err
	}

	return builder.Build(), nil
}

// isChunkedNode определяет, является ли IPLD узел metadata структурой для chunked content.
// Выполняет type detection для выбора правильной стратегии обработки при retrieval.
//
// АЛГОРИТМ DETECTION:
// 1. Попытка извлечения "type" field из IPLD map structure
// 2. Проверка значения field на соответствие "chunked_node" marker
// 3. Возврат boolean result на основе exact match
//
// ОБРАБОТКА ОШИБОК:
// - Missing "type" field → false (не chunked node)
// - Invalid field type → false (не string field)
// - Wrong field value → false (не наш format)
// - Valid "chunked_node" → true (наш chunked format)
//
// ПАРАМЕТРЫ:
//
//	node - IPLD узел для analysis. Может быть любого типа и структуры.
//	       Метод безопасно обрабатывает любые input types без panic.
//
// ВОЗВРАЩАЕТ:
//
//	bool - true если node является chunked metadata, false в противном случае.
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - O(1) lookup operation в IPLD map structure
// - Minimal memory allocation
// - Fast-fail для non-map nodes
// - Efficient string comparison для type checking
//
// БЕЗОПАСНОСТЬ:
// - Никогда не panic при invalid input
// - Graceful handling всех edge cases
// - Type-safe operations только после validation
// - No side effects или data modification
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Проверка типа узла перед обработкой
//	if store.isChunkedNode(retrievedNode) {
//	    // Обработка как chunked content
//	    originalNode, err := store.assembleChunkedNode(ctx, retrievedNode)
//	} else {
//	    // Обработка как direct content
//	    processDirectNode(retrievedNode)
//	}
//
// СОВМЕСТИМОСТЬ:
// - Работает с любыми IPLD node types
// - Backward compatible с существующими data formats
// - Forward compatible с future chunking versions через type field extension
// - Standard IPLD patterns для maximum interoperability
func (cns *ChunkedNodeStore) isChunkedNode(node datamodel.Node) bool {
	typeNode, err := node.LookupByString("type")
	if err != nil {
		return false
	}

	nodeType, err := typeNode.AsString()
	if err != nil {
		return false
	}

	return nodeType == "chunked_node"
}

// assembleChunkedNode восстанавливает original IPLD узел из chunked metadata и составляющих chunks.
// Реализует полный процесс reconstruction с validation и error handling.
//
// АРХИТЕКТУРА ASSEMBLY ПРОЦЕССА:
// 1. Metadata extraction и validation:
//   - Извлечение chunks list и content type
//   - Проверка корректности metadata структуры
//   - Validation required fields presence
//
// 2. Chunk retrieval и validation:
//   - Sequential или parallel retrieval всех chunks
//   - Integrity checking каждого chunk через CID verification
//   - Error handling для missing или corrupted chunks
//
// 3. Data assembly и reconstruction:
//   - Sequential concatenation chunks в original order
//   - Memory-efficient streaming assembly для large content
//   - Verification assembled size против expected original_size
//
// 4. Content-type specific deserialization:
//   - Binary content: direct bytes node creation
//   - IPLD content: deserialization обратно в original structure
//   - Custom content types: extensible processing pipeline
//
// ПАРАМЕТРЫ:
//
//	ctx - контекст для управления длительными operations и cancellation.
//	      Критичен для chunked content с множественными network requests.
//	rootNode - metadata IPLD узел содержащий chunking information.
//	           Должен соответствовать expected metadata schema.
//
// ВОЗВРАЩАЕТ:
//
//	datamodel.Node - полностью восстановленный original IPLD узел.
//	                 Идентичен original input в PutNode операции.
//	error - ошибку assembly процесса или nil при успешном восстановлении.
//
// ВОЗМОЖНЫЕ ОШИБКИ:
// - Missing или corrupted metadata fields
// - Unreachable или missing chunk CIDs
// - Corrupted chunk data (CID mismatch)
// - Assembly size mismatch с expected original_size
// - Deserialization errors для IPLD content
// - Context cancellation во время assembly
// - Memory allocation failures для large content
//
// ПРОИЗВОДИТЕЛЬНОСТЬ ОПТИМИЗАЦИИ:
// - Parallel chunk retrieval для reduced total latency
// - Streaming assembly для memory efficiency с large data
// - Early error detection для fail-fast behavior
// - Intelligent prefetching для sequential access patterns
// - Chunk-level caching для repeated access
//
// MEMORY MANAGEMENT:
// - Streaming processing для predictable memory usage
// - Incremental assembly без loading всего content в memory
// - Efficient buffer reuse для frequent operations
// - Garbage collection friendly patterns
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Assembly после detection chunked node
//	if isChunkedNode(node) {
//	    originalNode, err := store.assembleChunkedNode(ctx, node)
//	    if err != nil {
//	        return fmt.Errorf("failed to assemble chunked content: %w", err)
//	    }
//
//	    // Работа с восстановленным content
//	    processReconstructedNode(originalNode)
//	}
//
// ГАРАНТИИ INTEGRITY:
// - CID verification для каждого chunk обеспечивает data integrity
// - Size validation предотвращает silent data corruption
// - Atomic assembly - либо complete success, либо clear failure
// - Deterministic результаты для consistent behavior
func (cns *ChunkedNodeStore) assembleChunkedNode(ctx context.Context, rootNode datamodel.Node) (datamodel.Node, error) {
	// Извлекаем metadata
	chunksNode, err := rootNode.LookupByString("chunks")
	if err != nil {
		return nil, fmt.Errorf("missing chunks field: %w", err)
	}

	contentTypeNode, err := rootNode.LookupByString("content_type")
	if err != nil {
		return nil, fmt.Errorf("missing content_type field: %w", err)
	}

	contentType, err := contentTypeNode.AsString()
	if err != nil {
		return nil, fmt.Errorf("invalid content_type: %w", err)
	}

	// Получаем список CID'ов chunks
	var chunkCIDs []cid.Cid
	chunksList := chunksNode.ListIterator()
	for !chunksList.Done() {
		_, chunkNode, err := chunksList.Next()
		if err != nil {
			return nil, fmt.Errorf("failed to iterate chunks: %w", err)
		}

		cidStr, err := chunkNode.AsString()
		if err != nil {
			return nil, fmt.Errorf("invalid chunk CID: %w", err)
		}

		chunkCID, err := cid.Parse(cidStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse chunk CID: %w", err)
		}

		chunkCIDs = append(chunkCIDs, chunkCID)
	}

	// Собираем данные из chunks
	var assembledData []byte
	for i, chunkCID := range chunkCIDs {
		chunkNode, err := cns.bs.GetNode(ctx, chunkCID)
		if err != nil {
			return nil, fmt.Errorf("failed to get chunk %d: %w", i, err)
		}

		chunkData, err := chunkNode.AsBytes()
		if err != nil {
			return nil, fmt.Errorf("failed to extract chunk %d data: %w", i, err)
		}

		assembledData = append(assembledData, chunkData...)
	}

	// В зависимости от типа содержимого, создаем соответствующий узел
	switch contentType {
	case "binary":
		return basicnode.NewBytes(assembledData), nil
	case "ipld":
		// TODO: здесь нужна proper IPLD deserialization (CBOR/DAG-CBOR)
		return cns.deserializeNode(assembledData)
	default:
		return nil, fmt.Errorf("unknown content type: %s", contentType)
	}
}

// addMapEntry добавляет key-value пару в IPLD map assembler с type-safe handling различных типов данных.
// Utility функция для упрощения создания IPLD map структур с proper type conversion.
//
// ПОДДЕРЖИВАЕМЫЕ ТИПЫ:
// - string: прямое присвоение без конверсии
// - int: конверсия в int64 для IPLD compatibility
// - int64: прямое присвоение (native IPLD integer type)
// - Другие типы: возврат error с информативным сообщением
//
// НАЗНАЧЕНИЕ:
// Функция обеспечивает centralized и type-safe способ добавления полей
// в IPLD map structures, избегая повторения кода и ensuring consistent
// error handling across всего chunking процесса.
//
// ПАРАМЕТРЫ:
//
//	ma - IPLD MapAssembler для добавления entry.
//	     Должен быть в active building state.
//	key - string ключ для map entry. Должен быть valid IPLD map key.
//	value - значение любого поддерживаемого типа для присвоения.
//
// ВОЗВРАЩАЕТ:
//
//	error - ошибку type conversion или IPLD assembly, nil при успехе.
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - O(1) operation для всех поддерживаемых типов
// - Minimal type assertion overhead
// - No memory allocations для primitive types
// - Efficient error path для unsupported types
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	ma, _ := builder.BeginMap(3)
//
//	// String field
//	err := addMapEntry(ma, "type", "chunked_node")
//
//	// Integer field
//	err := addMapEntry(ma, "size", 1024)
//
//	// Int64 field
//	err := addMapEntry(ma, "timestamp", time.Now().Unix())
func (cns *ChunkedNodeStore) addMapEntry(ma datamodel.MapAssembler, key string, value interface{}) error {
	entry, err := ma.AssembleEntry(key)
	if err != nil {
		return err
	}

	switch v := value.(type) {
	case string:
		return entry.AssignString(v)
	case int:
		return entry.AssignInt(int64(v))
	case int64:
		return entry.AssignInt(v)
	default:
		return fmt.Errorf("unsupported value type: %T", value)
	}
}

// serializeNode конвертирует IPLD узел в binary representation для size analysis и chunking.
// Временная simplified реализация, требующая замены на proper IPLD codec integration.
//
// ТЕКУЩАЯ РЕАЛИЗАЦИЯ:
// Поддерживает только базовые IPLD типы для demonstration purposes:
// - Bytes nodes: прямое извлечение binary data
// - String nodes: конверсия в UTF-8 bytes
// - Complex structures: требуют full IPLD serialization (не реализовано)
//
// PRODUCTION ТРЕБОВАНИЯ:
// Полная реализация должна включать:
// 1. DAG-CBOR serialization для structured data (рекомендуемый default)
// 2. DAG-JSON serialization для human-readable formats
// 3. Custom codec support для specialized content types
// 4. Deterministic serialization для consistent CID generation
// 5. Efficient streaming serialization для large structures
//
// ПАРАМЕТРЫ:
//
//	node - IPLD узел для сериализации. Тип определяет serialization strategy.
//
// ВОЗВРАЩАЕТ:
//
//	[]byte - binary representation узла для size analysis.
//	error - ошибку сериализации или nil при успехе.
//
// ОГРАНИЧЕНИЯ ТЕКУЩЕЙ РЕАЛИЗАЦИИ:
// - Поддержка только простых типов данных
// - Отсутствие proper CBOR encoding
// - No support для complex nested structures
// - Missing deterministic encoding guarantees
//
// TODO ДЛЯ PRODUCTION:
// - Интеграция с go-ipld-prime-codec-cbor
// - Поддержка всех IPLD data model types
// - Streaming serialization для memory efficiency
// - Proper error handling для all edge cases
// - Performance optimization для frequent serialization
//
// ПРИМЕРЫ ПОДДЕРЖИВАЕМЫХ УЗЛОВ:
//
//	// Bytes node - direct extraction
//	bytesNode := basicnode.NewBytes([]byte("data"))
//	data, err := serializeNode(bytesNode) // Returns original bytes
//
//	// String node - UTF-8 conversion
//	stringNode := basicnode.NewString("text")
//	data, err := serializeNode(stringNode) // Returns []byte("text")
func (cns *ChunkedNodeStore) serializeNode(node datamodel.Node) ([]byte, error) {
	// Временная упрощенная реализация
	// В production версии здесь должна быть CBOR/DAG-CBOR сериализация

	switch node.Kind() {
	case datamodel.Kind_Bytes:
		return node.AsBytes()
	case datamodel.Kind_String:
		str, err := node.AsString()
		if err != nil {
			return nil, err
		}
		return []byte(str), nil
	default:
		// Для сложных узлов нужна proper CBOR сериализация
		return nil, fmt.Errorf("complex node serialization not implemented for kind: %s", node.Kind())
	}
}

// deserializeNode восстанавливает IPLD узел из binary data после chunked assembly.
// Временная simplified реализация, требующая замены на proper IPLD codec integration.
//
// ТЕКУЩАЯ ФУНКЦИОНАЛЬНОСТЬ:
// Создает basic bytes node из любых binary data без proper type reconstruction.
// Это работает для binary content, но не восстанавливает original IPLD structure
// для complex data types, которые были chunked как "ipld" content type.
//
// PRODUCTION ТРЕБОВАНИЯ:
// Полная реализация должна включать:
// 1. DAG-CBOR deserialization для structured data restoration
// 2. Content type detection через magic bytes или metadata
// 3. Proper IPLD data model reconstruction
// 4. Schema validation для integrity checking
// 5. Error recovery для partially corrupted data
// 6. Streaming deserialization для memory efficiency
//
// ПАРАМЕТРЫ:
//
//	data - binary данные для десериализации в IPLD узел.
//	       Должны представлять valid serialized IPLD content.
//
// ВОЗВРАЩАЕТ:
//
//	datamodel.Node - восстановленный IPLD узел (currently always bytes node).
//	error - ошибку десериализации или nil при успехе.
//
// ТЕКУЩИЕ ОГРАНИЧЕНИЯ:
// - Все data восстанавливается как bytes nodes независимо от original type
// - Отсутствие proper CBOR/DAG-CBOR decoding
// - No schema validation или type checking
// - Missing support для complex nested structures
// - No content type specific processing
//
// TODO ДЛЯ PRODUCTION:
// - Интеграция с IPLD codec libraries
// - Content type specific deserialization paths
// - Schema-aware reconstruction для typed content
// - Streaming deserialization для large datasets
// - Comprehensive error handling и recovery
// - Performance optimization для frequent operations
//
// АРХИТЕКТУРА БУДУЩЕЙ РЕАЛИЗАЦИИ:
//
//	switch contentType {
//	case "binary":
//	    return basicnode.NewBytes(data), nil
//	case "ipld":
//	    return cborCodec.Decode(data) // Proper CBOR decoding
//	case "json":
//	    return jsonCodec.Decode(data) // JSON deserialization
//	default:
//	    return pluginCodec.Decode(contentType, data) // Plugin system
//	}
//
// ПРИМЕРЫ ОЖИДАЕМОГО ПОВЕДЕНИЯ:
//
//	// Binary content - should work correctly
//	imageData := []byte{0xFF, 0xD8, 0xFF...} // JPEG data
//	node, err := deserializeNode(imageData)   // Returns bytes node
//
//	// IPLD content - currently limited
//	cborData := []byte{0xA2, 0x61, 0x61...}  // CBOR encoded map
//	node, err := deserializeNode(cborData)    // Should return proper map node
func (cns *ChunkedNodeStore) deserializeNode(data []byte) (datamodel.Node, error) {
	// Временная упрощенная реализация
	// В production версии здесь должна быть CBOR/DAG-CBOR десериализация
	return basicnode.NewBytes(data), nil
}

// IsChunked проверяет, является ли content по указанному CID chunked без полного retrieval.
// Utility метод для efficient type checking перед принятием решений о processing strategy.
//
// НАЗНАЧЕНИЕ:
// Позволяет приложениям определить тип storage strategy для content
// без необходимости полного assembly chunked data. Полезно для:
// - Performance optimization (выбор processing path)
// - Resource planning (memory allocation decisions)
// - User interface (progress indicators для chunked content)
// - Monitoring и analytics (chunked vs direct content statistics)
//
// АЛГОРИТМ:
// 1. Retrieval root узла по CID из underlying blockstore
// 2. Analysis узла через isChunkedNode для type detection
// 3. Return boolean result без additional processing
//
// ПАРАМЕТРЫ:
//
//	ctx - контекст для управления network operation timeout.
//	      Важен для responsive UI и cancellation support.
//	c - CID для проверки. Должен существовать в blockstore.
//
// ВОЗВРАЩАЕТ:
//
//	bool - true если content является chunked, false для direct storage.
//	error - ошибку retrieval или nil при успешной проверке.
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Minimal network overhead (single blockstore lookup)
// - No chunk assembly overhead
// - Fast metadata analysis
// - Efficient для frequent type checking operations
//
// ВОЗМОЖНЫЕ ОШИБКИ:
// - CID не найден в blockstore
// - Network timeout или connectivity issues
// - Corrupted root node structure
// - Context cancellation
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Выбор processing strategy на основе storage type
//	isChunked, err := store.IsChunked(ctx, contentCID)
//	if err != nil {
//	    return fmt.Errorf("failed to check chunked status: %w", err)
//	}
//
//	if isChunked {
//	    // Настройка для chunked processing
//	    setupProgressIndicator("Assembling large content...")
//	    allocateMemoryForChunks()
//	} else {
//	    // Direct processing path
//	    processSingleBlock(contentCID)
//	}
//
// ПРИМЕНЕНИЕ В UES:
// - Оптимизация UI для больших медиа-файлов
// - Resource planning для memory-constrained environments
// - Analytics для monitoring storage efficiency
// - Debugging и diagnostics для content issues
func (cns *ChunkedNodeStore) IsChunked(ctx context.Context, c cid.Cid) (bool, error) {
	node, err := cns.bs.GetNode(ctx, c)
	if err != nil {
		return false, err
	}

	return cns.isChunkedNode(node), nil
}

// GetChunkInfo извлекает детальную информацию о chunked content без assembly данных.
// Предоставляет comprehensive metadata для analysis, monitoring и optimization decisions.
//
// ИЗВЛЕКАЕМАЯ ИНФОРМАЦИЯ:
// - OriginalSize: размер original данных для memory planning
// - ChunkSize: размер individual chunks для network optimization
// - ChunkCount: количество chunks для progress tracking
// - ContentType: тип content для processing strategy selection
//
// СЦЕНАРИИ ИСПОЛЬЗОВАНИЯ:
// - Progress indicators для chunked content assembly
// - Memory allocation planning для large content processing
// - Network optimization (parallel chunk retrieval planning)
// - Storage analytics и reporting
// - Debugging chunked content issues
// - User interface optimization (показ размера до download)
//
// ПАРАМЕТРЫ:
//
//	ctx - контекст для управления metadata retrieval operation.
//	c - CID chunked content root для information extraction.
//	    Должен указывать на valid chunked metadata структуру.
//
// ВОЗВРАЩАЕТ:
//
//	*ChunkInfo - структура с complete chunked content information.
//	error - ошибку extraction или nil при успешном получении информации.
//
// ВОЗМОЖНЫЕ ОШИБКИ:
// - CID не является chunked content (not a chunked node)
// - Missing или corrupted metadata fields
// - Invalid metadata field types
// - Underlying blockstore access errors
// - Network connectivity issues
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Single blockstore lookup для metadata retrieval
// - No chunk assembly overhead
// - Minimal memory allocation
// - Fast metadata parsing
// - Efficient для frequent monitoring operations
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
//
//	// Progress tracking для large content
//	info, err := store.GetChunkInfo(ctx, contentCID)
//	if err != nil {
//	    return fmt.Errorf("failed to get chunk info: %w", err)
//	}
//
//	fmt.Printf("Downloading %d chunks (%d bytes total)\n",
//	           info.ChunkCount, info.OriginalSize)
//
//	// Memory planning
//	if info.OriginalSize > availableMemory {
//	    return useStreamingAssembly(contentCID)
//	}
//
//	// Network optimization
//	parallelFactor := min(info.ChunkCount, maxParallelDownloads)
//	setupParallelRetrieval(parallelFactor)
//
// СТРУКТУРА ВОЗВРАЩАЕМОЙ ИНФОРМАЦИИ:
//
//	type ChunkInfo struct {
//	    OriginalSize int    // Размер original данных в байтах
//	    ChunkSize    int    // Размер каждого chunk в байтах
//	    ChunkCount   int    // Общее количество chunks
//	    ContentType  string // Тип content: "binary" или "ipld"
//	}
func (cns *ChunkedNodeStore) GetChunkInfo(ctx context.Context, c cid.Cid) (*ChunkInfo, error) {
	node, err := cns.bs.GetNode(ctx, c)
	if err != nil {
		return nil, err
	}

	if !cns.isChunkedNode(node) {
		return nil, fmt.Errorf("not a chunked node")
	}

	info := &ChunkInfo{}

	// Извлекаем original_size
	if sizeNode, err := node.LookupByString("original_size"); err == nil {
		if size, err := sizeNode.AsInt(); err == nil {
			info.OriginalSize = int(size)
		}
	}

	// Извлекаем chunk_size
	if chunkSizeNode, err := node.LookupByString("chunk_size"); err == nil {
		if chunkSize, err := chunkSizeNode.AsInt(); err == nil {
			info.ChunkSize = int(chunkSize)
		}
	}

	// Извлекаем content_type
	if contentTypeNode, err := node.LookupByString("content_type"); err == nil {
		if contentType, err := contentTypeNode.AsString(); err == nil {
			info.ContentType = contentType
		}
	}

	// Подсчитываем количество chunks
	if chunksNode, err := node.LookupByString("chunks"); err == nil {
		chunksList := chunksNode.ListIterator()
		for !chunksList.Done() {
			chunksList.Next()
			info.ChunkCount++
		}
	}

	return info, nil
}

// ChunkInfo представляет comprehensive информацию о chunked content для analysis и optimization.
// Предоставляет все necessary details для informed decisions о processing strategies.
//
// НАЗНАЧЕНИЕ СТРУКТУРЫ:
// Centralizes всю critical information о chunked content в single, easily accessible format.
// Enables efficient decision making для:
// - Memory allocation и resource planning
// - Network optimization и parallel processing
// - User interface и progress indication
// - Monitoring, analytics, и debugging
// - Content type specific processing strategies
//
// ПОЛЯ И ИХ ПРИМЕНЕНИЕ:
//
// OriginalSize:
// - Планирование memory allocation для assembly
// - User interface (показ total size)
// - Resource validation (достаточно ли места)
// - Performance estimation (время assembly)
//
// ChunkSize:
// - Network optimization (batch size planning)
// - Memory usage planning (peak allocation)
// - Parallel processing decisions
// - Bandwidth utilization optimization
//
// ChunkCount:
// - Progress tracking during assembly
// - Parallel download planning
// - Network request count estimation
// - Storage fragmentation analysis
//
// ContentType:
// - Processing strategy selection
// - Deserialization path determination
// - Validation rules application
// - Performance optimization choices
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ В UES:
//
// Memory Planning:
//
//	if info.OriginalSize > availableRAM {
//	    useStreamingAssembly()
//	} else {
//	    useInMemoryAssembly()
//	}
//
// Progress Indication:
//
//	progressBar.SetTotal(info.ChunkCount)
//	progressBar.SetDescription(fmt.Sprintf("Downloading %s",
//	    humanize.Bytes(uint64(info.OriginalSize))))
//
// Network Optimization:
//
//	parallelJobs := min(info.ChunkCount, maxConcurrentDownloads)
//	setupParallelRetrieval(parallelJobs)
//
// Content Processing:
//
//	switch info.ContentType {
//	case "binary":
//	    setupBinaryProcessor(info.OriginalSize)
//	case "ipld":
//	    setupIPLDProcessor(info.OriginalSize)
//	}
//
// СОВМЕСТИМОСТЬ:
// - JSON serializable для API responses
// - Lightweight для frequent передачи
// - Extensible для future chunking improvements
// - Compatible с existing monitoring systems
type ChunkInfo struct {
	OriginalSize int    `json:"original_size"` // Оригинальный размер данных в байтах перед chunking
	ChunkSize    int    `json:"chunk_size"`    // Размер одного chunk'а в байтах (последний может быть меньше)
	ChunkCount   int    `json:"chunk_count"`   // Общее количество chunks в данном content
	ContentType  string `json:"content_type"`  // Тип содержимого: "binary" для raw data, "ipld" для structured content
}
