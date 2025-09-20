package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"ues/blockstore"
	ds "ues/datastore"
	"ues/repository"

	"github.com/ipfs/go-datastore"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/multicodec"
	"github.com/ipld/go-ipld-prime/node/basicnode"
)

func main() {
	// Инициализируем кодеки IPLD
	multicodec.RegisterEncoder(0x71, dagcbor.Encode)
	multicodec.RegisterDecoder(0x71, dagcbor.Decode)

	ctx := context.Background()

	// Создаем временное хранилище
	tempDir := "/tmp/ues_example"
	err := os.MkdirAll(tempDir, 0755)
	if err != nil {
		log.Fatalf("Ошибка создания временной директории: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ds, err := ds.NewDatastorage(tempDir, nil)
	if err != nil {
		log.Fatalf("Ошибка создания datastore: %v", err)
	}
	defer ds.Close()

	bs := blockstore.NewBlockstore(ds)

	// Создаем новый репозиторий
	repo := repository.New(bs)

	fmt.Println("=== Начальное состояние репозитория ===")
	fmt.Println("Коллекции:", repo.ListCollections())
	fmt.Println()

	// Создаем коллекцию "xxx"
	fmt.Println("=== Создание коллекции 'xxx' ===")
	_, err = repo.CreateCollection(ctx, "xxx")
	if err != nil {
		log.Fatalf("Ошибка создания коллекции: %v", err)
	}

	fmt.Println("Коллекции после создания 'xxx':", repo.ListCollections())

	// Проверяем записи в коллекции (должна быть пустой)
	records, err := repo.ListRecords(ctx, "xxx")
	if err != nil {
		log.Fatalf("Ошибка получения записей: %v", err)
	}
	fmt.Printf("Записи в коллекции 'xxx': %d записей\n", len(records))
	fmt.Println()

	// Создаем документ для добавления
	fmt.Println("=== Добавление документа с ключом 'ooo' ===")

	// Создаем тестовый документ
	nodeBuilder := basicnode.Prototype.Any.NewBuilder()
	mapAssembler, _ := nodeBuilder.BeginMap(2)

	mapAssembler.AssembleKey().AssignString("title")
	mapAssembler.AssembleValue().AssignString("Тестовый документ")

	mapAssembler.AssembleKey().AssignString("content")
	mapAssembler.AssembleValue().AssignString("Это содержимое документа с ключом ooo")

	mapAssembler.Finish()
	document := nodeBuilder.Build()

	// Добавляем документ с ключом "ooo"
	recordCID, err := repo.PutRecord(ctx, "xxx", datastore.NewKey("ooo").String(), document)
	if err != nil {
		log.Fatalf("Ошибка добавления записи: %v", err)
	}

	fmt.Printf("Документ добавлен с CID: %s\n", recordCID.String())
	fmt.Println()

	// Показываем итоговое состояние репозитория
	fmt.Println("=== Итоговое состояние репозитория ===")
	fmt.Println("Коллекции:", repo.ListCollections())

	// Получаем список всех записей в коллекции "xxx"
	records, err = repo.ListRecords(ctx, "xxx")
	if err != nil {
		log.Fatalf("Ошибка получения записей: %v", err)
	}

	fmt.Printf("Записи в коллекции 'xxx': %d записей\n", len(records))
	for i, entry := range records {
		fmt.Printf("  %d. Ключ: '%s', CID: %s\n", i+1, entry.Key, entry.Value.String())
	}
	fmt.Println()

	// Показываем структуру ключей репозитория
	fmt.Println("=== Структура ключей репозитория ===")
	collections := repo.ListCollections()
	fmt.Println("Полный список ключей в репозитории:")

	for _, collectionName := range collections {
		fmt.Printf("Коллекция: %s\n", collectionName)

		records, err := repo.ListRecords(ctx, collectionName)
		if err != nil {
			fmt.Printf("  Ошибка получения записей: %v\n", err)
			continue
		}

		if len(records) == 0 {
			fmt.Println("  (пустая коллекция)")
		} else {
			for _, entry := range records {
				fmt.Printf("  └── %s/%s -> %s\n", collectionName, entry.Key, entry.Value.String())
			}
		}
		fmt.Println()
	}

	// Проверяем доступ к документу
	fmt.Println("=== Проверка доступа к документу ===")
	retrievedDoc, found, err := repo.GetRecord(ctx, "xxx", "ooo")
	if err != nil {
		log.Fatalf("Ошибка получения документа: %v", err)
	}

	if found {
		fmt.Println("Документ успешно найден!")
		titleNode, _ := retrievedDoc.LookupByString("title")
		title, _ := titleNode.AsString()
		fmt.Printf("Заголовок: %s\n", title)

		contentNode, _ := retrievedDoc.LookupByString("content")
		content, _ := contentNode.AsString()
		fmt.Printf("Содержимое: %s\n", content)
	} else {
		fmt.Println("Документ не найден!")
	}

	keys, es, err := ds.Keys(context.Background(), datastore.RawKey(""))
	if err != nil {
		log.Fatalf("Ошибка получения ключей из datastore: %v", err)
	}
	fmt.Println("Все ключи в datastore:")
	for key := range keys {
		fmt.Println(" -", key.String())
	}
	if es != nil {
		fmt.Println("Ошибки при получении ключей:")
		for e := range es {
			fmt.Println(" -err", e)
		}
	}

}
