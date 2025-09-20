package repository

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"ues/blockstore"
	"ues/datastore"
	"ues/headstorage"
	"ues/indexer"
	"ues/lexicon"
	"ues/mst"
	"ues/sqliteindexer"

	"github.com/ipfs/go-cid"
	badger4 "github.com/ipfs/go-ds-badger4"
	"github.com/ipld/go-ipld-prime/datamodel"
)

// Repository управляет контент-адресованной коллекцией записей, сгруппированных по имени коллекции.
// Репозиторий представляет собой версионированное хранилище данных, где каждая запись
// имеет уникальный идентификатор (rkey) в рамках своей коллекции и связана с CID содержимого.
// Использует MST (Merkle Search Tree) индекс для эффективного поиска и хранения записей,
// а также систему коммитов для отслеживания истории изменений.
//
// Основные компоненты:
//   - bs: блочное хранилище для сохранения IPLD узлов
//   - index: MST индекс для быстрого поиска записей по collection/rkey
//   - head: CID текущего коммита (HEAD репозитория)
//   - prev: CID предыдущего коммита (для цепочки истории)
//   - mu: мьютекс для обеспечения потокобезопасности
type Repository struct {
	bs          blockstore.Blockstore
	index       *indexer.Index
	sqliteIndex *sqliteindexer.SimpleSQLiteIndexer // SQLite индексер для быстрого поиска и запросов
	lexicon     *lexicon.Registry                  // Реестр лексиконов для валидации схем
	headStorage headstorage.HeadStorage            // Persistent storage для HEAD состояния
	headstorage.RepositoryState
	mu sync.RWMutex
}

// NewWithFullFeatures создает репозиторий с поддержкой SQLite индексирования и лексиконов
//
// Параметры:
//   - bs: блочное хранилище для сохранения IPLD данных
//   - sqliteDBPath: путь к файлу SQLite базы данных для индексирования
//   - lexicons: реестр лексиконов для валидации схем данных
//
// Возвращает:
//   - *Repository: новый экземпляр репозитория с полным функционалом
//   - error: ошибка инициализации компонентов
func NewRepository(dataPath, sqliteDBPath, lexiconPath, repoID string) (*Repository, error) {

	ctx := context.Background()

	ds, err := datastore.NewDatastorage(dataPath, &badger4.DefaultOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create datastore: %w", err)
	}

	bs := blockstore.NewBlockstore(ds)

	hStorage := headstorage.NewHeadStorage(ds)
	state, err := hStorage.LoadHead(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to load head state: %w", err)
	}

	index := indexer.NewIndex(bs, state.Head)

	sqliteIndex, err := sqliteindexer.NewSimpleSQLiteIndexer(sqliteDBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create SQLite indexer: %w", err)
	}

	lex := lexicon.NewRegistry(lexiconPath)

	return &Repository{
		bs:              bs,
		index:           index,
		sqliteIndex:     sqliteIndex,
		lexicon:         lex,
		headStorage:     hStorage,
		RepositoryState: state,
	}, nil
}

// Commit сохраняет текущее состояние репозитория в headStorage.
func (r *Repository) Commit(ctx context.Context) error {
	if r.headStorage == nil {
		return nil // Если storage не настроен, просто пропускаем
	}

	r.mu.RLock()
	state := headstorage.RepositoryState{
		Head:      r.Head,
		Prev:      r.Prev,
		RootIndex: r.index.Root(),
		Version:   1,
		RepoID:    r.RepoID,
	}
	r.mu.RUnlock()

	return r.headStorage.SaveHead(ctx, r.RepoID, state)
}

// PutRecord сохраняет узел записи в блочном хранилище и индексирует его под указанным collection/rkey.
// Этот метод выполняет двухэтапную операцию: сначала сохраняет содержимое записи как IPLD узел,
// затем добавляет ссылку на этот узел в индекс репозитория для быстрого поиска.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - collection: имя коллекции, в которую добавляется запись (например, "posts", "users")
//   - rkey: уникальный ключ записи в рамках коллекции (record key)
//   - node: IPLD узел с данными записи для сохранения
//
// Возвращает:
//   - cid.Cid: CID сохраненного узла записи, который можно использовать для прямого доступа
//   - error: ошибка сохранения или индексирования
//
// Процесс выполнения:
// 1. Сериализация и сохранение узла в blockstore (получение CID)
// 2. Добавление mapping (collection, rkey) -> CID в индекс
// 3. Возврат CID для дальнейшего использования
//
// Важно: изменения индекса остаются в памяти до вызова Commit()
func (r *Repository) PutRecord(ctx context.Context, collection, rkey string, node datamodel.Node) (cid.Cid, error) {

	// === ВАЛИДАЦИЯ ЧЕРЕЗ ЛЕКСИКОНЫ ===
	// Если лексиконы включены, валидируем данные против схемы коллекции
	if r.lexicon != nil {
		if err := r.validateRecordWithLexicon(ctx, collection, node); err != nil {
			return cid.Undef, fmt.Errorf("lexicon validation failed for %s/%s: %w", collection, rkey, err)
		}
	}

	// === Сохранение узла записи в blockstore ===
	// Сериализуем IPLD узел и сохраняем его в блочном хранилище
	// blockstore автоматически вычисляет CID на основе содержимого узла
	valueCID, err := r.bs.PutNode(ctx, node)
	if err != nil {
		// Если не удается сохранить узел (проблемы с сериализацией, недоступность хранилища),
		// возвращаем неопределенный CID и обернутую ошибку с контекстом
		return cid.Undef, fmt.Errorf("store record node: %w", err)
	}

	// === Индексирование записи в MST ===
	// Добавляем mapping от (collection, rkey) к CID в индекс репозитория
	// Это позволяет быстро находить записи по их логическому адресу
	// index.Put может изменить структуру MST индекса для поддержания упорядоченности
	if _, err := r.index.Put(ctx, collection, rkey, valueCID); err != nil {
		// Если индексирование не удалось (например, проблемы с обновлением MST),
		// возвращаем ошибку. Узел уже сохранен в blockstore, но не проиндексирован
		return cid.Undef, err
	}

	// === Индексирование записи в SQLite (если включено) ===
	if r.sqliteIndex != nil {
		if err := r.indexRecordInSQLite(ctx, valueCID, collection, rkey, node); err != nil {
			// Логируем ошибку SQLite индексирования, но не прерываем операцию
			// MST индекс уже обновлен и это основной механизм
			fmt.Printf("Warning: SQLite indexing failed for %s/%s: %v\n", collection, rkey, err)
		}
	}

	if err := r.Commit(ctx); err != nil {
		return cid.Undef, fmt.Errorf("commit after put record: %w", err)
	}

	// Успешно сохранили и проиндексировали запись
	// Возвращаем CID для возможности прямого доступа к содержимому
	return valueCID, nil
}

// indexRecordInSQLite индексирует запись в SQLite для быстрого поиска
func (r *Repository) indexRecordInSQLite(ctx context.Context, recordCID cid.Cid, collection, rkey string, node datamodel.Node) error {

	// Извлекаем данные из IPLD узла
	data, err := extractDataFromNode(node)
	if err != nil {
		return fmt.Errorf("failed to extract data from node: %w", err)
	}

	// Генерируем текст для полнотекстового поиска
	searchText := generateSearchText(data)

	// Создаем метаданные для индексирования
	metadata := sqliteindexer.IndexMetadata{
		Collection: collection,
		RKey:       rkey,
		RecordType: inferRecordType(collection, data),
		Data:       data,
		SearchText: searchText,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	return r.sqliteIndex.IndexRecord(ctx, recordCID, metadata)
}

// extractDataFromNode извлекает данные из IPLD узла в map[string]interface{}
func extractDataFromNode(node datamodel.Node) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Обходим все поля узла
	iterator := node.MapIterator()

	for !iterator.Done() {
		key, value, err := iterator.Next()
		if err != nil {
			return nil, err
		}

		keyStr, err := key.AsString()
		if err != nil {
			continue // Пропускаем нестроковые ключи
		}

		// Конвертируем значение в go типы
		goValue, err := nodeToGoValue(value)
		if err != nil {
			continue // Пропускаем проблемные значения
		}

		result[keyStr] = goValue
	}

	return result, nil
}

// nodeToGoValue конвертирует IPLD Node в Go значение
func nodeToGoValue(node datamodel.Node) (interface{}, error) {

	switch node.Kind() {
	case datamodel.Kind_String:
		return node.AsString()

	case datamodel.Kind_Bool:
		return node.AsBool()

	case datamodel.Kind_Int:
		return node.AsInt()

	case datamodel.Kind_Float:
		return node.AsFloat()

	case datamodel.Kind_List:
		var result []interface{}
		iterator := node.ListIterator()
		for !iterator.Done() {
			_, value, err := iterator.Next()
			if err != nil {
				return nil, err
			}
			goValue, err := nodeToGoValue(value)
			if err != nil {
				continue
			}
			result = append(result, goValue)
		}
		return result, nil

	case datamodel.Kind_Map:
		result := make(map[string]interface{})
		iterator := node.MapIterator()
		for !iterator.Done() {
			key, value, err := iterator.Next()
			if err != nil {
				return nil, err
			}
			keyStr, err := key.AsString()
			if err != nil {
				continue
			}
			goValue, err := nodeToGoValue(value)
			if err != nil {
				continue
			}
			result[keyStr] = goValue
		}
		return result, nil

	default:
		return fmt.Sprintf("%v", node), nil
	}
}

// inferRecordType определяет тип записи на основе коллекции и данных
func inferRecordType(collection string, data map[string]interface{}) string {

	// Пытаемся определить тип из поля $type
	if recordType, exists := data["$type"]; exists {
		if typeStr, ok := recordType.(string); ok {
			return typeStr
		}
	}

	// Определяем тип на основе имени коллекции
	switch collection {
	case "posts", "app.bsky.feed.post":
		return "post"

	case "follows", "app.bsky.graph.follow":
		return "follow"

	case "likes", "app.bsky.feed.like":
		return "like"

	case "profiles", "app.bsky.actor.profile":
		return "profile"

	default:
		return "record"
	}
}

// generateSearchText создает текст для полнотекстового поиска из данных записи
func generateSearchText(data map[string]interface{}) string {

	var parts []string

	// Обходим все поля и собираем текстовые значения
	for key, value := range data {

		parts = append(parts, key) // Добавляем имя поля

		switch v := value.(type) {
		case string:
			parts = append(parts, v)

		case []interface{}:
			for _, item := range v {
				if str, ok := item.(string); ok {
					parts = append(parts, str)
				}
			}

		case map[string]interface{}:
			// Рекурсивно обрабатываем вложенные объекты
			for _, nested := range v {
				if str, ok := nested.(string); ok {
					parts = append(parts, str)
				}
			}
		}
	}

	return strings.Join(parts, " ")
}

// validateRecordWithLexicon валидирует IPLD узел против лексикона коллекции
//
// ЛОГИКА ВАЛИДАЦИИ:
// 1. Определить лексикон для коллекции (по соглашению об именах)
// 2. Получить схему лексикона из реестра
// 3. Валидировать узел против IPLD схемы
// 4. Применить кастомные валидаторы из лексикона
//
// Параметры:
//   - ctx: контекст операции
//   - collection: имя коллекции (используется для определения лексикона)
//   - node: IPLD узел для валидации
//
// Возвращает:
//   - error: ошибка валидации или nil при успехе
func (r *Repository) validateRecordWithLexicon(ctx context.Context, collection string, node datamodel.Node) error {

	lexiconID := inferLexiconID(collection)

	// Получаем актуальную версию лексикона
	definition, err := r.lexicon.GetSchema(lexiconID) // nil = последняя стабильная версия
	if err != nil {
		// Если лексикон не найден, это может быть:
		// 1. Новая коллекция без определенной схемы (разрешаем)
		// 2. Ошибка в реестре лексиконов (блокируем)
		if strings.Contains(err.Error(), "not found") {
			// Лексикон не найден - разрешаем операцию (backward compatibility)
			return nil
		}
		return fmt.Errorf("failed to get lexicon %s: %w", lexiconID, err)
	}

	// Проверяем статус лексикона
	if definition.Status == lexicon.SchemaStatusArchived {
		return fmt.Errorf("lexicon %s is archived and cannot be used", lexiconID)
	}

	if definition.Status == lexicon.SchemaStatusDeprecated {
		return fmt.Errorf("lexicon %s is deprecated", lexiconID)
	}

	// Валидируем данные против лексикона
	if err := r.lexicon.ValidateData(lexiconID, node); err != nil {
		return fmt.Errorf("data validation failed: %w", err)
	}

	return nil
}

// inferRecordType определяет тип записи на основе коллекции и данных
func inferLexiconID(collection string) string {
	// return fmt.Sprintf("com.example.%s.record", collection)
	return collection
}

// DeleteRecord удаляет mapping записи из индекса репозитория.
// Этот метод удаляет связь между логическим адресом (collection, rkey) и CID содержимого
// из индекса репозитория. Важно отметить, что сами данные в blockstore не удаляются -
// удаляется только ссылка на них из индекса.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - collection: имя коллекции, из которой удаляется запись
//   - rkey: ключ записи для удаления из указанной коллекции
//
// Возвращает:
//   - bool: true, если запись была найдена и удалена; false, если запись не существовала
//   - error: ошибка удаления, если операция не удалась
//
// Поведение:
// - Если запись существует: удаляет её из индекса и возвращает true
// - Если запись не существует: не выполняет действий и возвращает false
// - Изменения индекса остаются в памяти до вызова Commit()
//
// Важно: данные в blockstore остаются доступными по CID даже после удаления из индекса
func (r *Repository) DeleteRecord(ctx context.Context, collection, rkey string) (bool, error) {
	// Получаем CID записи перед удалением для SQLite индексирования
	var recordCID cid.Cid
	if r.sqliteIndex != nil {
		if cid, found, err := r.index.Get(ctx, collection, rkey); err == nil && found {
			recordCID = cid
		}
	}

	// Вызываем метод Delete индекса для удаления mapping (collection, rkey) -> CID
	// index.Delete возвращает три значения:
	// 1. старый CID (который мы игнорируем через _)
	// 2. флаг removed - был ли элемент действительно удален
	// 3. ошибка операции
	_, removed, err := r.index.Delete(ctx, collection, rkey)
	if err != nil {
		// Если произошла ошибка при удалении (например, проблемы с обновлением MST),
		// возвращаем false и ошибку операции
		return false, err
	}

	// Удаляем из SQLite индекса (если включен и запись была найдена)
	if r.sqliteIndex != nil && removed && recordCID != cid.Undef {
		if err := r.sqliteIndex.DeleteRecord(ctx, recordCID); err != nil {
			// Логируем ошибку SQLite удаления, но не прерываем операцию
			fmt.Printf("Warning: SQLite deletion failed for %s/%s: %v\n", collection, rkey, err)
		}
	}

	// Возвращаем флаг removed, который указывает:
	// - true: запись существовала и была успешно удалена
	// - false: запись не существовала в индексе (операция без изменений)
	return removed, nil
}

// GetRecordCID разрешает CID содержимого для записи collection/rkey из индекса.
// Этот метод выполняет поиск в индексе репозитория для получения CID, связанного
// с указанным логическим адресом записи. CID можно затем использовать для
// прямого получения содержимого записи из blockstore.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - collection: имя коллекции для поиска записи
//   - rkey: ключ записи в указанной коллекции
//
// Возвращает:
//   - cid.Cid: CID содержимого записи (если найдена) или cid.Undef (если не найдена)
//   - bool: true, если запись найдена в индексе; false, если запись отсутствует
//   - error: ошибка поиска, если операция не удалась
//
// Использование результатов:
//
//	cid, found, err := repo.GetRecordCID(ctx, "posts", "post123")
//	if found {
//	    node, err := blockstore.GetNode(ctx, cid)
//	    // работа с содержимым записи
//	}
//
// Потокобезопасность: метод только читает из индекса, поэтому безопасен для параллельного использования
func (r *Repository) GetRecordCID(ctx context.Context, collection, rkey string) (cid.Cid, bool, error) {
	// Делегируем поиск индексу репозитория
	// index.Get выполняет поиск в MST структуре по ключу (collection, rkey)
	// и возвращает связанный с ним CID, если запись существует
	return r.index.Get(ctx, collection, rkey)
}

// ListCollection возвращает упорядоченные записи индекса для указанной коллекции.
// Этот метод извлекает все записи из указанной коллекции и возвращает их CID в том порядке,
// в котором они хранятся в MST индексе (лексикографический порядок по rkey).
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - collection: имя коллекции для получения списка записей
//
// Возвращает:
//   - []cid.Cid: срез CID всех записей в коллекции, упорядоченный по rkey
//   - error: ошибка получения списка, если операция не удалась
//
// Особенности:
// - Записи возвращаются в лексикографическом порядке их rkey
// - Пустая коллекция возвращает пустой срез (не nil)
// - MST гарантирует эффективное получение упорядоченного списка
//
// Использование:
//
//	cids, err := repo.ListCollection(ctx, "posts")
//	for _, cid := range cids {
//	    node, err := blockstore.GetNode(ctx, cid)
//	    // обработка каждой записи
//	}
//
// Производительность: O(n) где n - количество записей в коллекции
func (r *Repository) ListCollection(ctx context.Context, collection string) ([]cid.Cid, error) {
	// Получаем полный список записей коллекции из индекса
	// index.ListCollection возвращает срез Entry структур, содержащих rkey и CID
	entries, err := r.index.ListCollection(ctx, collection)
	if err != nil {
		// Если произошла ошибка при обходе MST или чтении данных,
		// возвращаем nil срез и ошибку
		return nil, err
	}

	// Создаем выходной срез с предварительно выделенной памятью
	// Это избегает множественных реаллокаций при добавлении элементов
	out := make([]cid.Cid, len(entries))

	// Извлекаем только CID из каждой записи индекса
	// Entry содержит как rkey, так и CID, но нам нужны только CID
	for i, entry := range entries {
		out[i] = entry.Value
	}

	// Возвращаем срез CID в том же порядке, что и записи в индексе
	return out, nil
}

// SearchRecords выполняет поиск записей через SQLite индексер (если включен)
// Обеспечивает быстрый поиск с поддержкой фильтров, полнотекстового поиска и сортировки.
//
// Параметры:
//   - ctx: контекст для отмены операции
//   - query: запрос поиска с фильтрами и параметрами
//
// Возвращает:
//   - []SearchResult: результаты поиска с метаданными
//   - error: ошибка выполнения поиска или отсутствие SQLite индексера
func (r *Repository) SearchRecords(ctx context.Context, query sqliteindexer.SearchQuery) ([]sqliteindexer.SearchResult, error) {
	if r.sqliteIndex == nil {
		return nil, fmt.Errorf("SQLite indexer is not enabled for this repository")
	}

	return r.sqliteIndex.SearchRecords(ctx, query)
}

// GetCollectionStats возвращает статистику по коллекции через SQLite индексер
//
// Параметры:
//   - ctx: контекст для отмены операции
//   - collection: имя коллекции для получения статистики
//
// Возвращает:
//   - map[string]interface{}: статистика коллекции (количество записей, типы, даты)
//   - error: ошибка получения статистики или отсутствие SQLite индексера
func (r *Repository) GetCollectionStats(ctx context.Context, collection string) (map[string]interface{}, error) {
	if r.sqliteIndex == nil {
		return nil, fmt.Errorf("SQLite indexer is not enabled for this repository")
	}

	return r.sqliteIndex.GetCollectionStats(ctx, collection)
}

// HasSQLiteIndex проверяет, включен ли SQLite индексер для этого репозитория
func (r *Repository) HasSQLiteIndex() bool {
	return r.sqliteIndex != nil
}

// CloseSQLiteIndex безопасно закрывает SQLite индексер
func (r *Repository) CloseSQLiteIndex() error {
	if r.sqliteIndex == nil {
		return nil
	}

	err := r.sqliteIndex.Close()
	r.sqliteIndex = nil
	return err
}

// CreateCollection создает новую пустую запись коллекции в репозитории.
// Этот метод является обертокой вокруг index.CreateCollection, предоставляя
// удобный API уровня репозитория для создания новых коллекций. После создания
// коллекция готова для добавления записей через PutRecord.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - name: имя новой коллекции (должно быть уникальным в рамках репозитория)
//
// Возвращает:
//   - cid.Cid: CID материализованного узла индекса после создания коллекции
//   - error: ошибка создания, если коллекция уже существует или операция не удалась
//
// Поведение:
// - Создает пустую коллекцию с неопределенным MST корнем (cid.Undef)
// - Материализует обновленный индекс репозитория
// - Возвращает ошибку, если коллекция с таким именем уже существует
//
// Использование:
//
//	rootCID, err := repo.CreateCollection(ctx, "posts")
//	if err != nil {
//	    // обработка ошибки (например, коллекция уже существует)
//	}
//	// коллекция "posts" готова для добавления записей
//
// Связанные методы: PutRecord для добавления записей в созданную коллекцию
func (r *Repository) CreateCollection(ctx context.Context, name string) (cid.Cid, error) {
	return r.index.CreateCollection(ctx, name)
}

// DeleteCollection удаляет коллекцию из репозитория.
// Этот метод является обертокой вокруг index.DeleteCollection, предоставляя
// API уровня репозитория для удаления коллекций. Важно отметить, что удаление
// коллекции удаляет только её запись из индекса - сами блоки данных MST и записей
// остаются в blockstore и могут быть недоступны для сборки мусора.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - name: имя коллекции для удаления из репозитория
//
// Возвращает:
//   - cid.Cid: CID материализованного узла индекса после удаления коллекции
//   - error: ошибка удаления, если коллекция не найдена или операция не удалась
//
// Поведение:
// - Удаляет коллекцию из карты индекса репозитория
// - Материализует обновленный индекс без удаленной коллекции
// - Возвращает ошибку, если коллекция не существует
// - Данные MST остаются в blockstore (только ссылка удаляется)
//
// Использование:
//
//	rootCID, err := repo.DeleteCollection(ctx, "posts")
//	if err != nil {
//	    // обработка ошибки (например, коллекция не найдена)
//	}
//	// коллекция "posts" больше недоступна в репозитории
//
// Важно: для полного удаления данных может потребоваться сборка мусора blockstore
func (r *Repository) DeleteCollection(ctx context.Context, name string) (cid.Cid, error) {
	return r.index.DeleteCollection(ctx, name)
}

// HasCollection проверяет существование коллекции в репозитории.
// Этот метод является обертокой вокруг index.HasCollection, предоставляя
// удобный API уровня репозитория для быстрой проверки наличия коллекции
// с указанным именем без загрузки её содержимого.
//
// Параметры:
//   - name: имя коллекции для проверки существования
//
// Возвращает:
//   - bool: true, если коллекция существует в репозитории; false в противном случае
//
// Особенности:
// - Выполняет быструю проверку в карте индекса (O(1))
// - Не загружает содержимое коллекции
// - Возвращает true как для пустых, так и для непустых коллекций
// - Потокобезопасная операция (только чтение)
//
// Использование:
//
//	if repo.HasCollection("posts") {
//	    // коллекция "posts" существует и готова к использованию
//	    records, err := repo.ListRecords(ctx, "posts")
//	} else {
//	    // коллекция не существует, возможно нужно создать
//	    _, err := repo.CreateCollection(ctx, "posts")
//	}
//
// Производительность: очень быстрая операция, подходит для частых проверок
func (r *Repository) HasCollection(name string) bool {
	return r.index.HasCollection(name)
}

// ListCollections возвращает отсортированные имена коллекций.
// Этот метод является обертокой вокруг index.Collections, предоставляя
// API уровня репозитория для получения полного списка всех коллекций
// в репозитории, отсортированного в лексикографическом порядке.
//
// Возвращает:
//   - []string: срез имен всех коллекций в репозитории, отсортированный по алфавиту
//
// Особенности:
// - Возвращает копию данных, безопасную для модификации клиентским кодом
// - Включает как пустые, так и непустые коллекции
// - Порядок детерминирован и воспроизводим
// - Потокобезопасная операция (только чтение)
//
// Использование:
//
//	collections := repo.ListCollections()
//	fmt.Printf("Репозиторий содержит %d коллекций:\n", len(collections))
//	for i, name := range collections {
//	    fmt.Printf("%d. %s\n", i+1, name)
//	    if repo.HasCollection(name) {
//	        records, _ := repo.ListRecords(ctx, name)
//	        fmt.Printf("   Записей: %d\n", len(records))
//	    }
//	}
//
// Производительность: O(n log n) где n - количество коллекций
// Типичное использование: администрирование, отладка, пользовательские интерфейсы
func (r *Repository) ListCollections() []string {
	return r.index.Collections()
}

// CollectionRoot возвращает CID корня MST для коллекции.
// Этот метод является обертокой вокруг index.CollectionRoot, предоставляя
// API уровня репозитория для получения прямого доступа к корневому CID
// MST структуры указанной коллекции. Используется для низкоуровневых операций
// и интеграции с другими компонентами.
//
// Параметры:
//   - name: имя коллекции для получения корня MST
//
// Возвращает:
//   - cid.Cid: CID корня MST коллекции (cid.Undef для пустой коллекции)
//   - bool: true, если коллекция найдена; false, если коллекция не существует
//
// Интерпретация результатов:
// - (cid.Defined(), true): коллекция существует и содержит записи
// - (cid.Undef, true): коллекция существует, но пуста
// - (cid.Undef, false): коллекция не существует
//
// Использование:
//
//	rootCID, found := repo.CollectionRoot("posts")
//	if !found {
//	    fmt.Println("Коллекция 'posts' не существует")
//	} else if !rootCID.Defined() {
//	    fmt.Println("Коллекция 'posts' пуста")
//	} else {
//	    fmt.Printf("Коллекция 'posts' имеет корень: %s\n", rootCID.String())
//	    // можно использовать rootCID для прямого доступа к MST
//	}
//
// Применение: низкоуровневые операции, отладка, мониторинг состояния
func (r *Repository) CollectionRoot(name string) (cid.Cid, bool) {
	return r.index.CollectionRoot(name)
}

// CollectionRootHash возвращает байты хеша, хранящиеся в корне MST.
// Этот метод является обертокой вокруг index.CollectionRootHash, предоставляя
// API уровня репозитория для получения криптографического хеша корневого узла
// MST коллекции. Хеш используется для быстрого сравнения состояний коллекций
// без необходимости загрузки полного содержимого.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - name: имя коллекции для получения хеша корня
//
// Возвращает:
//   - []byte: копия байтов хеша корневого узла MST
//   - bool: true, если коллекция найдена; false, если коллекция не существует
//   - error: ошибка получения хеша, если узел поврежден или недоступен
//
// Поведение для разных состояний коллекции:
// - Коллекция не существует: (nil, false, error)
// - Коллекция пуста: (nil, true, nil)
// - Коллекция содержит данные: (hash_bytes, true, nil)
//
// Использование:
//
//	hash1, found1, err1 := repo.CollectionRootHash(ctx, "posts")
//	hash2, found2, err2 := repo.CollectionRootHash(ctx, "users")
//
//	if found1 && found2 && err1 == nil && err2 == nil {
//	    if bytes.Equal(hash1, hash2) {
//	        fmt.Println("Коллекции имеют одинаковое содержимое")
//	    } else {
//	        fmt.Println("Коллекции различаются")
//	    }
//	}
//
// Применение: синхронизация, кэширование, проверка целостности, дедупликация
func (r *Repository) CollectionRootHash(ctx context.Context, name string) ([]byte, bool, error) {
	return r.index.CollectionRootHash(ctx, name)
}

// GetRecord загружает IPLD узел для записи collection/rkey.
// Этот метод выполняет полную операцию получения записи: сначала разрешает
// CID записи через индекс, затем загружает фактическое содержимое записи
// из blockstore. Возвращает готовый к использованию IPLD узел с данными записи.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - collection: имя коллекции, содержащей искомую запись
//   - rkey: ключ записи для поиска в указанной коллекции
//
// Возвращает:
//   - datamodel.Node: IPLD узел с содержимым записи (если найдена)
//   - bool: true, если запись найдена и загружена; false, если запись не существует
//   - error: ошибка операции (коллекция не найдена, запись повреждена, blockstore недоступен)
//
// Процесс выполнения:
// 1. Поиск CID записи в индексе коллекции (index.Get)
// 2. Если запись не найдена - возврат (nil, false, nil)
// 3. Загрузка содержимого записи из blockstore по CID
// 4. Возврат десериализованного IPLD узла
//
// Использование:
//
//	node, found, err := repo.GetRecord(ctx, "posts", "post123")
//	if err != nil {
//	    return fmt.Errorf("ошибка получения записи: %w", err)
//	}
//	if !found {
//	    return fmt.Errorf("запись не найдена")
//	}
//
//	// Работа с содержимым записи
//	titleNode, _ := node.LookupByString("title")
//	title, _ := titleNode.AsString()
//	fmt.Printf("Заголовок поста: %s\n", title)
//
// Производительность: O(log n) для поиска + O(1) для загрузки из blockstore
func (r *Repository) GetRecord(ctx context.Context, collection, rkey string) (datamodel.Node, bool, error) {
	// === Поиск CID записи в индексе ===
	// Используем индекс для разрешения логического адреса (collection, rkey) в CID
	c, ok, err := r.index.Get(ctx, collection, rkey)
	if err != nil || !ok {
		// Если произошла ошибка поиска или запись не найдена,
		// возвращаем результат без попытки загрузки
		return nil, ok, err
	}

	// === Загрузка содержимого записи ===
	// Получаем IPLD узел записи из blockstore по найденному CID
	n, err := r.bs.GetNode(ctx, c)
	if err != nil {
		// Если не удается загрузить узел (поврежденные данные, недоступность blockstore),
		// возвращаем ошибку. Запись существует в индексе, но недоступна
		return nil, false, err
	}

	// Успешно получили и десериализовали запись
	return n, true, nil
}

// ListRecords возвращает упорядоченные записи (rkey, CID значения) в коллекции.
// Этот метод является обертокой вокруг index.ListCollection, предоставляя
// API уровня репозитория для получения полного списка записей в указанной
// коллекции. Записи возвращаются в лексикографическом порядке их ключей.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - collection: имя коллекции для получения списка записей
//
// Возвращает:
//   - []mst.Entry: срез записей коллекции, упорядоченный по rkey
//   - error: ошибка получения списка, если коллекция не найдена или MST недоступен
//
// Структура mst.Entry:
//
//	type Entry struct {
//	    Key   string   // rkey записи (уникальный ключ в коллекции)
//	    Value cid.Cid  // CID содержимого записи в blockstore
//	}
//
// Особенности:
// - Пустая коллекция возвращает пустой срез (не nil)
// - Порядок записей детерминирован (лексикографический по rkey)
// - MST обеспечивает эффективный обход в порядке сортировки
// - Возвращаются только метаданные записей (ключи и CID), не содержимое
//
// Использование:
//
//	entries, err := repo.ListRecords(ctx, "posts")
//	if err != nil {
//	    return fmt.Errorf("ошибка получения списка записей: %w", err)
//	}
//
//	fmt.Printf("Коллекция 'posts' содержит %d записей:\n", len(entries))
//	for i, entry := range entries {
//	    fmt.Printf("%d. Ключ: %s, CID: %s\n", i+1, entry.Key, entry.Value.String())
//
//	    // При необходимости можно загрузить содержимое записи
//	    node, found, err := repo.GetRecord(ctx, "posts", entry.Key)
//	    if found && err == nil {
//	        // работа с содержимым записи
//	    }
//	}
//
// Производительность: O(n) где n - количество записей в коллекции
// Применение: листинги, экспорт данных, администрирование, отладка
func (r *Repository) ListRecords(ctx context.Context, collection string) ([]mst.Entry, error) {
	return r.index.ListCollection(ctx, collection)
}

// InclusionPath возвращает путь CID узлов от корня до позиции поиска для rkey.
// Этот метод является обертокой вокруг index.InclusionPath, предоставляя
// API уровня репозитория для построения пути включения (inclusion path)
// в MST структуре коллекции. Используется для создания криптографических
// доказательств включения или исключения записей.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - collection: имя коллекции для построения пути
//   - rkey: ключ записи для поиска пути
//
// Возвращает:
//   - []cid.Cid: срез CID узлов от корня MST до позиции поиска
//   - bool: true, если rkey существует в дереве; false, если ключ отсутствует
//   - error: ошибка построения пути, если коллекция не найдена или узлы недоступны
//
// Структура пути:
// - path[0]: корневой узел MST коллекции
// - path[1]: узел второго уровня (левый или правый потомок корня)
// - ...
// - path[n-1]: конечный узел (содержащий ключ или позицию для вставки)
//
// Применение inclusion path:
// 1. Криптографические доказательства включения/исключения
// 2. Верификация целостности данных
// 3. Синхронизация между репозиториями
// 4. Аудит и мониторинг изменений
//
// Использование:
//
//	path, present, err := repo.InclusionPath(ctx, "posts", "post123")
//	if err != nil {
//	    return fmt.Errorf("ошибка построения пути: %w", err)
//	}
//
//	if present {
//	    fmt.Printf("Запись 'post123' найдена, путь включения: %d узлов\n", len(path))
//	    for i, cid := range path {
//	        fmt.Printf("  Уровень %d: %s\n", i, cid.String())
//	    }
//	} else {
//	    fmt.Printf("Запись 'post123' отсутствует, путь исключения: %d узлов\n", len(path))
//	}
//
// Производительность: O(log n) где n - количество записей в коллекции
func (r *Repository) InclusionPath(ctx context.Context, collection, rkey string) ([]cid.Cid, bool, error) {
	return r.index.InclusionPath(ctx, collection, rkey)
}

// ExportCollectionCAR записывает CARv2 для MST коллекции, используя explore-all селектор.
// Этот метод экспортирует полное содержимое коллекции в формате CAR (Content Addressable aRchive),
// включая все узлы MST и связанные данные. CAR файл может использоваться для резервного
// копирования, передачи данных между системами или автономного хранения коллекции.
//
// Параметры:
//   - ctx: контекст для отмены операции и передачи значений
//   - collection: имя коллекции для экспорта
//   - w: io.Writer для записи CAR данных (файл, сетевое соединение, буфер)
//
// Возвращает:
//   - error: ошибка экспорта, если коллекция не найдена, пуста или запись не удалась
//
// Поведение:
// - Коллекция не существует: возвращает ошибку "collection not found"
// - Коллекция пуста: возвращает ошибку "collection is empty"
// - Коллекция содержит данные: экспортирует все связанные блоки в CAR формат
//
// Особенности CAR экспорта:
// - Используется CARv2 формат (совместим с IPFS/IPLD экосистемой)
// - explore-all селектор включает все достижимые узлы от корня MST
// - Экспортируются как узлы MST структуры, так и содержимое записей
// - Результирующий файл является самодостаточным архивом
//
// Использование:
//
//	// Экспорт в файл
//	file, err := os.Create("posts_backup.car")
//	if err != nil {
//	    return err
//	}
//	defer file.Close()
//
//	err = repo.ExportCollectionCAR(ctx, "posts", file)
//	if err != nil {
//	    return fmt.Errorf("ошибка экспорта коллекции: %w", err)
//	}
//	fmt.Println("Коллекция 'posts' успешно экспортирована")
//
//	// Экспорт в буфер для передачи по сети
//	var buffer bytes.Buffer
//	err = repo.ExportCollectionCAR(ctx, "users", &buffer)
//	if err == nil {
//	    // отправка buffer.Bytes() по сети
//	}
//
// Применение: резервное копирование, миграция данных, обмен между системами
// Производительность: O(n) где n - общий размер всех блоков в коллекции
func (r *Repository) ExportCollectionCAR(ctx context.Context, collection string, w io.Writer) error {
	// === Получение корня MST коллекции ===
	// Проверяем существование коллекции и получаем её корневой CID
	root, ok := r.index.CollectionRoot(collection)
	if !ok {
		// Если коллекция не найдена в индексе, возвращаем ошибку
		return fmt.Errorf("collection not found: %s", collection)
	}

	// === Проверка на пустую коллекцию ===
	// Если корень MST не определен, коллекция пуста и экспортировать нечего
	if !root.Defined() {
		return fmt.Errorf("collection is empty: %s", collection)
	}

	// === Подготовка селектора для экспорта ===
	// Создаем explore-all селектор, который включает все достижимые узлы
	// от корневого CID. Это гарантирует полный экспорт MST структуры и данных
	selectorNode := blockstore.BuildSelectorNodeExploreAll()

	// === Выполнение экспорта в CAR формат ===
	// Используем blockstore для создания CARv2 архива, начиная с корневого CID
	// и следуя всем ссылкам согласно селектору
	return r.bs.ExportCARV2(ctx, root, selectorNode, w)
}

// Close безопасно закрывает репозиторий, освобождая ресурсы.
// Этот метод должен быть вызван, когда репозиторий больше не нужен,
// чтобы гарантировать корректное завершение всех фоновых операций,
// сохранение данных и освобождение занятых ресурсов.
//
// Возвращает:
//   - error: ошибка закрытия, если операция не удалась
//
// Поведение:
// - Закрывает все открытые соединения с blockstore
// - Завершает работу индекса (MST) и сохраняет изменения
// - Закрывает SQLite индексер, если он был инициализирован
// - После вызова Close репозиторий становится недоступен для дальнейших операций
//
// Использование:
//
//	repo, err := OpenRepository(...)
//	if err != nil {
//	    log.Fatalf("failed to open repository: %v", err)
//	}
//	defer func() {
//	    if err := repo.Close(); err != nil {
//	        log.Printf("failed to close repository: %v", err)
//	    }
//	}()
//
//	// работа с репозиторием
//
// Производительность: операция может занять некоторое время в зависимости от
// количества несохраненных изменений и состояния индекса
func (r *Repository) Close() error {
	var firstErr error

	// Закрываем SQLite индексер, если он был инициализирован
	if r.sqliteIndex != nil {
		if err := r.sqliteIndex.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to close SQLite indexer: %w", err)
		}
		r.sqliteIndex = nil
	}

	// Закрываем индекс MST, сохраняя все изменения
	if r.index != nil {
		if err := r.index.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to close index: %w", err)
		}
		r.index = nil
	}

	// Закрываем blockstore, освобождая все связанные ресурсы
	if r.bs != nil {
		if err := r.bs.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to close blockstore: %w", err)
		}
		r.bs = nil
	}

	return firstErr
}

// Datastore возвращает datastore, используемый blockstore репозитория.
func (r *Repository) Datastore() datastore.Datastore {
	return r.bs.Datastore()
}
