package repository

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepository_CollectionRootHash(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Тест для несуществующей коллекции
	hash, exists, err := repo.CollectionRootHash(ctx, "posts")
	require.Error(t, err)
	assert.False(t, exists)
	assert.Nil(t, hash)

	// Создаем коллекцию
	_, err = repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Получаем хеш корня пустой коллекции
	hash, exists, err = repo.CollectionRootHash(ctx, "posts")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Nil(t, hash) // Пустая коллекция возвращает nil hash

	// Добавляем запись
	testRecord, _ := createTestNode(map[string]interface{}{"content": "test content"})
	recordCID, err := repo.PutRecord(ctx, "posts", "post1", testRecord)
	require.NoError(t, err)

	// Коммитим изменения
	_, err = repo.Commit(ctx)
	require.NoError(t, err)

	// Получаем новый хеш корня
	newHash, exists, err := repo.CollectionRootHash(ctx, "posts")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.NotNil(t, newHash)         // После добавления записи хеш должен быть определен
	assert.NotEqual(t, hash, newHash) // Хеш должен измениться

	// Проверяем, что CID не пустой
	assert.True(t, recordCID.Defined())
}

func TestRepository_GetRecord(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Тест для несуществующей коллекции
	_, found, err := repo.GetRecord(ctx, "posts", "post1")
	require.Error(t, err)
	assert.False(t, found)

	// Создаем коллекцию
	_, err = repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Тест для несуществующей записи
	_, found, err = repo.GetRecord(ctx, "posts", "post1")
	require.NoError(t, err) // Не ошибка, просто не найдено
	assert.False(t, found)

	// Добавляем запись
	testRecord, _ := createTestNode(map[string]interface{}{"content": "test content"})
	_, err = repo.PutRecord(ctx, "posts", "post1", testRecord)
	require.NoError(t, err)

	// Коммитим изменения
	_, err = repo.Commit(ctx)
	require.NoError(t, err)

	// Получаем запись
	retrievedRecord, found, err := repo.GetRecord(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.NotNil(t, retrievedRecord)

	// Проверяем содержимое записи
	assert.Equal(t, testRecord.Kind(), retrievedRecord.Kind())
}

func TestRepository_ListRecords(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Тест для несуществующей коллекции
	_, err := repo.ListRecords(ctx, "posts")
	require.Error(t, err)

	// Создаем коллекцию
	_, err = repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Список из пустой коллекции
	records, err := repo.ListRecords(ctx, "posts")
	require.NoError(t, err)
	assert.Empty(t, records)

	// Добавляем несколько записей
	testContents := []map[string]interface{}{
		{"content": "content1"},
		{"content": "content2"},
		{"content": "content3"},
	}
	for i, content := range testContents {
		testRecord, _ := createTestNode(content)
		_, err = repo.PutRecord(ctx, "posts", string(rune('a'+i)), testRecord)
		require.NoError(t, err)
	}

	// Коммитим изменения
	_, err = repo.Commit(ctx)
	require.NoError(t, err)

	// Получаем список записей
	records, err = repo.ListRecords(ctx, "posts")
	require.NoError(t, err)
	assert.Len(t, records, 3)

	// Проверяем, что все записи имеют правильную структуру
	for _, record := range records {
		assert.NotEmpty(t, record.Key)
		assert.True(t, record.Value.Defined())
	}
}

func TestRepository_InclusionPath(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Тест для несуществующей коллекции
	_, found, err := repo.InclusionPath(ctx, "posts", "post1")
	require.Error(t, err)
	assert.False(t, found)

	// Создаем коллекцию
	_, err = repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Тест для несуществующей записи
	_, found, err = repo.InclusionPath(ctx, "posts", "post1")
	require.NoError(t, err) // Не ошибка, просто не найдено
	assert.False(t, found)

	// Добавляем запись
	testRecord, _ := createTestNode(map[string]interface{}{"content": "test content"})
	_, err = repo.PutRecord(ctx, "posts", "post1", testRecord)
	require.NoError(t, err)

	// Коммитим изменения
	_, err = repo.Commit(ctx)
	require.NoError(t, err)

	// Получаем путь включения
	path, found, err := repo.InclusionPath(ctx, "posts", "post1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.NotEmpty(t, path)

	// Проверяем, что путь содержит CID узлы
	for _, cid := range path {
		assert.True(t, cid.Defined())
	}
}

func TestRepository_ExportCollectionCAR(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()

	// Тест для несуществующей коллекции (с валидным writer)
	var buf bytes.Buffer
	err := repo.ExportCollectionCAR(ctx, "nonexistent", &buf)
	require.Error(t, err)

	// Создаем коллекцию
	_, err = repo.CreateCollection(ctx, "posts")
	require.NoError(t, err)

	// Добавляем записи
	testRecord, _ := createTestNode(map[string]interface{}{"content": "test content"})
	_, err = repo.PutRecord(ctx, "posts", "post1", testRecord)
	require.NoError(t, err)

	// Коммитим изменения
	_, err = repo.Commit(ctx)
	require.NoError(t, err)

	// Экспортируем в CAR формат с валидным writer
	var exportBuf bytes.Buffer
	err = repo.ExportCollectionCAR(ctx, "posts", &exportBuf)
	require.NoError(t, err)
	assert.Greater(t, exportBuf.Len(), 0) // Должен быть записан некоторый контент
}
