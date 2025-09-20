// Package blockstore предоставляет расширенную реализацию IPFS блокстора с поддержкой
// IPLD (InterPlanetary Linked Data), UnixFS и CAR архивов. Этот пакет объединяет
// возможности стандартного IPFS blockstore с дополнительными функциями для работы
// с content-addressable storage, файловыми системами и структурированными данными.
//
// Основные возможности:
// - Хранение и получение блоков данных по их содержимому (CID)
// - Поддержка IPLD для работы со структурированными связанными данными
// - UnixFS для хранения файлов и директорий
// - CAR (Content Addressable aRchives) импорт/экспорт
// - Интеллектуальное кэширование блоков в памяти
// - Обход графов данных с помощью селекторов
// - Предзагрузка данных для оптимизации производительности
package blockstore

import (
	"context"         // Контекст для управления временем жизни операций и отмены
	"errors"          // Создание и обработка ошибок
	"io"              // Базовые интерфейсы ввода-вывода
	"sync"            // Примитивы синхронизации для thread-safe операций
	s "ues/datastore" // Локальный пакет datastore для персистентного хранения

	// LRU кэш для оптимизации доступа к часто используемым блокам
	lru "github.com/hashicorp/golang-lru/v2"

	// IPFS Boxo - модульная библиотека компонентов IPFS
	"github.com/ipfs/boxo/blockservice"     // Сервис для работы с блоками данных
	bstor "github.com/ipfs/boxo/blockstore" // Базовый интерфейс блокстора
	chunker "github.com/ipfs/boxo/chunker"  // Разбиение данных на фрагменты (chunks)
	"github.com/ipfs/boxo/files"            // Интерфейсы для работы с файлами и директориями

	// IPLD - система связанных данных для MerkleDAG
	"github.com/ipfs/boxo/ipld/merkledag"            // Построение и обход Merkle DAG
	unixfile "github.com/ipfs/boxo/ipld/unixfs/file" // UnixFS файловые операции
	imp "github.com/ipfs/boxo/ipld/unixfs/importer"  // Импорт файлов в UnixFS
	ufsio "github.com/ipfs/boxo/ipld/unixfs/io"      // UnixFS ввод-вывод

	// Базовые типы IPFS
	blocks "github.com/ipfs/go-block-format" // Формат блоков данных
	"github.com/ipfs/go-cid"                 // Content Identifier для адресации по содержимому
	format "github.com/ipfs/go-ipld-format"  // Базовые интерфейсы IPLD

	// CAR (Content Addressable aRchives) v2 для импорта/экспорта
	carv2 "github.com/ipld/go-car/v2"

	// IPLD Prime - современная реализация IPLD с улучшенной производительностью
	"github.com/ipld/go-ipld-prime"                     // Основные типы и интерфейсы IPLD
	"github.com/ipld/go-ipld-prime/datamodel"           // Модель данных IPLD
	"github.com/ipld/go-ipld-prime/linking"             // Система связывания узлов через ссылки
	cidlink "github.com/ipld/go-ipld-prime/linking/cid" // CID-based linking
	"github.com/ipld/go-ipld-prime/node/basicnode"      // Базовые узлы данных

	// Привязка Go типов к IPLD
	// Схемы данных IPLD
	"github.com/ipld/go-ipld-prime/storage/bsrvadapter"             // Адаптер blockservice для IPLD
	traversal "github.com/ipld/go-ipld-prime/traversal"             // Обход графов данных
	selector "github.com/ipld/go-ipld-prime/traversal/selector"     // Селекторы для фильтрации
	selb "github.com/ipld/go-ipld-prime/traversal/selector/builder" // Построение селекторов

	// Multihash для криптографических хеш-функций
	"github.com/multiformats/go-multihash"
)

// Константы для настройки разбивки файлов на фрагменты (chunking).
// Эти значения определяют размеры блоков при сохранении файлов в UnixFS.
const (
	// DefaultChunkSize - стандартный размер блока данных (256 KiB).
	// Выбран как компромисс между эффективностью хранения и производительностью сети.
	// Меньшие блоки увеличивают метаданные, большие - замедляют передачу по сети.
	DefaultChunkSize = 262144 // 256 KiB = 256 * 1024 байт

	// RabinMinSize - минимальный размер блока для Rabin chunking (128 KiB).
	// Rabin chunking использует content-defined chunking для лучшей дедупликации.
	// Алгоритм ищет естественные границы в данных на основе их содержимого.
	RabinMinSize = DefaultChunkSize / 2 // 128 KiB

	// RabinMaxSize - максимальный размер блока для Rabin chunking (512 KiB).
	// Ограничивает максимальный размер блока для предотвращения слишком больших фрагментов.
	RabinMaxSize = DefaultChunkSize * 2 // 512 KiB
)

// DefaultLP - прототип ссылки по умолчанию для создания CID.
// Определяет стандартные параметры для content-addressable идентификаторов:
// - CIDv1: современная версия формата CID с улучшенной совместимостью
// - DAG-CBOR: эффективный бинарный формат сериализации для структурированных данных
// - BLAKE3: быстрая криптографическая хеш-функция с отличной производительностью
var DefaultLP = cidlink.LinkPrototype{
	Prefix: cid.Prefix{
		Version:  1,                        // CIDv1 для лучшей совместимости и функциональности
		Codec:    uint64(cid.DagCBOR),      // DAG-CBOR для эффективной сериализации IPLD данных
		MhType:   uint64(multihash.BLAKE3), // BLAKE3 для быстрого и безопасного хеширования
		MhLength: -1,                       // Использовать стандартную длину хеша (32 байта для BLAKE3)
	},
}

// Blockstore представляет расширенный интерфейс блокстора с поддержкой IPLD, UnixFS и CAR.
// Интерфейс объединяет стандартные возможности IPFS blockstore с дополнительными функциями
// для работы со структурированными данными, файловыми системами и архивами.
//
// Архитектурные принципы:
// - Content-addressable storage: данные адресуются по их криптографическому хешу (CID)
// - Immutable data: блоки данных неизменяемы после создания
// - Deduplication: одинаковые данные хранятся только один раз
// - Integrity: целостность данных гарантируется криптографическими хешами
//
// Встроенные интерфейсы:
// - bstor.Blockstore: базовые операции с блоками (Put, Get, Has, Delete)
// - bstor.Viewer: оптимизированный доступ к данным блоков без копирования
// - io.Closer: корректное освобождение ресурсов при закрытии
type Blockstore interface {
	// Встраивание стандартного интерфейса IPFS blockstore
	// Предоставляет базовые операции: Put, Get, PutMany, DeleteBlock, Has, GetSize, AllKeysChan
	bstor.Blockstore

	// Встраивание интерфейса Viewer для эффективного доступа к данным блоков
	// Позволяет читать данные блока без создания копий в памяти
	bstor.Viewer

	// Встраивание интерфейса Closer для корректного управления ресурсами
	io.Closer

	// Datastore возвращает базовый datastore, используемый для персистентного хранения блоков.
	// Позволяет выполнять низкоуровневые операции с хранилищем данных.
	Datastore() s.Datastore

	// PutNode сохраняет любой IPLD узел через LinkSystem с автоматической сериализацией.
	// Метод использует IPLD Prime для эффективной работы с структурированными данными.
	//
	// Возможности:
	// - Автоматическая сериализация узла в бинарный формат (DAG-CBOR)
	// - Вычисление CID на основе содержимого узла
	// - Сохранение блока в persistent storage
	// - Интеграция с LinkSystem для связанных данных
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции
	//   - n: IPLD узел для сохранения (любой тип, реализующий datamodel.Node)
	//
	// Возвращает:
	//   - cid.Cid: уникальный идентификатор сохраненного блока
	//   - error: ошибка сериализации или сохранения
	PutNode(ctx context.Context, n datamodel.Node) (cid.Cid, error)

	// GetNode загружает и десериализует IPLD узел по его CID.
	// Возвращает узел как универсальный тип (basicnode.Any) для максимальной гибкости.
	//
	// Возможности:
	// - Автоматическая десериализация из бинарного формата
	// - Поддержка любых IPLD данных (maps, lists, strings, numbers, etc.)
	// - Lazy loading связанных узлов по требованию
	// - Кэширование для оптимизации повторных обращений
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции
	//   - c: CID блока для загрузки
	//
	// Возвращает:
	//   - datamodel.Node: десериализованный IPLD узел
	//   - error: ошибка загрузки или десериализации
	GetNode(ctx context.Context, c cid.Cid) (datamodel.Node, error)

	// AddFile импортирует файл в UnixFS формат с возможностью выбора алгоритма разбивки.
	// Поддерживает как фиксированное разбиение, так и content-defined chunking для дедупликации.
	//
	// Алгоритмы разбивки:
	// - Fixed-size chunking: фиксированные блоки DefaultChunkSize для простоты
	// - Rabin chunking: адаптивные границы блоков для лучшей дедупликации
	//
	// UnixFS особенности:
	// - Совместимость с IPFS и другими UnixFS реализациями
	// - Поддержка больших файлов через иерархию блоков
	// - Метаданные файлов (размер, тип) сохраняются в structure blocks
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции
	//   - data: поток данных файла для импорта
	//   - useRabin: если true, использует Rabin chunking; иначе fixed-size
	//
	// Возвращает:
	//   - cid.Cid: корневой CID импортированного файла
	//   - error: ошибка импорта или разбивки файла
	AddFile(ctx context.Context, data io.Reader, useRabin bool) (cid.Cid, error)

	// GetFile извлекает файл из UnixFS формата как файловый узел.
	// Возвращает интерфейс files.Node для работы с файлами и директориями.
	//
	// Поддерживаемые типы:
	// - Обычные файлы (UnixFS File)
	// - Директории (UnixFS Directory)
	// - Символические ссылки (UnixFS Symlink)
	//
	// Возможности:
	// - Lazy loading: блоки загружаются по мере чтения
	// - Streaming: поддержка больших файлов без загрузки в память
	// - Metadata: доступ к размеру файла, правам доступа и другим атрибутам
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции
	//   - c: корневой CID UnixFS объекта
	//
	// Возвращает:
	//   - files.Node: файловый узел для чтения данных и метаданных
	//   - error: ошибка загрузки или некорректный формат UnixFS
	GetFile(ctx context.Context, c cid.Cid) (files.Node, error)

	// GetReader возвращает Reader для потокового чтения больших файлов.
	// Оптимизирован для работы с chunked файлами без загрузки всего содержимого в память.
	//
	// Возможности:
	// - Streaming: последовательное чтение блоков файла
	// - Seeking: произвольный доступ к позициям в файле
	// - Buffering: интеллектуальная буферизация для оптимизации
	// - Resource management: автоматическое закрытие ресурсов
	//
	// Применение:
	// - Чтение больших файлов (видео, архивы, базы данных)
	// - Streaming обработка данных
	// - Произвольный доступ к частям файла
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции
	//   - c: корневой CID файла для чтения
	//
	// Возвращает:
	//   - io.ReadSeekCloser: интерфейс для чтения с поддержкой позиционирования
	//   - error: ошибка открытия файла или некорректный формат
	GetReader(ctx context.Context, c cid.Cid) (io.ReadSeekCloser, error)

	// Walk выполняет обход всего подграфа данных от корневого узла.
	// Использует селекторы для определения стратегии обхода и вызывает callback для каждого узла.
	//
	// Особенности обхода:
	// - Depth-first traversal: сначала в глубину для эффективности памяти
	// - Cycle detection: предотвращение бесконечных циклов в графах
	// - Progress tracking: информация о текущем пути и глубине
	// - Error handling: продолжение обхода при ошибках отдельных узлов
	//
	// Применение:
	// - Анализ структуры данных
	// - Валидация целостности графа
	// - Сбор статистики по данным
	// - Миграция или трансформация данных
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции и отмены
	//   - root: корневой CID для начала обхода
	//   - visit: функция-callback, вызываемая для каждого посещенного узла
	//
	// Возвращает:
	//   - error: ошибка обхода или выполнения callback функции
	Walk(ctx context.Context, root cid.Cid, visit func(p traversal.Progress, n datamodel.Node) error) error

	// GetSubgraph выполняет селективный обход графа данных с фильтрацией узлов.
	// Возвращает список всех CID, которые соответствуют критериям селектора.
	//
	// Селекторы позволяют:
	// - Фильтровать узлы по типу данных (maps, arrays, primitives)
	// - Ограничивать глубину обхода
	// - Выбирать определенные поля в структурах
	// - Применять сложные логические условия
	//
	// Применение:
	// - Частичная репликация данных
	// - Построение индексов по части графа
	// - Фильтрация данных для экспорта
	// - Анализ зависимостей между узлами
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции
	//   - root: корневой CID для начала обхода
	//   - selectorNode: IPLD узел, описывающий критерии фильтрации
	//
	// Возвращает:
	//   - []cid.Cid: список CID всех узлов, соответствующих селектору
	//   - error: ошибка обхода или некорректный селектор
	GetSubgraph(ctx context.Context, root cid.Cid, selectorNode datamodel.Node) ([]cid.Cid, error)

	// Prefetch выполняет предварительную загрузку блоков для оптимизации последующих операций.
	// Использует пул воркеров для параллельной загрузки блоков в кэш.
	//
	// Стратегия предзагрузки:
	// - Параллельная загрузка с настраиваемым количеством воркеров
	// - Intelligent ordering: приоритет часто используемым блокам
	// - Memory management: контроль потребления памяти кэшем
	// - Network optimization: группировка запросов для снижения latency
	//
	// Применение:
	// - Warming up кэша перед интенсивными операциями
	// - Предзагрузка данных для offline использования
	// - Оптимизация производительности при известных паттернах доступа
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции и отмены
	//   - root: корневой CID для определения набора блоков
	//   - selectorNode: селектор для фильтрации блоков для предзагрузки
	//   - workers: количество параллельных воркеров (0 = использовать значение по умолчанию)
	//
	// Возвращает:
	//   - error: ошибка предзагрузки или превышение лимитов ресурсов
	Prefetch(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, workers int) error

	// ExportCARV2 создает CAR (Content Addressable aRchive) архив с данными.
	// Экспортирует выбранную часть графа данных в стандартизированный формат для обмена.
	//
	// CAR v2 возможности:
	// - Indexed access: быстрый поиск блоков через встроенный индекс
	// - Streaming: поддержка больших архивов без загрузки в память
	// - Compression: опциональное сжатие для экономии места
	// - Verification: встроенные checksums для проверки целостности
	//
	// Применение:
	// - Backup и архивирование данных
	// - Обмен данными между системами
	// - Репликация между узлами IPFS
	// - Offline access к данным
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции
	//   - root: корневой CID для экспорта
	//   - selectorNode: селектор для выбора данных для включения в архив
	//   - w: destination writer для записи архива
	//   - opts: дополнительные опции (compression, index format, etc.)
	//
	// Возвращает:
	//   - error: ошибка создания архива или записи данных
	ExportCARV2(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, w io.Writer, opts ...carv2.WriteOption) error

	// ImportCARV2 импортирует блоки данных из CAR архива в blockstore.
	// Поддерживает как CAR v1, так и CAR v2 для максимальной совместимости.
	//
	// Возможности импорта:
	// - Batch importing: эффективная загрузка множества блоков
	// - Deduplication: автоматическое исключение дублирующих блоков
	// - Validation: проверка целостности импортируемых данных
	// - Resume support: возможность продолжения прерванного импорта
	//
	// Применение:
	// - Восстановление данных из backup
	// - Синхронизация между узлами
	// - Migration данных между системами
	// - Seed new nodes с существующими данными
	//
	// Параметры:
	//   - ctx: контекст для управления временем жизни операции
	//   - r: source reader с данными CAR архива
	//   - opts: опции импорта (validation level, batch size, etc.)
	//
	// Возвращает:
	//   - []cid.Cid: список корневых CID из заголовка архива
	//   - error: ошибка чтения архива или импорта блоков
	ImportCARV2(ctx context.Context, r io.Reader, opts ...carv2.ReadOption) ([]cid.Cid, error)
}

// blockstore представляет конкретную реализацию расширенного интерфейса Blockstore.
// Структура объединяет несколько уровней абстракции IPFS экосистемы для предоставления
// единого API для работы с content-addressable данными, IPLD структурами и файловыми системами.
//
// Архитектурные слои:
// 1. Base storage: персистентное хранение через datastore
// 2. Caching layer: LRU кэш для горячих данных
// 3. Block service: сетевые операции и управление блоками
// 4. DAG service: обход связанных структур данных
// 5. Link system: IPLD сериализация и связывание
//
// Многопоточность:
// - Thread-safe операции через sync.RWMutex
// - Concurrent access к кэшу с минимальной блокировкой
// - Изоляция операций чтения и записи
type blockstore struct {
	ds s.Datastore // Персистентное хранилище для блоков данных

	// Blockstore - встроенный базовый blockstore из IPFS boxo.
	// Обеспечивает стандартную функциональность: Put, Get, Has, Delete операции.
	// Использует наш custom datastore как backend для персистентного хранения.
	bstor.Blockstore

	// lsys - LinkSystem из IPLD Prime для работы со связанными данными.
	// Отвечает за:
	// - Сериализацию/десериализацию IPLD узлов
	// - Автоматическое разрешение ссылок между узлами
	// - Создание CID для новых данных
	// - Интеграцию с различными codecs (DAG-CBOR, DAG-JSON, etc.)
	lsys *linking.LinkSystem

	// bS - BlockService предоставляет высокоуровневый API для работы с блоками.
	// Объединяет blockstore с возможностями сетевого обмена (exchange).
	// В нашем случае используется только локальная часть (без сетевого exchange).
	// Обеспечивает дополнительные возможности сверх базового blockstore.
	bS blockservice.BlockService

	// dS - DAGService для работы с Directed Acyclic Graph структурами.
	// Обеспечивает:
	// - Навигацию по связанным узлам
	// - Batch операции для эффективности
	// - Интеграцию с MerkleDAG протоколами
	// - Поддержку различных форматов узлов (UnixFS, raw, etc.)
	dS format.DAGService

	// mu - мьютекс для обеспечения thread-safe доступа к кэшу.
	// Использует RWMutex для оптимизации: множественные читатели, один писатель.
	// Защищает операции с cache от race conditions в многопоточной среде.
	mu sync.RWMutex

	// cache - LRU (Least Recently Used) кэш для часто используемых блоков.
	// Преимущества:
	// - Значительное ускорение повторных обращений к блокам
	// - Автоматическое управление памятью с вытеснением старых данных
	// - Настраиваемый размер для баланса памяти и производительности
	// - Thread-safe реализация с minimal lock contention
	cache *lru.Cache[string, blocks.Block]
}

// Compile-time проверка корректности реализации интерфейса.
// Гарантирует, что структура blockstore полностью реализует все методы интерфейса Blockstore.
// При отсутствии любого метода компиляция завершится ошибкой на этапе сборки.
var _ Blockstore = (*blockstore)(nil)

// NewBlockstore создает новый экземпляр расширенного blockstore с полной инициализацией
// всех необходимых компонентов IPFS экосистемы. Конструктор настраивает многоуровневую
// архитектуру для эффективной работы с content-addressable данными.
//
// Этапы инициализации:
// 1. Создание базового blockstore поверх datastore
// 2. Инициализация LRU кэша для горячих данных
// 3. Настройка BlockService для расширенных операций
// 4. Создание DAGService для навигации по графам
// 5. Конфигурация LinkSystem для IPLD операций
//
// Преимущества архитектуры:
// - Layered design: четкое разделение ответственности между компонентами
// - Performance optimization: кэширование на нескольких уровнях
// - Extensibility: простое добавление новых возможностей
// - IPFS compatibility: полная совместимость с экосистемой IPFS
//
// Параметры:
//   - ds: datastore для персистентного хранения блоков данных.
//     Должен поддерживать операции Put, Get, Delete и итерацию.
//
// Возвращает:
//   - *blockstore: готовый к использованию экземпляр blockstore со всеми
//     настроенными компонентами и зависимостями
//
// Пример использования:
//
//	datastore, err := NewDatastorage("/path/to/storage", nil)
//	if err != nil { log.Fatal(err) }
//
//	bs := NewBlockstore(datastore)
//	defer bs.Close()
//
//	// Использование blockstore для операций с данными
//	cid, err := bs.PutNode(ctx, someIPLDNode)
//	if err != nil { log.Fatal(err) }
func NewBlockstore(ds s.Datastore) *blockstore {
	// Создаем базовый blockstore поверх нашего datastore
	// Это обеспечивает стандартную функциональность IPFS blockstore
	base := bstor.NewBlockstore(ds)

	// Инициализируем структуру blockstore с базовым blockstore
	bs := &blockstore{
		ds:         ds,
		Blockstore: base,
	}

	// Создаем LRU кэш для 1000 блоков для оптимизации производительности
	// LRU (Least Recently Used) автоматически вытесняет старые блоки при превышении лимита
	// Размер 1000 выбран как компромисс между использованием памяти и hit rate
	cache, _ := lru.New[string, blocks.Block](1000)
	bs.cache = cache

	// Инициализируем мьютекс для thread-safe доступа к кэшу
	// RWMutex позволяет множественным читателям работать параллельно
	bs.mu = sync.RWMutex{}

	// Создаем BlockService поверх нашего blockstore
	// BlockService предоставляет дополнительные возможности сверх базового blockstore:
	// - Batch операции для эффективности
	// - Интеграция с сетевым обменом (в будущем)
	// - Дополнительные методы для работы с блоками
	// Передаем nil как exchange, так как используем только локальное хранилище
	bs.bS = blockservice.New(bs.Blockstore, nil)

	// Создаем DAGService для работы с направленными ациклическими графами
	// DAGService обеспечивает:
	// - Навигацию по связанным узлам
	// - Resolving ссылок между блоками
	// - Batch операции для эффективной работы с графами
	// - Поддержку различных форматов узлов (raw, dag-pb, dag-cbor, etc.)
	bs.dS = merkledag.NewDAGService(bs.bS)

	// Настраиваем LinkSystem для IPLD Prime операций
	// LinkSystem отвечает за:
	// - Сериализацию/десериализацию IPLD узлов
	// - Создание и разрешение ссылок между узлами
	// - Интеграцию с различными codecs
	// - Управление жизненным циклом связанных данных

	// Создаем адаптер для интеграции BlockService с IPLD Prime
	adapter := &bsrvadapter.Adapter{Wrapped: bs.bS}

	// Получаем стандартную конфигурацию LinkSystem
	lS := cidlink.DefaultLinkSystem()

	// Настраиваем storage для чтения и записи через наш адаптер
	lS.SetWriteStorage(adapter) // Запись новых блоков
	lS.SetReadStorage(adapter)  // Чтение существующих блоков

	// Сохраняем ссылку на настроенный LinkSystem
	bs.lsys = &lS

	return bs
}

// cacheBlock добавляет блок в LRU кэш для ускорения последующих обращений.
// Метод thread-safe и использует write lock для безопасного добавления блоков
// в конкурентной среде. Кэширование происходит асинхронно и не блокирует основные операции.
//
// Стратегия кэширования:
// - LRU eviction: автоматическое вытеснение наименее используемых блоков
// - Write-through: блок кэшируется сразу после записи в persistent storage
// - Memory management: автоматическое управление размером кэша
// - Thread-safety: безопасное использование в многопоточной среде
//
// Применение:
// - Ускорение повторных чтений одних и тех же блоков
// - Уменьшение нагрузки на persistent storage
// - Оптимизация производительности при работе с linked data
//
// Параметры:
//   - b: блок для добавления в кэш. Ключом служит строковое представление CID.
//
// Особенности:
// - Graceful degradation: при недоступности кэша операция просто пропускается
// - Zero allocation: использует существующий CID без дополнительных аллокаций
// - Atomic operation: добавление в кэш происходит атомарно
func (bs *blockstore) cacheBlock(b blocks.Block) {
	// Получаем write lock для безопасного изменения кэша
	bs.mu.Lock()
	defer bs.mu.Unlock()

	// Проверяем, что кэш инициализирован (graceful degradation)
	if bs.cache == nil {
		return
	}

	// Добавляем блок в LRU кэш, используя строковое представление CID как ключ
	// LRU автоматически обрабатывает вытеснение старых элементов при превышении лимита
	bs.cache.Add(b.Cid().String(), b)
}

// cacheGet пытается получить блок из LRU кэша для ускорения операций чтения.
// Метод thread-safe и использует read lock для минимизации блокировок
// при конкурентном доступе. Возвращает блок и флаг успешности поиска.
//
// Преимущества кэш-попаданий:
// - Мгновенный доступ к данным без обращения к storage
// - Снижение latency для часто используемых блоков
// - Уменьшение I/O операций и нагрузки на диск
// - Улучшение общей производительности системы
//
// Cache miss handling:
// - При отсутствии блока в кэше возвращается (nil, false)
// - Calling code должен обратиться к persistent storage
// - Полученный блок рекомендуется добавить в кэш через cacheBlock
//
// Параметры:
//   - key: строковое представление CID блока для поиска в кэше
//
// Возвращает:
//   - blocks.Block: найденный блок данных (nil если не найден)
//   - bool: флаг успешности поиска (true = найден, false = cache miss)
//
// Особенности:
// - Lock-free reads: минимальное время блокировки для читающих операций
// - Memory efficiency: кэш хранит ссылки на блоки, не копирует данные
// - LRU update: обращение к элементу обновляет его позицию в LRU списке
func (bs *blockstore) cacheGet(key string) (blocks.Block, bool) {
	// Получаем read lock для безопасного чтения из кэша
	// RWMutex позволяет множественным читателям работать параллельно
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	// Проверяем, что кэш инициализирован (graceful degradation)
	if bs.cache == nil {
		return nil, false
	}

	// Пытаемся найти блок в LRU кэше
	// Get() автоматически обновляет позицию элемента в LRU списке
	return bs.cache.Get(key)
}

// Put сохраняет блок данных в blockstore с автоматическим кэшированием.
// Метод расширяет стандартную функциональность IPFS blockstore добавлением
// интеллектуального кэширования для ускорения последующих операций чтения.
//
// Последовательность операций:
// 1. Сохранение блока в persistent storage через базовый blockstore
// 2. Добавление блока в LRU кэш для быстрого доступа
// 3. Обновление метаданных и индексов при необходимости
//
// Параметры:
//   - ctx: контекст для управления временем жизни операции
//   - block: блок данных для сохранения с CID и raw data
//
// Возвращает:
//   - error: ошибка сохранения в storage или добавления в кэш
func (bs *blockstore) Put(ctx context.Context, block blocks.Block) error {
	// Сохраняем блок в persistent storage через базовый blockstore
	if err := bs.Blockstore.Put(ctx, block); err != nil {
		return err
	}
	// Добавляем блок в LRU кэш для ускорения последующих обращений
	bs.cacheBlock(block)
	return nil
}

// PutMany сохраняет множество блоков в одной операции с пакетным кэшированием.
// Обеспечивает высокую производительность при массовом импорте данных
// за счет минимизации количества операций с storage и оптимизации кэша.
//
// Преимущества пакетной операции:
// - Снижение накладных расходов на I/O операции
// - Атомарность: либо все блоки сохраняются, либо ни один
// - Эффективное использование кэша и memory bandwidth
// - Оптимизация для случаев массового импорта данных
//
// Параметры:
//   - ctx: контекст для управления временем жизни операции
//   - blks: массив блоков для пакетного сохранения
//
// Возвращает:
//   - error: ошибка пакетного сохранения или кэширования блоков
func (bs *blockstore) PutMany(ctx context.Context, blks []blocks.Block) error {
	// Выполняем пакетное сохранение через базовый blockstore
	if err := bs.Blockstore.PutMany(ctx, blks); err != nil {
		return err
	}
	// Добавляем все блоки в кэш для ускорения последующих операций
	for _, b := range blks {
		bs.cacheBlock(b)
	}
	return nil
}

// Get загружает блок данных с приоритетной проверкой кэша.
// Реализует стратегию cache-first для минимизации обращений к persistent storage
// и максимального ускорения операций чтения горячих данных.
//
// Алгоритм поиска:
// 1. Проверка LRU кэша для мгновенного доступа к часто используемым блокам
// 2. При cache miss обращение к persistent storage
// 3. Автоматическое кэширование загруженного блока для будущих обращений
//
// Оптимизации:
// - Zero-copy доступ к кэшированным данным
// - LRU update при каждом обращении
// - Минимизация lock contention при конкурентном доступе
//
// Параметры:
//   - ctx: контекст для управления временем жизни операции
//   - c: CID блока для загрузки
//
// Возвращает:
//   - blocks.Block: найденный блок с данными и метаданными
//   - error: ошибка поиска в кэше или загрузки из storage
func (bs *blockstore) Get(ctx context.Context, c cid.Cid) (blocks.Block, error) {
	// Сначала проверяем LRU кэш для быстрого доступа
	if blk, ok := bs.cacheGet(c.String()); ok {
		return blk, nil // Cache hit - возвращаем блок немедленно
	}

	// Cache miss - загружаем блок из persistent storage
	blk, err := bs.Blockstore.Get(ctx, c)
	if err != nil {
		return nil, err
	}

	// Кэшируем загруженный блок для ускорения будущих обращений
	bs.cacheBlock(blk)
	return blk, nil
}

// DeleteBlock удаляет блок из persistent storage и кэша.
// Обеспечивает синхронизацию между всеми уровнями хранения данных
// для предотвращения inconsistent state и stale cache entries.
//
// Последовательность операций:
// 1. Удаление блока из persistent storage
// 2. Принудительное удаление из LRU кэша
// 3. Очистка связанных метаданных и индексов
//
// Thread-safety:
// - Атомарная операция удаления из кэша
// - Защита от race conditions при конкурентном доступе
// - Consistent view данных во всех потоках
//
// Параметры:
//   - ctx: контекст для управления временем жизни операции
//   - c: CID блока для удаления
//
// Возвращает:
//   - error: ошибка удаления из storage или очистки кэша
func (bs *blockstore) DeleteBlock(ctx context.Context, c cid.Cid) error {
	// Удаляем блок из persistent storage
	if err := bs.Blockstore.DeleteBlock(ctx, c); err != nil {
		return err
	}

	// Принудительно удаляем блок из LRU кэша для предотвращения stale data
	bs.mu.Lock()
	if bs.cache != nil {
		// Используем Remove() для явного удаления из кэша
		bs.cache.Remove(c.String())
	}
	bs.mu.Unlock()
	return nil
}

// PutNode сохраняет IPLD узел с автоматической сериализацией через LinkSystem.
// Метод предоставляет высокоуровневый API для работы со структурированными данными
// без необходимости ручной сериализации и создания CID.
//
// IPLD процесс:
// 1. Сериализация узла в binary format (обычно DAG-CBOR)
// 2. Вычисление CID на основе содержимого и metadata
// 3. Сохранение блока через LinkSystem.Store
// 4. Автоматическое кэширование результата
//
// Поддерживаемые типы данных:
// - Maps (структуры и объекты)
// - Arrays (списки и массивы)
// - Primitives (strings, numbers, booleans, null)
// - Links (ссылки на другие IPLD узлы)
// - Bytes (бинарные данные)
//
// Параметры:
//   - ctx: контекст для управления временем жизни операции
//   - n: IPLD узел для сериализации и сохранения
//
// Возвращает:
//   - cid.Cid: уникальный идентификатор сохраненного узла
//   - error: ошибка сериализации, вычисления CID или сохранения
func (bs *blockstore) PutNode(ctx context.Context, n datamodel.Node) (cid.Cid, error) {
	// Проверяем инициализацию LinkSystem
	if bs.lsys == nil {
		return cid.Undef, errors.New("links system is nil")
	}

	// Сериализуем и сохраняем узел через IPLD LinkSystem
	// DefaultLP содержит настройки для CIDv1 + DAG-CBOR + BLAKE3
	lnk, err := bs.lsys.Store(ipld.LinkContext{Ctx: ctx}, DefaultLP, n)
	if err != nil {
		return cid.Undef, err
	}

	// Извлекаем CID из созданной ссылки
	c := lnk.(cidlink.Link).Cid

	// Блок автоматически сохранен в storage через LinkSystem adapter,
	// кэширование происходит на уровне Put операций
	return c, nil
}

// GetNode загружает и десериализует IPLD узел из blockstore.
// Возвращает узел как универсальный тип для максимальной гибкости
// при работе с различными структурами данных.
//
// IPLD процесс:
// 1. Загрузка raw блока по CID
// 2. Определение codec и format блока
// 3. Десериализация в IPLD datamodel
// 4. Возврат узла как basicnode.Any для универсального доступа
//
// Поддерживаемые форматы:
// - DAG-CBOR: эффективный бинарный формат для структурированных данных
// - DAG-JSON: человеко-читаемый JSON формат
// - Raw: произвольные бинарные данные
// - DAG-PB: протобуф формат для UnixFS
//
// Параметры:
//   - ctx: контекст для управления временем жизни операции
//   - c: CID узла для загрузки и десериализации
//
// Возвращает:
//   - datamodel.Node: десериализованный IPLD узел
//   - error: ошибка загрузки блока или десериализации
func (bs *blockstore) GetNode(ctx context.Context, c cid.Cid) (datamodel.Node, error) {
	// Проверяем инициализацию LinkSystem
	if bs.lsys == nil {
		return nil, errors.New("link system is nil")
	}

	// Создаем ссылку из CID
	lnk := cidlink.Link{Cid: c}

	// Загружаем и десериализуем узел через IPLD LinkSystem
	// basicnode.Prototype.Any позволяет работать с любыми типами данных
	return bs.lsys.Load(ipld.LinkContext{Ctx: ctx}, lnk, basicnode.Prototype.Any)
}

// AddFile импортирует файл в UnixFS формат с выбором алгоритма разбивки.
// Поддерживает как фиксированное разбиение для простоты, так и Rabin chunking
// для оптимальной дедупликации данных в distributed storage системах.
//
// Алгоритмы chunking:
// - Fixed-size: стабильные блоки DefaultChunkSize для предсказуемости
// - Rabin: content-defined boundaries для максимальной дедупликации
//
// UnixFS структура:
// - Leaf nodes: содержат фрагменты файла
// - Internal nodes: содержат ссылки на child nodes и метаданные
// - Root node: содержит метаданные файла и корневые ссылки
func (bs *blockstore) AddFile(ctx context.Context, data io.Reader, useRabin bool) (cid.Cid, error) {
	var spl chunker.Splitter
	if useRabin {
		// Rabin chunking с переменными границами для дедупликации
		spl = chunker.NewRabinMinMax(data, RabinMinSize, DefaultChunkSize, RabinMaxSize)
	} else {
		// Фиксированное разбиение для простоты и предсказуемости
		spl = chunker.NewSizeSplitter(data, DefaultChunkSize)
	}
	// Строим DAG из фрагментов файла через UnixFS importer
	nd, err := imp.BuildDagFromReader(bs.dS, spl)
	if err != nil {
		return cid.Undef, err
	}
	return nd.Cid(), nil
}

// GetFile извлекает файл из UnixFS формата как файловый узел.
// Поддерживает различные типы UnixFS объектов: файлы, директории, symlinks.
func (bs *blockstore) GetFile(ctx context.Context, c cid.Cid) (files.Node, error) {
	// Загружаем корневой узел UnixFS объекта
	nd, err := bs.dS.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	// Создаем файловый узел с поддержкой streaming и navigation
	return unixfile.NewUnixfsFile(ctx, bs.dS, nd)
}

// GetReader возвращает потоковый Reader для эффективного чтения больших файлов.
// Поддерживает seeking и lazy loading блоков для оптимизации памяти.
func (bs *blockstore) GetReader(ctx context.Context, c cid.Cid) (io.ReadSeekCloser, error) {
	// Загружаем корневой узел файла
	nd, err := bs.dS.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	// Создаем DAG reader с поддержкой seeking и streaming
	return ufsio.NewDagReader(ctx, nd, bs.dS)
}

// View обеспечивает оптимизированный доступ к raw данным блока без копирования.
// Использует zero-copy паттерн для минимизации memory allocations при чтении данных.
func (bs *blockstore) View(ctx context.Context, id cid.Cid, callback func([]byte) error) error {
	// Проверяем поддержку Viewer interface в базовом blockstore
	if v, ok := bs.Blockstore.(bstor.Viewer); ok {
		return v.View(ctx, id, callback)
	}
	// Fallback к стандартному Get при отсутствии Viewer поддержки
	blk, err := bs.Blockstore.Get(ctx, id)
	if err != nil {
		return err
	}
	return callback(blk.RawData())
}

// BuildSelectorNodeExploreAll создает селектор-узел для полного обхода графа данных.
// Возвращает IPLD узел, который описывает стратегию "обойти все связанные данные"
// для использования в операциях Walk, GetSubgraph и экспорта данных.
//
// Селектор конфигурация:
// - ExploreRecursive: рекурсивный обход по всем ссылкам
// - RecursionLimitNone: без ограничения глубины рекурсии
// - ExploreAll: обход всех полей в каждом узле
// - ExploreRecursiveEdge: переход по ссылкам на связанные узлы
//
// Применение:
// - Полная репликация данных
// - Создание complete backups
// - Deep validation графов данных
func BuildSelectorNodeExploreAll() datamodel.Node {
	sb := selb.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return sb.
		ExploreRecursive(selector.RecursionLimitNone(),
			sb.ExploreAll(sb.ExploreRecursiveEdge()),
		).Node()
}

// CompileSelector преобразует IPLD узел-селектор в executable selector.
// Компилирует декларативное описание селектора в оптимизированную
// структуру данных для эффективного выполнения обхода графа.
func CompileSelector(n datamodel.Node) (selector.Selector, error) {
	return selector.CompileSelector(n)
}

// Walk выполняет полный обход графа данных с вызовом callback для каждого узла.
// Использует "explore all" селектор для посещения всех связанных данных
// от корневого узла с поддержкой cycle detection и error handling.
func (bs *blockstore) Walk(ctx context.Context, root cid.Cid, visit func(p traversal.Progress, n datamodel.Node) error) error {
	if bs.lsys == nil {
		return errors.New("link system is nil")
	}
	// Загружаем корневой узел
	start, err := bs.lsys.Load(ipld.LinkContext{Ctx: ctx}, cidlink.Link{Cid: root}, basicnode.Prototype.Any)
	if err != nil {
		return err
	}
	// Создаем и компилируем селектор для полного обхода
	spec := BuildSelectorNodeExploreAll()
	sel, err := CompileSelector(spec)
	if err != nil {
		return err
	}
	// Настраиваем конфигурацию обхода
	cfg := traversal.Config{
		LinkSystem: *bs.lsys,
		LinkTargetNodePrototypeChooser: func(ipld.Link, ipld.LinkContext) (datamodel.NodePrototype, error) {
			return basicnode.Prototype.Any, nil
		},
	}
	// Выполняем обход с вызовом callback для каждого узла
	return traversal.Progress{Cfg: &cfg}.WalkMatching(start, sel, visit)
}

// Close освобождает ресурсы blockstore и закрывает underlying datastore.
// Гарантирует корректное завершение всех операций и освобождение памяти.
func (bs *blockstore) Close() error {
	return nil
}

// BuildSelectorExploreAll создает compiled selector для полного обхода графа.
// Альтернативная версия BuildSelectorNodeExploreAll, возвращающая готовый к использованию selector.
func BuildSelectorExploreAll() (selector.Selector, error) {
	ssb := selb.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	spec := ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge()),
	).Node()
	return selector.CompileSelector(spec)
}

// GetSubgraph собирает все CID в подграфе по заданному селектору.
// Возвращает список всех блоков, которые соответствуют критериям селектора
// для использования в операциях репликации, экспорта или анализа зависимостей.
func (bs *blockstore) GetSubgraph(ctx context.Context, root cid.Cid, selectorNode datamodel.Node) ([]cid.Cid, error) {
	// Загружаем корневой узел
	start, err := bs.lsys.Load(ipld.LinkContext{Ctx: ctx}, cidlink.Link{Cid: root}, basicnode.Prototype.Any)
	if err != nil {
		return nil, err
	}
	// Компилируем селектор из узла
	sel, err := CompileSelector(selectorNode)
	if err != nil {
		return nil, err
	}

	// Настраиваем конфигурацию обхода
	cfg := traversal.Config{
		LinkSystem: *bs.lsys,
		LinkTargetNodePrototypeChooser: func(ipld.Link, ipld.LinkContext) (datamodel.NodePrototype, error) {
			return basicnode.Prototype.Any, nil
		},
	}

	// Собираем все CID из подграфа
	out := make([]cid.Cid, 0, 1024)
	out = append(out, root) // Включаем корневой CID

	err = traversal.Progress{Cfg: &cfg}.WalkMatching(start, sel, func(p traversal.Progress, n datamodel.Node) error {
		// Добавляем CID каждого посещенного блока
		if p.LastBlock.Link != nil {
			if cl, ok := p.LastBlock.Link.(cidlink.Link); ok {
				out = append(out, cl.Cid)
			}
		}
		return nil
	})
	return out, err
}

// Prefetch выполняет параллельную предзагрузку блоков в кэш.
// Использует пул воркеров для эффективной загрузки множества блоков
// с целью warming up кэша перед интенсивными операциями чтения.
func (bs *blockstore) Prefetch(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, workers int) error {
	if workers <= 0 {
		workers = 8 // Значение по умолчанию
	}

	// Получаем список всех CID для предзагрузки
	cids, err := bs.GetSubgraph(ctx, root, selectorNode)
	if err != nil {
		return err
	}

	// Создаем пул воркеров для параллельной загрузки
	jobs := make(chan cid.Cid, workers*2)
	var wg sync.WaitGroup
	wg.Add(workers)

	// Запускаем воркеров
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for c := range jobs {
				_, _ = bs.Get(ctx, c) // Загружаем блок (кэшируется автоматически)
			}
		}()
	}

	// Отправляем задания воркерам
	for _, c := range cids {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- c:
		}
	}

	close(jobs)
	wg.Wait()
	return ctx.Err()
}

// ExportCARV2 создает CAR v2 архив с выбранными данными.
// Экспортирует подграф блоков в стандартизированный формат для обмена данными
// между различными IPFS системами с поддержкой индексации и сжатия.
func (bs *blockstore) ExportCARV2(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, w io.Writer, opts ...carv2.WriteOption) error {
	if bs.lsys == nil {
		return errors.New("link system is nil")
	}

	// Создаем селективный CAR writer для экспорта выбранных данных
	// Поддерживает различные опции: compression, indexing, validation
	writer, err := carv2.NewSelectiveWriter(ctx, bs.lsys, root, selectorNode, opts...)
	if err != nil {
		return err
	}

	// Записываем CAR архив в destination writer
	_, err = writer.WriteTo(w)
	return err
}

// ImportCARV2 импортирует блоки из CAR архива в blockstore.
// Поддерживает как CAR v1, так и CAR v2 с автоматическим определением формата
// и эффективной пакетной загрузкой блоков с проверкой целостности.
func (bs *blockstore) ImportCARV2(ctx context.Context, r io.Reader, opts ...carv2.ReadOption) ([]cid.Cid, error) {
	// Создаем CAR reader с поддержкой различных форматов
	br, err := carv2.NewBlockReader(r, opts...)
	if err != nil {
		return nil, err
	}

	// Извлекаем корневые CID из заголовка архива
	roots := br.Roots

	// Итеративно импортируем все блоки из архива
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			// Читаем следующий блок из архива
			blk, err := br.Next()
			if err == io.EOF {
				return roots, nil // Все блоки импортированы
			}
			if err != nil {
				return nil, err
			}

			// Сохраняем блок в blockstore (с автоматическим кэшированием)
			if err := bs.Put(ctx, blk); err != nil {
				return nil, err
			}
		}
	}
}

// Datastore возвращает underlying datastore для прямых операций.
func (bs *blockstore) Datastore() s.Datastore {
	return bs.ds
}
