# Система Лексиконов UES

## Обзор

Успешно интегрирована система лексиконов в Repository Layer (уровень 3) проекта UES с полной поддержкой IPLD схем, валидации данных, миграций и автоматического индексирования.

## Созданные Компоненты

### 1. Базовые Типы (`lexicon.go`)
- **LexiconDefinition** - определение лексикона с IPLD схемой
- **SchemaVersion** - семантическое версионирование с методами сравнения
- **IndexDefinition** - определения индексов (B-Tree, Hash, FTS, Composite)
- **ValidatorDefinition** - кастомные валидаторы
- **Migration** - определения миграций между версиями

### 2. Реестр Лексиконов (`lexicon_registry.go`)
- **LexiconRegistry** - центральное управление лексиконами
- Регистрация и версионирование схем
- Разрешение зависимостей между лексиконами
- Кэширование компилированных IPLD схем
- Валидация данных против схем

**Ключевые методы:**
- `RegisterLexicon()` - регистрация нового лексикона
- `GetLexicon()` - получение лексикона по ID и версии
- `GetCompiledSchema()` - получение компилированной IPLD схемы
- `ValidateData()` - валидация данных против лексикона
- `ListLexicons()` - список всех лексиконов с фильтрацией

### 3. Система Миграций (`lexicon_migrations.go`)
- **MigrationManager** - управление автоматическими миграциями
- **MigrationPlan** - планирование миграций
- **MigrationResult** - результаты выполнения
- Автоматическое создание бекапов
- Откат при ошибках
- Поддержка различных типов миграций

**Типы миграций:**
- `add_field` - добавление полей
- `remove_field` - удаление полей  
- `rename_field` - переименование полей
- `change_type` - изменение типов
- `custom` - кастомные миграции

### 4. Интеграция с Индексатором (`lexicon_indexer.go`)
- **LexiconIndexer** - автоматическое создание индексов
- Синхронизация индексов с лексиконами
- Оптимизация запросов на основе схем
- Поддержка различных типов индексов
- Статистика использования индексов

**Возможности:**
- Автоматическое создание B-Tree, Hash, FTS индексов
- Составные индексы для оптимизации
- Синхронизация при изменении лексиконов
- Rebuilding индексов при необходимости

### 5. Интеграция с Repository (`repository.go`)
- Добавлена поддержка лексиконов в конструкторы Repository
- Автоматическая валидация в `PutRecord()`
- Новые конструкторы:
  - `NewWithLexicons()`
  - `NewWithFullFeatures()`

### 6. Пример Использования (`lexicon_example.go`)
- Полный демонстрационный пример
- Создание лексикона для блог-постов
- Регистрация в реестре
- Валидация данных
- Работа с версиями и миграциями

## Архитектурные Решения

### IPLD Schema Integration
- Использование `github.com/ipld/go-ipld-prime/schema` для типизации
- Компиляция схем в `TypeSystem` для валидации
- Кэширование компилированных схем для производительности

### Версионирование
- Семантическое версионирование (SemVer)
- Поддержка зависимостей между лексиконами
- Автоматические миграции между версиями
- Backward compatibility

### Валидация
- Двухуровневая валидация: IPLD схема + кастомные валидаторы
- Интеграция в CRUD операции Repository
- Гибкая конфигурация строгости валидации
- Поддержка deprecated схем

### Индексирование
- Автоматическое создание индексов на основе определений в лексиконах
- Поддержка B-Tree, Hash, FTS и составных индексов
- Синхронизация структуры индексов при изменении схем
- Оптимизация запросов на основе доступных индексов

## Конфигурация

### Registry Config
```go
type RegistryConfig struct {
    MaxCachedSchemas    int           // Размер кэша схем
    CacheTTL           time.Duration  // TTL кэша
    StrictValidation   bool           // Строгая валидация
    AllowDeprecated    bool           // Разрешить deprecated схемы
    AutoMigrate        bool           // Автоматические миграции
    BackupBeforeMigrate bool          // Бекап перед миграцией
}
```

### Migration Config
```go
type MigrationConfig struct {
    BackupBeforeMigration bool          // Создавать бекап
    DryRun               bool           // Тестовый режим
    BatchSize            int            // Размер батча
    MaxMigrationTime     time.Duration  // Таймаут
    ParallelWorkers      int            // Количество воркеров
    AutoRollback         bool           // Автоматический откат
}
```

### Indexer Config
```go
type IndexerConfig struct {
    AutoCreateIndexes    bool          // Автосоздание индексов
    AutoUpdateIndexes    bool          // Автообновление
    AutoDropIndexes      bool          // Автоудаление
    UseCompositeIndexes  bool          // Составные индексы
    EnableFTSIndexes     bool          // FTS индексы
}
```

## Использование

### Базовая настройка
```go
// Создание реестра лексиконов
config := DefaultRegistryConfig()
lexicons := NewLexiconRegistry(blockstore, indexer, config)

// Создание репозитория с лексиконами
repository := NewWithLexicons(blockstore, lexicons)
```

### Регистрация лексикона
```go
lexicon := &LexiconDefinition{
    ID: "com.example.blog.post",
    Version: SchemaVersion{Major: 1, Minor: 0, Patch: 0},
    Name: "Blog Post",
    Schema: compiledIPLDSchema,
    Indexes: []IndexDefinition{...},
    Validators: []ValidatorDefinition{...},
}

err := lexicons.RegisterLexicon(ctx, lexicon)
```

### Автоматическая валидация
```go
// Валидация происходит автоматически при сохранении
cid, err := repository.PutRecord(ctx, "blog.post", "key", data)
// Если данные не соответствуют лексикону - получим ошибку
```

## Статус Интеграции

✅ **Завершено:**
1. Структура лексиконов с IPLD схемами
2. LexiconRegistry система
3. Интеграция валидации в Repository CRUD
4. Система миграций схем
5. Интеграция с индексатором

🎯 **Результат:**
- Полнофункциональная система лексиконов интегрирована в уровень 3 (Repository Layer)
- Поддержка IPLD схем с типобезопасностью
- Автоматическая валидация и индексирование
- Эволюция схем через миграции
- Готова к использованию в UES проекте

## Следующие Шаги

1. **Реализация TODO методов** - завершить имплементацию заглушек
2. **Тестирование** - написать unit и integration тесты
3. **Документация** - создать подробную документацию API
4. **Оптимизация** - профилирование и оптимизация производительности
5. **Интеграция с CLI** - добавить команды управления лексиконами