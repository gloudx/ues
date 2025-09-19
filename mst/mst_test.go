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

// ==============================================================================
// MOCK BLOCKSTORE - ПОЛНАЯ РЕАЛИЗАЦИЯ ДЛЯ ТЕСТИРОВАНИЯ
// ==============================================================================

// mockBlockstore реализует полный интерфейс blockstore.Blockstore для тестирования.
// Это in-memory имплементация, которая позволяет:
// 1. Хранить как IPLD узлы (datamodel.Node), так и сырые блоки (blocks.Block)
// 2. Симулировать различные типы ошибок хранилища
// 3. Тестировать все аспекты MST без реального IPFS
type mockBlockstore struct {
	// Основное хранилище - блоки как datamodel.Node для совместимости с MST
	blocks map[string]datamodel.Node
	// Дополнительное хранилище для сырых блоков (для совместимости с bstor.Blockstore)
	rawBlocks map[string]blocks.Block
	// Мьютекс для потокобезопасности (имитирует поведение реального blockstore)
	mutex sync.RWMutex

	// Настройки для тестирования различных сценариев ошибок
	shouldFailGet    bool // Симуляция ошибок при чтении
	shouldFailPut    bool // Симуляция ошибок при записи
	shouldFailDelete bool // Симуляция ошибок при удалении
	shouldFailHas    bool // Симуляция ошибок при проверке существования

	// Настройка для имитации режима проверки хешей при чтении
	hashOnRead bool
}

// newMockBlockstore создаёт новый экземпляр mock blockstore.
// Инициализирует пустые мапы для хранения блоков и узлов.
func newMockBlockstore() *mockBlockstore {
	return &mockBlockstore{
		blocks:    make(map[string]datamodel.Node),
		rawBlocks: make(map[string]blocks.Block),
	}
}

// ==============================================================================
// РЕАЛИЗАЦИЯ ИНТЕРФЕЙСА blockstore.Blockstore
// ==============================================================================

// PutNode сохраняет IPLD узел в хранилище.
// Это ключевой метод для MST, так как все узлы дерева сохраняются как datamodel.Node.
// Создаёт уникальный CID на основе адреса узла и временной метки для детерминированности.
func (m *mockBlockstore) PutNode(ctx context.Context, node datamodel.Node) (cid.Cid, error) {
	if m.shouldFailPut {
		return cid.Undef, fmt.Errorf("mock error: put node failed")
	}

	// Создаём псевдо-уникальный хеш для тестирования.
	// В реальном blockstore CID вычисляется криптографически от содержимого
	hash := fmt.Sprintf("test-node-hash-%p-%v", node, time.Now().UnixNano())
	h, _ := multihash.Sum([]byte(hash), multihash.SHA2_256, -1)
	c := cid.NewCidV1(cid.DagCBOR, h) // Используем DagCBOR как MST

	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Сохраняем узел по строковому представлению CID для быстрого поиска
	m.blocks[c.String()] = node
	return c, nil
}

// GetNode загружает IPLD узел из хранилища по CID.
// Это основной метод чтения для MST - все операции поиска используют его.
func (m *mockBlockstore) GetNode(ctx context.Context, c cid.Cid) (datamodel.Node, error) {
	if m.shouldFailGet {
		return nil, fmt.Errorf("mock error: get node failed")
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Ищем узел в хранилище IPLD узлов
	node, exists := m.blocks[c.String()]
	if !exists {
		// Возвращаем ошибку, совместимую с реальным IPFS blockstore
		return nil, fmt.Errorf("block not found: %s", c.String())
	}

	return node, nil
}

// ==============================================================================
// РЕАЛИЗАЦИЯ БАЗОВОГО ИНТЕРФЕЙСА bstor.Blockstore
// ==============================================================================

// Put сохраняет сырой блок данных.
// Необходим для совместимости с базовым интерфейсом blockstore,
// хотя MST напрямую его не использует.
func (m *mockBlockstore) Put(ctx context.Context, block blocks.Block) error {
	if m.shouldFailPut {
		return fmt.Errorf("mock error: put block failed")
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Сохраняем блок по его CID (который уже вычислен при создании блока)
	m.rawBlocks[block.Cid().String()] = block
	return nil
}

// PutMany сохраняет несколько сырых блоков за одну операцию.
// Оптимизация для массовых вставок, уменьшает количество блокировок мьютекса.
func (m *mockBlockstore) PutMany(ctx context.Context, blks []blocks.Block) error {
	if m.shouldFailPut {
		return fmt.Errorf("mock error: put many blocks failed")
	}

	// Одна блокировка для всей пачки блоков - эффективнее
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, block := range blks {
		m.rawBlocks[block.Cid().String()] = block
	}
	return nil
}

// Get получает сырой блок по CID.
// Используется для загрузки данных, которые не являются IPLD узлами.
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

// GetSize возвращает размер блока в байтах.
// Полезно для оптимизации сетевых операций и оценки использования памяти.
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

// Has проверяет существование блока без его загрузки.
// Быстрая операция для проверки кэша или планирования загрузок.
func (m *mockBlockstore) Has(ctx context.Context, c cid.Cid) (bool, error) {
	if m.shouldFailHas {
		return false, fmt.Errorf("mock error: has check failed")
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Проверяем наличие в обоих хранилищах (сырые блоки и IPLD узлы)
	_, existsRaw := m.rawBlocks[c.String()]
	_, existsNode := m.blocks[c.String()]

	return existsRaw || existsNode, nil
}

// HashOnRead устанавливает режим проверки хешей при чтении.
// В реальном blockstore включение этой опции заставляет проверять
// целостность данных при каждом чтении.
func (m *mockBlockstore) HashOnRead(enabled bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.hashOnRead = enabled
}

// DeleteBlock удаляет блок из хранилища.
// Важно удалять из обоих хранилищ для консистентности.
func (m *mockBlockstore) DeleteBlock(ctx context.Context, c cid.Cid) error {
	if m.shouldFailDelete {
		return fmt.Errorf("mock error: delete block failed")
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	key := c.String()
	// Удаляем из обоих хранилищ - неважно, в каком именно находился блок
	delete(m.rawBlocks, key)
	delete(m.blocks, key)

	return nil
}

// AllKeysChan возвращает канал со всеми CID в хранилище.
// Используется для итерации по всем блокам, например, при экспорте или очистке.
func (m *mockBlockstore) AllKeysChan(ctx context.Context) (<-chan cid.Cid, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Буферизированный канал для предотвращения блокировок
	ch := make(chan cid.Cid, len(m.rawBlocks)+len(m.blocks))

	go func() {
		defer close(ch)

		// Множество для исключения дубликатов (блок может быть в обоих хранилищах)
		sent := make(map[string]bool)

		// Отправляем CID сырых блоков
		for cidStr, block := range m.rawBlocks {
			if !sent[cidStr] {
				select {
				case ch <- block.Cid():
					sent[cidStr] = true
				case <-ctx.Done():
					return // Прерывание по контексту
				}
			}
		}

		// Отправляем CID IPLD узлов (парсим CID из строкового ключа)
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

// ==============================================================================
// РЕАЛИЗАЦИЯ ИНТЕРФЕЙСА bstor.Viewer
// ==============================================================================

// View выполняет функцию callback с прямым доступом к сырым данным блока.
// Позволяет читать данные без копирования - важно для производительности.
func (m *mockBlockstore) View(ctx context.Context, c cid.Cid, callback func([]byte) error) error {
	if m.shouldFailGet {
		return fmt.Errorf("mock error: view failed")
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if block, exists := m.rawBlocks[c.String()]; exists {
		// Вызываем callback с сырыми данными блока
		return callback(block.RawData())
	}

	return fmt.Errorf("block not found: %s", c.String())
}

// ==============================================================================
// РЕАЛИЗАЦИЯ ИНТЕРФЕЙСА io.Closer
// ==============================================================================

// Close освобождает ресурсы и очищает хранилище.
// В реальном blockstore может закрывать файлы, сетевые соединения и т.д.
func (m *mockBlockstore) Close() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Полная очистка всех данных для освобождения памяти
	m.blocks = make(map[string]datamodel.Node)
	m.rawBlocks = make(map[string]blocks.Block)

	return nil
}

// ==============================================================================
// ДОПОЛНИТЕЛЬНЫЕ МЕТОДЫ ИНТЕРФЕЙСА blockstore.Blockstore (ЗАГЛУШКИ)
// ==============================================================================

// AddFile добавляет файл в UnixFS формате.
// Для MST не критично, но нужно для полноты интерфейса.
func (m *mockBlockstore) AddFile(ctx context.Context, data io.Reader, useRabin bool) (cid.Cid, error) {
	// Читаем все содержимое файла
	content, err := io.ReadAll(data)
	if err != nil {
		return cid.Undef, err
	}

	// Создаём блок из содержимого - он автоматически получит правильный CID
	// на основе хеша содержимого (в реальном IPFS)
	block := blocks.NewBlock(content)

	// Сохраняем блок в хранилище сырых блоков
	err = m.Put(ctx, block)
	if err != nil {
		return cid.Undef, err
	}

	// Возвращаем реальный CID блока (не случайный)
	return block.Cid(), nil
}

// GetFile получает файл в UnixFS формате (заглушка для тестирования).
// В реальной реализации распаковывал бы UnixFS структуру.
func (m *mockBlockstore) GetFile(ctx context.Context, c cid.Cid) (files.Node, error) {
	return nil, fmt.Errorf("mock: GetFile not implemented")
}

// GetReader возвращает Reader для потокового чтения файла.
// Поддерживает Seek для произвольного доступа к частям файла.
func (m *mockBlockstore) GetReader(ctx context.Context, c cid.Cid) (io.ReadSeekCloser, error) {
	// Загружаем блок как сырые данные
	block, err := m.Get(ctx, c)
	if err != nil {
		return nil, err
	}

	// Возвращаем обёртку, поддерживающую интерфейс ReadSeekCloser
	return &mockReadSeekCloser{data: block.RawData()}, nil
}

// Walk обходит весь подграф начиная с корневого узла (заглушка).
// В реальной реализации следовал бы по всем ссылкам в IPLD графе.
func (m *mockBlockstore) Walk(ctx context.Context, root cid.Cid, visit func(p traversal.Progress, n datamodel.Node) error) error {
	// Простая заглушка - загружаем только корневой узел
	node, err := m.GetNode(ctx, root)
	if err != nil {
		return err
	}

	// Создаём минимальный Progress для совместимости
	progress := traversal.Progress{}
	return visit(progress, node)
}

// GetSubgraph собирает все CID в подграфе по селектору (заглушка).
// В реальности обходил бы граф по селектору и собирал все посещённые CID.
func (m *mockBlockstore) GetSubgraph(ctx context.Context, root cid.Cid, selectorNode datamodel.Node) ([]cid.Cid, error) {
	// Простая заглушка - проверяем существование корня и возвращаем только его
	_, err := m.GetNode(ctx, root)
	if err != nil {
		return nil, err
	}

	return []cid.Cid{root}, nil
}

// Prefetch предварительно загружает подграф для улучшения производительности.
// В реальности запускал бы параллельную загрузку всех блоков подграфа.
func (m *mockBlockstore) Prefetch(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, workers int) error {
	// Простая заглушка - проверяем доступность корневого узла
	_, err := m.GetNode(ctx, root)
	return err
}

// ExportCARV2 экспортирует подграф в CAR архив (заглушка).
// CAR (Content Addressed aRchive) - стандартный формат для упаковки IPLD данных.
func (m *mockBlockstore) ExportCARV2(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, w io.Writer, opts ...carv2.WriteOption) error {
	return fmt.Errorf("mock: ExportCARV2 not implemented")
}

// ImportCARV2 импортирует данные из CAR архива (заглушка).
// Должен распаковывать все блоки из архива и сохранять их в blockstore.
func (m *mockBlockstore) ImportCARV2(ctx context.Context, r io.Reader, opts ...carv2.ReadOption) ([]cid.Cid, error) {
	return nil, fmt.Errorf("mock: ImportCARV2 not implemented")
}

// ==============================================================================
// ВСПОМОГАТЕЛЬНЫЕ СТРУКТУРЫ ДЛЯ ТЕСТИРОВАНИЯ
// ==============================================================================

// mockReadSeekCloser обеспечивает интерфейс ReadSeekCloser для тестирования файловых операций.
// Позволяет читать данные последовательно и с произвольным доступом.
type mockReadSeekCloser struct {
	data   []byte // Данные для чтения
	offset int64  // Текущая позиция чтения
}

// Read читает данные из текущей позиции в буфер.
// Реализует стандартный интерфейс io.Reader.
func (r *mockReadSeekCloser) Read(p []byte) (int, error) {
	// Если достигли конца данных, возвращаем EOF
	if r.offset >= int64(len(r.data)) {
		return 0, io.EOF
	}

	// Копируем данные от текущей позиции в буфер
	n := copy(p, r.data[r.offset:])
	r.offset += int64(n)
	return n, nil
}

// Seek изменяет позицию чтения согласно стандартным правилам io.Seeker.
// Поддерживает три режима: от начала, от текущей позиции, от конца.
func (r *mockReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		// Позиция от начала файла
		r.offset = offset
	case io.SeekCurrent:
		// Относительная позиция от текущей
		r.offset += offset
	case io.SeekEnd:
		// Позиция от конца файла
		r.offset = int64(len(r.data)) + offset
	default:
		return 0, fmt.Errorf("invalid whence")
	}

	// Ограничиваем позицию диапазоном [0, len(data)]
	if r.offset < 0 {
		r.offset = 0
	}
	if r.offset > int64(len(r.data)) {
		r.offset = int64(len(r.data))
	}

	return r.offset, nil
}

// Close закрывает поток (в данном случае ничего не делает).
func (r *mockReadSeekCloser) Close() error {
	return nil
}

// Проверяем во время компиляции, что mockBlockstore реализует нужный интерфейс.
// Если интерфейс изменится, компиляция упадёт с понятной ошибкой.
var _ blockstore.Blockstore = (*mockBlockstore)(nil)

// ==============================================================================
// ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ ДЛЯ ТЕСТИРОВАНИЯ
// ==============================================================================

// createTestCID создаёт детерминированный CID для тестовых данных.
// Использует содержимое строки для генерации уникального, но воспроизводимого CID.
func createTestCID(data string) cid.Cid {
	// Хешируем строку данных SHA2-256
	h, _ := multihash.Sum([]byte(data), multihash.SHA2_256, -1)
	// Создаём CIDv1 с raw codec (для простоты)
	return cid.NewCidV1(cid.Raw, h)
}

// setupTestTree создаёт новое дерево MST с mock blockstore для тестирования.
// Возвращает интерфейс blockstore.Blockstore для обычных тестов.
func setupTestTree() (*Tree, blockstore.Blockstore) {
	bs := newMockBlockstore()
	tree := NewTree(bs)
	return tree, bs
}

// setupTestTreeWithMock создаёт дерево с доступом к конкретному типу mock blockstore.
// Нужно для тестов, которым требуется управлять настройками ошибок.
func setupTestTreeWithMock() (*Tree, *mockBlockstore) {
	bs := newMockBlockstore()
	tree := NewTree(bs)
	return tree, bs
}

// ==============================================================================
// БАЗОВЫЕ ТЕСТЫ КОНСТРУКТОРА И ИНИЦИАЛИЗАЦИИ
// ==============================================================================

// TestNewTree проверяет корректную инициализацию нового дерева MST.
// Тестирует:
// - Создание экземпляра Tree
// - Правильное сохранение ссылки на blockstore
// - Инициализация пустого корня (cid.Undef)
func TestNewTree(t *testing.T) {
	// Создаём mock blockstore
	bs := newMockBlockstore()

	// Создаём новое дерево поверх этого blockstore
	tree := NewTree(bs)

	// Проверяем корректность инициализации
	require.NotNil(t, tree)                 // Дерево создано
	assert.Equal(t, bs, tree.bs)            // Blockstore сохранён
	assert.Equal(t, cid.Undef, tree.Root()) // Корень неопределён (пустое дерево)
}

// TestTree_Root_EmptyTree проверяет поведение метода Root() для пустого дерева.
// Тестирует:
// - Возвращение cid.Undef для пустого дерева
// - Правильное состояние флага Defined()
func TestTree_Root_EmptyTree(t *testing.T) {
	tree, _ := setupTestTree()

	// Получаем корень пустого дерева
	root := tree.Root()

	// Проверяем, что корень не определён
	assert.Equal(t, cid.Undef, root)
	assert.False(t, root.Defined())
}

// ==============================================================================
// ТЕСТЫ ЗАГРУЗКИ ДЕРЕВА ИЗ ХРАНИЛИЩА
// ==============================================================================

// TestTree_Load_EmptyRoot проверяет загрузку дерева с неопределённым корнем.
// Это валидный случай для пустого дерева в IPLD.
// Тестирует:
// - Обработку cid.Undef как пустого дерева
// - Отсутствие ошибок при загрузке пустого дерева
// - Сохранение состояния пустого дерева после загрузки
func TestTree_Load_EmptyRoot(t *testing.T) {
	tree, _ := setupTestTree()

	// Пытаемся загрузить дерево с неопределённым корнем
	err := tree.Load(context.Background(), cid.Undef)

	// Проверяем успешность операции и состояние
	require.NoError(t, err)
	assert.Equal(t, cid.Undef, tree.Root())
}

// TestTree_Load_ValidRoot проверяет загрузку дерева с существующим корневым узлом.
// Имитирует реальный сценарий восстановления дерева из хранилища.
// Тестирует:
// - Сохранение данных в одном дереве
// - Загрузку сохранённого корня в новое дерево
// - Корректность установки корневого CID после загрузки
func TestTree_Load_ValidRoot(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Сначала создаём дерево с данными
	testCID := createTestCID("test-value")
	rootCID, err := tree.Put(ctx, "test-key", testCID)
	require.NoError(t, err)

	// Создаём новое дерево с тем же хранилищем (имитация перезапуска)
	newTree, _ := setupTestTree()
	newTree.bs = tree.bs // Используем то же хранилище для доступа к данным

	// Загружаем сохранённый корень в новое дерево
	err = newTree.Load(ctx, rootCID)

	// Проверяем корректность загрузки
	require.NoError(t, err)
	assert.Equal(t, rootCID, newTree.Root())
}

// TestTree_Load_InvalidRoot проверяет обработку ошибки при загрузке несуществующего корня.
// Тестирует:
// - Правильную обработку попытки загрузки несуществующего CID
// - Возвращение соответствующей ошибки
// - Отсутствие изменений в состоянии дерева при ошибке
func TestTree_Load_InvalidRoot(t *testing.T) {
	tree, _ := setupTestTree()

	// Пытаемся загрузить несуществующий CID
	fakeCID := createTestCID("nonexistent")
	err := tree.Load(context.Background(), fakeCID)

	// Проверяем, что получили ожидаемую ошибку
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "block not found")
}

// ==============================================================================
// ТЕСТЫ ВСТАВКИ И ОБНОВЛЕНИЯ ДАННЫХ
// ==============================================================================

// TestTree_Put_EmptyTree проверяет вставку первого элемента в пустое дерево.
// Это фундаментальная операция создания корня дерева.
// Тестирует:
// - Создание корневого узла при первой вставке
// - Правильное возвращение CID нового корня
// - Обновление состояния дерева (Root() возвращает новый CID)
func TestTree_Put_EmptyTree(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем первый элемент в пустое дерево
	testCID := createTestCID("test-value")
	rootCID, err := tree.Put(ctx, "test-key", testCID)

	// Проверяем успешность вставки и обновление корня
	require.NoError(t, err)
	assert.True(t, rootCID.Defined())     // Корень теперь определён
	assert.Equal(t, rootCID, tree.Root()) // Дерево обновило свой корень
}

// TestTree_Put_MultipleKeys проверяет вставку нескольких ключей и автобалансировку.
// Это критичный тест для проверки:
// - Корректности работы AVL-балансировки при множественных вставках
// - Сохранения всех вставленных данных после балансировки
// - Правильности поиска всех ключей после реструктуризации дерева
func TestTree_Put_MultipleKeys(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Набор тестовых ключей в алфавитном порядке для предсказуемого тестирования
	keys := []string{"apple", "banana", "cherry", "date"}
	values := make([]cid.Cid, len(keys))

	// Вставляем ключи по одному, проверяя промежуточные состояния
	for i, key := range keys {
		values[i] = createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, values[i])
		require.NoError(t, err, "Failed to insert key: %s", key)
	}

	// Проверяем, что все ключи можно найти после всех вставок
	// Это гарантирует, что балансировка не потеряла данные
	for i, key := range keys {
		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err, "Error getting key: %s", key)
		assert.True(t, found, "Key not found: %s", key)
		assert.Equal(t, values[i], value, "Wrong value for key: %s", key)
	}
}

// TestTree_Put_UpdateExistingKey проверяет обновление значения существующего ключа.
// Тестирует:
// - Корректную замену старого значения новым
// - Сохранение структуры дерева при обновлении (без лишней балансировки)
// - Правильность поиска обновлённого значения
func TestTree_Put_UpdateExistingKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	key := "test-key"
	originalValue := createTestCID("original-value")
	updatedValue := createTestCID("updated-value")

	// Вставляем оригинальное значение
	_, err := tree.Put(ctx, key, originalValue)
	require.NoError(t, err)

	// Обновляем значение того же ключа
	_, err = tree.Put(ctx, key, updatedValue)
	require.NoError(t, err)

	// Проверяем, что значение действительно обновилось
	value, found, err := tree.Get(ctx, key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, updatedValue, value, "Value should be updated")
}

// TestTree_Put_InvalidInputs проверяет обработку некорректных входных данных.
// Валидация входных данных критична для стабильности дерева.
// Тестирует:
// - Отклонение пустого ключа (ключ не может быть пустой строкой)
// - Отклонение неопределённого CID значения
// - Правильные сообщения об ошибках для диагностики
func TestTree_Put_InvalidInputs(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	testCID := createTestCID("test-value")

	// Тестируем пустой ключ
	_, err := tree.Put(ctx, "", testCID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty key")

	// Тестируем неопределённый CID
	_, err = tree.Put(ctx, "test-key", cid.Undef)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "undefined value CID")
}

// ==============================================================================
// ТЕСТЫ УДАЛЕНИЯ ДАННЫХ
// ==============================================================================

// TestTree_Delete_ExistingKey проверяет удаление существующего ключа из дерева.
// Это сложная операция, включающая:
// - Поиск узла для удаления
// - Реструктуризацию дерева (замена узла преемником при необходимости)
// - Балансировку дерева после удаления
// - Сохранение остальных ключей без изменений
func TestTree_Delete_ExistingKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Создаём дерево с несколькими ключами
	keys := []string{"apple", "banana", "cherry"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Удаляем средний ключ (наиболее сложный случай для BST)
	_, removed, err := tree.Delete(ctx, "banana")
	require.NoError(t, err)
	assert.True(t, removed, "Key should be marked as removed")

	// Проверяем, что ключ действительно удалён
	_, found, err := tree.Get(ctx, "banana")
	require.NoError(t, err)
	assert.False(t, found, "Deleted key should not be found")

	// Проверяем, что остальные ключи остались без изменений
	for _, key := range []string{"apple", "cherry"} {
		_, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found, "Remaining key should still exist: %s", key)
	}
}

// TestTree_Delete_NonexistentKey проверяет удаление несуществующего ключа.
// Тестирует:
// - Корректную обработку попытки удаления отсутствующего ключа
// - Возвращение флага removed = false
// - Отсутствие ошибок (это валидная операция)
// - Неизменность дерева после попытки удаления
func TestTree_Delete_NonexistentKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Пытаемся удалить ключ из пустого дерева
	_, removed, err := tree.Delete(ctx, "nonexistent")

	// Проверяем корректную обработку
	require.NoError(t, err)
	assert.False(t, removed, "Non-existent key should not be marked as removed")
}

// TestTree_Delete_LastKey проверяет удаление последнего ключа из дерева.
// После удаления последнего ключа дерево должно стать пустым.
// Тестирует:
// - Превращение дерева в пустое состояние
// - Установку корня в cid.Undef
// - Правильность флага removed = true
func TestTree_Delete_LastKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем единственный ключ
	testCID := createTestCID("test-value")
	_, err := tree.Put(ctx, "only-key", testCID)
	require.NoError(t, err)

	// Удаляем единственный ключ
	_, removed, err := tree.Delete(ctx, "only-key")
	require.NoError(t, err)
	assert.True(t, removed)

	// Проверяем, что дерево стало пустым
	assert.Equal(t, cid.Undef, tree.Root(), "Tree should be empty after deleting last key")
}

// TestTree_Delete_EmptyKey проверяет валидацию входных данных при удалении.
// Тестирует:
// - Отклонение пустого ключа
// - Правильное сообщение об ошибке
func TestTree_Delete_EmptyKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	_, _, err := tree.Delete(ctx, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty key")
}

// ==============================================================================
// ТЕСТЫ ПОИСКА ДАННЫХ
// ==============================================================================

// TestTree_Get_ExistingKey проверяет поиск существующего ключа.
// Базовая операция чтения, которая должна работать быстро и надёжно.
// Тестирует:
// - Успешное нахождение ранее сохранённого ключа
// - Правильное возвращение соответствующего значения
// - Корректность флага found = true
func TestTree_Get_ExistingKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	key := "test-key"
	expectedValue := createTestCID("test-value")

	// Сначала сохраняем ключ
	_, err := tree.Put(ctx, key, expectedValue)
	require.NoError(t, err)

	// Затем ищем его
	value, found, err := tree.Get(ctx, key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, expectedValue, value)
}

// TestTree_Get_NonexistentKey проверяет поиск несуществующего ключа.
// Тестирует:
// - Корректную обработку отсутствующего ключа
// - Возвращение cid.Undef как значения
// - Правильность флага found = false
// - Отсутствие ошибок (это нормальная ситуация)
func TestTree_Get_NonexistentKey(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	value, found, err := tree.Get(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, cid.Undef, value)
}

// TestTree_Get_EmptyTree проверяет поиск в пустом дереве.
// Граничный случай, который должен обрабатываться эффективно.
// Тестирует:
// - Поиск в дереве без данных
// - Быстрое определение отсутствия ключа
// - Правильные возвращаемые значения
func TestTree_Get_EmptyTree(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	value, found, err := tree.Get(ctx, "any-key")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, cid.Undef, value)
}

// ==============================================================================
// ТЕСТЫ ДИАПАЗОННОГО ПОИСКА
// ==============================================================================

// TestTree_Range_FullRange проверяет получение всех ключей из дерева.
// Диапазонный поиск критичен для операций экспорта, синхронизации и аналитики.
// Тестирует:
// - Обход всего дерева с пустыми границами
// - Правильную сортировку ключей в лексикографическом порядке
// - Полноту результата (все ключи присутствуют)
func TestTree_Range_FullRange(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем ключи в произвольном порядке для проверки сортировки
	testData := map[string]string{
		"delta":   "value-delta",
		"alpha":   "value-alpha",
		"gamma":   "value-gamma",
		"beta":    "value-beta",
		"epsilon": "value-epsilon",
	}

	// Сохраняем все ключи
	for key, valueStr := range testData {
		value := createTestCID(valueStr)
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Получаем весь диапазон (пустые границы означают "от начала до конца")
	entries, err := tree.Range(ctx, "", "")
	require.NoError(t, err)

	// Проверяем полноту результата
	assert.Len(t, entries, len(testData))

	// Извлекаем ключи из результата
	var keys []string
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}

	// Проверяем правильность сортировки
	expectedKeys := []string{"alpha", "beta", "delta", "epsilon", "gamma"}
	sort.Strings(expectedKeys) // Сортируем ожидаемые ключи
	assert.Equal(t, expectedKeys, keys)
}

// TestTree_Range_LimitedRange проверяет поиск в ограниченном диапазоне.
// Тестирует:
// - Правильную фильтрацию ключей по границам диапазона
// - Включение граничных значений (closed interval [start, end])
// - Сортировку результатов
func TestTree_Range_LimitedRange(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем последовательность ключей
	keys := []string{"a", "b", "c", "d", "e"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Ищем диапазон [b, d] - должны получить b, c, d
	entries, err := tree.Range(ctx, "b", "d")
	require.NoError(t, err)

	expectedKeys := []string{"b", "c", "d"}
	assert.Len(t, entries, len(expectedKeys))

	// Проверяем правильность результата
	for i, entry := range entries {
		assert.Equal(t, expectedKeys[i], entry.Key)
	}
}

// TestTree_Range_EmptyTree проверяет диапазонный поиск в пустом дереве.
// Тестирует:
// - Возвращение пустого результата без ошибок
// - Корректную обработку граничного случая
func TestTree_Range_EmptyTree(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	entries, err := tree.Range(ctx, "", "")
	require.NoError(t, err)
	assert.Len(t, entries, 0)
}

// TestTree_Range_NoMatches проверяет диапазонный поиск без совпадений.
// Тестирует:
// - Поиск в диапазоне, где нет ключей
// - Возвращение пустого результата
// - Отсутствие ошибок при отсутствии совпадений
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

	// Ищем диапазон [f, z] - ничего не должно найтись
	entries, err := tree.Range(ctx, "f", "z")
	require.NoError(t, err)
	assert.Len(t, entries, 0)
}

// ==============================================================================
// ТЕСТЫ AVL БАЛАНСИРОВКИ ДЕРЕВА
// ==============================================================================

// TestTree_BalancingLeftHeavy проверяет балансировку при левостороннем дисбалансе.
// Вставка ключей в убывающем порядке создаёт левостороннее вырожденное дерево,
// которое AVL-алгоритм должен автоматически сбалансировать.
// Тестирует:
// - Автоматическую балансировку при левом дисбалансе
// - Сохранность всех данных после поворотов
// - Правильность поиска после балансировки
func TestTree_BalancingLeftHeavy(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем ключи в убывающем порядке - создаём левый дисбаланс
	keys := []string{"e", "d", "c", "b", "a"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err, "Failed to insert key: %s", key)
	}

	// Проверяем, что все ключи доступны после балансировки
	// Если балансировка работает неправильно, некоторые ключи могут потеряться
	for i, key := range keys {
		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found, "Key should be found after balancing: %s", key)
		expectedValue := createTestCID(fmt.Sprintf("value-%d", i))
		assert.Equal(t, expectedValue, value)
	}
}

// TestTree_BalancingRightHeavy проверяет балансировку при правостороннем дисбалансе.
// Вставка ключей в возрастающем порядке создаёт правостороннее вырожденное дерево.
// Тестирует:
// - Автоматическую балансировку при правом дисбалансе
// - Корректность левых поворотов
// - Сохранение всех данных при реструктуризации
func TestTree_BalancingRightHeavy(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Вставляем ключи в возрастающем порядке - создаём правый дисбаланс
	keys := []string{"a", "b", "c", "d", "e"}
	for i, key := range keys {
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err, "Failed to insert key: %s", key)
	}

	// Проверяем доступность всех ключей
	for i, key := range keys {
		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found, "Key should be found after balancing: %s", key)
		expectedValue := createTestCID(fmt.Sprintf("value-%d", i))
		assert.Equal(t, expectedValue, value)
	}
}

// ==============================================================================
// НАГРУЗОЧНЫЕ ТЕСТЫ
// ==============================================================================

// TestTree_LargeDataset проверяет работу дерева с большим количеством данных.
// Это критично для проверки:
// - Производительности при масштабировании
// - Стабильности балансировки при большом количестве операций
// - Отсутствия утечек памяти или переполнений
// - Правильности поиска в глубоких деревьях
func TestTree_LargeDataset(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	const numItems = 1000 // Достаточно для проверки балансировки без замедления тестов

	// Вставляем большое количество элементов
	for i := 0; i < numItems; i++ {
		key := fmt.Sprintf("key-%04d", i) // Форматируем с ведущими нулями для правильной сортировки
		value := createTestCID(fmt.Sprintf("value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err, "Failed to insert item %d", i)
	}

	// Проверяем случайные элементы для подтверждения целостности
	testIndices := []int{0, 100, 500, 750, 999}
	for _, i := range testIndices {
		key := fmt.Sprintf("key-%04d", i)
		expectedValue := createTestCID(fmt.Sprintf("value-%d", i))

		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found, "Item %d should be found", i)
		assert.Equal(t, expectedValue, value, "Wrong value for item %d", i)
	}

	// Проверяем диапазонный поиск на подмножестве
	entries, err := tree.Range(ctx, "key-0100", "key-0110")
	require.NoError(t, err)
	assert.Len(t, entries, 11, "Should find 11 keys in range [key-0100, key-0110]")
}

// ==============================================================================
// ТЕСТЫ ОБРАБОТКИ ОШИБОК
// ==============================================================================

// TestTree_StorageErrors проверяет обработку ошибок базового хранилища.
// MST должно корректно пробрасывать ошибки blockstore наверх.
// Тестирует:
// - Обработку ошибок записи в хранилище
// - Обработку ошибок чтения из хранилища
// - Правильные сообщения об ошибках для диагностики
func TestTree_StorageErrors(t *testing.T) {
	tree, bs := setupTestTreeWithMock()
	ctx := context.Background()

	// Тестируем ошибку записи
	bs.shouldFailPut = true
	_, err := tree.Put(ctx, "test-key", createTestCID("test-value"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: put node failed")

	// Восстанавливаем запись и добавляем элемент
	bs.shouldFailPut = false
	_, err = tree.Put(ctx, "test-key", createTestCID("test-value"))
	require.NoError(t, err)

	// Тестируем ошибку чтения
	bs.shouldFailGet = true
	_, _, err = tree.Get(ctx, "test-key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error: get node failed")
}

// ==============================================================================
// ТЕСТЫ МНОГОПОТОЧНОСТИ
// ==============================================================================

// TestTree_ConcurrentOperations проверяет безопасность параллельных записей.
// MST должно корректно обрабатывать одновременные вставки из разных горутин.
// Тестирует:
// - Потокобезопасность операций записи
// - Отсутствие состояний гонки при параллельных Put()
// - Целостность данных после параллельных операций
func TestTree_ConcurrentOperations(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	const numGoroutines = 10  // Количество параллельных потоков
	const numOperations = 100 // Операций на поток

	var wg sync.WaitGroup

	// Запускаем параллельные вставки
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			// Каждая горутина вставляет свой набор ключей
			for i := 0; i < numOperations; i++ {
				key := fmt.Sprintf("g%d-key%d", goroutineID, i)
				value := createTestCID(fmt.Sprintf("g%d-value%d", goroutineID, i))

				_, err := tree.Put(ctx, key, value)
				assert.NoError(t, err, "Concurrent insert failed")
			}
		}(g)
	}

	// Ждём завершения всех горутин
	wg.Wait()

	// Проверяем, что все элементы были корректно вставлены
	for g := 0; g < numGoroutines; g++ {
		for i := 0; i < numOperations; i++ {
			key := fmt.Sprintf("g%d-key%d", g, i)
			expectedValue := createTestCID(fmt.Sprintf("g%d-value%d", g, i))

			value, found, err := tree.Get(ctx, key)
			require.NoError(t, err)
			assert.True(t, found, "Concurrent insert result not found: %s", key)
			assert.Equal(t, expectedValue, value)
		}
	}
}

// TestTree_ConcurrentReadWrite проверяет безопасность параллельных чтений и записей.
// Это реалистичный сценарий работы MST в продакшене.
// Тестирует:
// - Корректность RWMutex блокировок
// - Отсутствие блокировок читателей между собой
// - Правильную изоляцию писателей от читателей
func TestTree_ConcurrentReadWrite(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Предварительно заполняем дерево данными
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("initial-key-%d", i)
		value := createTestCID(fmt.Sprintf("initial-value-%d", i))
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup

	// Запускаем читателей (много параллельных читателей)
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			// Каждый читатель выполняет много операций чтения
			for i := 0; i < 200; i++ {
				key := fmt.Sprintf("initial-key-%d", i%100)
				_, found, err := tree.Get(ctx, key)
				assert.NoError(t, err)
				assert.True(t, found, "Reader %d: key should exist", readerID)
			}
		}(r)
	}

	// Запускаем писателей (меньше писателей, больше контента)
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()

			// Каждый писатель добавляет новые ключи
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

// ==============================================================================
// ТЕСТЫ ВНУТРЕННИХ ФУНКЦИЙ И УТИЛИТ
// ==============================================================================

// TestCloneNode проверяет корректность клонирования узлов дерева.
// Клонирование критично из-за иммутабельности данных в IPLD.
// Тестирует:
// - Создание независимой копии узла
// - Правильное копирование всех полей
// - Независимость копии от оригинала (deep copy для слайсов)
func TestCloneNode(t *testing.T) {
	// Создаём оригинальный узел с полным набором данных
	original := &node{
		Entry: Entry{
			Key:   "test-key",
			Value: createTestCID("test-value"),
		},
		Left:   createTestCID("left-child"),
		Right:  createTestCID("right-child"),
		Height: 2,
		Hash:   []byte{1, 2, 3, 4}, // Важно проверить копирование слайса
	}

	// Клонируем узел
	cloned := cloneNode(original)

	// Проверяем корректность клонирования
	require.NotNil(t, cloned)
	assert.Equal(t, original.Entry, cloned.Entry)
	assert.Equal(t, original.Left, cloned.Left)
	assert.Equal(t, original.Right, cloned.Right)
	assert.Equal(t, original.Height, cloned.Height)
	assert.Equal(t, original.Hash, cloned.Hash)

	// Проверяем независимость объектов (разные адреса в памяти)
	assert.NotSame(t, original, cloned)
	// Для слайсов проверяем, что это разные слайсы (разные указатели на базовый массив)
	if len(original.Hash) > 0 && len(cloned.Hash) > 0 {
		assert.NotSame(t, &original.Hash[0], &cloned.Hash[0])
	}

	// Проверяем независимость: модификация клона не влияет на оригинал
	cloned.Hash[0] = 99
	assert.NotEqual(t, original.Hash[0], cloned.Hash[0])
}

// TestCloneNode_Nil проверяет обработку nil узла при клонировании.
// Граничный случай, который должен обрабатываться корректно.
func TestCloneNode_Nil(t *testing.T) {
	cloned := cloneNode(nil)
	assert.Nil(t, cloned)
}

// TestMax проверяет вспомогательную функцию поиска максимума.
// Используется для вычисления высоты узлов в AVL-дереве.
func TestMax(t *testing.T) {
	assert.Equal(t, 5, max(3, 5))
	assert.Equal(t, 5, max(5, 3))
	assert.Equal(t, 5, max(5, 5))
	assert.Equal(t, 0, max(-1, 0))
	assert.Equal(t, -1, max(-5, -1))
}

// ==============================================================================
// ТЕСТЫ СЕРИАЛИЗАЦИИ И ДЕСЕРИАЛИЗАЦИИ
// ==============================================================================

// TestNodeSerialization проверяет сериализацию узла в IPLD формат.
// Каждый узел MST должен корректно преобразовываться в datamodel.Node для сохранения.
// Тестирует:
// - Корректную сериализацию всех полей узла
// - Правильную десериализацию обратно в узел
// - Сохранение целостности данных при круговом преобразовании
func TestNodeSerialization(t *testing.T) {
	tree, _ := setupTestTree()

	// Создаём узел с полным набором данных
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

	// Сериализуем в IPLD datamodel.Node
	dm, err := tree.nodeToNode(original)
	require.NoError(t, err)

	// Десериализуем обратно в внутреннее представление
	deserialized, err := tree.nodeFromNode(dm)
	require.NoError(t, err)

	// Проверяем полное соответствие данных
	assert.Equal(t, original.Key, deserialized.Key)
	assert.Equal(t, original.Value, deserialized.Value)
	assert.Equal(t, original.Left, deserialized.Left)
	assert.Equal(t, original.Right, deserialized.Right)
	assert.Equal(t, original.Height, deserialized.Height)
	assert.Equal(t, original.Hash, deserialized.Hash)
}

// TestNodeSerialization_MinimalNode проверяет сериализацию минимального узла.
// Листовые узлы не имеют детей - важно правильно обрабатывать cid.Undef.
// Тестирует:
// - Сериализацию узла только с обязательными полями
// - Правильную обработку отсутствующих детей
// - Корректность минимального представления
func TestNodeSerialization_MinimalNode(t *testing.T) {
	tree, _ := setupTestTree()

	// Создаём минимальный узел (листовой)
	original := &node{
		Entry: Entry{
			Key:   "minimal",
			Value: createTestCID("minimal-value"),
		},
		Left:   cid.Undef, // Нет левого ребёнка
		Right:  cid.Undef, // Нет правого ребёнка
		Height: 1,         // Листовая высота
		Hash:   []byte{0xFF},
	}

	// Тестируем круговую сериализацию
	dm, err := tree.nodeToNode(original)
	require.NoError(t, err)

	deserialized, err := tree.nodeFromNode(dm)
	require.NoError(t, err)

	// Проверяем правильность десериализации
	assert.Equal(t, original.Key, deserialized.Key)
	assert.Equal(t, original.Value, deserialized.Value)
	assert.Equal(t, cid.Undef, deserialized.Left)
	assert.Equal(t, cid.Undef, deserialized.Right)
	assert.Equal(t, original.Height, deserialized.Height)
	assert.Equal(t, original.Hash, deserialized.Hash)
}

// TestNodeDeserialization_InvalidData проверяет обработку невалидных данных при десериализации.
// Защита от повреждённых или неполных данных из хранилища.
// Тестирует:
// - Правильную обработку отсутствующих обязательных полей
// - Возвращение соответствующих ошибок
// - Диагностические сообщения об ошибках
func TestNodeDeserialization_InvalidData(t *testing.T) {
	tree, _ := setupTestTree()

	// Создаём узел с отсутствующим обязательным полем "key"
	builder := basicnode.Prototype.Map.NewBuilder()
	ma, _ := builder.BeginMap(1)
	// Добавляем только "value", но не "key"
	entry, _ := ma.AssembleEntry("value")
	entry.AssignLink(cidlink.Link{Cid: createTestCID("test")})
	ma.Finish()
	invalidNode := builder.Build()

	// Пытаемся десериализовать невалидный узел
	_, err := tree.nodeFromNode(invalidNode)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "node missing key")
}

// ==============================================================================
// ТЕСТЫ СЕЛЕКТОРОВ
// ==============================================================================

// TestBuildSelector проверяет создание селекторов для обхода дерева.
// Селекторы используются IPLD для определения, какие части графа нужно обойти.
// Тестирует:
// - Успешное создание селектора
// - Отсутствие ошибок компиляции
// - Готовность селектора к использованию
func TestBuildSelector(t *testing.T) {
	selector, err := BuildSelector()
	require.NoError(t, err)
	assert.NotNil(t, selector)
}

// ==============================================================================
// ИНТЕГРАЦИОННЫЕ ТЕСТЫ
// ==============================================================================

// TestTree_ComplexScenario проверяет сложный сценарий использования MST.
// Имитирует реальное использование с различными типами операций.
// Тестирует:
// - Последовательность операций: вставка → чтение → обновление → удаление
// - Диапазонный поиск после модификаций
// - Целостность данных на всех этапах
// - Правильную работу с префиксными ключами
func TestTree_ComplexScenario(t *testing.T) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Подготавливаем тестовые данные, имитирующие реальные структуры
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

	// Фаза 1: Массовая вставка данных
	t.Log("Phase 1: Inserting initial data")
	for _, item := range testData {
		value := createTestCID(item.value)
		_, err := tree.Put(ctx, item.key, value)
		require.NoError(t, err, "Failed to insert %s", item.key)
	}

	// Фаза 2: Проверка целостности всех данных
	t.Log("Phase 2: Verifying all inserted data")
	for _, item := range testData {
		expectedValue := createTestCID(item.value)
		value, found, err := tree.Get(ctx, item.key)
		require.NoError(t, err)
		assert.True(t, found, "Key %s not found", item.key)
		assert.Equal(t, expectedValue, value, "Wrong value for key %s", item.key)
	}

	// Фаза 3: Обновление некоторых значений
	t.Log("Phase 3: Updating selected values")
	updates := map[string]string{
		"user:alice": "updated-profile-alice",
		"post:123":   "updated-content-123",
	}

	for key, newValue := range updates {
		value := createTestCID(newValue)
		_, err := tree.Put(ctx, key, value)
		require.NoError(t, err, "Failed to update %s", key)
	}

	// Проверяем обновления
	for key, expectedValueStr := range updates {
		expectedValue := createTestCID(expectedValueStr)
		value, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, expectedValue, value, "Update failed for %s", key)
	}

	// Фаза 4: Выборочное удаление
	t.Log("Phase 4: Deleting selected keys")
	keysToDelete := []string{"user:charlie", "comment:789"}

	for _, key := range keysToDelete {
		_, removed, err := tree.Delete(ctx, key)
		require.NoError(t, err)
		assert.True(t, removed, "Key %s should have been removed", key)
	}

	// Проверяем, что ключи действительно удалены
	for _, key := range keysToDelete {
		_, found, err := tree.Get(ctx, key)
		require.NoError(t, err)
		assert.False(t, found, "Key %s should not exist after deletion", key)
	}

	// Фаза 5: Диапазонный поиск по префиксам
	t.Log("Phase 5: Testing range queries")
	userEntries, err := tree.Range(ctx, "user:", "user:zzz")
	require.NoError(t, err)

	// После удаления user:charlie должны остаться только alice и bob
	assert.Len(t, userEntries, 2, "Should have 2 user entries after deletion")

	var userKeys []string
	for _, entry := range userEntries {
		userKeys = append(userKeys, entry.Key)
	}
	sort.Strings(userKeys)
	assert.Equal(t, []string{"user:alice", "user:bob"}, userKeys)

	t.Log("Complex scenario completed successfully")
}

// ==============================================================================
// ТЕСТЫ MOCK BLOCKSTORE ИНТЕРФЕЙСА
// ==============================================================================

// TestMockBlockstore_BlockstoreInterface проверяет реализацию базового интерфейса blockstore.
// Важно убедиться, что mock полностью совместим с реальным blockstore.
// Тестирует все основные операции с сырыми блоками.
func TestMockBlockstore_BlockstoreInterface(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Подготавливаем тестовые данные
	testData := []byte("test block data")
	block := blocks.NewBlock(testData)

	// Тестируем сохранение сырого блока
	err := bs.Put(ctx, block)
	require.NoError(t, err)

	// Тестируем загрузку сырого блока
	retrievedBlock, err := bs.Get(ctx, block.Cid())
	require.NoError(t, err)
	assert.Equal(t, testData, retrievedBlock.RawData())

	// Тестируем проверку существования
	exists, err := bs.Has(ctx, block.Cid())
	require.NoError(t, err)
	assert.True(t, exists)

	// Тестируем получение размера
	size, err := bs.GetSize(ctx, block.Cid())
	require.NoError(t, err)
	assert.Equal(t, len(testData), size)

	// Тестируем View операцию
	var viewedData []byte
	err = bs.View(ctx, block.Cid(), func(data []byte) error {
		viewedData = make([]byte, len(data))
		copy(viewedData, data)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, testData, viewedData)

	// Тестируем удаление
	err = bs.DeleteBlock(ctx, block.Cid())
	require.NoError(t, err)

	// Проверяем, что блок удалён
	exists, err = bs.Has(ctx, block.Cid())
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestMockBlockstore_PutMany проверяет массовую вставку блоков.
// Оптимизация для улучшения производительности при пакетных операциях.
func TestMockBlockstore_PutMany(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Создаём несколько тестовых блоков
	var testBlocks []blocks.Block
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("test data %d", i))
		block := blocks.NewBlock(data)
		testBlocks = append(testBlocks, block)
	}

	// Массовая вставка
	err := bs.PutMany(ctx, testBlocks)
	require.NoError(t, err)

	// Проверяем, что все блоки сохранены
	for _, block := range testBlocks {
		exists, err := bs.Has(ctx, block.Cid())
		require.NoError(t, err)
		assert.True(t, exists, "Block should exist after PutMany")
	}
}

// TestMockBlockstore_AllKeysChan проверяет итерацию по всем ключам в хранилище.
// Важно для операций экспорта, синхронизации и анализа содержимого.
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

	// Получаем канал всех ключей
	ch, err := bs.AllKeysChan(ctx)
	require.NoError(t, err)

	// Собираем все CID из канала
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

// TestMockBlockstore_Close проверяет корректность закрытия и очистки ресурсов.
func TestMockBlockstore_Close(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Добавляем тестовые данные
	testData := []byte("test data")
	block := blocks.NewBlock(testData)
	err := bs.Put(ctx, block)
	require.NoError(t, err)

	// Закрываем blockstore
	err = bs.Close()
	require.NoError(t, err)

	// После закрытия данные должны быть очищены
	exists, err := bs.Has(ctx, block.Cid())
	require.NoError(t, err)
	assert.False(t, exists, "Data should be cleared after Close()")
}

// TestMockBlockstore_ErrorHandling проверяет симуляцию различных типов ошибок.
// Критично для тестирования обработки ошибок в MST.
func TestMockBlockstore_ErrorHandling(t *testing.T) {
	bs := newMockBlockstore()
	ctx := context.Background()

	// Тестируем различные типы ошибок Get
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

// TestMockBlockstore_FileOperations проверяет файловые операции mock blockstore.
// Важно для полноты интерфейса, хотя MST их напрямую не использует.
func TestMockBlockstore_FileOperations(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Тестируем AddFile
	testData := "Hello, World!"
	reader := strings.NewReader(testData)

	fileCID, err := bs.AddFile(ctx, reader, false)
	require.NoError(t, err)
	assert.True(t, fileCID.Defined())

	// Тестируем GetReader
	readSeeker, err := bs.GetReader(ctx, fileCID)
	require.NoError(t, err)
	defer readSeeker.Close()

	// Читаем данные обратно
	retrievedData, err := io.ReadAll(readSeeker)
	require.NoError(t, err)
	assert.Equal(t, testData, string(retrievedData))

	// Тестируем операции Seek
	offset, err := readSeeker.Seek(0, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, int64(0), offset)

	offset, err = readSeeker.Seek(7, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, int64(7), offset)

	// Читаем с позиции 7 ("World!")
	remainingData, err := io.ReadAll(readSeeker)
	require.NoError(t, err)
	assert.Equal(t, "World!", string(remainingData))
}

// TestMockBlockstore_AdvancedOperations проверяет расширенные операции blockstore.
// Эти операции реализованы как заглушки, но должны работать без ошибок.
func TestMockBlockstore_AdvancedOperations(t *testing.T) {
	_, bs := setupTestTree()
	ctx := context.Background()

	// Создаём тестовый узел
	testNode := basicnode.NewString("test node")
	nodeCID, err := bs.PutNode(ctx, testNode)
	require.NoError(t, err)

	// Тестируем Walk (заглушка)
	err = bs.Walk(ctx, nodeCID, func(p traversal.Progress, n datamodel.Node) error {
		// Проверяем, что получили наш узел
		str, strErr := n.AsString()
		if strErr == nil {
			assert.Equal(t, "test node", str)
		}
		return nil
	})
	require.NoError(t, err)

	// Тестируем GetSubgraph (заглушка)
	cids, err := bs.GetSubgraph(ctx, nodeCID, basicnode.NewString("selector"))
	require.NoError(t, err)
	assert.Contains(t, cids, nodeCID)

	// Тестируем Prefetch (заглушка)
	err = bs.Prefetch(ctx, nodeCID, basicnode.NewString("selector"), 1)
	require.NoError(t, err)

	// Тестируем заглушки, которые должны возвращать ошибки "not implemented"
	err = bs.ExportCARV2(ctx, nodeCID, basicnode.NewString("selector"), io.Discard)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	_, err = bs.ImportCARV2(ctx, strings.NewReader("fake car data"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	_, err = bs.GetFile(ctx, nodeCID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")
}

// ==============================================================================
// БЕНЧМАРКИ ПРОИЗВОДИТЕЛЬНОСТИ
// ==============================================================================

// BenchmarkTree_Put измеряет производительность вставки элементов.
// Критично для оценки масштабируемости MST при больших нагрузках.
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

// BenchmarkTree_Get измеряет производительность поиска элементов.
// Важно для оценки эффективности индексирования и балансировки.
func BenchmarkTree_Get(b *testing.B) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Предварительно заполняем дерево данными
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

// BenchmarkTree_Range измеряет производительность диапазонных запросов.
// Критично для аналитических операций и массового экспорта данных.
func BenchmarkTree_Range(b *testing.B) {
	tree, _ := setupTestTree()
	ctx := context.Background()

	// Предварительно заполняем дерево
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
