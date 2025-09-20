// Package repository содержит тесты для SQLite индексера
// Эти тесты проверяют интеграцию SQLite индексера с Repository Layer (уровень 3)
// и валидируют функциональность поиска, индексации и статистики
package repository

import (
	"context"
	"os"
	"testing"

	// IPLD библиотека для создания структурированных данных
	// basicnode предоставляет базовые типы узлов для построения IPLD графов
	"github.com/ipld/go-ipld-prime/node/basicnode"

	// Внутренние пакеты UES для тестирования
	"ues/blockstore" // Блочное хранилище для content-addressed данных
	"ues/datastore"  // Низкоуровневое хранилище данных на основе BadgerDB
)

// TestSQLiteIndexer - основной интеграционный тест SQLite индексера
//
// Этот тест проверяет полный жизненный цикл работы с SQLite индексером:
// 1. Создание репозитория с включенным SQLite индексером
// 2. Добавление записи и автоматическую индексацию
// 3. Поиск записей по коллекции
// 4. Полнотекстовый поиск по содержимому
// 5. Получение статистики коллекции
// 6. Удаление записи и проверку консистентности индекса
//
// Тест использует временные файлы для изоляции и не влияет на рабочие данные
func TestSQLiteIndexer(t *testing.T) {
	// === ПОДГОТОВКА ТЕСТОВОЙ СРЕДЫ ===

	// Создаем путь к временной SQLite базе данных для тестирования
	// Используем /tmp для обеспечения изоляции тестов
	dbPath := "/tmp/test_ues_sqlite.db"
	// defer гарантирует очистку временного файла после завершения теста
	defer os.Remove(dbPath)

	// Создаем временное datastore хранилище на основе BadgerDB
	// datastore - это низкоуровневый слой для персистентного хранения ключ-значение
	// Путь "/tmp/test_ues_data" обеспечивает изоляцию от рабочих данных
	ds, err := datastore.NewDatastorage("/tmp/test_ues_data", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	// Обеспечиваем корректное закрытие datastore и очистку временных файлов
	defer ds.Close()
	defer os.RemoveAll("/tmp/test_ues_data")

	// Создаем blockstore поверх datastore
	// blockstore предоставляет content-addressed storage с CID адресацией
	// Это уровень 1 (Blockstore Layer) в архитектуре UES
	bs := blockstore.NewBlockstore(ds)

	// === СОЗДАНИЕ РЕПОЗИТОРИЯ С SQLITE ИНДЕКСЕРОМ ===

	// Создаем репозиторий с интегрированным SQLite индексером
	// NewWithSQLiteIndex() инициализирует:
	// - Основной репозиторий с MST индексом (для структурной навигации)
	// - SQLite индексер с FTS5 (для быстрого поиска и аналитики)
	// - Автоматическую синхронизацию между MST и SQLite при CRUD операциях
	repo, err := NewWithSQLiteIndex(bs, dbPath)
	if err != nil {
		t.Fatalf("Failed to create repository with SQLite indexer: %v", err)
	}
	// Обеспечиваем корректное закрытие SQLite соединений
	defer repo.CloseSQLiteIndex()

	// Создаем контекст для всех операций с репозиторием
	// context.Background() подходит для тестов, в продакшене используется request context
	ctx := context.Background()

	// === ПРОВЕРКА ИНИЦИАЛИЗАЦИИ SQLITE ИНДЕКСЕРА ===

	// Проверяем, что SQLite индексер успешно инициализирован и доступен
	// HasSQLiteIndex() проверяет наличие активного соединения с SQLite базой
	if !repo.HasSQLiteIndex() {
		t.Fatal("SQLite indexer should be enabled")
	}

	// === СОЗДАНИЕ ТЕСТОВОЙ ЗАПИСИ ===

	// Создаем IPLD узел для тестовой записи используя basicnode builder
	// IPLD (InterPlanetary Linked Data) - это формат структурированных данных
	// который используется в UES для представления записей
	recordBuilder := basicnode.Prototype.Map.NewBuilder()

	// Начинаем сборку карты (map) с 2 полями: title и content
	// Карта в IPLD представляет объект с ключ-значение парами
	ma, _ := recordBuilder.BeginMap(2)

	// Добавляем поле "title" со значением "Test Post"
	// Это поле будет проиндексировано как для структурного, так и для полнотекстового поиска
	ma.AssembleKey().AssignString("title")
	ma.AssembleValue().AssignString("Test Post")

	// Добавляем поле "content" с текстовым содержимым
	// Содержимое будет проиндексировано FTS5 движком SQLite для полнотекстового поиска
	ma.AssembleKey().AssignString("content")
	ma.AssembleValue().AssignString("This is a test post content")

	// Завершаем сборку карты
	ma.Finish()
	// Строим финальный IPLD узел, готовый для сохранения в репозитории
	testRecord := recordBuilder.Build()

	// === СОХРАНЕНИЕ ЗАПИСИ С АВТОМАТИЧЕСКОЙ ИНДЕКСАЦИЕЙ ===

	// Сохраняем запись в репозиторий с автоматической индексацией
	// PutRecord выполняет следующие операции:
	// 1. Сериализует IPLD узел в CBOR формат
	// 2. Сохраняет данные в blockstore с генерацией CID
	// 3. Обновляет MST индекс для структурной навигации
	// 4. Автоматически индексирует запись в SQLite для быстрого поиска
	// 5. Извлекает текстовые поля для FTS5 полнотекстового поиска
	recordCID, err := repo.PutRecord(ctx, "posts", "post1", testRecord)
	if err != nil {
		t.Fatalf("Failed to put record: %v", err)
	}

	// Логируем CID записи для отладки
	// CID (Content Identifier) - это криптографический хэш содержимого
	// который служит уникальным адресом записи в content-addressed storage
	t.Logf("Record CID: %s", recordCID)

	// === ТЕСТИРОВАНИЕ ПОИСКА ПО КОЛЛЕКЦИИ ===

	// Создаем поисковый запрос для коллекции "posts"
	// SearchQuery определяет параметры поиска:
	// - Collection: ограничивает поиск конкретной коллекцией
	// - Limit: максимальное количество результатов
	query := SearchQuery{
		Collection: "posts", // Поиск только в коллекции "posts"
		Limit:      10,      // Ограничиваем результаты 10 записями
	}

	// Выполняем поиск через SQLite индексер
	// SearchRecords использует SQL запросы для быстрого поиска
	// в отличие от последовательного сканирования MST индекса
	results, err := repo.SearchRecords(ctx, query)
	if err != nil {
		t.Fatalf("Failed to search records: %v", err)
	}

	// Проверяем, что найдена ровно одна запись
	// Поскольку мы добавили только одну запись в коллекцию "posts"
	if len(results) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(results))
	}

	// === ВАЛИДАЦИЯ РЕЗУЛЬТАТОВ ПОИСКА ===

	// Получаем первый (и единственный) результат поиска
	result := results[0]

	// Проверяем, что CID в результате поиска соответствует CID сохраненной записи
	// Это гарантирует, что SQLite индекс корректно связан с content-addressed storage
	if result.CID != recordCID {
		t.Errorf("Expected CID %s, got %s", recordCID, result.CID)
	}

	// Проверяем правильность коллекции в результате
	// Это подтверждает, что фильтрация по коллекции работает корректно
	if result.Collection != "posts" {
		t.Errorf("Expected collection 'posts', got '%s'", result.Collection)
	}

	// Проверяем правильность ключа записи (record key)
	// RKey - это уникальный идентификатор записи в рамках коллекции
	if result.RKey != "post1" {
		t.Errorf("Expected rkey 'post1', got '%s'", result.RKey)
	}

	// === ТЕСТИРОВАНИЕ ПОЛНОТЕКСТОВОГО ПОИСКА (FTS5) ===

	// Создаем запрос для полнотекстового поиска
	// FullTextQuery использует SQLite FTS5 движок для поиска по содержимому
	// FTS5 поддерживает:
	// - Поиск по частичным словам
	// - Ранжирование результатов по релевантности
	// - Быстрый поиск в больших объемах текста
	ftsQuery := SearchQuery{
		FullTextQuery: "test", // Ищем слово "test" во всех текстовых полях
		Limit:         10,     // Ограничиваем количество результатов
	}

	// Выполняем полнотекстовый поиск
	// SQLite FTS5 проиндексирует содержимое полей title и content
	// и найдет записи, содержащие искомое слово "test"
	ftsResults, err := repo.SearchRecords(ctx, ftsQuery)
	if err != nil {
		t.Fatalf("Failed to perform full-text search: %v", err)
	}

	// Проверяем, что полнотекстовый поиск нашел нашу запись
	// Слово "test" присутствует в поле title ("Test Post")
	if len(ftsResults) != 1 {
		t.Fatalf("Expected 1 result from full-text search, got %d", len(ftsResults))
	}

	// === ТЕСТИРОВАНИЕ СТАТИСТИКИ КОЛЛЕКЦИИ ===

	// Получаем статистику для коллекции "posts"
	// GetCollectionStats предоставляет аналитическую информацию:
	// - record_count: количество записей в коллекции
	// - collection_count: общее количество коллекций (при пустом collection параметре)
	// - other_metrics: дополнительные метрики, рассчитываемые SQLite
	stats, err := repo.GetCollectionStats(ctx, "posts")
	if err != nil {
		t.Fatalf("Failed to get collection stats: %v", err)
	}

	// Извлекаем количество записей из статистики
	// stats возвращает map[string]interface{}, поэтому нужна type assertion
	recordCount, ok := stats["record_count"].(int)
	if !ok || recordCount != 1 {
		t.Errorf("Expected record_count 1, got %v", stats["record_count"])
	}

	// === ТЕСТИРОВАНИЕ УДАЛЕНИЯ ЗАПИСИ ===

	// Удаляем запись из репозитория
	// DeleteRecord выполняет следующие операции:
	// 1. Удаляет запись из MST индекса
	// 2. Автоматически удаляет соответствующую запись из SQLite индекса
	// 3. Блоки в blockstore остаются (garbage collection - отдельная задача)
	// 4. Возвращает true, если запись была найдена и удалена
	removed, err := repo.DeleteRecord(ctx, "posts", "post1")
	if err != nil {
		t.Fatalf("Failed to delete record: %v", err)
	}

	// Проверяем, что запись действительно была удалена
	// removed должно быть true, если запись существовала и была успешно удалена
	if !removed {
		t.Error("Record should have been removed")
	}

	// === ПРОВЕРКА КОНСИСТЕНТНОСТИ ПОСЛЕ УДАЛЕНИЯ ===

	// Выполняем тот же поисковый запрос после удаления записи
	// Это проверяет, что SQLite индекс корректно синхронизирован с MST индексом
	resultsAfterDelete, err := repo.SearchRecords(ctx, query)
	if err != nil {
		t.Fatalf("Failed to search after delete: %v", err)
	}

	// Проверяем, что поиск не возвращает удаленную запись
	// Это подтверждает, что:
	// 1. SQLite индекс корректно обновлен при удалении
	// 2. Нет "висячих" записей в индексе
	// 3. Консистентность между MST и SQLite индексами поддерживается
	if len(resultsAfterDelete) != 0 {
		t.Errorf("Expected 0 records after delete, got %d", len(resultsAfterDelete))
	}
}

// TestRepositoryWithoutSQLiteIndexer - тест негативного сценария
//
// Этот тест проверяет поведение репозитория без SQLite индексера
// и валидирует корректную обработку ошибок при попытке использования
// функций поиска и статистики, когда SQLite индексер не активирован.
//
// Цели теста:
// 1. Убедиться, что репозиторий корректно работает без SQLite индексера
// 2. Проверить, что методы поиска возвращают осмысленные ошибки
// 3. Валидировать изоляцию функциональности SQLite индексера
func TestRepositoryWithoutSQLiteIndexer(t *testing.T) {
	// === ПОДГОТОВКА РЕПОЗИТОРИЯ БЕЗ SQLITE ИНДЕКСЕРА ===

	// Создаем отдельное временное datastore для изоляции теста
	// Используем другой путь, чтобы избежать конфликтов с основным тестом
	ds, err := datastore.NewDatastorage("/tmp/test_ues_data_no_sqlite", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	// Обеспечиваем очистку ресурсов после теста
	defer ds.Close()
	defer os.RemoveAll("/tmp/test_ues_data_no_sqlite")

	// Создаем blockstore поверх datastore
	bs := blockstore.NewBlockstore(ds)

	// Создаем обычный репозиторий БЕЗ SQLite индексера
	// New() создает репозиторий только с MST индексом
	// SQLite индексер не инициализируется и не доступен
	repo := New(bs, "", nil)

	// === ПРОВЕРКА ОТСУТСТВИЯ SQLITE ИНДЕКСЕРА ===

	// Проверяем, что SQLite индексер действительно отключен
	// HasSQLiteIndex() должен возвращать false для репозитория,
	// созданного через New() без SQLite компонента
	if repo.HasSQLiteIndex() {
		t.Fatal("SQLite indexer should be disabled")
	}

	// Создаем контекст для операций
	ctx := context.Background()

	// === ТЕСТИРОВАНИЕ ОБРАБОТКИ ОШИБОК ПРИ ПОИСКЕ ===

	// Создаем валидный поисковый запрос
	// Запрос сам по себе корректен, но должен завершиться ошибкой
	// из-за отсутствия SQLite индексера
	query := SearchQuery{
		Collection: "posts", // Коллекция для поиска
		Limit:      10,      // Лимит результатов
	}

	// Попытка поиска должна возвращать ошибку
	// SearchRecords требует активного SQLite индексера для работы
	// Без него метод должен возвращать информативную ошибку,
	// а не панику или неопределенное поведение
	_, err = repo.SearchRecords(ctx, query)
	if err == nil {
		t.Fatal("SearchRecords should return error when SQLite indexer is disabled")
	}

	// === ТЕСТИРОВАНИЕ ОБРАБОТКИ ОШИБОК ДЛЯ СТАТИСТИКИ ===

	// Попытка получения статистики должна возвращать ошибку
	// GetCollectionStats также требует SQLite индексер для работы
	// Статистика рассчитывается на основе данных в SQLite базе,
	// поэтому без индексера метод должен возвращать ошибку
	_, err = repo.GetCollectionStats(ctx, "posts")
	if err == nil {
		t.Fatal("GetCollectionStats should return error when SQLite indexer is disabled")
	}

	// === ЗАКЛЮЧЕНИЕ НЕГАТИВНОГО ТЕСТА ===
	//
	// Этот тест подтверждает, что:
	// 1. Репозиторий может работать без SQLite индексера (базовая функциональность)
	// 2. Методы, требующие SQLite, корректно обрабатывают его отсутствие
	// 3. Ошибки возвращаются предсказуемо, а не вызывают panic
	// 4. Система gracefully деградирует при отсутствии дополнительных компонентов
}
