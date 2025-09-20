// SQLiteIndexer Demo Application
//
// Демонстрационное приложение для тестирования всех возможностей SQLiteIndexer.
// Показывает работу с индексацией, поиском, аналитикой и различными типами данных.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"ues/sqliteindexer"
)

// DemoApp представляет демонстрационное приложение
type DemoApp struct {
	indexer *sqliteindexer.SimpleSQLiteIndexer
	ctx     context.Context
}

func main() {
	fmt.Println("🚀 SQLiteIndexer Demo Application")
	fmt.Println("==================================")

	// Создаем временную базу данных
	tmpDir := os.TempDir()
	dbPath := filepath.Join(tmpDir, fmt.Sprintf("sqlite_indexer_demo_%d.db", time.Now().Unix()))

	fmt.Printf("📄 Создаем базу данных: %s\n", dbPath)

	// Инициализируем SimpleSQLiteIndexer (без FTS5)
	indexer, err := sqliteindexer.NewSimpleSQLiteIndexer(dbPath)
	if err != nil {
		log.Fatalf("❌ Ошибка создания индексера: %v", err)
	}
	defer func() {
		indexer.Close()
		// Удаляем временную БД после демо
		os.Remove(dbPath)
		fmt.Printf("🗑️  База данных удалена: %s\n", dbPath)
	}()

	app := &DemoApp{
		indexer: indexer,
		ctx:     context.Background(),
	}

	// Проверяем аргументы командной строки
	if len(os.Args) > 1 && os.Args[1] == "cli" {
		// Интерактивный режим
		app.runInteractiveCLI()
	} else {
		// Автоматическая демонстрация
		app.runDemo()
	}
}

// runDemo запускает полную демонстрацию возможностей
func (app *DemoApp) runDemo() {
	fmt.Println("\n🔍 Демонстрация возможностей SQLiteIndexer")
	fmt.Println("==========================================")

	// 1. Добавляем тестовые данные
	fmt.Println("\n📝 1. ИНДЕКСАЦИЯ ЗАПИСЕЙ")
	app.demoIndexing()

	// 2. Тестируем поиск
	fmt.Println("\n🔎 2. ТЕСТИРОВАНИЕ ПОИСКА")
	app.demoSearch()

	// 3. Показываем аналитику
	fmt.Println("\n📊 3. АНАЛИТИКА КОЛЛЕКЦИЙ")
	app.demoAnalytics()

	// 4. Тестируем обновление и удаление
	fmt.Println("\n✏️  4. ОБНОВЛЕНИЕ И УДАЛЕНИЕ")
	app.demoUpdatesAndDeletes()

	fmt.Println("\n✅ Демонстрация завершена успешно!")
}

// demoIndexing демонстрирует индексацию различных типов записей
func (app *DemoApp) demoIndexing() {
	testRecords := app.generateTestData()

	fmt.Printf("📊 Индексируем %d тестовых записей...\n", len(testRecords))

	for i, record := range testRecords {
		// Создаем фиктивный CID для демонстрации
		cid := app.generateCID(fmt.Sprintf("record_%d", i))

		err := app.indexer.IndexRecord(app.ctx, cid, record)
		if err != nil {
			fmt.Printf("❌ Ошибка индексации записи %d: %v\n", i, err)
			continue
		}

		fmt.Printf("  ✅ Записать %s/%s (CID: %s)\n",
			record.Collection, record.RKey, cid.String()[:12]+"...")
	}
}

// demoSearch демонстрирует различные типы поиска
func (app *DemoApp) demoSearch() {
	searchTests := []struct {
		name  string
		query sqliteindexer.SearchQuery
	}{
		{
			name: "Поиск всех постов",
			query: sqliteindexer.SearchQuery{
				Collection: "posts",
				Limit:      5,
			},
		},
		{
			name: "Полнотекстовый поиск 'технология'",
			query: sqliteindexer.SearchQuery{
				FullTextQuery: "технология",
				Limit:         3,
			},
		},
		{
			name: "Поиск по автору",
			query: sqliteindexer.SearchQuery{
				Collection: "posts",
				Filters: map[string]interface{}{
					"author": "alice",
				},
				Limit: 3,
			},
		},
		{
			name: "Поиск активных пользователей",
			query: sqliteindexer.SearchQuery{
				Collection: "users",
				Filters: map[string]interface{}{
					"active": "true",
				},
				Limit: 3,
			},
		},
		{
			name: "Поиск с сортировкой по дате",
			query: sqliteindexer.SearchQuery{
				Collection: "posts",
				SortBy:     "created_at",
				SortOrder:  "DESC",
				Limit:      3,
			},
		},
	}

	for _, test := range searchTests {
		fmt.Printf("\n🔍 %s:\n", test.name)

		results, err := app.indexer.SearchRecords(app.ctx, test.query)
		if err != nil {
			fmt.Printf("  ❌ Ошибка поиска: %v\n", err)
			continue
		}

		if len(results) == 0 {
			fmt.Printf("  📭 Результатов не найдено\n")
			continue
		}

		for i, result := range results {
			fmt.Printf("  %d. [%s/%s] %s",
				i+1, result.Collection, result.RKey, result.CID.String()[:12]+"...")

			// Показываем релевантность для FTS поиска
			if result.Relevance > 0 {
				fmt.Printf(" (релевантность: %.2f)", result.Relevance)
			}

			// Показываем краткое содержимое
			if title, ok := result.Data["title"].(string); ok {
				fmt.Printf(" - %s", title)
			} else if name, ok := result.Data["name"].(string); ok {
				fmt.Printf(" - %s", name)
			}
			fmt.Println()
		}
	}
}

// demoAnalytics демонстрирует аналитические возможности
func (app *DemoApp) demoAnalytics() {
	collections := []string{"posts", "users", "comments", "tags"}

	for _, collection := range collections {
		stats, err := app.indexer.GetCollectionStats(app.ctx, collection)
		if err != nil {
			fmt.Printf("❌ Ошибка получения статистики для %s: %v\n", collection, err)
			continue
		}

		fmt.Printf("📊 Коллекция '%s':\n", collection)
		fmt.Printf("  📄 Записей: %v\n", stats["record_count"])
		fmt.Printf("  🏷️  Типов: %v\n", stats["type_count"])

		if firstRecord, ok := stats["first_record"].(time.Time); ok && !firstRecord.IsZero() {
			fmt.Printf("  🕐 Первая запись: %s\n", firstRecord.Format("2006-01-02 15:04:05"))
		}

		if lastUpdated, ok := stats["last_updated"].(time.Time); ok && !lastUpdated.IsZero() {
			fmt.Printf("  🔄 Последнее обновление: %s\n", lastUpdated.Format("2006-01-02 15:04:05"))
		}

		fmt.Println()
	}
}

// demoUpdatesAndDeletes демонстрирует обновление и удаление записей
func (app *DemoApp) demoUpdatesAndDeletes() {
	// Создаем новую запись
	cid := app.generateCID("update_test")
	record := sqliteindexer.IndexMetadata{
		Collection: "test",
		RKey:       "update_demo",
		RecordType: "demo",
		Data: map[string]interface{}{
			"title":   "Тестовая запись для обновления",
			"status":  "draft",
			"version": 1,
		},
		SearchText: "Тестовая запись для обновления черновик",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	fmt.Println("📝 Создаем тестовую запись...")
	err := app.indexer.IndexRecord(app.ctx, cid, record)
	if err != nil {
		fmt.Printf("❌ Ошибка создания записи: %v\n", err)
		return
	}

	// Проверяем, что запись создалась
	results, err := app.indexer.SearchRecords(app.ctx, sqliteindexer.SearchQuery{
		Collection: "test",
		Limit:      1,
	})
	if err != nil || len(results) == 0 {
		fmt.Println("❌ Запись не найдена после создания")
		return
	}
	fmt.Printf("✅ Запись создана: %s\n", results[0].Data["title"])

	// Обновляем запись
	fmt.Println("\n✏️  Обновляем запись...")
	record.Data["title"] = "Обновленная тестовая запись"
	record.Data["status"] = "published"
	record.Data["version"] = 2
	record.SearchText = "Обновленная тестовая запись опубликовано"
	record.UpdatedAt = time.Now()

	err = app.indexer.IndexRecord(app.ctx, cid, record)
	if err != nil {
		fmt.Printf("❌ Ошибка обновления записи: %v\n", err)
		return
	}

	// Проверяем обновление
	results, err = app.indexer.SearchRecords(app.ctx, sqliteindexer.SearchQuery{
		Collection: "test",
		Limit:      1,
	})
	if err != nil || len(results) == 0 {
		fmt.Println("❌ Запись не найдена после обновления")
		return
	}
	fmt.Printf("✅ Запись обновлена: %s (статус: %s)\n",
		results[0].Data["title"], results[0].Data["status"])

	// Удаляем запись
	fmt.Println("\n🗑️  Удаляем запись...")
	err = app.indexer.DeleteRecord(app.ctx, cid)
	if err != nil {
		fmt.Printf("❌ Ошибка удаления записи: %v\n", err)
		return
	}

	// Проверяем удаление
	results, err = app.indexer.SearchRecords(app.ctx, sqliteindexer.SearchQuery{
		Collection: "test",
		Limit:      1,
	})
	if err != nil {
		fmt.Printf("❌ Ошибка поиска после удаления: %v\n", err)
		return
	}

	if len(results) == 0 {
		fmt.Println("✅ Запись успешно удалена")
	} else {
		fmt.Println("❌ Запись все еще существует после удаления")
	}
}

// generateTestData создает набор тестовых данных
func (app *DemoApp) generateTestData() []sqliteindexer.IndexMetadata {
	now := time.Now()

	return []sqliteindexer.IndexMetadata{
		// Посты блога
		{
			Collection: "posts",
			RKey:       "post_1",
			RecordType: "article",
			Data: map[string]interface{}{
				"title":     "Введение в распределенные системы",
				"author":    "alice",
				"content":   "Распределенные системы представляют собой сложную технологию...",
				"tags":      []string{"технология", "программирование", "архитектура"},
				"likes":     42,
				"published": true,
			},
			SearchText: "Введение в распределенные системы alice технология программирование архитектура",
			CreatedAt:  now.Add(-24 * time.Hour),
			UpdatedAt:  now.Add(-12 * time.Hour),
		},
		{
			Collection: "posts",
			RKey:       "post_2",
			RecordType: "article",
			Data: map[string]interface{}{
				"title":     "Основы блокчейн технологий",
				"author":    "bob",
				"content":   "Блокчейн - это инновационная технология распределенного реестра...",
				"tags":      []string{"блокчейн", "криптография", "технология"},
				"likes":     38,
				"published": true,
			},
			SearchText: "Основы блокчейн технологий bob криптография технология",
			CreatedAt:  now.Add(-18 * time.Hour),
			UpdatedAt:  now.Add(-6 * time.Hour),
		},
		{
			Collection: "posts",
			RKey:       "post_3",
			RecordType: "tutorial",
			Data: map[string]interface{}{
				"title":     "Как использовать Go для микросервисов",
				"author":    "alice",
				"content":   "Go является отличным языком для создания микросервисной архитектуры...",
				"tags":      []string{"go", "микросервисы", "программирование"},
				"likes":     67,
				"published": false,
			},
			SearchText: "Как использовать Go для микросервисов alice программирование",
			CreatedAt:  now.Add(-6 * time.Hour),
			UpdatedAt:  now.Add(-2 * time.Hour),
		},

		// Пользователи
		{
			Collection: "users",
			RKey:       "user_alice",
			RecordType: "developer",
			Data: map[string]interface{}{
				"name":      "Alice Johnson",
				"email":     "alice@example.com",
				"active":    true,
				"role":      "senior_developer",
				"skills":    []string{"Go", "Python", "JavaScript", "Docker"},
				"join_date": "2022-01-15",
			},
			SearchText: "Alice Johnson alice@example.com senior_developer Go Python JavaScript Docker",
			CreatedAt:  now.Add(-720 * time.Hour), // 30 дней назад
			UpdatedAt:  now.Add(-1 * time.Hour),
		},
		{
			Collection: "users",
			RKey:       "user_bob",
			RecordType: "developer",
			Data: map[string]interface{}{
				"name":      "Bob Smith",
				"email":     "bob@example.com",
				"active":    true,
				"role":      "tech_lead",
				"skills":    []string{"Java", "Kubernetes", "AWS", "Terraform"},
				"join_date": "2021-08-20",
			},
			SearchText: "Bob Smith bob@example.com tech_lead Java Kubernetes AWS Terraform",
			CreatedAt:  now.Add(-1000 * time.Hour), // ~42 дня назад
			UpdatedAt:  now.Add(-48 * time.Hour),
		},
		{
			Collection: "users",
			RKey:       "user_carol",
			RecordType: "designer",
			Data: map[string]interface{}{
				"name":      "Carol Davis",
				"email":     "carol@example.com",
				"active":    false,
				"role":      "ui_designer",
				"skills":    []string{"Figma", "Sketch", "CSS", "React"},
				"join_date": "2023-03-10",
			},
			SearchText: "Carol Davis carol@example.com ui_designer Figma Sketch CSS React",
			CreatedAt:  now.Add(-200 * time.Hour),
			UpdatedAt:  now.Add(-120 * time.Hour),
		},

		// Комментарии
		{
			Collection: "comments",
			RKey:       "comment_1",
			RecordType: "post_comment",
			Data: map[string]interface{}{
				"post_id": "post_1",
				"author":  "bob",
				"content": "Отличная статья! Очень полезная информация о распределенных системах.",
				"upvotes": 5,
			},
			SearchText: "Отличная статья полезная информация распределенных системах bob",
			CreatedAt:  now.Add(-12 * time.Hour),
			UpdatedAt:  now.Add(-12 * time.Hour),
		},
		{
			Collection: "comments",
			RKey:       "comment_2",
			RecordType: "post_comment",
			Data: map[string]interface{}{
				"post_id": "post_2",
				"author":  "alice",
				"content": "Интересный взгляд на блокчейн технологии. Хотелось бы больше примеров.",
				"upvotes": 3,
			},
			SearchText: "Интересный взгляд блокчейн технологии примеров alice",
			CreatedAt:  now.Add(-8 * time.Hour),
			UpdatedAt:  now.Add(-8 * time.Hour),
		},

		// Теги
		{
			Collection: "tags",
			RKey:       "tag_tech",
			RecordType: "category",
			Data: map[string]interface{}{
				"name":        "технология",
				"description": "Статьи о современных технологиях и инновациях",
				"post_count":  15,
				"color":       "#2196F3",
			},
			SearchText: "технология современных технологиях инновациях",
			CreatedAt:  now.Add(-2000 * time.Hour),
			UpdatedAt:  now.Add(-24 * time.Hour),
		},
		{
			Collection: "tags",
			RKey:       "tag_programming",
			RecordType: "category",
			Data: map[string]interface{}{
				"name":        "программирование",
				"description": "Статьи о языках программирования и разработке",
				"post_count":  23,
				"color":       "#4CAF50",
			},
			SearchText: "программирование языках программирования разработке",
			CreatedAt:  now.Add(-1800 * time.Hour),
			UpdatedAt:  now.Add(-12 * time.Hour),
		},
	}
}

// generateCID создает фиктивный CID для демонстрации
func (app *DemoApp) generateCID(data string) cid.Cid {
	// Создаем простой hash из строки для демонстрации
	hash, _ := mh.Sum([]byte(data), mh.SHA2_256, -1)
	c := cid.NewCidV1(cid.Raw, hash)
	return c
}

// Дополнительные утилиты для красивого вывода

func prettyPrintJSON(v interface{}) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("Error: %v", err)
		return
	}
	fmt.Println(string(b))
}

// runInteractiveCLI запускает интерактивный CLI режим
func (app *DemoApp) runInteractiveCLI() {
	fmt.Println("\n🎯 Интерактивный режим SQLiteIndexer")
	fmt.Println("===================================")
	fmt.Println("Доступные команды:")
	fmt.Println("  1. setup     - Создать тестовые данные")
	fmt.Println("  2. search    - Поиск записей")
	fmt.Println("  3. stats     - Статистика коллекций")
	fmt.Println("  4. add       - Добавить запись")
	fmt.Println("  5. update    - Обновить запись")
	fmt.Println("  6. delete    - Удалить запись")
	fmt.Println("  7. demo      - Запустить полную демонстрацию")
	fmt.Println("  8. help      - Показать эту справку")
	fmt.Println("  9. exit      - Выход")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("indexer> ")
		if !scanner.Scan() {
			break
		}

		command := strings.TrimSpace(scanner.Text())
		if command == "" {
			continue
		}

		parts := strings.Fields(command)
		cmd := parts[0]

		switch cmd {
		case "setup":
			app.cmdSetup()
		case "search":
			app.cmdSearch(parts[1:])
		case "stats":
			app.cmdStats(parts[1:])
		case "add":
			app.cmdAdd()
		case "update":
			app.cmdUpdate()
		case "delete":
			app.cmdDelete()
		case "demo":
			app.runDemo()
		case "help":
			app.cmdHelp()
		case "exit", "quit":
			fmt.Println("👋 До свидания!")
			return
		default:
			fmt.Printf("❌ Неизвестная команда: %s. Введите 'help' для справки.\n", cmd)
		}
		fmt.Println()
	}
}

func (app *DemoApp) cmdSetup() {
	fmt.Println("📝 Создание тестовых данных...")
	app.demoIndexing()
	fmt.Println("✅ Тестовые данные созданы!")
}

func (app *DemoApp) cmdSearch(args []string) {
	if len(args) == 0 {
		fmt.Println("Использование: search <тип_поиска>")
		fmt.Println("Типы поиска:")
		fmt.Println("  collection <название>  - Поиск по коллекции")
		fmt.Println("  text <запрос>         - Полнотекстовый поиск")
		fmt.Println("  author <имя>          - Поиск по автору")
		fmt.Println("  active                - Поиск активных пользователей")
		return
	}

	searchType := args[0]
	var query sqliteindexer.SearchQuery

	switch searchType {
	case "collection":
		if len(args) < 2 {
			fmt.Println("❌ Укажите название коллекции")
			return
		}
		query = sqliteindexer.SearchQuery{
			Collection: args[1],
			Limit:      10,
		}
	case "text":
		if len(args) < 2 {
			fmt.Println("❌ Укажите поисковый запрос")
			return
		}
		query = sqliteindexer.SearchQuery{
			FullTextQuery: strings.Join(args[1:], " "),
			Limit:         10,
		}
	case "author":
		if len(args) < 2 {
			fmt.Println("❌ Укажите имя автора")
			return
		}
		query = sqliteindexer.SearchQuery{
			Collection: "posts",
			Filters: map[string]interface{}{
				"author": args[1],
			},
			Limit: 10,
		}
	case "active":
		query = sqliteindexer.SearchQuery{
			Collection: "users",
			Filters: map[string]interface{}{
				"active": "true",
			},
			Limit: 10,
		}
	default:
		fmt.Printf("❌ Неизвестный тип поиска: %s\n", searchType)
		return
	}

	results, err := app.indexer.SearchRecords(app.ctx, query)
	if err != nil {
		fmt.Printf("❌ Ошибка поиска: %v\n", err)
		return
	}

	if len(results) == 0 {
		fmt.Println("📭 Результатов не найдено")
		return
	}

	fmt.Printf("🔍 Найдено %d результатов:\n", len(results))
	for i, result := range results {
		fmt.Printf("\n%d. [%s/%s] %s",
			i+1, result.Collection, result.RKey, result.CID.String()[:12]+"...")

		if result.Relevance > 0 {
			fmt.Printf(" (релевантность: %.2f)", result.Relevance)
		}

		if title, ok := result.Data["title"].(string); ok {
			fmt.Printf("\n   📄 %s", title)
		} else if name, ok := result.Data["name"].(string); ok {
			fmt.Printf("\n   👤 %s", name)
		}

		if author, ok := result.Data["author"].(string); ok {
			fmt.Printf("\n   ✍️  Автор: %s", author)
		}

		fmt.Printf("\n   🕐 Создано: %s\n", result.CreatedAt.Format("2006-01-02 15:04:05"))
	}
}

func (app *DemoApp) cmdStats(args []string) {
	collections := []string{"posts", "users", "comments", "tags"}

	if len(args) > 0 {
		collections = args
	}

	fmt.Println("📊 Статистика коллекций:")
	for _, collection := range collections {
		stats, err := app.indexer.GetCollectionStats(app.ctx, collection)
		if err != nil {
			fmt.Printf("❌ Ошибка получения статистики для %s: %v\n", collection, err)
			continue
		}

		fmt.Printf("\n🏷️  %s:\n", collection)
		fmt.Printf("  📄 Записей: %v\n", stats["record_count"])
		fmt.Printf("  🏷️  Типов: %v\n", stats["type_count"])

		if firstRecord, ok := stats["first_record"].(time.Time); ok && !firstRecord.IsZero() {
			fmt.Printf("  🕐 Первая запись: %s\n", firstRecord.Format("2006-01-02 15:04:05"))
		}

		if lastUpdated, ok := stats["last_updated"].(time.Time); ok && !lastUpdated.IsZero() {
			fmt.Printf("  🔄 Последнее обновление: %s\n", lastUpdated.Format("2006-01-02 15:04:05"))
		}
	}
}

func (app *DemoApp) cmdAdd() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("Коллекция: ")
	scanner.Scan()
	collection := strings.TrimSpace(scanner.Text())

	fmt.Print("Ключ записи: ")
	scanner.Scan()
	rkey := strings.TrimSpace(scanner.Text())

	fmt.Print("Тип записи: ")
	scanner.Scan()
	recordType := strings.TrimSpace(scanner.Text())

	fmt.Print("Заголовок: ")
	scanner.Scan()
	title := strings.TrimSpace(scanner.Text())

	fmt.Print("Автор: ")
	scanner.Scan()
	author := strings.TrimSpace(scanner.Text())

	fmt.Print("Контент: ")
	scanner.Scan()
	content := strings.TrimSpace(scanner.Text())

	cid := app.generateCID(fmt.Sprintf("%s_%s_%d", collection, rkey, time.Now().Unix()))

	record := sqliteindexer.IndexMetadata{
		Collection: collection,
		RKey:       rkey,
		RecordType: recordType,
		Data: map[string]interface{}{
			"title":   title,
			"author":  author,
			"content": content,
		},
		SearchText: fmt.Sprintf("%s %s %s", title, author, content),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	err := app.indexer.IndexRecord(app.ctx, cid, record)
	if err != nil {
		fmt.Printf("❌ Ошибка добавления записи: %v\n", err)
		return
	}

	fmt.Printf("✅ Запись добавлена: %s (CID: %s)\n", title, cid.String()[:12]+"...")
}

func (app *DemoApp) cmdUpdate() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("CID записи для обновления: ")
	scanner.Scan()
	cidStr := strings.TrimSpace(scanner.Text())

	parsedCID, err := cid.Parse(cidStr)
	if err != nil {
		fmt.Printf("❌ Некорректный CID: %v\n", err)
		return
	}

	// Сначала найдем запись
	results, err := app.indexer.SearchRecords(app.ctx, sqliteindexer.SearchQuery{
		Limit: 1000,
	})
	if err != nil {
		fmt.Printf("❌ Ошибка поиска записи: %v\n", err)
		return
	}

	var foundRecord *sqliteindexer.SearchResult
	for _, result := range results {
		if result.CID.String() == parsedCID.String() {
			foundRecord = &result
			break
		}
	}

	if foundRecord == nil {
		fmt.Println("❌ Запись не найдена")
		return
	}

	fmt.Printf("Найдена запись: %v\n", foundRecord.Data["title"])
	fmt.Print("Новый заголовок (Enter - оставить как есть): ")
	scanner.Scan()
	newTitle := strings.TrimSpace(scanner.Text())

	if newTitle != "" {
		foundRecord.Data["title"] = newTitle
	}

	record := sqliteindexer.IndexMetadata{
		Collection: foundRecord.Collection,
		RKey:       foundRecord.RKey,
		RecordType: foundRecord.RecordType,
		Data:       foundRecord.Data,
		SearchText: fmt.Sprintf("%v %v %v", foundRecord.Data["title"], foundRecord.Data["author"], foundRecord.Data["content"]),
		CreatedAt:  foundRecord.CreatedAt,
		UpdatedAt:  time.Now(),
	}

	err = app.indexer.IndexRecord(app.ctx, parsedCID, record)
	if err != nil {
		fmt.Printf("❌ Ошибка обновления записи: %v\n", err)
		return
	}

	fmt.Println("✅ Запись обновлена!")
}

func (app *DemoApp) cmdDelete() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("CID записи для удаления: ")
	scanner.Scan()
	cidStr := strings.TrimSpace(scanner.Text())

	parsedCID, err := cid.Parse(cidStr)
	if err != nil {
		fmt.Printf("❌ Некорректный CID: %v\n", err)
		return
	}

	err = app.indexer.DeleteRecord(app.ctx, parsedCID)
	if err != nil {
		fmt.Printf("❌ Ошибка удаления записи: %v\n", err)
		return
	}

	fmt.Println("✅ Запись удалена!")
}

func (app *DemoApp) cmdHelp() {
	fmt.Println("📖 Справка по командам:")
	fmt.Println()
	fmt.Println("🛠️  НАСТРОЙКА:")
	fmt.Println("  setup                     - Создать тестовые данные")
	fmt.Println()
	fmt.Println("🔍 ПОИСК:")
	fmt.Println("  search collection <имя>   - Поиск по коллекции")
	fmt.Println("  search text <запрос>      - Полнотекстовый поиск")
	fmt.Println("  search author <имя>       - Поиск по автору")
	fmt.Println("  search active             - Активные пользователи")
	fmt.Println()
	fmt.Println("📊 АНАЛИТИКА:")
	fmt.Println("  stats                     - Статистика всех коллекций")
	fmt.Println("  stats <коллекция>         - Статистика конкретной коллекции")
	fmt.Println()
	fmt.Println("✏️  УПРАВЛЕНИЕ ДАННЫМИ:")
	fmt.Println("  add                       - Добавить новую запись")
	fmt.Println("  update                    - Обновить существующую запись")
	fmt.Println("  delete                    - Удалить запись")
	fmt.Println()
	fmt.Println("🎯 ДРУГОЕ:")
	fmt.Println("  demo                      - Запустить полную демонстрацию")
	fmt.Println("  help                      - Показать эту справку")
	fmt.Println("  exit                      - Выход из программы")
	fmt.Println()
	fmt.Println("💡 Примеры:")
	fmt.Println("  search collection posts")
	fmt.Println("  search text 'технология программирование'")
	fmt.Println("  search author alice")
	fmt.Println("  stats posts users")
}
