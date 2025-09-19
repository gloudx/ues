package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Импортируем кодеки для регистрации
	_ "github.com/ipld/go-ipld-prime/codec/dagcbor"
	_ "github.com/ipld/go-ipld-prime/codec/dagjson"

	"ues/blockstore"
	"ues/datastore"
)

// setupTestRepo создает новый репозиторий для тестирования
func setupTestRepo(t *testing.T) (*Repository, blockstore.Blockstore, func()) {
	// Создаем временную директорию для тестов
	tempDir := t.TempDir()

	// Создаем datastore с временной директорией
	ds, err := datastore.NewDatastorage(tempDir, nil)
	require.NoError(t, err)

	// Создаем blockstore поверх datastore
	bs := blockstore.NewBlockstore(ds)

	// Создаем репозиторий
	repo := New(bs)

	// Функция очистки
	cleanup := func() {
		bs.Close()
		ds.Close()
	}

	return repo, bs, cleanup
}

// createTestNode создает тестовый IPLD узел с использованием Any протоипа
func createTestNode(data map[string]interface{}) (datamodel.Node, error) {
	// Используем Any prototype для универсальности
	builder := basicnode.Prototype.Any.NewBuilder()
	ma, err := builder.BeginMap(int64(len(data)))
	if err != nil {
		return nil, err
	}

	for key, value := range data {
		entry, err := ma.AssembleEntry(key)
		if err != nil {
			return nil, err
		}

		switch v := value.(type) {
		case string:
			if err := entry.AssignString(v); err != nil {
				return nil, err
			}
		case int:
			if err := entry.AssignInt(int64(v)); err != nil {
				return nil, err
			}
		case bool:
			if err := entry.AssignBool(v); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported type: %T", v)
		}
	}

	if err := ma.Finish(); err != nil {
		return nil, err
	}

	return builder.Build(), nil
}

func TestNew(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	assert.NotNil(t, repo)
	assert.NotNil(t, repo.bs)
	assert.NotNil(t, repo.index)
	assert.Equal(t, cid.Undef, repo.head)
	assert.Equal(t, cid.Undef, repo.prev)
}

func TestRepository_LoadHead_Empty(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Загрузка пустого репозитория (cid.Undef)
	err := repo.LoadHead(ctx, cid.Undef)
	require.NoError(t, err)

	assert.Equal(t, cid.Undef, repo.head)
	assert.Equal(t, cid.Undef, repo.prev)
}

func TestRepository_PutRecord(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию перед добавлением записей
	_, err := repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Создаем тестовые данные
	testData := map[string]interface{}{
		"title":   "Test Post",
		"content": "This is a test post",
		"author":  "testuser",
	}

	node, err := createTestNode(testData)
	require.NoError(t, err)

	// Добавляем запись
	recordCID, err := repo.PutRecord(ctx, "posts", "post1", node)
	require.NoError(t, err)
	assert.True(t, recordCID.Defined())

	// Проверяем, что запись можно получить
	retrievedCID, found, err := repo.GetRecordCID(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, recordCID, retrievedCID)
}

func TestRepository_GetRecordCID(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию
	_, err := repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Проверяем несуществующую запись
	_, found, err := repo.GetRecordCID(ctx, "posts", "nonexistent")
	require.NoError(t, err)
	assert.False(t, found)

	// Добавляем запись
	testData := map[string]interface{}{
		"title": "Test Post",
	}
	node, err := createTestNode(testData)
	require.NoError(t, err)

	recordCID, err := repo.PutRecord(ctx, "posts", "post1", node)
	require.NoError(t, err)

	// Проверяем, что запись найдена
	retrievedCID, found, err := repo.GetRecordCID(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, recordCID, retrievedCID)
}

func TestRepository_DeleteRecord(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию
	_, err := repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Попытка удалить несуществующую запись
	removed, err := repo.DeleteRecord(ctx, "posts", "nonexistent")
	require.NoError(t, err)
	assert.False(t, removed)

	// Добавляем запись
	testData := map[string]interface{}{
		"title": "Test Post",
	}
	node, err := createTestNode(testData)
	require.NoError(t, err)

	_, err = repo.PutRecord(ctx, "posts", "post1", node)
	require.NoError(t, err)

	// Проверяем, что запись существует
	_, found, err := repo.GetRecordCID(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, found)

	// Удаляем запись
	removed, err = repo.DeleteRecord(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, removed)

	// Проверяем, что запись удалена
	_, found, err = repo.GetRecordCID(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestRepository_ListCollection(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию
	_, err := repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Пустая коллекция
	records, err := repo.ListCollection(ctx, "posts")
	require.NoError(t, err)
	assert.Empty(t, records)

	// Добавляем несколько записей
	for i := 1; i <= 3; i++ {
		testData := map[string]interface{}{
			"title": fmt.Sprintf("Post %d", i),
		}
		node, err := createTestNode(testData)
		require.NoError(t, err)

		_, err = repo.PutRecord(ctx, "posts", fmt.Sprintf("post%d", i), node)
		require.NoError(t, err)
	}

	// Получаем список записей
	records, err = repo.ListCollection(ctx, "posts")
	require.NoError(t, err)
	assert.Len(t, records, 3)

	// Проверяем, что все CID определены
	for _, recordCID := range records {
		assert.True(t, recordCID.Defined())
	}
}

func TestRepository_Commit(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Первый коммит (пустой репозиторий)
	commitCID1, err := repo.Commit(ctx)
	require.NoError(t, err)
	assert.True(t, commitCID1.Defined())

	// Проверяем состояние после коммита
	assert.Equal(t, commitCID1, repo.head)
	assert.Equal(t, cid.Undef, repo.prev) // первый коммит, нет предыдущего

	// Создаем коллекцию и добавляем запись
	_, err = repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	testData := map[string]interface{}{
		"title": "Test Post",
	}
	node, err := createTestNode(testData)
	require.NoError(t, err)

	_, err = repo.PutRecord(ctx, "posts", "post1", node)
	require.NoError(t, err)

	// Второй коммит
	commitCID2, err := repo.Commit(ctx)
	require.NoError(t, err)
	assert.True(t, commitCID2.Defined())
	assert.NotEqual(t, commitCID1, commitCID2)

	// Проверяем состояние после второго коммита
	assert.Equal(t, commitCID2, repo.head)
	assert.Equal(t, commitCID1, repo.prev) // предыдущий коммит
}

func TestRepository_LoadHead_WithCommit(t *testing.T) {
	repo, bs, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию и добавляем запись и создаем коммит
	_, err := repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	testData := map[string]interface{}{
		"title": "Test Post",
	}
	node, err := createTestNode(testData)
	require.NoError(t, err)

	_, err = repo.PutRecord(ctx, "posts", "post1", node)
	require.NoError(t, err)

	commitCID, err := repo.Commit(ctx)
	require.NoError(t, err)

	// Создаем новый репозиторий и загружаем состояние
	repo2 := New(bs)
	err = repo2.LoadHead(ctx, commitCID)
	require.NoError(t, err)

	// Проверяем, что состояние восстановлено
	assert.Equal(t, commitCID, repo2.head)

	// Проверяем, что запись доступна
	retrievedCID, found, err := repo2.GetRecordCID(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.True(t, retrievedCID.Defined())
}

func TestBuildCommitNode(t *testing.T) {
	// Создаем тестовые CID
	rootCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	prevCID := cid.MustParse("bafybeie5gq4jxvzmsym6hjlwxej4amtoj7svlnqnghzymq4sfjfpfsxqge")
	ts := time.Unix(1640995200, 0) // 2022-01-01 00:00:00 UTC

	// Тест с предыдущим коммитом
	node, err := buildCommitNode(rootCID, prevCID, ts)
	require.NoError(t, err)
	assert.NotNil(t, node)

	// Проверяем поля
	rootField, err := node.LookupByString("root")
	require.NoError(t, err)
	rootLink, err := rootField.AsLink()
	require.NoError(t, err)

	prevField, err := node.LookupByString("prev")
	require.NoError(t, err)
	prevLink, err := prevField.AsLink()
	require.NoError(t, err)

	tsField, err := node.LookupByString("timestamp")
	require.NoError(t, err)
	tsValue, err := tsField.AsInt()
	require.NoError(t, err)

	assert.Equal(t, rootCID.String(), rootLink.String())
	assert.Equal(t, prevCID.String(), prevLink.String())
	assert.Equal(t, ts.Unix(), tsValue)

	// Тест без предыдущего коммита (первый коммит)
	node2, err := buildCommitNode(rootCID, cid.Undef, ts)
	require.NoError(t, err)

	prevField2, err := node2.LookupByString("prev")
	require.NoError(t, err)
	assert.True(t, prevField2.IsNull())
}

func TestParseCommit(t *testing.T) {
	rootCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	prevCID := cid.MustParse("bafybeie5gq4jxvzmsym6hjlwxej4amtoj7svlnqnghzymq4sfjfpfsxqge")
	ts := time.Unix(1640995200, 0)

	// Создаем коммит с предыдущим коммитом
	node, err := buildCommitNode(rootCID, prevCID, ts)
	require.NoError(t, err)

	// Парсим коммит
	parsedRoot, parsedPrev, err := parseCommit(node)
	require.NoError(t, err)

	assert.Equal(t, rootCID, parsedRoot)
	assert.Equal(t, prevCID, parsedPrev)

	// Создаем коммит без предыдущего коммита
	node2, err := buildCommitNode(rootCID, cid.Undef, ts)
	require.NoError(t, err)

	// Парсим коммит
	parsedRoot2, parsedPrev2, err := parseCommit(node2)
	require.NoError(t, err)

	assert.Equal(t, rootCID, parsedRoot2)
	assert.Equal(t, cid.Undef, parsedPrev2)
}

func TestRepository_MultipleCollections(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Добавляем записи в разные коллекции
	collections := []string{"posts", "users", "comments"}

	// Создаем коллекции
	for _, collection := range collections {
		_, err := repo.CreateCollection(ctx, collection)
		require.NoError(t, err)
	}

	for _, collection := range collections {
		for i := 1; i <= 2; i++ {
			testData := map[string]interface{}{
				"id":   fmt.Sprintf("%s_%d", collection, i),
				"data": fmt.Sprintf("Test data for %s %d", collection, i),
			}
			node, err := createTestNode(testData)
			require.NoError(t, err)

			_, err = repo.PutRecord(ctx, collection, fmt.Sprintf("item%d", i), node)
			require.NoError(t, err)
		}
	}

	// Проверяем каждую коллекцию
	for _, collection := range collections {
		records, err := repo.ListCollection(ctx, collection)
		require.NoError(t, err)
		assert.Len(t, records, 2, "Collection %s should have 2 records", collection)

		// Проверяем конкретные записи
		for i := 1; i <= 2; i++ {
			_, found, err := repo.GetRecordCID(ctx, collection, fmt.Sprintf("item%d", i))
			require.NoError(t, err)
			assert.True(t, found, "Record item%d should exist in collection %s", i, collection)
		}
	}
}

func TestRepository_CommitChain(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	var commits []cid.Cid

	// Создаем коллекцию
	_, err := repo.CreateCollection(ctx, "counter")
	require.NoError(t, err)

	// Создаем цепочку коммитов
	for i := 1; i <= 3; i++ {
		// Добавляем запись
		testData := map[string]interface{}{
			"counter": i,
		}
		node, err := createTestNode(testData)
		require.NoError(t, err)

		_, err = repo.PutRecord(ctx, "counter", fmt.Sprintf("count%d", i), node)
		require.NoError(t, err)

		// Создаем коммит
		commitCID, err := repo.Commit(ctx)
		require.NoError(t, err)
		commits = append(commits, commitCID)

		// Проверяем состояние
		assert.Equal(t, commitCID, repo.head)
		if i > 1 {
			assert.Equal(t, commits[i-2], repo.prev)
		} else {
			assert.Equal(t, cid.Undef, repo.prev)
		}
	}

	assert.Len(t, commits, 3)

	// Все коммиты должны быть разными
	for i := 0; i < len(commits); i++ {
		for j := i + 1; j < len(commits); j++ {
			assert.NotEqual(t, commits[i], commits[j])
		}
	}
}

func TestRepository_ManyRecords(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию
	_, err := repo.CreateCollection(ctx, "test")
	require.NoError(t, err)

	// Тест последовательного добавления записей для избежания race conditions
	const numRecords = 50

	// Добавляем записи последовательно
	for i := 0; i < numRecords; i++ {
		testData := map[string]interface{}{
			"index": i,
			"data":  fmt.Sprintf("Record %d", i),
		}
		node, err := createTestNode(testData)
		require.NoError(t, err)

		_, err = repo.PutRecord(ctx, "test", fmt.Sprintf("record%d", i), node)
		require.NoError(t, err)
	}

	// Проверяем, что все записи добавлены
	records, err := repo.ListCollection(ctx, "test")
	require.NoError(t, err)
	assert.Len(t, records, numRecords)

	// Проверяем, что все записи доступны по ключам
	for i := 0; i < numRecords; i++ {
		_, found, err := repo.GetRecordCID(ctx, "test", fmt.Sprintf("record%d", i))
		require.NoError(t, err)
		assert.True(t, found, "Record %d should be found", i)
	}
}

func TestRepository_ErrorCases(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Тест загрузки несуществующего коммита
	nonexistentCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	err := repo.LoadHead(ctx, nonexistentCID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "load commit node")
}

// Тесты для методов коллекций

func TestRepository_CreateCollection(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Создание новой коллекции
	rootCID, err := repo.CreateCollection(ctx, "users")
	require.NoError(t, err)
	assert.True(t, rootCID.Defined())

	// Попытка создать коллекцию с тем же именем
	_, err = repo.CreateCollection(ctx, "users")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "collection already exists")
}

func TestRepository_DeleteCollection(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию
	_, err := repo.CreateCollection(ctx, "temp")
	require.NoError(t, err)

	// Добавляем запись в коллекцию
	testData := map[string]interface{}{
		"test": "data",
	}
	node, err := createTestNode(testData)
	require.NoError(t, err)

	_, err = repo.PutRecord(ctx, "temp", "record1", node)
	require.NoError(t, err)

	// Проверяем, что коллекция существует
	has := repo.HasCollection("temp")
	assert.True(t, has)

	// Удаляем коллекцию
	rootCID, err := repo.DeleteCollection(ctx, "temp")
	require.NoError(t, err)
	assert.True(t, rootCID.Defined())

	// Проверяем, что коллекция удалена
	has = repo.HasCollection("temp")
	assert.False(t, has)

	// Попытка удалить несуществующую коллекцию
	_, err = repo.DeleteCollection(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "collection not found")
}

func TestRepository_HasCollection(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Проверка несуществующей коллекции
	has := repo.HasCollection("posts")
	assert.False(t, has)

	// Создаем коллекцию
	_, err := repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Проверяем, что коллекция существует
	has = repo.HasCollection("posts")
	assert.True(t, has)
}

func TestRepository_ListCollections(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Пустой список коллекций
	collections := repo.ListCollections()
	assert.Empty(t, collections)

	// Создаем несколько коллекций
	collectionNames := []string{"posts", "users", "comments"}
	for _, name := range collectionNames {
		_, err := repo.CreateCollection(ctx, name)
		require.NoError(t, err)
	}

	// Получаем список коллекций
	collections = repo.ListCollections()
	assert.Len(t, collections, 3)

	// Проверяем, что все коллекции в списке
	collectionSet := make(map[string]bool)
	for _, name := range collections {
		collectionSet[name] = true
	}

	for _, expectedName := range collectionNames {
		assert.True(t, collectionSet[expectedName], "Collection %s should be in the list", expectedName)
	}
}

func TestRepository_CollectionRoot(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Проверка корня несуществующей коллекции
	_, found := repo.CollectionRoot("posts")
	assert.False(t, found)

	// Создаем коллекцию
	_, err := repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Проверяем корень пустой коллекции
	rootCID, found := repo.CollectionRoot("posts")
	assert.True(t, found)
	assert.False(t, rootCID.Defined()) // пустая коллекция имеет cid.Undef корень

	// Добавляем запись
	testData := map[string]interface{}{
		"title": "Test Post",
	}
	node, err := createTestNode(testData)
	require.NoError(t, err)

	_, err = repo.PutRecord(ctx, "posts", "post1", node)
	require.NoError(t, err)

	// Проверяем корень коллекции с записями
	rootCID, found = repo.CollectionRoot("posts")
	assert.True(t, found)
	assert.True(t, rootCID.Defined()) // коллекция с записями имеет определенный корень
}
