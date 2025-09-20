package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/ipfs/go-cid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Импортируем кодеки для регистрации
	_ "github.com/ipld/go-ipld-prime/codec/dagcbor"
	_ "github.com/ipld/go-ipld-prime/codec/dagjson"

	"ues/blockstore"
	"ues/datastore"
)

// setupTestIndex создает новый индекс для тестирования
func setupTestIndex(t *testing.T) (*Index, blockstore.Blockstore, func()) {
	// Создаем временную директорию для тестов
	tempDir := t.TempDir()

	// Создаем datastore с временной директорией
	ds, err := datastore.NewDatastorage(tempDir, nil)
	require.NoError(t, err)

	// Создаем blockstore поверх datastore
	bs := blockstore.NewBlockstore(ds)

	// Создаем индекс
	index := NewIndex(bs)

	// Функция очистки
	cleanup := func() {
		bs.Close()
		ds.Close()
	}

	return index, bs, cleanup
}

func TestNewIndex(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	assert.NotNil(t, index)
	assert.NotNil(t, index.bs)
	assert.NotNil(t, index.roots)
	assert.Equal(t, cid.Undef, index.root)
	assert.Empty(t, index.roots)
}

func TestIndex_CreateCollection(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Создание новой коллекции
	rootCID, err := index.CreateCollection(ctx, "users")
	require.NoError(t, err)
	assert.True(t, rootCID.Defined())

	// Проверяем, что индекс был материализован
	assert.Equal(t, rootCID, index.Root())

	// Попытка создать коллекцию с тем же именем
	_, err = index.CreateCollection(ctx, "users")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "collection already exists")
}

func TestIndex_DeleteCollection(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию
	_, err := index.CreateCollection(ctx, "temp")
	require.NoError(t, err)

	// Проверяем, что коллекция существует
	_, exists := index.collectionRoot("temp")
	assert.True(t, exists)

	// Удаляем коллекцию
	rootCID, err := index.DeleteCollection(ctx, "temp")
	require.NoError(t, err)
	assert.True(t, rootCID.Defined())

	// Проверяем, что коллекция удалена
	_, exists = index.collectionRoot("temp")
	assert.False(t, exists)

	// Попытка удалить несуществующую коллекцию
	_, err = index.DeleteCollection(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "collection not found")
}

func TestIndex_Put(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию
	_, err := index.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Создаем тестовый CID
	testCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	// Добавляем запись
	newRoot, err := index.Put(ctx, "posts", "post1", testCID)
	require.NoError(t, err)
	assert.True(t, newRoot.Defined())

	// Проверяем, что корень коллекции обновился
	collRoot, exists := index.collectionRoot("posts")
	assert.True(t, exists)
	assert.True(t, collRoot.Defined())
	assert.NotEqual(t, cid.Undef, collRoot)
}

func TestIndex_Get(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Попытка получить из несуществующей коллекции
	_, exists, err := index.Get(ctx, "posts", "key1")
	require.Error(t, err) // Должна быть ошибка "collection not found"
	assert.False(t, exists)

	// Создаем коллекцию
	_, err = index.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Попытка получить несуществующую запись
	_, exists, err = index.Get(ctx, "posts", "key1")
	require.NoError(t, err)
	assert.False(t, exists)

	// Добавляем запись
	testCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	_, err = index.Put(ctx, "posts", "key1", testCID)
	require.NoError(t, err)

	// Получаем запись
	retrievedCID, exists, err := index.Get(ctx, "posts", "key1")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, testCID, retrievedCID)
}

func TestIndex_Delete(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Попытка удалить из несуществующей коллекции
	_, removed, err := index.Delete(ctx, "posts", "post1")
	require.Error(t, err) // Должна быть ошибка "collection not found"
	assert.False(t, removed)

	// Создаем коллекцию и добавляем запись
	_, err = index.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	testCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	_, err = index.Put(ctx, "posts", "post1", testCID)
	require.NoError(t, err)

	// Проверяем, что запись существует
	_, found, err := index.Get(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, found)

	// Удаляем запись
	newRoot, removed, err := index.Delete(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, removed)
	assert.True(t, newRoot.Defined())

	// Проверяем, что запись удалена
	_, found, err = index.Get(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.False(t, found)

	// Попытка удалить несуществующую запись
	_, removed, err = index.Delete(ctx, "posts", "nonexistent")
	require.NoError(t, err)
	assert.False(t, removed)
}

func TestIndex_ListCollection(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Список из несуществующей коллекции должен возвращать ошибку
	_, err := index.ListCollection(ctx, "posts")
	require.Error(t, err) // Должна быть ошибка "collection not found"

	// Создаем коллекцию
	_, err = index.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Список из пустой коллекции
	records, err := index.ListCollection(ctx, "posts")
	require.NoError(t, err)
	assert.Empty(t, records)

	// Добавляем несколько записей
	testCIDs := []cid.Cid{
		cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"),
		cid.MustParse("bafybeie5gq4jxvzmsym6hjlwxej4amtoj7svlnqnghzymq4sfjfpfsxqge"),
		cid.MustParse("bafybeih4yt2ehufj6ykmphlhfebgjhgwz27yb4hywfegvlrj6fzzqpnfbm"),
	}

	for i, testCID := range testCIDs {
		_, err = index.Put(ctx, "posts", fmt.Sprintf("post%d", i+1), testCID)
		require.NoError(t, err)
	}

	// Получаем список записей
	records, err = index.ListCollection(ctx, "posts")
	require.NoError(t, err)
	assert.Len(t, records, 3)

	// Проверяем, что все записи имеют правильную структуру
	for _, entry := range records {
		assert.NotEmpty(t, entry.Key)
		assert.True(t, entry.Value.Defined())
	}
}

func TestIndex_Load(t *testing.T) {
	index1, bs, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем коллекцию и добавляем записи в первый индекс
	_, err := index1.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	testCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	_, err = index1.Put(ctx, "posts", "post1", testCID)
	require.NoError(t, err)

	// Получаем корень первого индекса
	rootCID := index1.Root()
	assert.True(t, rootCID.Defined())

	// Создаем второй индекс и загружаем состояние
	index2 := NewIndex(bs)
	err = index2.Load(ctx, rootCID)
	require.NoError(t, err)

	// Проверяем, что состояние восстановлено
	assert.Equal(t, rootCID, index2.Root())

	// Проверяем, что запись доступна
	retrievedCID, found, err := index2.Get(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, testCID, retrievedCID)
}

func TestIndex_CollectionRoot(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Проверка корня несуществующей коллекции
	_, found := index.CollectionRoot("posts")
	assert.False(t, found)

	// Создаем коллекцию
	_, err := index.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Проверяем корень пустой коллекции
	collRoot, found := index.CollectionRoot("posts")
	assert.True(t, found)
	assert.False(t, collRoot.Defined()) // пустая коллекция имеет cid.Undef корень

	// Добавляем запись
	testCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	_, err = index.Put(ctx, "posts", "post1", testCID)
	require.NoError(t, err)

	// Проверяем корень коллекции с записями
	collRoot, found = index.CollectionRoot("posts")
	assert.True(t, found)
	assert.True(t, collRoot.Defined()) // коллекция с записями имеет определенный корень
}

func TestIndex_MultipleCollections(t *testing.T) {
	index, _, cleanup := setupTestIndex(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем несколько коллекций
	collections := []string{"posts", "users", "comments"}
	for _, collection := range collections {
		_, err := index.CreateCollection(ctx, collection)
		require.NoError(t, err)
	}

	// Добавляем записи в каждую коллекцию
	testCID := cid.MustParse("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	for i, collection := range collections {
		_, err := index.Put(ctx, collection, fmt.Sprintf("item%d", i+1), testCID)
		require.NoError(t, err)
	}

	// Проверяем каждую коллекцию
	for i, collection := range collections {
		// Проверяем существование коллекции
		_, found := index.CollectionRoot(collection)
		assert.True(t, found, "Collection %s should exist", collection)

		// Проверяем запись в коллекции
		retrievedCID, found, err := index.Get(ctx, collection, fmt.Sprintf("item%d", i+1))
		require.NoError(t, err)
		assert.True(t, found, "Record should exist in collection %s", collection)
		assert.Equal(t, testCID, retrievedCID)
	}
}
