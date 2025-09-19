package mst

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ues/blockstore"

	"github.com/ipfs/boxo/files"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	carv2 "github.com/ipld/go-car/v2"
	"github.com/ipld/go-ipld-prime/datamodel"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/ipld/go-ipld-prime/traversal"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBlockstore реализует полный интерфейс blockstore.Blockstore для тестирования
type mockBlockstore struct {
	// Основное хранилище - блоки как datamodel.Node для совместимости с MST
	blocks map[string]datamodel.Node
	// Дополнительное хранилище для сырых блоков
	rawBlocks map[string]blocks.Block
	mutex     sync.RWMutex

	// Настройки для тестирования ошибок
	shouldFailGet    bool
	shouldFailPut    bool
	shouldFailDelete bool
	shouldFailHas    bool

	// Настройка HashOnRead
	hashOnRead bool
}

func newMockBlockstore() *mockBlockstore {
	return &mockBlockstore{
		blocks:    make(map[string]datamodel.Node),
		rawBlocks: make(map[string]blocks.Block),
	}
}

// Реализация интерфейса blockstore.Blockstore

// PutNode сохраняет любой ipld-prime узел
func (m *mockBlockstore) PutNode(ctx context.Context, node datamodel.Node) (cid.Cid, error) {
	if m.shouldFailPut {
		return cid.Undef, fmt.Errorf("mock error: put node failed")
	}

	// Создаём детерминированный CID на основе содержимого
	hash := fmt.Sprintf("test-node-hash-%p-%v", node, time.Now().UnixNano())
	h, _ := multihash.Sum([]byte(hash), multihash.SHA2_256, -1)
	c := cid.NewCidV1(cid.DagCBOR, h)

	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.blocks[c.String()] = node
	return c, nil
}

// GetNode загружает узел как datamodel.Node
func (m *mockBlockstore) GetNode(ctx context.Context, c cid.Cid) (datamodel.Node, error) {
	if m.shouldFailGet {
		return nil, fmt.Errorf("mock error: get node failed")
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	node, exists := m.blocks[c.String()]
	if !exists {
		return nil, fmt.Errorf("block not found: %s", c.String())
	}

	return node, nil
}

// Реализация bstor.Blockstore

// Put сохраняет сырой блок
func (m *mockBlockstore) Put(ctx context.Context, block blocks.Block) error {
	if m.shouldFailPut {
		return fmt.Errorf("mock error: put block failed")
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.rawBlocks[block.Cid().String()] = block
	return nil
}

// PutMany сохраняет несколько сырых блоков
func (m *mockBlockstore) PutMany(ctx context.Context, blks []blocks.Block) error {
	if m.shouldFailPut {
		return fmt.Errorf("mock error: put many blocks failed")
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, block := range blks {
		m.rawBlocks[block.Cid().String()] = block
	}
	return nil
}

// Get получает сырой блок
func (m *mockBlockstore) Get(ctx context.Context, c cid.Cid) (blocks.Block, error) {
	if m.shouldFailGet {
		return nil, fmt.Errorf("mock error: get block failed")
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	block, exists := m.rawBlocks[c.String()]
	if !exists {
		return nil, fmt.Errorf("block not found: %s", c.String())
	}

	return block, nil
}

// GetSize возвращает размер блока
func (m *mockBlockstore) GetSize(ctx context.Context, c cid.Cid) (int, error) {
	if m.shouldFailGet {
		return 0, fmt.Errorf("mock error: get size failed")
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if block, exists := m.rawBlocks[c.String()]; exists {
		return len(block.RawData()), nil
	}

	return 0, fmt.Errorf("block not found: %s", c.String())
}

// Has проверяет наличие блока
func (m *mockBlockstore) Has(ctx context.Context, c cid.Cid) (bool, error) {
	if m.shouldFailHas {
		return false, fmt.Errorf("mock error: has check failed")
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	_, existsRaw := m.rawBlocks[c.String()]
	_, existsNode := m.blocks[c.String()]

	return existsRaw || existsNode, nil
}

// HashOnRead устанавливает режим хеширования при чтении
func (m *mockBlockstore) HashOnRead(enabled bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.hashOnRead = enabled
}

// DeleteBlock удаляет блок
func (m *mockBlockstore) DeleteBlock(ctx context.Context, c cid.Cid) error {
	if m.shouldFailDelete {
		return fmt.Errorf("mock error: delete block failed")
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	key := c.String()
	delete(m.rawBlocks, key)
	delete(m.blocks, key)

	return nil
}

// AllKeysChan возвращает канал всех ключей в блокстере
func (m *mockBlockstore) AllKeysChan(ctx context.Context) (<-chan cid.Cid, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	ch := make(chan cid.Cid, len(m.rawBlocks)+len(m.blocks))

	go func() {
		defer close(ch)

		sent := make(map[string]bool)

		// Отправляем CID сырых блоков
		for cidStr, block := range m.rawBlocks {
			if !sent[cidStr] {
				select {
				case ch <- block.Cid():
					sent[cidStr] = true
				case <-ctx.Done():
					return
				}
			}
		}

		// Отправляем CID узлов (парсим CID из строки)
		for cidStr := range m.blocks {
			if !sent[cidStr] {
				if c, err := cid.Decode(cidStr); err == nil {
					select {
					case ch <- c:
						sent[cidStr] = true
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return ch, nil
}

// Реализация bstor.Viewer

// View выполняет функцию с сырыми данными блока
func (m *mockBlockstore) View(ctx context.Context, c cid.Cid, callback func([]byte) error) error {
	if m.shouldFailGet {
		return fmt.Errorf("mock error: view failed")
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if block, exists := m.rawBlocks[c.String()]; exists {
		return callback(block.RawData())
	}

	return fmt.Errorf("block not found: %s", c.String())
}

// Реализация io.Closer

// Close закрывает блокстер
func (m *mockBlockstore) Close() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Очищаем все данные
	m.blocks = make(map[string]datamodel.Node)
	m.rawBlocks = make(map[string]blocks.Block)

	return nil
}

// Дополнительные методы интерфейса blockstore.Blockstore (заглушки для тестирования)

// AddFile добавляет файл (заглушка)
func (m *mockBlockstore) AddFile(ctx context.Context, data io.Reader, useRabin bool) (cid.Cid, error) {
	// Простая заглушка - создаём фиктивный CID
	content, err := io.ReadAll(data)
	if err != nil {
		return cid.Undef, err
	}

	hash := fmt.Sprintf("test-file-hash-%s", string(content))
	h, _ := multihash.Sum([]byte(hash), multihash.SHA2_256, -1)
	c := cid.NewCidV1(cid.Raw, h)

	// Сохраняем как сырой блок
	block := blocks.NewBlock(content)
	return c, m.Put(ctx, block)
}

// GetFile получает файл (заглушка)
func (m *mockBlockstore) GetFile(ctx context.Context, c cid.Cid) (files.Node, error) {
	return nil, fmt.Errorf("mock: GetFile not implemented")
}

// GetReader возвращает Reader для файла (заглушка)
func (m *mockBlockstore) GetReader(ctx context.Context, c cid.Cid) (io.ReadSeekCloser, error) {
	block, err := m.Get(ctx, c)
	if err != nil {
		return nil, err
	}

	return &mockReadSeekCloser{data: block.RawData()}, nil
}

// Walk обходит подграф (заглушка)
func (m *mockBlockstore) Walk(ctx context.Context, root cid.Cid, visit func(p traversal.Progress, n datamodel.Node) error) error {
	// Простая заглушка - пытаемся загрузить только корневой узел
	node, err := m.GetNode(ctx, root)
	if err != nil {
		return err
	}

	// Создаём фиктивный Progress для вызова visit
	progress := traversal.Progress{}
	return visit(progress, node)
}

// GetSubgraph обходит подграф и возвращает CID (заглушка)
func (m *mockBlockstore) GetSubgraph(ctx context.Context, root cid.Cid, selectorNode datamodel.Node) ([]cid.Cid, error) {
	// Простая заглушка - возвращаем только корневой CID
	_, err := m.GetNode(ctx, root)
	if err != nil {
		return nil, err
	}

	return []cid.Cid{root}, nil
}

// Prefetch прогревает кэш (заглушка)
func (m *mockBlockstore) Prefetch(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, workers int) error {
	// Простая заглушка - проверяем наличие корневого узла
	_, err := m.GetNode(ctx, root)
	return err
}

// ExportCARV2 экспортирует в CAR формат (заглушка)
func (m *mockBlockstore) ExportCARV2(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, w io.Writer, opts ...carv2.WriteOption) error {
	return fmt.Errorf("mock: ExportCARV2 not implemented")
}

// ImportCARV2 импортирует из CAR формата (заглушка)
func (m *mockBlockstore) ImportCARV2(ctx context.Context, r io.Reader, opts ...carv2.ReadOption) ([]cid.Cid, error) {
	return nil, fmt.Errorf("mock: ImportCARV2 not implemented")
}

// Вспомогательный тип для GetReader
type mockReadSeekCloser struct {
	data   []byte
	offset int64
}

func (r *mockReadSeekCloser) Read(p []byte) (int, error) {
	if r.offset >= int64(len(r.data)) {
		return 0, io.EOF
	}

	n := copy(p, r.data[r.offset:])
	r.offset += int64(n)
	return n, nil
}

func (r *mockReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.offset = offset
	case io.SeekCurrent:
		r.offset += offset
	case io.SeekEnd:
		r.offset = int64(len(r.data)) + offset
	default:
		return 0, fmt.Errorf("invalid whence")
	}

	if r.offset < 0 {
		r.offset = 0
	}
	if r.offset > int64(len(r.data)) {
		r.offset = int64(len(r.data))
	}

	return r.offset, nil
}

func (r *mockReadSeekCloser) Close() error {
	return nil
}

// Проверяем, что mockBlockstore реализует интерфейс blockstore.Blockstore
var _ blockstore.Blockstore = (*mockBlockstore)(nil)

// Тестовые утилиты

func createTestCID(data string) cid.Cid {
	h, _ := multihash.Sum([]byte(data), multihash.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, h)
}

func setupTestTree() (*Tree, blockstore.Blockstore) {
	bs := newMockBlockstore()
	tree := NewTree(bs)
	return tree, bs
}

func setupTestTreeWithMock() (*Tree, *mockBlockstore) {
	bs := newMockBlockstore()
	tree := NewTree(bs)
	return tree, bs
}

// Основные тесты конструктора и базовой функциональности

func TestNewTree(t *testing.T) {
	bs := newMockBlockstore()
	tree := NewTree(bs)

	require.NotNil(t, tree)
	assert.Equal(t, bs, tree.bs)
	assert.Equal(t, cid.Undef, tree.Root())
}

func TestTree_Root_EmptyTree(t *testing.T) {
	tree, _ := setupTestTree()

	root := tree.Root()
	assert.Equal(t, cid.Undef, root)
	assert.False(t, root.Defined())
}

// Тесты загрузки дерева

func TestTree_Load_EmptyRoot(t *testing.T) {
	tree, _ := setupTestTree()

	err := tree.Load(context.Background(), cid.Undef)
	require.NoError(t, err)
	assert.Equal(t, cid.Undef, tree.Root())
}

func TestTree_Load_ValidRoot(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Сначала вставляем элемент
	testCID := createTestCID("test-value")
	rootCID, err := tree.Put(ctx, "test-key", testCID)
	require.NoError(t, err)

	// Создаём новое дерево и загружаем сохранённый корень
	newTree, _ := setupTestTree()
	newTree.bs = tree.bs // Используем то же хранилище

	err = newTree.Load(ctx, rootCID)
	require.NoError(t, err)
	assert.Equal(t, rootCID, newTree.Root())
}

func TestTree_Load_InvalidRoot(t *testing.T) {
	tree, _ := setupTestTree()

	// Пытаемся загрузить несуществующий CID
	fakeCID := createTestCID("nonexistent")
	err := tree.Load(context.Background(), fakeCID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "block not found")
}

// Тесты вставки

func TestTree_Put_EmptyTree(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	testCID := createTestCID("test-value")
	rootCID, err := tree.Put(ctx, "test-key", testCID)

	require.NoError(t, err)
	assert.True(t, rootCID.Defined())
	assert.Equal(t, rootCID, tree.Root())
}

func TestTree_Put_MultipleKeys(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	keys := []string{"apple", "banana", "cherry", "date"}
	values := make([]cid.Cid, len(keys))

	// Вставляем несколько ключей
	for i, key := range keys {
		values[i] = createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, values[i])
		require.NoError(t, err)
	}

	// Проверяем, что все ключи можно найти
	for i, key := range keys {
		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, values[i], value)
	}
}

func TestTree_Put_UpdateExistingKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	key := "test-key"
	originalValue := createTestCID("original-value")
	updatedValue := createTestCID("updated-value")

	// Вставляем оригинальное значение
	_, err := tree.Put(ctx, key, originalValue)
	require.NoError(t, err)

	// Обновляем значение
	_, err = tree.Put(ctx, key, updatedValue)
	require.NoError(t, err)

	// Проверяем, что значение обновлено
	value, found, err := tree.Get(ctx, key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, updatedValue, value)
}

func TestTree_Put_InvalidInputs(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	testCID := createTestCID("test-value")

	// Тест с пустым ключом
	_, err := tree.Put(ctx, "", testCID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty key")

	// Тест с неопределённым CID
	_, err = tree.Put(ctx, "test-key", cid.Undef)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "undefined value CID")
}

// Тесты удаления

func TestTree_Delete_ExistingKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем несколько ключей
	keys := []string{"apple", "banana", "cherry"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Удаляем средний ключ
	_, removed, err := tree.Delete(ctx, "banana")
	require.NoError(t, err)
	assert.True(t, removed)

	// Проверяем, что ключ удалён
	_, found, err := tree.Get(ctx, "banana")
	require.NoError(t, err)
	assert.False(t, found)

	// Проверяем, что остальные ключи остались
	for _, key := range []string{"apple", "cherry"} {
		_, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found)
	}
}

func TestTree_Delete_NonexistentKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Удаляем ключ из пустого дерева
	_, removed, err := tree.Delete(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, removed)
}

func TestTree_Delete_LastKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем один ключ
	testCID := createTestCID("test-value")
	_, err := tree.Put(ctx, "only-key", testCID)
	require.NoError(t, err)

	// Удаляем единственный ключ
	_, removed, err := tree.Delete(ctx, "only-key")
	require.NoError(t, err)
	assert.True(t, removed)

	// Дерево должно стать пустым
	assert.Equal(t, cid.Undef, tree.Root())
}

func TestTree_Delete_EmptyKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	_, _, err := tree.Delete(ctx, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty key")
}

// Тесты поиска

func TestTree_Get_ExistingKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	key := "test-key"
	expectedValue := createTestCID("test-value")

	_, err := tree.Put(ctx, key, expectedValue)
	require.NoError(t, err)

	value, found, err := tree.Get(ctx, key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, expectedValue, value)
}

func TestTree_Get_NonexistentKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	value, found, err := tree.Get(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, cid.Undef, value)
}

func TestTree_Get_EmptyTree(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	value, found, err := tree.Get(ctx, "any-key")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, cid.Undef, value)
}

// Тесты диапазонного поиска

func TestTree_Range_FullRange(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем ключи в произвольном порядке
	testData := map[string]string{
		"delta":   "value-delta",
		"alpha":   "value-alpha",
		"gamma":   "value-gamma",
		"beta":    "value-beta",
		"epsilon": "value-epsilon",
	}

	for key, valueStr := range testData {
		value := createTestCID(valueStr)
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Получаем весь диапазон (пустые границы)
	entries, err := tree.Range(ctx, "", "")
	require.NoError(t, err)

	// Проверяем, что все ключи присутствуют и отсортированы
	assert.Len(t, entries, len(testData))

	var keys []string
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}

	expectedKeys := []string{"alpha", "beta", "delta", "epsilon", "gamma"}
	sort.Strings(expectedKeys)
	assert.Equal(t, expectedKeys, keys)
}

func TestTree_Range_LimitedRange(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем ключи a, b, c, d, e
	keys := []string{"a", "b", "c", "d", "e"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Ищем диапазон [b, d]
	entries, err := tree.Range(ctx, "b", "d")
	require.NoError(t, err)

	expectedKeys := []string{"b", "c", "d"}
	assert.Len(t, entries, len(expectedKeys))

	for i, entry := range entries {
		assert.Equal(t, expectedKeys[i], entry.Key)
	}
}

func TestTree_Range_EmptyTree(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	entries, err := tree.Range(ctx, "", "")
	require.NoError(t, err)
	assert.Len(t, entries, 0)
}

func TestTree_Range_NoMatches(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем ключи a, c, e
	keys := []string{"a", "c", "e"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Ищем диапазон [f, z] - должен быть пустым
	entries, err := tree.Range(ctx, "f", "z")
	require.NoError(t, err)
	assert.Len(t, entries, 0)
}

// Тесты балансировки дерева

func TestTree_BalancingLeftHeavy(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем ключи в убывающем порядке для создания левого дисбаланса
	keys := []string{"e", "d", "c", "b", "a"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Проверяем, что все ключи доступны (дерево сбалансировано)
	for i, key := range keys {
		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found)
		expectedValue := createTestCID(fmt.Sprintf("value-%d", i))
		assert.Equal(t, expectedValue, value)
	}
}

func TestTree_BalancingRightHeavy(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем ключи в возрастающем порядке для создания правого дисбаланса
	keys := []string{"a", "b", "c", "d", "e"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Проверяем, что все ключи доступны
	for i, key := range keys {
		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found)
		expectedValue := createTestCID(fmt.Sprintf("value-%d", i))
		assert.Equal(t, expectedValue, value)
	}
}

// Тесты операций с большим количеством данных

func TestTree_LargeDataset(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	const numItems = 1000

	// Вставляем большое количество элементов
	for i := 0; i < numItems; i++ {
		key := fmt.Sprintf("key-%04d", i)
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Проверяем несколько случайных элементов
	testIndices := []int{0, 100, 500, 750, 999}
	for _, i := range testIndices {
		key := fmt.Sprintf("key-%04d", i)
		expectedValue := createTestCID(fmt.Sprintf("value-%d", i))

		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, expectedValue, value)
	}

	// Проверяем диапазонный поиск
	entries, err := tree.Range(ctx, "key-0100", "key-0110")
	require.NoError(t, err)
	assert.Len(t, entries, 11) // key-0100 до key-0110 включительно
}

// Тесты ошибок хранилища

func TestTree_StorageErrors(t *testing.T) {
	tree, bs := setupTestTreeWithMock()
	ctx := context.Background()

	// Тест ошибки при сохранении
	bs.shouldFailPut = true
	_, err := tree.Put(ctx, "test-key", createTestCID("test-value"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: put node failed")

	// Восстанавливаем нормальную работу и вставляем элемент
	bs.shouldFailPut = false
	_, err = tree.Put(ctx, "test-key", createTestCID("test-value"))
	require.NoError(t, err)

	// Тест ошибки при загрузке
	bs.shouldFailGet = true
	_, _, err = tree.Get(ctx, "test-key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: get node failed")
}

// Тесты многопоточности

func TestTree_ConcurrentOperations(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup

	// Запускаем несколько горутин для вставки
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for i := 0; i < numOperations; i++ {
				key := fmt.Sprintf("g%d-key%d", goroutineID, i)
				value := createTestCID(fmt.Sprintf("g%d-value%d", goroutineID, i))

				_, err := tree.Put(ctx, key, value)
				assert.NoError(t, err)
			}
		}(g)
	}

	wg.Wait()

	// Проверяем, что все элементы были вставлены
	for g := 0; g < numGoroutines; g++ {
		for i := 0; i < numOperations; i++ {
			key := fmt.Sprintf("g%d-key%d", g, i)
			expectedValue := createTestCID(fmt.Sprintf("g%d-value%d", g, i))

			value, found, err := tree.Get(ctx, key)
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, expectedValue, value)
		}
	}
}

func TestTree_ConcurrentReadWrite(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Предварительно вставляем некоторые данные
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("initial-key-%d", i)
		value := createTestCID(fmt.Sprintf("initial-value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup

	// Читатели
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			for i := 0; i < 200; i++ {
				key := fmt.Sprintf("initial-key-%d", i%100)
				_, found, err := tree.Get(ctx, key)
				assert.NoError(t, err)
				assert.True(t, found)
			}
		}(r)
	}

	// Писатели
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()

			for i := 0; i < 50; i++ {
				key := fmt.Sprintf("writer-%d-key-%d", writerID, i)
				value := createTestCID(fmt.Sprintf("writer-%d-value-%d", writerID, i))
				_, err := tree.Put(ctx, key, value)
				assert.NoError(t, err)
			}
		}(w)
	}

	wg.Wait()
}

// Тесты внутренних функций

func TestCloneNode(t *testing.T) {
	original := &node{
		Entry: Entry{
			Key:   "test-key",
			Value: createTestCID("test-value"),
		},
		Left:   createTestCID("left-child"),
		Right:  createTestCID("right-child"),
		Height: 2,
		Hash:   []byte{1, 2, 3, 4},
	}

	cloned := cloneNode(original)

	require.NotNil(t, cloned)
	assert.Equal(t, original.Entry, cloned.Entry)
	assert.Equal(t, original.Left, cloned.Left)
	assert.Equal(t, original.Right, cloned.Right)
	assert.Equal(t, original.Height, cloned.Height)
	assert.Equal(t, original.Hash, cloned.Hash)

	// Проверяем, что это разные объекты
	assert.NotSame(t, original, cloned)
	assert.NotSame(t, original.Hash, cloned.Hash)

	// Модификация клона не должна влиять на оригинал
	cloned.Hash[0] = 99
	assert.NotEqual(t, original.Hash[0], cloned.Hash[0])
}

func TestCloneNode_Nil(t *testing.T) {
	cloned := cloneNode(nil)
	assert.Nil(t, cloned)
}

func TestMax(t *testing.T) {
	assert.Equal(t, 5, max(3, 5))
	assert.Equal(t, 5, max(5, 3))
	assert.Equal(t, 5, max(5, 5))
	assert.Equal(t, 0, max(-1, 0))
	assert.Equal(t, -1, max(-5, -1))
}

// Тесты сериализации

func TestNodeSerialization(t *testing.T) {
	tree, _ := setupTestTree()

	original := &node{
		Entry: Entry{
			Key:   "test-key",
			Value: createTestCID("test-value"),
		},
		Left:   createTestCID("left-child"),
		Right:  createTestCID("right-child"),
		Height: 3,
		Hash:   []byte{1, 2, 3, 4, 5},
	}

	// Сериализуем в datamodel.Node
	dm, err := tree.nodeToNode(original)
	require.NoError(t, err)

	// Десериализуем обратно
	deserialized, err := tree.nodeFromNode(dm)
	require.NoError(t, err)

	// Проверяем, что данные совпадают
	assert.Equal(t, original.Key, deserialized.Key)
	assert.Equal(t, original.Value, deserialized.Value)
	assert.Equal(t, original.Left, deserialized.Left)
	assert.Equal(t, original.Right, deserialized.Right)
	assert.Equal(t, original.Height, deserialized.Height)
	assert.Equal(t, original.Hash, deserialized.Hash)
}

func TestNodeSerialization_MinimalNode(t *testing.T) {
	tree, _ := setupTestTree()

	// Узел только с обязательными полями
	original := &node{
		Entry: Entry{
			Key:   "minimal",
			Value: createTestCID("minimal-value"),
		},
		Left:   cid.Undef,
		Right:  cid.Undef,
		Height: 1,
		Hash:   []byte{0xFF},
	}

	dm, err := tree.nodeToNode(original)
	require.NoError(t, err)

	deserialized, err := tree.nodeFromNode(dm)
	require.NoError(t, err)

	assert.Equal(t, original.Key, deserialized.Key)
	assert.Equal(t, original.Value, deserialized.Value)
	assert.Equal(t, cid.Undef, deserialized.Left)
	assert.Equal(t, cid.Undef, deserialized.Right)
	assert.Equal(t, original.Height, deserialized.Height)
	assert.Equal(t, original.Hash, deserialized.Hash)
}

// Тесты невалидной десериализации

func TestNodeDeserialization_InvalidData(t *testing.T) {
	tree, _ := setupTestTree()

	// Тест с отсутствующим ключом
	builder := basicnode.Prototype.Map.NewBuilder()
	ma, _ := builder.BeginMap(1)
	entry, _ := ma.AssembleEntry("value")
	entry.AssignLink(cidlink.Link{Cid: createTestCID("test")})
	ma.Finish()
	invalidNode := builder.Build()

	_, err := tree.nodeFromNode(invalidNode)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "node missing key")
}

// Тесты BuildSelector

func TestBuildSelector(t *testing.T) {
	selector, err := BuildSelector()
	require.NoError(t, err)
	assert.NotNil(t, selector)
}

// Интеграционные тесты

func TestTree_ComplexScenario(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Сценарий: вставка, обновление, удаление, поиск в различных комбинациях
	testData := []struct {
		key   string
		value string
	}{
		{"user:alice", "profile-alice"},
		{"user:bob", "profile-bob"},
		{"user:charlie", "profile-charlie"},
		{"post:123", "content-123"},
		{"post:456", "content-456"},
		{"comment:789", "text-789"},
	}

	// Фаза 1: Вставка всех данных
	for _, item := range testData {
		value := createTestCID(item.value)
		_, err := tree.Put(ctx, item.key, value)
		require.NoError(t, err, "Failed to insert %s", item.key)
	}

	// Фаза 2: Проверка всех данных
	for _, item := range testData {
		expectedValue := createTestCID(item.value)
		value, found, err := tree.Get(ctx, item.key)
		require.NoError(t, err)
		assert.True(t, found, "Key %s not found", item.key)
		assert.Equal(t, expectedValue, value, "Wrong value for key %s", item.key)
	}

	// Фаза 3: Обновление некоторых значений
	updates := map[string]string{
		"user:alice": "updated-profile-alice",
		"post:123":   "updated-content-123",
	}

	for key, newValue := range updates {
		value := createTestCID(newValue)
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Проверяем обновления
	for key, expectedValueStr := range updates {
		expectedValue := createTestCID(expectedValueStr)
		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, expectedValue, value)
	}

	// Фаза 4: Удаление некоторых ключей
	keysToDelete := []string{"user:charlie", "comment:789"}

	for _, key := range keysToDelete {
		_, removed, err := tree.Delete(ctx, key)
		require.NoError(t, err)
		assert.True(t, removed, "Key %s should have been removed", key)
	}

	// Проверяем, что ключи удалены
	for _, key := range keysToDelete {
		_, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.False(t, found, "Key %s should not exist", key)
	}

	// Фаза 5: Диапазонный поиск
	userEntries, err := tree.Range(ctx, "user:", "user:zzz")
	require.NoError(t, err)

	// Должны остаться только user:alice и user:bob (charlie удалён)
	assert.Len(t, userEntries, 2)

	var userKeys []string
	for _, entry := range userEntries {
		userKeys = append(userKeys, entry.Key)
	}
	sort.Strings(userKeys)
	assert.Equal(t, []string{"user:alice", "user:bob"}, userKeys)
}

// Бенчмарки

func BenchmarkTree_Put(b *testing.B) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%d", i)
		value := createTestCID(fmt.Sprintf("value-%d", i))
		tree.Put(ctx, key, value)
	}
}

func BenchmarkTree_Get(b *testing.B) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Предварительно вставляем данные
	const numItems = 10000
	for i := 0; i < numItems; i++ {
		key := fmt.Sprintf("key-%d", i)
		value := createTestCID(fmt.Sprintf("value-%d", i))
		tree.Put(ctx, key, value)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%d", i%numItems)
		tree.Get(ctx, key)
	}
}

func BenchmarkTree_Range(b *testing.B) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Предварительно вставляем данные
	const numItems = 1000
	for i := 0; i < numItems; i++ {
		key := fmt.Sprintf("key-%04d", i)
		value := createTestCID(fmt.Sprintf("value-%d", i))
		tree.Put(ctx, key, value)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := fmt.Sprintf("key-%04d", i%500)
		end := fmt.Sprintf("key-%04d", (i%500)+100)
		tree.Range(ctx, start, end)
	}
}

// Дополнительные тесты для полного интерфейса blockstore.Blockstore

func TestMockBlockstore_BlockstoreInterface(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Тестируем сырые блоки
	testData := []byte("test block data")
	block := blocks.NewBlock(testData)

	// Put сырого блока
	err := bs.Put(ctx, block)
	require.NoError(t, err)

	// Get сырого блока
	retrievedBlock, err := bs.Get(ctx, block.Cid())
	require.NoError(t, err)
	assert.Equal(t, testData, retrievedBlock.RawData())

	// Has проверка
	exists, err := bs.Has(ctx, block.Cid())
	require.NoError(t, err)
	assert.True(t, exists)

	// GetSize
	size, err := bs.GetSize(ctx, block.Cid())
	require.NoError(t, err)
	assert.Equal(t, len(testData), size)

	// View
	var viewedData []byte
	err = bs.View(ctx, block.Cid(), func(data []byte) error {
		viewedData = make([]byte, len(data))
		copy(viewedData, data)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, testData, viewedData)

	// DeleteBlock
	err = bs.DeleteBlock(ctx, block.Cid())
	require.NoError(t, err)

	// Проверяем, что блок удален
	exists, err = bs.Has(ctx, block.Cid())
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestMockBlockstore_PutMany(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Создаём несколько блоков
	var testBlocks []blocks.Block
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("test data %d", i))
		block := blocks.NewBlock(data)
		testBlocks = append(testBlocks, block)
	}

	// PutMany
	err := bs.PutMany(ctx, testBlocks)
	require.NoError(t, err)

	// Проверяем, что все блоки сохранены
	for _, block := range testBlocks {
		exists, err := bs.Has(ctx, block.Cid())
		require.NoError(t, err)
		assert.True(t, exists)
	}
}

func TestMockBlockstore_AllKeysChan(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Добавляем несколько блоков
	var expectedCIDs []cid.Cid
	for i := 0; i < 3; i++ {
		data := []byte(fmt.Sprintf("test data %d", i))
		block := blocks.NewBlock(data)
		err := bs.Put(ctx, block)
		require.NoError(t, err)
		expectedCIDs = append(expectedCIDs, block.Cid())
	}

	// Получаем все ключи
	ch, err := bs.AllKeysChan(ctx)
	require.NoError(t, err)

	var retrievedCIDs []cid.Cid
	for c := range ch {
		retrievedCIDs = append(retrievedCIDs, c)
	}

	assert.Len(t, retrievedCIDs, len(expectedCIDs))

	// Проверяем, что все ожидаемые CID присутствуют
	expectedSet := make(map[string]bool)
	for _, c := range expectedCIDs {
		expectedSet[c.String()] = true
	}

	for _, c := range retrievedCIDs {
		assert.True(t, expectedSet[c.String()], "Unexpected CID: %s", c.String())
	}
}

func TestMockBlockstore_Close(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Добавляем данные
	testData := []byte("test data")
	block := blocks.NewBlock(testData)
	err := bs.Put(ctx, block)
	require.NoError(t, err)

	// Закрываем
	err = bs.Close()
	require.NoError(t, err)

	// После закрытия данные должны быть очищены
	exists, err := bs.Has(ctx, block.Cid())
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestMockBlockstore_ErrorHandling(t *testing.T) {
	bs := newMockBlockstore()
	ctx := context.Background()

	// Тестируем ошибки Get
	bs.shouldFailGet = true
	_, err := bs.GetNode(ctx, createTestCID("test"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: get node failed")

	_, err = bs.Get(ctx, createTestCID("test"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: get block failed")

	_, err = bs.GetSize(ctx, createTestCID("test"))
	assert.Error(t, err)

	err = bs.View(ctx, createTestCID("test"), func([]byte) error { return nil })
	assert.Error(t, err)

	// Тестируем ошибки Put
	bs.shouldFailGet = false
	bs.shouldFailPut = true

	node := basicnode.NewString("test")
	_, err = bs.PutNode(ctx, node)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: put node failed")

	block := blocks.NewBlock([]byte("test"))
	err = bs.Put(ctx, block)
	assert.Error(t, err)

	err = bs.PutMany(ctx, []blocks.Block{block})
	assert.Error(t, err)

	// Тестируем ошибки Delete
	bs.shouldFailPut = false
	bs.shouldFailDelete = true

	err = bs.DeleteBlock(ctx, createTestCID("test"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: delete block failed")

	// Тестируем ошибки Has
	bs.shouldFailDelete = false
	bs.shouldFailHas = true

	_, err = bs.Has(ctx, createTestCID("test"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: has check failed")
}

func TestMockBlockstore_FileOperations(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// AddFile
	testData := "Hello, World!"
	reader := strings.NewReader(testData)

	fileCID, err := bs.AddFile(ctx, reader, false)
	require.NoError(t, err)
	assert.True(t, fileCID.Defined())

	// GetReader
	readSeeker, err := bs.GetReader(ctx, fileCID)
	require.NoError(t, err)
	defer readSeeker.Close()

	// Читаем данные обратно
	retrievedData, err := io.ReadAll(readSeeker)
	require.NoError(t, err)
	assert.Equal(t, testData, string(retrievedData))

	// Тестируем Seek
	offset, err := readSeeker.Seek(0, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, int64(0), offset)

	offset, err = readSeeker.Seek(7, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, int64(7), offset)

	// Читаем с позиции 7
	remainingData, err := io.ReadAll(readSeeker)
	require.NoError(t, err)
	assert.Equal(t, "World!", string(remainingData))
}

func TestMockBlockstore_AdvancedOperations(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Создаём тестовый узел
	testNode := basicnode.NewString("test node")
	nodeCID, err := bs.PutNode(ctx, testNode)
	require.NoError(t, err)

	// Walk (заглушка)
	err = bs.Walk(ctx, nodeCID, func(p traversal.Progress, n datamodel.Node) error {
		// Проверяем, что получили наш узел
		str, strErr := n.AsString()
		if strErr == nil {
			assert.Equal(t, "test node", str)
		}
		return nil
	})
	require.NoError(t, err)

	// GetSubgraph (заглушка)
	cids, err := bs.GetSubgraph(ctx, nodeCID, basicnode.NewString("selector"))
	require.NoError(t, err)
	assert.Contains(t, cids, nodeCID)

	// Prefetch (заглушка)
	err = bs.Prefetch(ctx, nodeCID, basicnode.NewString("selector"), 1)
	require.NoError(t, err)

	// ExportCARV2 и ImportCARV2 (заглушки)
	err = bs.ExportCARV2(ctx, nodeCID, basicnode.NewString("selector"), io.Discard)
	assert.Error(t, err) // Ожидаем ошибку "not implemented"

	_, err = bs.ImportCARV2(ctx, strings.NewReader("fake car data"))
	assert.Error(t, err) // Ожидаем ошибку "not implemented"

	// GetFile (заглушка)
	_, err = bs.GetFile(ctx, nodeCID)
	assert.Error(t, err) // Ожидаем ошибку "not implemented"
}
