package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"ues/blockstore"
	"ues/datastore"
	"ues/repository"

	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/multicodec"
	"github.com/ipld/go-ipld-prime/node/basicnode"
)

func main() {
	// Инициализируем кодеки IPLD
	multicodec.RegisterEncoder(0x71, dagcbor.Encode)
	multicodec.RegisterDecoder(0x71, dagcbor.Decode)

	ctx := context.Background()

	// Создаем временное хранилище
	tempDir := "/tmp/ues_complex_example"
	err := os.MkdirAll(tempDir, 0755)
	if err != nil {
		log.Fatalf("Ошибка создания временной директории: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ds, err := datastore.NewDatastorage(tempDir, nil)
	if err != nil {
		log.Fatalf("Ошибка создания datastore: %v", err)
	}
	defer ds.Close()

	bs := blockstore.NewBlockstore(ds)
	defer bs.Close()

	// Создаем новый репозиторий
	repo := repository.New(bs)

	fmt.Println("=== Создание сложной структуры репозитория ===")
	fmt.Println()

	// Создаем несколько коллекций
	collections := []string{"xxx", "posts", "users", "comments"}

	for _, collection := range collections {
		fmt.Printf("Создание коллекции '%s'...\n", collection)
		_, err = repo.CreateCollection(ctx, collection)
		if err != nil {
			log.Fatalf("Ошибка создания коллекции %s: %v", collection, err)
		}
	}

	fmt.Printf("Созданы коллекции: %v\n", repo.ListCollections())
	fmt.Println()

	// Добавляем документ с ключом "ooo" в коллекцию "xxx"
	fmt.Println("=== Добавление документа в коллекцию 'xxx' ===")

	doc1 := createDocument(map[string]interface{}{
		"title":   "Документ с ключом ooo",
		"content": "Содержимое документа в коллекции xxx",
		"type":    "example",
	})

	_, err = repo.PutRecord(ctx, "xxx", "ooo", doc1)
	if err != nil {
		log.Fatalf("Ошибка добавления записи: %v", err)
	}

	// Добавляем еще несколько записей для демонстрации
	fmt.Println("Добавление дополнительных записей...")

	// В коллекцию "posts"
	post1 := createDocument(map[string]interface{}{
		"title":   "Первый пост",
		"author":  "user1",
		"content": "Содержимое первого поста",
	})
	_, err = repo.PutRecord(ctx, "posts", "post_001", post1)
	if err != nil {
		log.Fatalf("Ошибка добавления поста: %v", err)
	}

	post2 := createDocument(map[string]interface{}{
		"title":   "Второй пост",
		"author":  "user2",
		"content": "Содержимое второго поста",
	})
	_, err = repo.PutRecord(ctx, "posts", "post_002", post2)
	if err != nil {
		log.Fatalf("Ошибка добавления поста: %v", err)
	}

	// В коллекцию "users"
	user1 := createDocument(map[string]interface{}{
		"name":  "Иван Иванов",
		"email": "ivan@example.com",
		"role":  "admin",
	})
	_, err = repo.PutRecord(ctx, "users", "user_ivan", user1)
	if err != nil {
		log.Fatalf("Ошибка добавления пользователя: %v", err)
	}

	user2 := createDocument(map[string]interface{}{
		"name":  "Мария Петрова",
		"email": "maria@example.com",
		"role":  "editor",
	})
	_, err = repo.PutRecord(ctx, "users", "user_maria", user2)
	if err != nil {
		log.Fatalf("Ошибка добавления пользователя: %v", err)
	}

	// В коллекцию "comments"
	comment1 := createDocument(map[string]interface{}{
		"post_id": "post_001",
		"author":  "user_maria",
		"text":    "Отличный пост!",
	})
	_, err = repo.PutRecord(ctx, "comments", "comment_001", comment1)
	if err != nil {
		log.Fatalf("Ошибка добавления комментария: %v", err)
	}

	fmt.Println()

	// Показываем итоговую структуру ключей репозитория
	fmt.Println("=== ПОЛНАЯ СТРУКТУРА КЛЮЧЕЙ РЕПОЗИТОРИЯ ===")
	fmt.Println()

	collections = repo.ListCollections()
	fmt.Printf("Всего коллекций: %d\n", len(collections))
	fmt.Println()

	totalRecords := 0

	for _, collectionName := range collections {
		fmt.Printf("📁 Коллекция: %s\n", collectionName)

		records, err := repo.ListRecords(ctx, collectionName)
		if err != nil {
			fmt.Printf("   ❌ Ошибка получения записей: %v\n", err)
			continue
		}

		if len(records) == 0 {
			fmt.Println("   (пустая коллекция)")
		} else {
			fmt.Printf("   Записей: %d\n", len(records))
			totalRecords += len(records)

			for i, entry := range records {
				fmt.Printf("   %d. 📄 %s/%s\n", i+1, collectionName, entry.Key)
				fmt.Printf("      CID: %s\n", entry.Value.String())

				// Получаем и показываем содержимое записи
				node, found, err := repo.GetRecord(ctx, collectionName, entry.Key)
				if found && err == nil {
					fmt.Print("      Содержимое: ")
					if titleNode, err := node.LookupByString("title"); err == nil {
						if title, err := titleNode.AsString(); err == nil {
							fmt.Printf("title='%s'", title)
						}
					}
					if nameNode, err := node.LookupByString("name"); err == nil {
						if name, err := nameNode.AsString(); err == nil {
							fmt.Printf("name='%s'", name)
						}
					}
					if textNode, err := node.LookupByString("text"); err == nil {
						if text, err := textNode.AsString(); err == nil {
							fmt.Printf("text='%s'", text)
						}
					}
					fmt.Println()
				}
				fmt.Println()
			}
		}
		fmt.Println()
	}

	fmt.Printf("=== ИТОГО ===\n")
	fmt.Printf("Коллекций: %d\n", len(collections))
	fmt.Printf("Записей: %d\n", totalRecords)
	fmt.Println()

	// Демонстрируем поиск конкретной записи
	fmt.Println("=== ПОИСК ЗАПИСИ xxx/ooo ===")
	node, found, err := repo.GetRecord(ctx, "xxx", "ooo")
	if err != nil {
		fmt.Printf("Ошибка поиска: %v\n", err)
	} else if !found {
		fmt.Println("Запись не найдена!")
	} else {
		fmt.Println("✅ Запись найдена!")
		titleNode, _ := node.LookupByString("title")
		title, _ := titleNode.AsString()
		fmt.Printf("Заголовок: %s\n", title)

		contentNode, _ := node.LookupByString("content")
		content, _ := contentNode.AsString()
		fmt.Printf("Содержимое: %s\n", content)
	}
}

func createDocument(data map[string]interface{}) datamodel.Node {
	nodeBuilder := basicnode.Prototype.Any.NewBuilder()
	mapAssembler, _ := nodeBuilder.BeginMap(int64(len(data)))

	for key, value := range data {
		mapAssembler.AssembleKey().AssignString(key)
		mapAssembler.AssembleValue().AssignString(fmt.Sprintf("%v", value))
	}

	mapAssembler.Finish()
	return nodeBuilder.Build()
}
