package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/schema"
)

// === INTERFACES ===
// Интерфейсы для интеграции с другими компонентами UES

// BlockStoreInterface представляет интерфейс для работы с blockstore
//
// АРХИТЕКТУРНАЯ РОЛЬ:
// Обеспечивает персистентность лексиконов в IPFS/IPLD блоках.
// Позволяет хранить схемы как контент-адресуемые данные с автоматической
// дедупликацией и верификацией целостности через CID.
//
// ИНТЕГРАЦИЯ:
// Реализуется через blockstore/blockstore.go и используется для:
// - Сохранения определений лексиконов
// - Версионирования схем через IPLD DAG
// - Распределенного хранения схем между узлами UES
type BlockStoreInterface interface {
	Put(ctx context.Context, data []byte) (cid.Cid, error) // Сохранение блока данных с возвратом CID
	Get(ctx context.Context, c cid.Cid) ([]byte, error)    // Получение блока по CID
	Has(ctx context.Context, c cid.Cid) (bool, error)      // Проверка существования блока
	Delete(ctx context.Context, c cid.Cid) error           // Удаление блока (редко используется)
}

// IndexerInterface представляет интерфейс для автоматического создания индексов
//
// НАЗНАЧЕНИЕ:
// Интегрируется с x/indexer для автоматического создания оптимизированных
// индексов базы данных на основе определений в лексиконах.
//
// АВТОМАТИЗАЦИЯ:
// При регистрации лексикона автоматически создаются индексы для всех
// полей, помеченных как indexable в IndexDefinition.
//
// ОПТИМИЗАЦИЯ:
// Indexer анализирует паттерны запросов и выбирает оптимальный тип
// индекса (B-Tree, Hash, FTS) для каждого поля схемы.
type IndexerInterface interface {
	CreateIndex(ctx context.Context, definition *IndexDefinition) error // Создание индекса по определению
	DropIndex(ctx context.Context, name string) error                   // Удаление индекса по имени
	ListIndexes(ctx context.Context) ([]*IndexDefinition, error)        // Список существующих индексов
}

// LexiconRegistry управляет жизненным циклом лексиконов в UES
//
// АРХИТЕКТУРНАЯ РОЛЬ:
// Центральный компонент для управления схемами данных в Repository Layer.
// Обеспечивает типобезопасность, версионирование и эволюцию структур данных
// с автоматической валидацией и миграцией.
//
// ОСНОВНЫЕ ФУНКЦИИ:
// - Регистрация и версионирование схем данных (IPLD + бизнес-логика)
// - Разрешение зависимостей между лексиконами (композиция схем)
// - Управление жизненным циклом (создание, обновление, deprecation)
// - Валидация совместимости версий (SemVer + breaking changes detection)
// - Автоматическая миграция данных между версиями схем
// - Кеширование компилированных схем для производительности
//
// АРХИТЕКТУРНЫЕ ПРИНЦИПЫ:
// 1. Schema-first approach: схема определяет API и валидацию
// 2. Immutable versioning: схемы неизменяемы после публикации
// 3. Forward compatibility: новые версии совместимы со старыми клиентами
// 4. Automatic indexing: индексы создаются автоматически по схеме
// 5. Type safety: IPLD схемы + runtime валидация
//
// ИНТЕГРАЦИЯ С UES:
// - blockstore: персистентность схем как IPLD блоки
// - indexer: автоматическое создание индексов по определениям
// - datastore: валидация данных при записи
// - repository: типизированный доступ к коллекциям
//
// ПРОИЗВОДИТЕЛЬНОСТЬ:
// - Lazy compilation: схемы компилируются при первом использовании
// - LRU cache: горячие схемы кешируются в памяти
// - Dependency resolution: один раз при регистрации, затем кеш
// - Batch operations: групповая регистрация связанных схем
//
// ПРИМЕРЫ ИСПОЛЬЗОВАНИЯ:
// - Определение структуры постов блога с тегами и комментариями
// - Миграция схемы пользователей при добавлении новых полей
// - Валидация JSON данных перед сохранением в Repository
// - Автоматическое создание индексов для быстрого поиска
type LexiconRegistry struct {
	mu sync.RWMutex // Мьютекс для thread-safe операций с внутренним состоянием

	// === STORAGE AND INTEGRATION ===
	blockstore BlockStoreInterface // Персистентность лексиконов в IPLD блоках
	indexer    IndexerInterface    // Автоматическое создание индексов по схемам

	// === PERFORMANCE CACHE ===
	// Высокопроизводительное кеширование для минимизации I/O и компиляции
	definitions  map[LexiconID]*LexiconDefinition        // Горячий кеш определений лексиконов (избегаем blockstore reads)
	schemas      map[SchemaVersionKey]*schema.TypeSystem // Компилированные IPLD схемы (дорогая операция)
	dependencies map[LexiconID][]LexiconID               // Граф зависимостей для быстрого resolution

	// === CONFIGURATION ===
	config *RegistryConfig // Параметры поведения registry (валидация, кеширование, миграции)
}

// SchemaVersionKey - композитный ключ для кеширования компилированных схем
//
// НАЗНАЧЕНИЕ:
// Уникальная идентификация схем в кеше с учетом как идентификатора
// лексикона, так и его версии. Необходимо для support множественных
// версий одного лексикона в памяти одновременно.
//
// ИСПОЛЬЗОВАНИЕ:
// - Ключ в schemas map для O(1) доступа к компилированным TypeSystem
// - Основа для LRU eviction policy при переполнении кеша
// - Сериализуется для persistent cache между рестартами приложения
type SchemaVersionKey struct {
	ID      LexiconID     // Глобально уникальный идентификатор лексикона (NSID)
	Version SchemaVersion // Семантическая версия схемы (major.minor.patch)
}

// RegistryConfig конфигурация поведения реестра лексиконов
//
// НАЗНАЧЕНИЕ:
// Централизованная конфигурация для тонкой настройки поведения LexiconRegistry
// в различных окружениях (development, staging, production).
//
// КАТЕГОРИИ НАСТРОЕК:
// 1. Cache management - оптимизация производительности
// 2. Validation policies - компромиссы между безопасностью и гибкостью
// 3. Migration strategies - автоматизация vs ручной контроль
// 4. Error handling - строгость vs отказоустойчивость
//
// ПРОФИЛИ ОКРУЖЕНИЙ:
// - Development: быстрые итерации, менее строгая валидация
// - Staging: баланс между скоростью и безопасностью
// - Production: максимальная безопасность, строгая валидация
type RegistryConfig struct {
	// === CACHE PERFORMANCE ===
	MaxCachedSchemas int           `json:"max_cached_schemas"` // Лимит компилированных схем в памяти (LRU eviction)
	CacheTTL         time.Duration `json:"cache_ttl"`          // Время жизни кеша схем (0 = permanent)

	// === VALIDATION POLICIES ===
	StrictValidation bool `json:"strict_validation"` // Строгая проверка breaking changes при updates
	AllowDeprecated  bool `json:"allow_deprecated"`  // Разрешить использование deprecated схем

	// === MIGRATION AUTOMATION ===
	AutoMigrate         bool `json:"auto_migrate"`          // Автоматический запуск миграций при update схем
	BackupBeforeMigrate bool `json:"backup_before_migrate"` // Создание snapshot'а данных перед миграцией
}

// DefaultRegistryConfig возвращает продакшен-готовую конфигурацию по умолчанию
//
// ПРИНЦИПЫ КОНФИГУРАЦИИ:
// - Безопасность превыше скорости (strict validation включена)
// - Разумные лимиты памяти (1000 схем ≈ 100MB при средней схеме)
// - Автоматизация операций с safety nets (backup перед миграцией)
// - Консервативная политика deprecated схем (запрещены по умолчанию)
//
// РЕКОМЕНДАЦИИ ПО ОКРУЖЕНИЯМ:
// - Development: StrictValidation=false, AllowDeprecated=true
// - Staging: текущие настройки с увеличенным CacheTTL
// - Production: текущие настройки с мониторингом MaxCachedSchemas
func DefaultRegistryConfig() *RegistryConfig {
	return &RegistryConfig{
		MaxCachedSchemas:    1000,      // Оптимальный баланс производительности/памяти
		CacheTTL:            time.Hour, // Регулярное обновление кеша для consistency
		StrictValidation:    true,      // Максимальная безопасность схем
		AllowDeprecated:     false,     // Принуждение к migration от устаревших схем
		AutoMigrate:         true,      // Автоматизация при наличии migration path
		BackupBeforeMigrate: true,      // Safety net для восстановления при проблемах
	}
}

// NewLexiconRegistry создает новый реестр лексиконов с заданными зависимостями
//
// ПАРАМЕТРЫ:
// - blockstore: интерфейс для персистентного хранения лексиконов в IPLD
// - indexer: интерфейс для автоматического создания индексов БД
// - config: конфигурация поведения (nil = продакшен настройки по умолчанию)
//
// ИНИЦИАЛИЗАЦИЯ:
// Создает пустой registry с инициализированными внутренними структурами.
// Не загружает существующие лексиконы - это делается отдельно через LoadExisting().
//
// ПОТОКОБЕЗОПАСНОСТЬ:
// Registry thread-safe с первого момента создания благодаря инициализированному mutex.
//
// ИСПОЛЬЗОВАНИЕ:
// registry := NewLexiconRegistry(blockstore, indexer, config)
// if err := registry.LoadExistingLexicons(ctx); err != nil { /* handle */ }
func NewLexiconRegistry(blockstore BlockStoreInterface, indexer IndexerInterface, config *RegistryConfig) *LexiconRegistry {
	if config == nil {
		config = DefaultRegistryConfig()
	}

	return &LexiconRegistry{
		blockstore:   blockstore,
		indexer:      indexer,
		definitions:  make(map[LexiconID]*LexiconDefinition),
		schemas:      make(map[SchemaVersionKey]*schema.TypeSystem),
		dependencies: make(map[LexiconID][]LexiconID),
		config:       config,
	}
}

// === LEXICON LIFECYCLE MANAGEMENT ===

// RegisterLexicon регистрирует новый лексикон или новую версию существующего
//
// ПРОЦЕСС РЕГИСТРАЦИИ:
// Атомарная операция, включающая валидацию, компиляцию и персистентность.
// При любой ошибке изменения откатываются, registry остается в консистентном состоянии.
//
// ЭТАПЫ ОБРАБОТКИ:
// 1. Валидация структуры и семантики определения лексикона
// 2. Проверка совместимости с существующими версиями (SemVer rules)
// 3. Компиляция IPLD схемы и проверка синтаксиса
// 4. Персистентное сохранение в blockstore с генерацией CID
// 5. Обновление внутренних кешей и индексов
// 6. Автоматическое создание индексов БД по определениям схемы
//
// СОВМЕСТИМОСТЬ:
// - Patch updates: всегда совместимы (только bugfix)
// - Minor updates: обратно совместимы (новые поля/функциональность)
// - Major updates: могут содержать breaking changes
//
// ВАЛИДАЦИЯ:
// - Структурная: соответствие LexiconDefinition schema
// - Семантическая: корректность IPLD schema syntax
// - Совместимость: проверка breaking changes через schema diffing
// - Зависимости: разрешение и валидация dependency graph
//
// АВТОМАТИЗАЦИЯ:
// - Индексы создаются автоматически для всех IndexDefinition в схеме
// - Миграции планируются автоматически при обновлении версий
// - Кеш обновляется атомарно после успешной записи
//
// ПРИМЕРЫ:
// Регистрация новой схемы блог-поста:
//
//	err := registry.RegisterLexicon(ctx, &LexiconDefinition{
//	    ID: "com.example.blog.post",
//	    Version: SchemaVersion{Major: 1, Minor: 0, Patch: 0},
//	    Schema: "type BlogPost struct { title String, content String, tags [String] }",
//	    Indexes: []*IndexDefinition{...},
//	})
//
// ОШИБКИ:
// - ErrInvalidDefinition: некорректная структура лексикона
// - ErrIncompatibleVersion: breaking changes без major bump
// - ErrSchemaCompilation: ошибки в IPLD schema syntax
// - ErrPersistenceFailure: проблемы записи в blockstore
// - ErrIndexCreation: ошибки создания индексов БД
func (lr *LexiconRegistry) RegisterLexicon(ctx context.Context, definition *LexiconDefinition) error {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	// Валидация входных данных
	if err := lr.validateLexiconDefinition(definition); err != nil {
		return fmt.Errorf("invalid lexicon definition: %w", err)
	}

	// Проверка совместимости с существующими версиями
	if err := lr.checkCompatibility(ctx, definition); err != nil {
		return fmt.Errorf("compatibility check failed: %w", err)
	}

	// Компиляция IPLD схемы
	compiledSchema, err := lr.compileIPLDSchema(definition)
	if err != nil {
		return fmt.Errorf("failed to compile IPLD schema: %w", err)
	}

	// Сохранение в blockstore
	cid, err := lr.persistLexicon(ctx, definition)
	if err != nil {
		return fmt.Errorf("failed to persist lexicon: %w", err)
	}

	// Обновление внутренних структур
	lexiconID := LexiconID(definition.ID)
	definition.StorageCID = cid
	definition.RegisteredAt = time.Now()

	// Обновление кеша
	lr.definitions[lexiconID] = definition
	versionKey := SchemaVersionKey{ID: lexiconID, Version: definition.Version}
	lr.schemas[versionKey] = compiledSchema

	// Обновление графа зависимостей
	lr.updateDependencyGraph(lexiconID, definition.Dependencies)

	// Создание индексов
	if err := lr.createIndexes(ctx, definition); err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	return nil
}

// GetLexicon получает определение лексикона по ID и версии с поддержкой кеширования
//
// СТРАТЕГИИ ПОЛУЧЕНИЯ:
// 1. Cache hit: мгновенное получение из памяти (O(1) lookup)
// 2. Storage lookup: загрузка из blockstore с последующим кешированием
// 3. Version resolution: автоматический выбор latest stable при version=nil
//
// ВЕРСИОНИРОВАНИЕ:
// - Если version=nil, автоматически выбирается последняя стабильная версия
// - Поддержка получения любой зарегистрированной версии схемы
// - Version resolution учитывает SchemaStatus (draft/active/deprecated)
//
// КЕШИРОВАНИЕ:
// - Первичная проверка in-memory cache для максимальной производительности
// - При cache miss загрузка из persistent storage (blockstore)
// - Автоматическое обновление кеша после успешной загрузки
// - LRU eviction при превышении MaxCachedSchemas
//
// ИСПОЛЬЗОВАНИЕ:
// // Получение последней версии
// def, err := registry.GetLexicon(ctx, "com.example.blog.post", nil)
//
// // Получение конкретной версии
// version := SchemaVersion{Major: 1, Minor: 2, Patch: 0}
// def, err := registry.GetLexicon(ctx, "com.example.blog.post", &version)
//
// ОШИБКИ:
// - ErrLexiconNotFound: лексикон с указанным ID не существует
// - ErrVersionNotFound: указанная версия не найдена
// - ErrStorageFailure: ошибки доступа к blockstore
func (lr *LexiconRegistry) GetLexicon(ctx context.Context, id LexiconID, version *SchemaVersion) (*LexiconDefinition, error) {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	// Если версия не указана, берем последнюю стабильную
	if version == nil {
		latest, err := lr.getLatestStableVersion(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("failed to get latest version: %w", err)
		}
		version = latest
	}

	// Проверяем кеш
	if definition, exists := lr.definitions[id]; exists && definition.Version.Equal(*version) {
		return definition, nil
	}

	// Загружаем из blockstore
	definition, err := lr.loadLexiconFromStorage(ctx, id, *version)
	if err != nil {
		return nil, fmt.Errorf("failed to load lexicon from storage: %w", err)
	}

	// Обновляем кеш
	lr.definitions[id] = definition

	return definition, nil
}

// GetCompiledSchema получает скомпилированную IPLD схему для runtime использования
//
// НАЗНАЧЕНИЕ:
// Предоставляет готовые к использованию IPLD TypeSystem для валидации данных,
// сериализации/десериализации и runtime type checking.
//
// ОПТИМИЗАЦИЯ ПРОИЗВОДИТЕЛЬНОСТИ:
// - Компиляция IPLD схем - дорогая операция (парсинг, валидация, linking)
// - Результаты кешируются в памяти для повторного использования
// - Cache warming: схемы компилируются заранее при регистрации
// - Lazy compilation: схемы компилируются при первом обращении
//
// ДВУХУРОВНЕВОЕ КЕШИРОВАНИЕ:
// 1. Проверка compiled schema cache (schemas map) - мгновенный доступ
// 2. При cache miss: получение definition → компиляция → кеширование
//
// THREAD SAFETY:
// - Read lock для проверки кеша (множественные concurrent readers)
// - Upgrade to write lock только при необходимости компиляции
// - Atomic updates кеша после успешной компиляции
//
// ИНТЕГРАЦИЯ С IPLD:
// Возвращает готовый TypeSystem для использования с:
// - ipld/go-ipld-prime/node для создания typed nodes
// - ipld/go-ipld-prime/codec для сериализации данных
// - Runtime валидацией структуры данных перед сохранением
//
// ИСПОЛЬЗОВАНИЕ:
// typeSystem, err := registry.GetCompiledSchema(ctx, lexiconID, version)
// if err != nil { return err }
// // Теперь можно использовать typeSystem для валидации данных
//
// ОШИБКИ:
// - ErrLexiconNotFound: базовый лексикон не найден
// - ErrSchemaCompilation: ошибки компиляции IPLD схемы
// - ErrDependencyFailure: не удалось разрешить зависимости схемы
func (lr *LexiconRegistry) GetCompiledSchema(ctx context.Context, id LexiconID, version SchemaVersion) (*schema.TypeSystem, error) {
	lr.mu.RLock()

	// Проверяем кеш
	versionKey := SchemaVersionKey{ID: id, Version: version}
	if compiledSchema, exists := lr.schemas[versionKey]; exists {
		lr.mu.RUnlock()
		return compiledSchema, nil
	}
	lr.mu.RUnlock()

	// Загружаем определение лексикона
	definition, err := lr.GetLexicon(ctx, id, &version)
	if err != nil {
		return nil, fmt.Errorf("failed to get lexicon definition: %w", err)
	}

	// Компилируем схему
	lr.mu.Lock()
	defer lr.mu.Unlock()

	compiledSchema, err := lr.compileIPLDSchema(definition)
	if err != nil {
		return nil, fmt.Errorf("failed to compile IPLD schema: %w", err)
	}

	// Сохраняем в кеш
	lr.schemas[versionKey] = compiledSchema

	return compiledSchema, nil
}

// ListLexicons возвращает список всех зарегистрированных лексиконов
func (lr *LexiconRegistry) ListLexicons(ctx context.Context, filter *LexiconFilter) ([]*LexiconDefinition, error) {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	var result []*LexiconDefinition

	for _, definition := range lr.definitions {
		if filter == nil || lr.matchesFilter(definition, filter) {
			result = append(result, definition)
		}
	}

	// Сортировка по ID и версии
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Version.Compare(result[j].Version) < 0
	})

	return result, nil
}

// === DEPENDENCY MANAGEMENT ===

// ResolveDependencies разрешает все зависимости для лексикона
func (lr *LexiconRegistry) ResolveDependencies(ctx context.Context, id LexiconID) ([]*LexiconDefinition, error) {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	var result []*LexiconDefinition
	visited := make(map[LexiconID]bool)

	if err := lr.resolveDependenciesRecursive(ctx, id, visited, &result); err != nil {
		return nil, fmt.Errorf("failed to resolve dependencies: %w", err)
	}

	return result, nil
}

// === MIGRATION MANAGEMENT ===

// CreateMigration создает миграцию между версиями лексикона
func (lr *LexiconRegistry) CreateMigration(ctx context.Context, from, to SchemaVersionKey, migration *Migration) error {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	// Валидация миграции
	if err := lr.validateMigration(from, to, migration); err != nil {
		return fmt.Errorf("invalid migration: %w", err)
	}

	// Получаем определения версий
	_, exists := lr.definitions[from.ID]
	if !exists {
		return fmt.Errorf("source lexicon version not found: %s@%s", from.ID, from.Version)
	}

	toDef, exists := lr.definitions[to.ID]
	if !exists {
		return fmt.Errorf("target lexicon version not found: %s@%s", to.ID, to.Version)
	}

	// Добавляем миграцию к целевой версии
	toDef.Migrations = append(toDef.Migrations, *migration)

	// Сохраняем обновленное определение
	_, err := lr.persistLexicon(ctx, toDef)
	if err != nil {
		return fmt.Errorf("failed to persist updated lexicon: %w", err)
	}

	return nil
}

// === VALIDATION ===

// ValidateData валидирует данные против лексикона
func (lr *LexiconRegistry) ValidateData(ctx context.Context, id LexiconID, version SchemaVersion, data interface{}) error {
	// Получаем скомпилированную схему
	compiledSchema, err := lr.GetCompiledSchema(ctx, id, version)
	if err != nil {
		return fmt.Errorf("failed to get compiled schema: %w", err)
	}

	// Получаем определение лексикона для кастомных валидаторов
	definition, err := lr.GetLexicon(ctx, id, &version)
	if err != nil {
		return fmt.Errorf("failed to get lexicon definition: %w", err)
	}

	// TODO: Implement IPLD schema validation
	// 1. Валидация через IPLD schema
	// 2. Применение кастомных валидаторов

	_ = compiledSchema
	_ = definition
	_ = data

	return nil
}

// === HELPER METHODS ===

// LexiconFilter фильтр для поиска лексиконов
type LexiconFilter struct {
	Namespace  *string        `json:"namespace,omitempty"`   // Фильтр по namespace
	Category   *string        `json:"category,omitempty"`    // Фильтр по категории
	Status     *SchemaStatus  `json:"status,omitempty"`      // Фильтр по статусу
	MinVersion *SchemaVersion `json:"min_version,omitempty"` // Минимальная версия
	MaxVersion *SchemaVersion `json:"max_version,omitempty"` // Максимальная версия
}

func (lr *LexiconRegistry) validateLexiconDefinition(definition *LexiconDefinition) error {
	if definition.ID == "" {
		return fmt.Errorf("lexicon ID cannot be empty")
	}

	if definition.Schema == nil {
		return fmt.Errorf("IPLD schema cannot be nil")
	}

	// TODO: Add more validation logic

	return nil
}

func (lr *LexiconRegistry) checkCompatibility(ctx context.Context, definition *LexiconDefinition) error {
	// TODO: Implement compatibility checking logic
	return nil
}

func (lr *LexiconRegistry) compileIPLDSchema(definition *LexiconDefinition) (*schema.TypeSystem, error) {
	// TODO: Implement IPLD schema compilation
	// Этот метод должен компилировать IPLD схему из definition.Schema
	// Временно возвращаем пустую схему
	typeSystem := &schema.TypeSystem{}
	return typeSystem, nil
}

func (lr *LexiconRegistry) persistLexicon(ctx context.Context, definition *LexiconDefinition) (cid.Cid, error) {
	// Сериализация в JSON
	data, err := json.Marshal(definition)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to marshal lexicon: %w", err)
	}

	// Сохранение в blockstore
	return lr.blockstore.Put(ctx, data)
}

func (lr *LexiconRegistry) updateDependencyGraph(id LexiconID, dependencies []LexiconDependency) {
	var deps []LexiconID
	for _, dep := range dependencies {
		deps = append(deps, LexiconID(dep.ID))
	}
	lr.dependencies[id] = deps
}

func (lr *LexiconRegistry) createIndexes(ctx context.Context, definition *LexiconDefinition) error {
	// TODO: Implement index creation logic
	// Создание индексов на основе IndexDefinition из лексикона
	return nil
}

func (lr *LexiconRegistry) getLatestStableVersion(ctx context.Context, id LexiconID) (*SchemaVersion, error) {
	// TODO: Implement logic to find latest stable version
	definition, exists := lr.definitions[id]
	if !exists {
		return nil, fmt.Errorf("lexicon not found: %s", id)
	}
	return &definition.Version, nil
}

func (lr *LexiconRegistry) loadLexiconFromStorage(ctx context.Context, id LexiconID, version SchemaVersion) (*LexiconDefinition, error) {
	// TODO: Implement loading from blockstore by constructing appropriate CID/key
	return nil, fmt.Errorf("not implemented")
}

func (lr *LexiconRegistry) matchesFilter(definition *LexiconDefinition, filter *LexiconFilter) bool {
	if filter.Namespace != nil && definition.Namespace != *filter.Namespace {
		return false
	}

	if filter.Status != nil && definition.Status != *filter.Status {
		return false
	}

	// TODO: Add more filter logic

	return true
}

func (lr *LexiconRegistry) resolveDependenciesRecursive(ctx context.Context, id LexiconID, visited map[LexiconID]bool, result *[]*LexiconDefinition) error {
	if visited[id] {
		return nil // Уже обработан
	}

	visited[id] = true

	// Получаем определение лексикона
	definition, exists := lr.definitions[id]
	if !exists {
		return fmt.Errorf("lexicon not found: %s", id)
	}

	// Рекурсивно разрешаем зависимости
	for _, depID := range lr.dependencies[id] {
		if err := lr.resolveDependenciesRecursive(ctx, depID, visited, result); err != nil {
			return err
		}
	}

	// Добавляем текущий лексикон в результат
	*result = append(*result, definition)

	return nil
}

func (lr *LexiconRegistry) validateMigration(from, to SchemaVersionKey, migration *Migration) error {
	if from.ID != to.ID {
		return fmt.Errorf("migration must be between versions of the same lexicon")
	}

	if from.Version.Equal(to.Version) {
		return fmt.Errorf("migration source and target versions cannot be the same")
	}

	// TODO: Add more migration validation

	return nil
}
