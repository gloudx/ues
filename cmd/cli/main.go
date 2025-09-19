package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/multicodec"
	"github.com/ipld/go-ipld-prime/node/basicnode"

	"ues/blockstore"
	uesDatastore "ues/datastore"
	"ues/repository"
)

const (
	defaultDataDir = "./ues-data"
)

func main() {
	// Инициализируем кодеки IPLD
	multicodec.RegisterEncoder(0x71, dagcbor.Encode)
	multicodec.RegisterDecoder(0x71, dagcbor.Decode)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	ctx := context.Background()

	switch command {
	case "help", "-h", "--help":
		printUsage()
	case "repo":
		handleRepoCommands(ctx, os.Args[2:])
	case "datastore", "ds":
		handleDatastoreCommands(ctx, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`UES CLI - Консольная утилита для работы с Universal Entity Streams

ИСПОЛЬЗОВАНИЕ:
    ues-cli <КОМАНДА> [АРГУМЕНТЫ]

КОМАНДЫ РЕПОЗИТОРИЯ:
    repo create-collection <коллекция>              - Создать новую коллекцию
    repo put <коллекция> <ключ> <JSON-данные>       - Добавить/обновить запись
    repo get <коллекция> <ключ>                     - Получить запись
    repo delete <коллекция> <ключ>                  - Удалить запись
    repo list <коллекция>                           - Список записей в коллекции
    repo list-collections                           - Список всех коллекций
    repo commit                                     - Создать коммит
    repo clear                                      - Очистить весь репозиторий

КОМАНДЫ ХРАНИЛИЩА ДАННЫХ:
    ds put <ключ> <значение>                        - Записать ключ-значение
    ds get <ключ>                                   - Получить значение по ключу
    ds has <ключ>                                   - Проверить существование ключа
    ds delete <ключ>                                - Удалить ключ
    ds list [префикс]                               - Список ключей (с опциональным префиксом)
    ds clear                                        - Очистить всё хранилище

ГЛОБАЛЬНЫЕ ФЛАГИ:
    --data-dir <путь>                               - Директория данных (по умолчанию: %s)

ПРИМЕРЫ:
    # Работа с репозиторием
    ues-cli repo create-collection posts
    ues-cli repo put posts post1 '{"title":"Hello","content":"World"}'
    ues-cli repo get posts post1
    ues-cli repo list posts
    ues-cli repo commit

    # Работа с хранилищем данных
    ues-cli ds put /user/settings '{"theme":"dark"}'
    ues-cli ds get /user/settings
    ues-cli ds list /user/
    ues-cli ds delete /user/settings

`, defaultDataDir)
}

func getDataDir() string {
	dataDir := defaultDataDir
	for i, arg := range os.Args {
		if arg == "--data-dir" && i+1 < len(os.Args) {
			dataDir = os.Args[i+1]
			break
		}
	}
	return dataDir
}

func setupStorage() (uesDatastore.Datastore, *repository.Repository, func(), error) {
	dataDir := getDataDir()

	// Создаем директорию если не существует
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, nil, nil, fmt.Errorf("ошибка создания директории данных: %w", err)
	}

	// Создаем datastore
	ds, err := uesDatastore.NewDatastorage(filepath.Join(dataDir, "datastore"), nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ошибка создания datastore: %w", err)
	}

	// Создаем blockstore
	bs := blockstore.NewBlockstore(ds)

	// Создаем репозиторий
	repo := repository.New(bs)

	// Пытаемся загрузить последний коммит из специального ключа
	ctx := context.Background()
	lastCommitKey := datastore.NewKey("/__ues_last_commit__")
	if lastCommitCIDBytes, err := ds.Get(ctx, lastCommitKey); err == nil {
		// Если есть сохраненный коммит, загружаем состояние репозитория
		lastCommitCIDStr := string(lastCommitCIDBytes)
		fmt.Printf("Загружаем репозиторий из коммита: %s\n", lastCommitCIDStr)
		if lastCommitCID, err := parseCID(lastCommitCIDStr); err == nil {
			if err := repo.LoadHead(ctx, lastCommitCID); err != nil {
				// Если не удалось загрузить, продолжаем с пустым репозиторием
				fmt.Fprintf(os.Stderr, "Предупреждение: не удалось загрузить последний коммит: %v\n", err)
			} else {
				fmt.Printf("Репозиторий успешно загружен из коммита\n")
			}
		} else {
			fmt.Fprintf(os.Stderr, "Предупреждение: не удалось распарсить CID коммита: %v\n", err)
		}
	} else {
		fmt.Printf("Создаем новый пустой репозиторий\n")
	}

	cleanup := func() {
		bs.Close()
		ds.Close()
	}

	return ds, repo, cleanup, nil
}

func parseCID(cidStr string) (cid.Cid, error) {
	// Простая функция для парсинга CID из строки
	// В реальной реализации можно использовать cid.Parse
	return cid.Parse(cidStr)
}

func handleRepoCommands(ctx context.Context, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Необходимо указать команду репозитория\n")
		printUsage()
		os.Exit(1)
	}

	ds, repo, cleanup, err := setupStorage()
	if err != nil {
		log.Fatalf("Ошибка инициализации хранилища: %v", err)
	}
	defer cleanup()

	command := args[0]

	switch command {
	case "create-collection":
		handleCreateCollection(ctx, repo, args[1:])
	case "put":
		handleRepoPut(ctx, repo, args[1:])
	case "get":
		handleRepoGet(ctx, repo, args[1:])
	case "delete":
		handleRepoDelete(ctx, repo, args[1:])
	case "list":
		handleRepoList(ctx, repo, args[1:])
	case "list-collections":
		handleListCollections(ctx, repo)
	case "commit":
		handleCommit(ctx, ds, repo)
	case "clear":
		handleRepoClear(ctx, ds, repo)
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда репозитория: %s\n", command)
		os.Exit(1)
	}
}

func handleCreateCollection(ctx context.Context, repo *repository.Repository, args []string) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Использование: repo create-collection <коллекция>\n")
		os.Exit(1)
	}

	collection := args[0]
	cid, err := repo.CreateCollection(ctx, collection)
	if err != nil {
		log.Fatalf("Ошибка создания коллекции '%s': %v", collection, err)
	}

	fmt.Printf("Коллекция '%s' создана с CID: %s\n", collection, cid)
}

func handleRepoPut(ctx context.Context, repo *repository.Repository, args []string) {
	if len(args) != 3 {
		fmt.Fprintf(os.Stderr, "Использование: repo put <коллекция> <ключ> <JSON-данные>\n")
		os.Exit(1)
	}

	collection, rkey, jsonData := args[0], args[1], args[2]

	// Парсим JSON данные
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
		log.Fatalf("Ошибка парсинга JSON: %v", err)
	}

	// Преобразуем в IPLD node
	node, err := convertMapToNode(data)
	if err != nil {
		log.Fatalf("Ошибка преобразования данных в IPLD node: %v", err)
	}

	// Добавляем запись
	cid, err := repo.PutRecord(ctx, collection, rkey, node)
	if err != nil {
		log.Fatalf("Ошибка добавления записи: %v", err)
	}

	fmt.Printf("Запись добавлена: %s/%s -> %s\n", collection, rkey, cid)
}

func handleRepoGet(ctx context.Context, repo *repository.Repository, args []string) {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "Использование: repo get <коллекция> <ключ>\n")
		os.Exit(1)
	}

	collection, rkey := args[0], args[1]

	node, found, err := repo.GetRecord(ctx, collection, rkey)
	if err != nil {
		log.Fatalf("Ошибка получения записи: %v", err)
	}

	if !found {
		fmt.Printf("Запись %s/%s не найдена\n", collection, rkey)
		return
	}

	// Преобразуем node обратно в JSON для вывода
	data, err := convertNodeToMap(node)
	if err != nil {
		log.Fatalf("Ошибка преобразования данных: %v", err)
	}

	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Fatalf("Ошибка сериализации JSON: %v", err)
	}

	fmt.Printf("Запись %s/%s:\n%s\n", collection, rkey, string(jsonBytes))
}

func handleRepoDelete(ctx context.Context, repo *repository.Repository, args []string) {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "Использование: repo delete <коллекция> <ключ>\n")
		os.Exit(1)
	}

	collection, rkey := args[0], args[1]

	removed, err := repo.DeleteRecord(ctx, collection, rkey)
	if err != nil {
		log.Fatalf("Ошибка удаления записи: %v", err)
	}

	if removed {
		fmt.Printf("Запись %s/%s удалена\n", collection, rkey)
	} else {
		fmt.Printf("Запись %s/%s не найдена\n", collection, rkey)
	}
}

func handleRepoList(ctx context.Context, repo *repository.Repository, args []string) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Использование: repo list <коллекция>\n")
		os.Exit(1)
	}

	collection := args[0]

	cids, err := repo.ListCollection(ctx, collection)
	if err != nil {
		log.Fatalf("Ошибка получения списка записей: %v", err)
	}

	if len(cids) == 0 {
		fmt.Printf("Коллекция '%s' пуста\n", collection)
		return
	}

	fmt.Printf("Записи в коллекции '%s':\n", collection)
	for i, cid := range cids {
		fmt.Printf("  [%d] -> %s\n", i, cid)
	}
}

func handleListCollections(ctx context.Context, repo *repository.Repository) {
	collections := repo.ListCollections()

	if len(collections) == 0 {
		fmt.Println("Коллекции не найдены")
		return
	}

	fmt.Println("Коллекции:")
	for _, collection := range collections {
		fmt.Printf("  %s\n", collection)
	}
}

func handleCommit(ctx context.Context, ds uesDatastore.Datastore, repo *repository.Repository) {
	cid, err := repo.Commit(ctx)
	if err != nil {
		log.Fatalf("Ошибка создания коммита: %v", err)
	}

	// Сохраняем CID последнего коммита для следующих сессий
	lastCommitKey := datastore.NewKey("/__ues_last_commit__")
	if err := ds.Put(ctx, lastCommitKey, []byte(cid.String())); err != nil {
		fmt.Fprintf(os.Stderr, "Предупреждение: не удалось сохранить последний коммит: %v\n", err)
	}

	fmt.Printf("Коммит создан: %s\n", cid)
}

func handleRepoClear(ctx context.Context, ds uesDatastore.Datastore, repo *repository.Repository) {
	if err := ds.Clear(ctx); err != nil {
		log.Fatalf("Ошибка очистки хранилища: %v", err)
	}

	fmt.Println("Репозиторий полностью очищен")
}

func handleDatastoreCommands(ctx context.Context, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Необходимо указать команду хранилища данных\n")
		printUsage()
		os.Exit(1)
	}

	ds, _, cleanup, err := setupStorage()
	if err != nil {
		log.Fatalf("Ошибка инициализации хранилища: %v", err)
	}
	defer cleanup()

	command := args[0]

	switch command {
	case "put":
		handleDatastorePut(ctx, ds, args[1:])
	case "get":
		handleDatastoreGet(ctx, ds, args[1:])
	case "has":
		handleDatastoreHas(ctx, ds, args[1:])
	case "delete":
		handleDatastoreDelete(ctx, ds, args[1:])
	case "list":
		handleDatastoreList(ctx, ds, args[1:])
	case "clear":
		handleDatastoreClear(ctx, ds)
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда хранилища данных: %s\n", command)
		os.Exit(1)
	}
}

func handleDatastorePut(ctx context.Context, ds uesDatastore.Datastore, args []string) {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "Использование: ds put <ключ> <значение>\n")
		os.Exit(1)
	}

	key, value := args[0], args[1]
	dsKey := datastore.NewKey(key)

	if err := ds.Put(ctx, dsKey, []byte(value)); err != nil {
		log.Fatalf("Ошибка записи: %v", err)
	}

	fmt.Printf("Записано: %s\n", key)
}

func handleDatastoreGet(ctx context.Context, ds uesDatastore.Datastore, args []string) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Использование: ds get <ключ>\n")
		os.Exit(1)
	}

	key := args[0]
	dsKey := datastore.NewKey(key)

	value, err := ds.Get(ctx, dsKey)
	if err != nil {
		if err == datastore.ErrNotFound {
			fmt.Printf("Ключ '%s' не найден\n", key)
			return
		}
		log.Fatalf("Ошибка получения: %v", err)
	}

	fmt.Printf("%s: %s\n", key, string(value))
}

func handleDatastoreHas(ctx context.Context, ds uesDatastore.Datastore, args []string) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Использование: ds has <ключ>\n")
		os.Exit(1)
	}

	key := args[0]
	dsKey := datastore.NewKey(key)

	exists, err := ds.Has(ctx, dsKey)
	if err != nil {
		log.Fatalf("Ошибка проверки: %v", err)
	}

	if exists {
		fmt.Printf("Ключ '%s' существует\n", key)
	} else {
		fmt.Printf("Ключ '%s' не существует\n", key)
	}
}

func handleDatastoreDelete(ctx context.Context, ds uesDatastore.Datastore, args []string) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Использование: ds delete <ключ>\n")
		os.Exit(1)
	}

	key := args[0]
	dsKey := datastore.NewKey(key)

	if err := ds.Delete(ctx, dsKey); err != nil {
		log.Fatalf("Ошибка удаления: %v", err)
	}

	fmt.Printf("Ключ '%s' удален\n", key)
}

func handleDatastoreList(ctx context.Context, ds uesDatastore.Datastore, args []string) {
	prefix := "/"
	if len(args) > 0 {
		prefix = args[0]
	}

	dsKey := datastore.NewKey(prefix)
	keys, errCh, err := ds.Keys(ctx, dsKey)
	if err != nil {
		log.Fatalf("Ошибка получения списка ключей: %v", err)
	}

	fmt.Printf("Ключи с префиксом '%s':\n", prefix)
	count := 0

	for {
		select {
		case key, ok := <-keys:
			if !ok {
				goto done
			}
			fmt.Printf("  %s\n", key.String())
			count++
		case err := <-errCh:
			if err != nil {
				log.Fatalf("Ошибка итерации: %v", err)
			}
		}
	}

done:
	fmt.Printf("Всего найдено ключей: %d\n", count)
}

func handleDatastoreClear(ctx context.Context, ds uesDatastore.Datastore) {
	if err := ds.Clear(ctx); err != nil {
		log.Fatalf("Ошибка очистки хранилища: %v", err)
	}

	fmt.Println("Хранилище данных полностью очищено")
}

// Вспомогательные функции для работы с IPLD nodes

func convertMapToNode(data map[string]interface{}) (datamodel.Node, error) {
	nb := basicnode.Prototype.Map.NewBuilder()
	ma, err := nb.BeginMap(int64(len(data)))
	if err != nil {
		return nil, err
	}

	for key, value := range data {
		if err := ma.AssembleKey().AssignString(key); err != nil {
			return nil, err
		}

		if err := assembleValue(ma.AssembleValue(), value); err != nil {
			return nil, err
		}
	}

	if err := ma.Finish(); err != nil {
		return nil, err
	}

	return nb.Build(), nil
}

func assembleValue(na datamodel.NodeAssembler, value interface{}) error {
	switch v := value.(type) {
	case string:
		return na.AssignString(v)
	case float64:
		return na.AssignFloat(v)
	case bool:
		return na.AssignBool(v)
	case nil:
		return na.AssignNull()
	case map[string]interface{}:
		ma, err := na.BeginMap(int64(len(v)))
		if err != nil {
			return err
		}
		for key, val := range v {
			if err := ma.AssembleKey().AssignString(key); err != nil {
				return err
			}
			if err := assembleValue(ma.AssembleValue(), val); err != nil {
				return err
			}
		}
		return ma.Finish()
	case []interface{}:
		la, err := na.BeginList(int64(len(v)))
		if err != nil {
			return err
		}
		for _, val := range v {
			if err := assembleValue(la.AssembleValue(), val); err != nil {
				return err
			}
		}
		return la.Finish()
	default:
		return fmt.Errorf("неподдерживаемый тип данных: %T", value)
	}
}

func convertNodeToMap(node datamodel.Node) (map[string]interface{}, error) {
	if node.Kind() != datamodel.Kind_Map {
		return nil, fmt.Errorf("узел не является картой")
	}

	result := make(map[string]interface{})
	iter := node.MapIterator()

	for !iter.Done() {
		key, value, err := iter.Next()
		if err != nil {
			return nil, err
		}

		keyStr, err := key.AsString()
		if err != nil {
			return nil, err
		}

		val, err := convertNodeToInterface(value)
		if err != nil {
			return nil, err
		}

		result[keyStr] = val
	}

	return result, nil
}

func convertNodeToInterface(node datamodel.Node) (interface{}, error) {
	switch node.Kind() {
	case datamodel.Kind_String:
		return node.AsString()
	case datamodel.Kind_Float:
		return node.AsFloat()
	case datamodel.Kind_Bool:
		return node.AsBool()
	case datamodel.Kind_Null:
		return nil, nil
	case datamodel.Kind_Map:
		return convertNodeToMap(node)
	case datamodel.Kind_List:
		length := node.Length()
		result := make([]interface{}, length)
		for i := int64(0); i < length; i++ {
			item, err := node.LookupByIndex(i)
			if err != nil {
				return nil, err
			}
			val, err := convertNodeToInterface(item)
			if err != nil {
				return nil, err
			}
			result[i] = val
		}
		return result, nil
	default:
		return nil, fmt.Errorf("неподдерживаемый тип узла: %v", node.Kind())
	}
}
