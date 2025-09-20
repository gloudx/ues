package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"ues/blockstore"
	ds "ues/datastore"
	"ues/repository"

	dsapi "github.com/ipfs/go-datastore"
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
	tempDir := "/tmp/ues_seeds_demo"
	err := os.MkdirAll(tempDir, 0755)
	if err != nil {
		log.Fatalf("Ошибка создания временной директории: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Инициализация хранилища
	datastore, err := ds.NewDatastorage(filepath.Join(tempDir, "datastore"), nil)
	if err != nil {
		log.Fatalf("Ошибка создания datastore: %v", err)
	}
	defer datastore.Close()

	blockstore := blockstore.NewBlockstore(datastore)
	repo := repository.New(blockstore)

	fmt.Println("🚀 Демонстрация ключей в датасторе с сидами aaa, bbb, ccc")
	fmt.Println("=================================================================")
	fmt.Println()

	// Создаем коллекцию "seeds"
	_, err = repo.CreateCollection(ctx, "seeds")
	if err != nil {
		log.Fatalf("Ошибка создания коллекции: %v", err)
	}
	fmt.Println("✅ Создана коллекция 'seeds'")

	// Создаем IPLD документы для каждого сида
	seeds := []string{"aaa", "bbb", "ccc"}

	for i, seed := range seeds {
		// Создаем IPLD документ
		nodeBuilder := basicnode.Prototype.Map.NewBuilder()
		mapAssembler, _ := nodeBuilder.BeginMap(3)

		mapAssembler.AssembleKey().AssignString("seed")
		mapAssembler.AssembleValue().AssignString(seed)

		mapAssembler.AssembleKey().AssignString("index")
		mapAssembler.AssembleValue().AssignInt(int64(i + 1))

		mapAssembler.AssembleKey().AssignString("description")
		mapAssembler.AssembleValue().AssignString(fmt.Sprintf("Документ для сида %s", seed))

		mapAssembler.Finish()
		document := nodeBuilder.Build()

		// Добавляем запись в репозиторий
		recordCID, err := repo.PutRecord(ctx, "seeds", seed, document)
		if err != nil {
			log.Fatalf("Ошибка добавления записи %s: %v", seed, err)
		}

		fmt.Printf("📄 Добавлен сид '%s' с CID: %s\n", seed, recordCID.String())
	}

	fmt.Println()

	// Коммитим изменения
	commitCID, err := repo.Commit(ctx)
	if err != nil {
		log.Fatalf("Ошибка создания коммита: %v", err)
	}
	fmt.Printf("💾 Коммит создан: %s\n", commitCID.String())
	fmt.Println()

	// Показываем все ключи в датасторе
	fmt.Println("🔍 ВСЕ КЛЮЧИ В ДАТАСТОРЕ:")
	fmt.Println("----------------------------------------")

	keys, errorChan, err := datastore.Keys(ctx, dsapi.NewKey(""))
	if err != nil {
		log.Fatalf("Ошибка получения ключей: %v", err)
	}

	var allKeys []string

	// Запускаем горутину для обработки ошибок
	go func() {
		for err := range errorChan {
			log.Printf("Ошибка при итерации ключей: %v", err)
		}
	}()

	// Собираем все ключи
	for key := range keys {
		allKeys = append(allKeys, key.String())
	}

	// Выводим ключи с группировкой
	fmt.Printf("Общее количество ключей: %d\n\n", len(allKeys))

	// Группируем ключи по типам
	blockKeys := []string{}
	indexKeys := []string{}
	otherKeys := []string{}

	for _, key := range allKeys {
		if len(key) > 10 && key[0] == '/' && (len(key) > 50 || containsCID(key)) {
			blockKeys = append(blockKeys, key)
		} else if containsWord(key, "index") || containsWord(key, "collection") {
			indexKeys = append(indexKeys, key)
		} else {
			otherKeys = append(otherKeys, key)
		}
	}

	// Выводим блочные ключи (CID)
	if len(blockKeys) > 0 {
		fmt.Printf("📦 БЛОЧНЫЕ КЛЮЧИ (CID blocks - %d шт.):\n", len(blockKeys))
		for i, key := range blockKeys {
			if i < 10 { // Показываем только первые 10
				fmt.Printf("   %s\n", key)
			}
		}
		if len(blockKeys) > 10 {
			fmt.Printf("   ... и еще %d блоков\n", len(blockKeys)-10)
		}
		fmt.Println()
	}

	// Выводим индексные ключи
	if len(indexKeys) > 0 {
		fmt.Printf("🗂️  ИНДЕКСНЫЕ КЛЮЧИ (%d шт.):\n", len(indexKeys))
		for _, key := range indexKeys {
			fmt.Printf("   %s\n", key)
		}
		fmt.Println()
	}

	// Выводим прочие ключи
	if len(otherKeys) > 0 {
		fmt.Printf("📋 ПРОЧИЕ КЛЮЧИ (%d шт.):\n", len(otherKeys))
		for _, key := range otherKeys {
			fmt.Printf("   %s\n", key)
		}
		fmt.Println()
	}

	// Показываем данные из репозитория
	fmt.Println("📊 ДАННЫЕ ИЗ РЕПОЗИТОРИЯ:")
	fmt.Println("------------------------------")

	collections := repo.ListCollections()
	fmt.Printf("Коллекции: %v\n", collections)

	records, err := repo.ListRecords(ctx, "seeds")
	if err != nil {
		log.Fatalf("Ошибка получения записей: %v", err)
	}

	fmt.Printf("Записи в коллекции 'seeds' (%d шт.):\n", len(records))
	for _, record := range records {
		fmt.Printf("   Ключ: '%s' -> CID: %s\n", record.Key, record.Value.String())
	}

	fmt.Println()
	fmt.Println("✨ Демонстрация завершена!")
}

// Вспомогательные функции
func containsCID(s string) bool {
	// Простая проверка на наличие характеристик CID
	return len(s) > 40 && (s[1] == 'b' || s[1] == 'z' || s[1] == 'Q')
}

func containsWord(s, word string) bool {
	return len(s) > len(word) &&
		(s[1:len(word)+1] == word ||
			s[len(s)-len(word):] == word ||
			fmt.Sprintf("/%s/", word) == s[0:len(word)+2] ||
			fmt.Sprintf("/%s", word) == s[len(s)-len(word)-1:])
}
