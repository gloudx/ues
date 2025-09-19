package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/ipld/go-ipld-prime/schema"
)

// LexiconExample демонстрирует полный жизненный цикл использования лексиконов в UES
//
// НАЗНАЧЕНИЕ ПРИМЕРА:
// Comprehensive демонстрация всех возможностей lexicon system:
// от создания schema definition до validation данных и automatic indexing.
//
// ПОКРЫВАЕМЫЕ СЦЕНАРИИ:
// 1. Schema definition - создание типизированной схемы для blog posts
// 2. Lexicon registration - регистрация в central registry
// 3. Data validation - проверка структуры и business rules
// 4. Automatic indexing - создание optimized database indexes
// 5. Version management - handling schema evolution
// 6. Error handling - comprehensive error management patterns
//
// АРХИТЕКТУРНЫЕ ПРИНЦИПЫ:
// - Schema-first approach: схема определяет структуру и правила
// - Type safety: IPLD schemas + runtime validation
// - Performance optimization: automatic index creation
// - Developer experience: простой API для complex operations
//
// РЕАЛЬНОЕ ИСПОЛЬЗОВАНИЕ:
// Этот example может быть адаптирован для любых типов данных в UES:
// - User profiles с validation и privacy settings
// - Media metadata с автоматической индексацией по tags
// - Analytics events с time-series optimizations
// - Configuration schemas с version migrations
//
// ИНТЕГРАЦИЯ:
// Example показывает integration points со всеми компонентами UES:
// - Repository Layer для data persistence
// - Indexer для query optimization
// - BlockStore для schema versioning
// - Validation framework для data integrity
//
// ТЕСТИРОВАНИЕ:
// Использует mock components для демонстрации без real dependencies.
// В production коде mock'и заменяются на real implementations.
//
// ИСПОЛЬЗОВАНИЕ:
//
//	if err := ExampleLexiconUsage(ctx); err != nil {
//	    log.Fatalf("Lexicon example failed: %v", err)
//	}
//
// ОБУЧАЮЩАЯ ЦЕННОСТЬ:
// - Best practices для schema design
// - Error handling patterns
// - Performance considerations
// - Integration patterns между UES components
func ExampleLexiconUsage(ctx context.Context) error {
	// === НАСТРОЙКА КОМПОНЕНТОВ ===

	// Создаем базовые компоненты (заглушки)
	// Для примера используем упрощенную инициализацию
	fmt.Println("Setting up lexicon system components...")

	// Создаем реестр лексиконов с mock компонентами
	mockBlockstore := &MockBlockStore{}
	mockIndexer := &MockIndexer{}
	lexiconRegistry := NewLexiconRegistry(mockBlockstore, mockIndexer, DefaultRegistryConfig())

	// Для примера создаем простой репозиторий без реального blockstore
	// В реальном использовании здесь был бы полноценный UES репозиторий
	fmt.Println("Lexicon registry created successfully")

	// === СОЗДАНИЕ ЛЕКСИКОНА ===

	// Определяем лексикон для блог-постов
	blogPostLexicon := &LexiconDefinition{
		ID:          "com.example.blog.post",
		Version:     SchemaVersion{Major: 1, Minor: 0, Patch: 0},
		Name:        "Blog Post",
		Description: "Schema for blog posts with title, content, and metadata",
		Namespace:   "com.example.blog",
		Status:      SchemaStatusActive,
		Author:      "UES Team",
		License:     "MIT",
		Keywords:    []string{"blog", "post", "content"},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),

		// IPLD Schema (упрощенная версия)
		Schema: &schema.TypeSystem{}, // В реальности здесь была бы компилированная схема

		// Определения индексов
		Indexes: []IndexDefinition{
			{
				Name:   "title_idx",
				Fields: []string{"title"},
				Type:   IndexTypeBTree,
				Unique: false,
			},
			{
				Name:   "created_at_idx",
				Fields: []string{"created_at"},
				Type:   IndexTypeBTree,
				Unique: false,
			},
			{
				Name:   "content_fts",
				Fields: []string{"content"},
				Type:   IndexTypeFTS,
				Unique: false,
			},
		},

		// Кастомные валидаторы
		Validators: []ValidatorDefinition{
			{
				Name:        "title_length",
				Description: "Title must be between 1 and 200 characters",
				Fields:      []string{"title"},
				Type:        ValidatorTypeLength,
				Config: map[string]interface{}{
					"min": 1,
					"max": 200,
				},
				ErrorMsg: "Title must be between 1 and 200 characters",
			},
		},
	}

	// === РЕГИСТРАЦИЯ ЛЕКСИКОНА ===

	fmt.Println("Registering blog post lexicon...")
	if err := lexiconRegistry.RegisterLexicon(ctx, blogPostLexicon); err != nil {
		return fmt.Errorf("failed to register lexicon: %w", err)
	}
	fmt.Printf("✓ Lexicon %s registered successfully\n", blogPostLexicon.ID)

	// === СОЗДАНИЕ ЗАПИСИ С ВАЛИДАЦИЕЙ ===

	// Создаем IPLD узел для блог-поста
	postData := basicnode.Prototype.Map.NewBuilder()
	postMap, err := postData.BeginMap(4)
	if err != nil {
		return fmt.Errorf("failed to create post map: %w", err)
	}

	// Заполняем данные поста
	if err := postMap.AssembleKey().AssignString("title"); err != nil {
		return err
	}
	if err := postMap.AssembleValue().AssignString("Getting Started with UES Lexicons"); err != nil {
		return err
	}

	if err := postMap.AssembleKey().AssignString("content"); err != nil {
		return err
	}
	if err := postMap.AssembleValue().AssignString("This is a comprehensive guide to using lexicons in UES..."); err != nil {
		return err
	}

	if err := postMap.AssembleKey().AssignString("author"); err != nil {
		return err
	}
	if err := postMap.AssembleValue().AssignString("Jane Doe"); err != nil {
		return err
	}

	if err := postMap.AssembleKey().AssignString("created_at"); err != nil {
		return err
	}
	if err := postMap.AssembleValue().AssignString(time.Now().Format(time.RFC3339)); err != nil {
		return err
	}

	if err := postMap.Finish(); err != nil {
		return err
	}

	postNode := postData.Build()

	// === СОХРАНЕНИЕ С АВТОМАТИЧЕСКОЙ ВАЛИДАЦИЕЙ ===

	fmt.Println("Demonstrating lexicon validation...")

	// Проверяем валидацию данных против лексикона
	lexiconID := LexiconID(blogPostLexicon.ID)
	err = lexiconRegistry.ValidateData(ctx, lexiconID, blogPostLexicon.Version, postNode)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	fmt.Printf("✓ Blog post data validated successfully against lexicon\n")

	// === ДЕМОНСТРАЦИЯ ВАЛИДАЦИИ ===

	// Попытка валидировать невалидные данные
	fmt.Println("Testing validation with invalid data...")

	// Создаем невалидные данные (пустой title)
	invalidPostData := basicnode.Prototype.Map.NewBuilder()
	invalidMap, err := invalidPostData.BeginMap(1)
	if err != nil {
		return err
	}

	if err := invalidMap.AssembleKey().AssignString("title"); err != nil {
		return err
	}
	if err := invalidMap.AssembleValue().AssignString(""); err != nil {
		return err
	}

	if err := invalidMap.Finish(); err != nil {
		return err
	}

	invalidNode := invalidPostData.Build()

	err = lexiconRegistry.ValidateData(ctx, lexiconID, blogPostLexicon.Version, invalidNode)
	if err != nil {
		fmt.Printf("✓ Validation correctly rejected invalid data: %s\n", err.Error())
	} else {
		fmt.Println("⚠ Validation should have rejected invalid data (not implemented yet)")
	}

	// === РАБОТА С ВЕРСИЯМИ ===

	fmt.Println("Creating updated version of lexicon...")

	// Создаем новую версию лексикона с дополнительным полем
	blogPostV2 := *blogPostLexicon
	blogPostV2.Version = SchemaVersion{Major: 1, Minor: 1, Patch: 0}
	blogPostV2.UpdatedAt = time.Now()

	// Добавляем миграцию
	blogPostV2.Migrations = []Migration{
		{
			FromVersion: SchemaVersion{Major: 1, Minor: 0, Patch: 0},
			ToVersion:   SchemaVersion{Major: 1, Minor: 1, Patch: 0},
			Type:        MigrationTypeAddField,
			CreatedAt:   time.Now(),
			Metadata: map[string]interface{}{
				"new_fields":  []string{"tags"},
				"description": "Added tags field for categorization",
			},
		},
	}

	// Регистрируем новую версию
	if err := lexiconRegistry.RegisterLexicon(ctx, &blogPostV2); err != nil {
		return fmt.Errorf("failed to register lexicon v2: %w", err)
	}
	fmt.Printf("✓ Lexicon v%s registered successfully\n", blogPostV2.Version.String())

	// === СПИСОК ЛЕКСИКОНОВ ===

	fmt.Println("Listing registered lexicons...")
	lexicons, err := lexiconRegistry.ListLexicons(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list lexicons: %w", err)
	}

	for _, lexicon := range lexicons {
		fmt.Printf("- %s v%s (%s)\n", lexicon.ID, lexicon.Version.String(), lexicon.Status)
	}

	fmt.Println("🎉 Lexicon system example completed successfully!")
	return nil
}

// === MOCK IMPLEMENTATIONS FOR TESTING AND DEMONSTRATION ===

// MockBlockStore простая заглушка blockstore для демонстрационных целей
//
// НАЗНАЧЕНИЕ:
// Provides minimal blockstore implementation для testing и examples
// без необходимости реального IPFS/blockstore setup.
//
// ОГРАНИЧЕНИЯ:
// - Не сохраняет данные persistently
// - Возвращает фиктивные CID'ы
// - Не проверяет integrity данных
// - Подходит только для testing/demo, НЕ для production
//
// РЕАЛЬНОЕ ИСПОЛЬЗОВАНИЕ:
// В production заменить на реальный blockstore из blockstore/blockstore.go
type MockBlockStore struct{}

func (m *MockBlockStore) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	// В реальности: сохранение в IPFS с генерацией real CID
	// Здесь: возвращаем undefined CID для демонстрации
	return cid.Undef, nil
}

func (m *MockBlockStore) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	// В реальности: получение данных по CID из IPFS
	// Здесь: возвращаем mock данные
	return []byte("mock data"), nil
}

func (m *MockBlockStore) Has(ctx context.Context, c cid.Cid) (bool, error) {
	// В реальности: проверка существования блока в IPFS
	// Здесь: всегда возвращаем true для демонстрации
	return true, nil
}

func (m *MockBlockStore) Delete(ctx context.Context, c cid.Cid) error {
	// В реальности: удаление блока из IPFS (редко используется)
	// Здесь: no-op operation
	return nil
}

// MockIndexer простая заглушка indexer для демонстрационных целей
//
// НАЗНАЧЕНИЕ:
// Minimal indexer implementation для testing без real database dependencies.
// Логирует операции для демонстрации process flow.
//
// ФУНКЦИОНАЛЬНОСТЬ:
// - Симулирует создание индексов с логированием
// - Предоставляет interface compatibility с real indexer
// - Безопасен для testing environment
//
// РЕАЛЬНОЕ ИСПОЛЬЗОВАНИЕ:
// В production заменить на реальный indexer из x/indexer/ компонентов
// с полной SQLite/database integration.
type MockIndexer struct{}

func (m *MockIndexer) CreateIndex(ctx context.Context, definition *IndexDefinition) error {
	// В реальности: CREATE INDEX SQL statements в SQLite
	// Здесь: логирование для демонстрации process
	fmt.Printf("Mock: Creating index %s (%s) on fields %v\n",
		definition.Name, definition.Type, definition.Fields)
	return nil
}

func (m *MockIndexer) DropIndex(ctx context.Context, name string) error {
	// В реальности: DROP INDEX SQL statements
	// Здесь: логирование operation
	fmt.Printf("Mock: Dropping index %s\n", name)
	return nil
}

func (m *MockIndexer) ListIndexes(ctx context.Context) ([]*IndexDefinition, error) {
	// В реальности: запрос существующих индексов из database metadata
	// Здесь: возвращаем пустой список для демонстрации
	return []*IndexDefinition{}, nil
}
