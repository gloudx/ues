// Package lexicon предоставляет систему управления схемами данных (лексиконами)
// для проекта UES (Universal Event Store). Этот пакет позволяет загружать,
// компилировать и валидировать IPLD схемы из YAML файлов.
//
// Основные возможности:
// - Загрузка схем из файловой системы
// - Кеширование скомпилированных схем для производительности
// - Валидация данных против определенных схем
// - Управление жизненным циклом схем (активные, черновики, устаревшие)
// - Thread-safe операции с использованием RWMutex
package lexicon

import (
	"context"       // Для контекста операций
	"fmt"           // Для форматирования строк и ошибок
	"io/fs"         // Для работы с файловой системой
	"os"            // Для чтения файлов
	"path/filepath" // Для работы с путями к файлам
	"strings"       // Для операций со строками
	"sync"          // Для синхронизации goroutines

	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/schema" // IPLD схемы для структурированных данных
	"gopkg.in/yaml.v3"                     // YAML парсер для конфигурационных файлов
)

// SchemaStatus определяет статус лексикона в жизненном цикле
//
// НАЗНАЧЕНИЕ:
// Управление жизненным циклом лексиконов от разработки до архивирования.
// Позволяет контролировать использование схем в продакшене и планировать их эволюцию.
//
// ПЕРЕХОДЫ СТАТУСОВ:
// draft -> active: после завершения разработки и тестирования
// active -> deprecated: когда появляется новая версия или схема устаревает
// deprecated -> archived: когда схема больше не поддерживается
//
// ПРАВИЛА ИСПОЛЬЗОВАНИЯ:
// - DRAFT: только для разработки и тестирования
// - ACTIVE: разрешено использование в продакшене
// - DEPRECATED: работает, но выдает предупреждения
// - ARCHIVED: блокируется создание новых записей
type SchemaStatus string

const (
	SchemaStatusDraft      SchemaStatus = "draft"      // Черновик - схема в разработке, не готова для продакшена
	SchemaStatusActive     SchemaStatus = "active"     // Активная - готова к полноценному использованию в продакшене
	SchemaStatusDeprecated SchemaStatus = "deprecated" // Устаревшая - работает, но не рекомендуется для новых проектов
	SchemaStatusArchived   SchemaStatus = "archived"   // Архивная - не используется, сохранена только для совместимости
)

// LexiconDefinition представляет определение схемы в YAML формате.
// Это основная структура данных для хранения метаинформации о схеме
// и самого определения схемы в текстовом виде.
//
// Структура соответствует формату YAML файлов схем:
// id: уникальный идентификатор схемы (например, "com.example.user.v1")
// version: версия схемы для контроля совместимости
// name: человеко-читаемое название схемы
// description: подробное описание назначения схемы
// status: состояние схемы (active/draft/deprecated)
// schema: текст IPLD схемы в DSL формате
type LexiconDefinition struct {
	ID          string       `yaml:"id"`          // Уникальный идентификатор схемы
	Version     string       `yaml:"version"`     // Версия схемы (семантическое версионирование)
	Name        string       `yaml:"name"`        // Человеко-читаемое название
	Description string       `yaml:"description"` // Подробное описание схемы
	Status      SchemaStatus `yaml:"status"`      // Статус: active, draft, deprecated
	Schema      string       `yaml:"schema"`      // IPLD схема в DSL формате
}

// Registry управляет лексиконами из файловой системы.
// Это центральный компонент для работы со схемами, который обеспечивает:
//
// 1. Thread-safe загрузку и кеширование схем
// 2. Ленивую компиляцию IPLD схем (компилируются только при первом обращении)
// 3. Валидацию данных против схем
// 4. Управление жизненным циклом схем
//
// Архитектура кеширования:
// - definitions: кеш загруженных YAML определений схем
// - compiledTypes: кеш скомпилированных IPLD TypeSystem для быстрого доступа
// - schemasDir: директория с YAML файлами схем
// - mu: RWMutex для thread-safe операций (читатели могут работать параллельно)
type Registry struct {
	mu            sync.RWMutex                  // Мьютекс для thread-safe доступа
	definitions   map[string]*LexiconDefinition // Кеш загруженных определений схем
	compiledTypes map[string]*schema.TypeSystem // Кеш скомпилированных IPLD схем
	schemasDir    string                        // Путь к директории с файлами схем
}

// NewRegistry создает новый реестр лексиконов.
// Инициализирует пустые кеши и устанавливает путь к директории со схемами.
//
// Параметры:
//
//	schemasDir - путь к директории содержащей YAML файлы со схемами.
//	             Поддерживаются файлы с расширениями .yaml и .yml
//
// Возвращает:
//
//	*Registry - готовый к использованию реестр схем
//
// Пример использования:
//
//	registry := NewRegistry("/path/to/schemas")
//	err := registry.LoadSchemas(context.Background())
func NewRegistry(schemasDir string) *Registry {
	return &Registry{
		definitions:   make(map[string]*LexiconDefinition), // Инициализируем пустую карту определений
		compiledTypes: make(map[string]*schema.TypeSystem), // Инициализируем пустую карту компилированных типов
		schemasDir:    schemasDir,                          // Сохраняем путь к директории схем
	}
}

// LoadSchemas загружает все схемы из директории.
// Выполняет рекурсивный обход директории schemasDir и загружает все файлы
// с расширениями .yaml и .yml как определения схем.
//
// Процесс загрузки:
// 1. Рекурсивный обход всех файлов и поддиректорий
// 2. Фильтрация только YAML файлов (.yaml/.yml)
// 3. Парсинг каждого файла как LexiconDefinition
// 4. Валидация корректности определения схемы
// 5. Сохранение в кеш definitions по ID схемы
//
// Параметры:
//
//	ctx - контекст для отмены операции или передачи метаданных
//
// Возвращает:
//
//	error - ошибка если не удалось загрузить или распарсить какой-либо файл
//
// Thread-safety: использует write lock для безопасного изменения кеша
func (r *Registry) LoadSchemas(ctx context.Context) error {
	r.mu.Lock()         // Захватываем write lock для изменения кеша
	defer r.mu.Unlock() // Освобождаем lock при выходе из функции

	// Рекурсивно обходим все файлы в директории схем
	return filepath.WalkDir(r.schemasDir, func(path string, d fs.DirEntry, err error) error {
		// Проверяем ошибки доступа к файлу/директории
		if err != nil {
			return err
		}

		// Пропускаем директории и файлы не являющиеся YAML
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil // Продолжаем обход
		}

		// Читаем содержимое YAML файла
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read schema file %s: %w", path, err)
		}

		// Парсим YAML в структуру LexiconDefinition
		var def LexiconDefinition
		if err := yaml.Unmarshal(data, &def); err != nil {
			return fmt.Errorf("failed to parse schema file %s: %w", path, err)
		}

		// Валидируем корректность определения схемы
		if err := r.validateDefinition(&def); err != nil {
			return fmt.Errorf("invalid schema in %s: %w", path, err)
		}

		// Сохраняем определение в кеш по ID схемы
		r.definitions[def.ID] = &def
		return nil // Продолжаем обход остальных файлов
	})
}

// GetSchema возвращает определение схемы по ID.
// Выполняет поиск схемы в кеше загруженных определений.
//
// Параметры:
//
//	id - уникальный идентификатор схемы (например, "com.example.user.v1")
//
// Возвращает:
//
//	*LexiconDefinition - определение схемы с метаданными и текстом схемы
//	error - ошибка если схема с указанным ID не найдена
//
// Thread-safety: использует read lock для безопасного чтения из кеша
func (r *Registry) GetSchema(id string) (*LexiconDefinition, error) {
	r.mu.RLock()         // Захватываем read lock для чтения из кеша
	defer r.mu.RUnlock() // Освобождаем lock при выходе

	// Ищем определение схемы в кеше
	def, exists := r.definitions[id]
	if !exists {
		return nil, fmt.Errorf("schema not found: %s", id)
	}

	return def, nil
}

// GetCompiledSchema возвращает компилированную IPLD схему.
// Реализует паттерн "ленивой загрузки" - схема компилируется только при первом обращении
// и затем кешируется для повторного использования.
//
// Процесс получения компилированной схемы:
// 1. Проверка наличия в кеше compiledTypes (под read lock)
// 2. Если найдена - возврат закешированной версии
// 3. Если не найдена - компиляция схемы (под write lock)
// 4. Двойная проверка после получения write lock (double-checked locking)
// 5. Компиляция текста схемы в IPLD TypeSystem
// 6. Сохранение в кеш для последующего использования
//
// Параметры:
//
//	id - уникальный идентификатор схемы
//
// Возвращает:
//
//	*schema.TypeSystem - скомпилированная IPLD схема готовая для валидации данных
//	error - ошибка если схема не найдена или не может быть скомпилирована
//
// Thread-safety: использует pattern double-checked locking для оптимизации
func (r *Registry) GetCompiledSchema(id string) (*schema.TypeSystem, error) {

	// Первая проверка под read lock (быстрый путь для уже скомпилированных схем)
	r.mu.RLock()
	compiled, exists := r.compiledTypes[id]
	r.mu.RUnlock()

	if exists {
		return compiled, nil // Возвращаем закешированную компилированную схему
	}

	// Компилируем схему под write lock (медленный путь для новых схем)
	r.mu.Lock()
	defer r.mu.Unlock()

	// Двойная проверка после получения write lock - другая goroutine могла
	// уже скомпилировать схему пока мы ждали lock
	if compiled, exists := r.compiledTypes[id]; exists {
		return compiled, nil
	}

	// Ищем определение схемы в кеше
	def, exists := r.definitions[id]
	if !exists {
		return nil, fmt.Errorf("schema not found: %s", id)
	}

	// Компилируем текст схемы в IPLD TypeSystem
	compiled, err := r.compileSchema(def.Schema)
	if err != nil {
		return nil, fmt.Errorf("failed to compile schema %s: %w", id, err)
	}

	// Сохраняем скомпилированную схему в кеш для повторного использования
	r.compiledTypes[id] = compiled
	return compiled, nil
}

// ValidateData валидирует данные против схемы.
// Основной метод для проверки соответствия структуры данных определенной схеме.
//
// Процесс валидации:
// 1. Получение скомпилированной IPLD схемы по ID
// 2. Извлечение корневого типа из схемы (предполагается единственный основной тип)
// 3. Рекурсивная валидация данных против типа схемы
//
// Поддерживаемые типы данных:
// - Структуры (map[string]interface{})
// - Списки ([]interface{})
// - Словари/карты (map[string]interface{})
// - Примитивные типы (string, bool, int, float)
//
// Параметры:
//
//	id - идентификатор схемы для валидации
//	data - данные для проверки (обычно map[string]interface{} или срез)
//
// Возвращает:
//
//	error - ошибка валидации с подробным описанием несоответствия или nil если данные валидны
//
// Пример использования:
//
//	err := registry.ValidateData("com.example.user.v1", userData)
//	if err != nil {
//	    log.Printf("Validation failed: %v", err)
//	}
func (r *Registry) ValidateData(id string, data interface{}) error {
	// Получаем скомпилированную схему (может включать компиляцию при первом обращении)
	compiled, err := r.GetCompiledSchema(id)
	if err != nil {
		return err
	}

	// Получаем основной тип схемы (предполагаем что он единственный или первый)
	// В IPLD схемах обычно есть один главный тип, который описывает структуру данных
	var rootType schema.Type
	for _, typ := range compiled.GetTypes() {
		rootType = typ
		break // берем первый тип как корневой
	}

	// Проверяем что в схеме есть хотя бы один тип
	if rootType == nil {
		return fmt.Errorf("no types found in schema %s", id)
	}

	// Выполняем рекурсивную валидацию данных против корневого типа
	return r.validateAgainstType(rootType, data)
}

// ListSchemas возвращает список всех загруженных схем.
// Полезно для отладки, мониторинга и пользовательских интерфейсов.
//
// Возвращает:
//
//	[]string - срез содержащий все ID загруженных схем
//
// Thread-safety: использует read lock для безопасного чтения списка схем
//
// Пример использования:
//
//	schemas := registry.ListSchemas()
//	fmt.Printf("Loaded schemas: %v\n", schemas)
func (r *Registry) ListSchemas() []string {
	r.mu.RLock()         // Захватываем read lock для чтения списка
	defer r.mu.RUnlock() // Освобождаем lock при выходе

	// Создаем срез с предварительно выделенной емкостью для оптимизации
	schemas := make([]string, 0, len(r.definitions))

	// Извлекаем все ID схем из кеша определений
	for id := range r.definitions {
		schemas = append(schemas, id)
	}

	return schemas
}

// ReloadSchemas перезагружает все схемы из файловой системы.
// Полезно для применения изменений в файлах схем без перезапуска приложения.
// Полностью очищает кеши и загружает схемы заново.
//
// Процесс перезагрузки:
// 1. Очистка кеша определений схем (definitions)
// 2. Очистка кеша скомпилированных схем (compiledTypes)
// 3. Повторная загрузка всех схем из файловой системы
//
// Параметры:
//
//	ctx - контекст для отмены операции
//
// Возвращает:
//
//	error - ошибка если не удалось перезагрузить схемы
//
// Thread-safety: использует write lock для безопасной очистки и перезагрузки кешей
//
// Внимание: операция может быть дорогостоящей при большом количестве схем
func (r *Registry) ReloadSchemas(ctx context.Context) error {
	r.mu.Lock()         // Захватываем write lock для полной перезагрузки
	defer r.mu.Unlock() // Освобождаем lock при выходе

	// Полностью очищаем кеш определений схем
	r.definitions = make(map[string]*LexiconDefinition)

	// Полностью очищаем кеш скомпилированных схем
	r.compiledTypes = make(map[string]*schema.TypeSystem)

	// Повторно загружаем все схемы из файловой системы
	return r.LoadSchemas(ctx)
}

// validateDefinition проверяет корректность определения схемы.
// Выполняет базовую валидацию структуры LexiconDefinition на корректность
// и попытку компиляции схемы для раннего обнаружения ошибок.
//
// Проверки:
// 1. ID не должен быть пустым (используется как ключ в кешах)
// 2. Version не должна быть пустой (для контроля совместимости)
// 3. Schema не должна быть пустой (основное содержимое)
// 4. Status должен быть одним из валидных значений
// 5. Схема должна успешно компилироваться в IPLD TypeSystem
//
// Параметры:
//
//	def - указатель на определение схемы для валидации
//
// Возвращает:
//
//	error - ошибка валидации с описанием проблемы или nil если определение корректно
//
// Валидные статусы схемы:
//   - "active": схема активна и готова к использованию
//   - "draft": схема в разработке, может изменяться
//   - "deprecated": схема устарела, не рекомендуется для новых данных
func (r *Registry) validateDefinition(def *LexiconDefinition) error {
	// Проверяем что ID схемы не пустой (используется как ключ в map)
	if def.ID == "" {
		return fmt.Errorf("schema ID cannot be empty")
	}

	// Проверяем что версия указана (важно для контроля совместимости)
	if def.Version == "" {
		return fmt.Errorf("schema version cannot be empty")
	}

	// Проверяем что есть определение схемы (основное содержимое)
	if def.Schema == "" {
		return fmt.Errorf("schema definition cannot be empty")
	}

	// Проверяем что статус схемы валидный
	if def.Status != "active" && def.Status != "draft" && def.Status != "deprecated" {
		return fmt.Errorf("invalid status: %s", def.Status)
	}

	// Проверяем что схема компилируется без ошибок (раннее обнаружение проблем)
	_, err := r.compileSchema(def.Schema)
	if err != nil {
		return fmt.Errorf("schema compilation failed: %w", err)
	}

	return nil // Определение схемы корректно
}

// compileSchema компилирует IPLD схему из текста.
// Преобразует текстовое представление схемы (в DSL формате) в готовую к использованию
// IPLD TypeSystem для валидации данных.
//
// ТЕКУЩАЯ РЕАЛИЗАЦИЯ - ЗАГЛУШКА:
// В данный момент возвращает пустую TypeSystem как placeholder.
// В полной реализации здесь должен быть:
// 1. Парсинг DSL схемы (например, через schemadmt.Parse)
// 2. Создание соответствующих IPLD типов
// 3. Сборка TypeSystem из этих типов
//
// Параметры:
//
//	schemaText - текст схемы в IPLD DSL формате
//
// Возвращает:
//
//	*schema.TypeSystem - скомпилированная система типов готовая для использования
//	error - ошибка компиляции если схема содержит синтаксические или логические ошибки
//

// compileSchema компилирует IPLD схему из текста.
// Преобразует текстовое представление схемы (DSL формат) в готовую к использованию
// IPLD TypeSystem для валидации данных.
//
// Использует ipld.LoadSchema который:
// 1. Парсит IPLD Schema DSL
// 2. Компилирует типы в standalone TypeSystem
// 3. Выполняет валидацию и typecheck
//
// Параметры:
//
//	schemaText - текст схемы в IPLD DSL формате
//
// Возвращает:
//
//	*schema.TypeSystem - скомпилированная система типов готовая для использования
//	error - ошибка компиляции если схема содержит синтаксические или логические ошибки
func (r *Registry) compileSchema(schemaText string) (*schema.TypeSystem, error) {

	// Используем официальную функцию LoadSchema из главного пакета ipld
	typeSystem, err := ipld.LoadSchemaBytes([]byte(schemaText))
	if err != nil {
		return nil, fmt.Errorf("failed to load and compile schema: %w", err)
	}

	// Проверяем что TypeSystem создана успешно
	if typeSystem == nil {
		return nil, fmt.Errorf("compilation resulted in empty type system")
	}

	// Дополнительная валидация - проверяем что есть типы
	hasTypes := false
	for range typeSystem.GetTypes() {
		hasTypes = true
		break
	}

	if !hasTypes {
		return nil, fmt.Errorf("schema contains no valid types")
	}

	return typeSystem, nil
}

// validateAgainstType выполняет базовую валидацию данных против типа.
// Рекурсивно проверяет соответствие структуры данных указанному IPLD типу.
// Является центральным методом системы валидации.
//
// Поддерживаемые типы IPLD:
// - TypeKind_Struct: структуры/объекты (проверка полей и их типов)
// - TypeKind_String: строковые значения
// - TypeKind_Bool: булевые значения (true/false)
// - TypeKind_Int: целые числа (int, int8, int16, int32, int64)
// - TypeKind_Float: числа с плавающей точкой (float32, float64)
// - TypeKind_List: массивы/списки (рекурсивная валидация элементов)
// - TypeKind_Map: словари/карты (рекурсивная валидация значений)
//
// Параметры:
//
//	typ - IPLD тип для валидации
//	data - данные для проверки (interface{} для максимальной гибкости)
//
// Возвращает:
//
//	error - ошибка валидации с подробным описанием несоответствия или nil если данные валидны
//
// Алгоритм:
// 1. Определение типа данных через typ.TypeKind()
// 2. Dispatch к специализированному методу валидации (validateStruct, validateList, etc.)
// 3. Для примитивных типов - прямая проверка типа Go
func (r *Registry) validateAgainstType(typ schema.Type, data interface{}) error {
	// Определяем тип схемы и выбираем соответствующий метод валидации
	switch typ.TypeKind() {
	case schema.TypeKind_Struct:
		// Структуры - сложная валидация с проверкой полей
		return r.validateStruct(typ, data)

	case schema.TypeKind_String:
		// Строки - простая проверка типа
		if _, ok := data.(string); !ok {
			return fmt.Errorf("expected string, got %T", data)
		}

	case schema.TypeKind_Bool:
		// Булевые значения - простая проверка типа
		if _, ok := data.(bool); !ok {
			return fmt.Errorf("expected bool, got %T", data)
		}

	case schema.TypeKind_Int:
		// Целые числа - проверка всех возможных типов int
		switch data.(type) {
		case int, int8, int16, int32, int64:
			// Все целочисленные типы допустимы
		default:
			return fmt.Errorf("expected int, got %T", data)
		}

	case schema.TypeKind_Float:
		// Числа с плавающей точкой - проверка float типов
		switch data.(type) {
		case float32, float64:
			// Оба типа float допустимы
		default:
			return fmt.Errorf("expected float, got %T", data)
		}

	case schema.TypeKind_List:
		// Списки - рекурсивная валидация элементов
		return r.validateList(typ, data)

	case schema.TypeKind_Map:
		// Словари - рекурсивная валидация значений
		return r.validateMap(typ, data)
	}

	// Если тип поддерживается - валидация прошла успешно
	return nil
}

// validateStruct валидирует структуру.
// Проверяет соответствие объекта (map[string]interface{}) определению структуры в схеме.
// Выполняет валидацию всех полей: обязательных и опциональных.
//
// Процесс валидации структуры:
// 1. Проверка что данные представлены как map[string]interface{}
// 2. Приведение типа схемы к *schema.TypeStruct
// 3. Получение списка полей из определения структуры
// 4. Для каждого поля проверка:
//   - Присутствие обязательных полей
//   - Рекурсивная валидация значений полей против их типов
//
// Параметры:
//
//	typ - IPLD тип структуры для валидации
//	data - данные для проверки (ожидается map[string]interface{})
//
// Возвращает:
//
//	error - ошибка валидации с указанием проблемного поля или nil если структура валидна
//
// Особенности:
// - Поддерживает опциональные поля (field.IsOptional())
// - Рекурсивно валидирует вложенные структуры
// - Предоставляет детальную информацию об ошибках валидации
func (r *Registry) validateStruct(typ schema.Type, data interface{}) error {
	// Проверяем что данные представлены как объект (map)
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("expected map[string]interface{}, got %T", data)
	}

	// Приводим тип схемы к структуре (должен быть указатель на TypeStruct)
	structType, ok := typ.(*schema.TypeStruct)
	if !ok {
		return fmt.Errorf("expected *schema.TypeStruct, got %T", typ)
	}

	// Получаем определения полей структуры из схемы
	fields := structType.Fields()

	// Проверяем каждое поле определенное в схеме
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		fieldName := field.Name()

		// Проверяем присутствует ли поле в данных
		value, exists := dataMap[fieldName]

		// Если поле отсутствует и оно обязательное - это ошибка
		if !exists && !field.IsOptional() {
			return fmt.Errorf("required field missing: %s", fieldName)
		}

		// Если поле присутствует - рекурсивно валидируем его значение
		if exists {
			if err := r.validateAgainstType(field.Type(), value); err != nil {
				return fmt.Errorf("field %s: %w", fieldName, err)
			}
		}
	}

	return nil // Все поля структуры валидны
}

// validateList валидирует список.
// Проверяет соответствие массива ([]interface{}) определению списка в схеме.
// Выполняет рекурсивную валидацию каждого элемента списка.
//
// Процесс валидации списка:
// 1. Проверка что данные представлены как []interface{}
// 2. Приведение типа схемы к *schema.TypeList
// 3. Получение типа элементов списка из схемы
// 4. Рекурсивная валидация каждого элемента против типа элемента
//
// Параметры:
//
//	typ - IPLD тип списка для валидации
//	data - данные для проверки (ожидается []interface{})
//
// Возвращает:
//
//	error - ошибка валидации с указанием индекса проблемного элемента или nil если список валиден
//
// Особенности:
// - Поддерживает любую длину списка (включая пустые списки)
// - Все элементы должны соответствовать одному типу (valueType)
// - Предоставляет информацию о номере элемента при ошибке валидации
func (r *Registry) validateList(typ schema.Type, data interface{}) error {
	// Проверяем что данные представлены как срез/массив
	slice, ok := data.([]interface{})
	if !ok {
		return fmt.Errorf("expected []interface{}, got %T", data)
	}

	// Приводим тип схемы к списку (должен быть указатель на TypeList)
	listType, ok := typ.(*schema.TypeList)
	if !ok {
		return fmt.Errorf("expected *schema.TypeList, got %T", typ)
	}

	// Получаем тип элементов списка из определения схемы
	valueType := listType.ValueType()

	// Валидируем каждый элемент списка против типа элемента
	for i, item := range slice {
		if err := r.validateAgainstType(valueType, item); err != nil {
			// Включаем индекс элемента в сообщение об ошибке для удобства отладки
			return fmt.Errorf("list item %d: %w", i, err)
		}
	}

	return nil // Все элементы списка валидны
}

// validateMap валидирует map.
// validateMap валидирует map.
// Проверяет соответствие словаря (map[string]interface{}) определению карты в схеме.
// Выполняет рекурсивную валидацию каждого значения в карте.
//
// Процесс валидации карты:
// 1. Проверка что данные представлены как map[string]interface{}
// 2. Приведение типа схемы к *schema.TypeMap
// 3. Получение типа значений карты из схемы
// 4. Рекурсивная валидация каждого значения против типа значения
//
// Параметры:
//
//	typ - IPLD тип карты для валидации
//	data - данные для проверки (ожидается map[string]interface{})
//
// Возвращает:
//
//	error - ошибка валидации с указанием проблемного ключа или nil если карта валидна
//
// Особенности:
// - Поддерживает любое количество ключей (включая пустые карты)
// - Все значения должны соответствовать одному типу (valueType)
// - Ключи всегда строковые (map[string]interface{})
// - Предоставляет информацию о проблемном ключе при ошибке валидации
func (r *Registry) validateMap(typ schema.Type, data interface{}) error {
	// Проверяем что данные представлены как карта/словарь
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("expected map[string]interface{}, got %T", data)
	}

	// Приводим тип схемы к карте (должен быть указатель на TypeMap)
	mapType, ok := typ.(*schema.TypeMap)
	if !ok {
		return fmt.Errorf("expected *schema.TypeMap, got %T", typ)
	}

	// Получаем тип значений карты из определения схемы
	valueType := mapType.ValueType()

	// Валидируем каждое значение в карте против типа значения
	for key, value := range dataMap {
		if err := r.validateAgainstType(valueType, value); err != nil {
			// Включаем ключ в сообщение об ошибке для удобства отладки
			return fmt.Errorf("map key %s: %w", key, err)
		}
	}

	return nil // Все значения карты валидны
}

// IsActive проверяет активна ли схема.
// Утилитарный метод для быстрой проверки статуса схемы.
// Полезно для фильтрации схем в production окружении.
//
// Логика работы:
// 1. Поиск определения схемы в кеше по ID
// 2. Проверка что статус схемы равен "active"
//
// Параметры:
//
//	id - уникальный идентификатор схемы для проверки
//
// Возвращает:
//
//	bool - true если схема существует и имеет статус "active", false в остальных случаях
//
// Thread-safety: использует read lock для безопасного чтения статуса
//
// Применение:
// - Фильтрация активных схем для валидации
// - Предотвращение использования deprecated схем
// - Проверки в пользовательских интерфейсах
//
// Пример:
//
//	if registry.IsActive("com.example.user.v1") {
//	    // Можно использовать схему для валидации
//	}
func (r *Registry) IsActive(id string) bool {
	r.mu.RLock()         // Захватываем read lock для чтения статуса
	defer r.mu.RUnlock() // Освобождаем lock при выходе

	// Ищем определение схемы в кеше
	def, exists := r.definitions[id]
	if !exists {
		return false // Схема не существует
	}

	// Проверяем что статус схемы "active"
	return def.Status == "active"
}
