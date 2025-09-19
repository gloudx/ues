package mst

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"ues/blockstore"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/datamodel"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	selector "github.com/ipld/go-ipld-prime/traversal/selector"
	selb "github.com/ipld/go-ipld-prime/traversal/selector/builder"
	"lukechampine.com/blake3"
)

// Tree реализует Merkle Search Tree (MST) поверх Blockstore.
// MST представляет собой самобалансирующееся бинарное дерево поиска (AVL-дерево),
// где каждый узел хранится в распределённом хранилище через IPFS/IPLD.
// Дерево автоматически поддерживает баланс для обеспечения логарифмической
// сложности операций поиска, вставки и удаления.
type Tree struct {
	bs      blockstore.Blockstore // Интерфейс для работы с блочным хранилищем IPFS
	rootCID cid.Cid               // CID (Content Identifier) корневого узла дерева
	mu      sync.RWMutex          // Мьютекс для безопасного многопоточного доступа
}

// Entry описывает пару ключ-значение, возвращаемую из MST.
// Это базовая единица данных, хранимая в дереве.
type Entry struct {
	Key   string  // Ключ для поиска и упорядочивания в дереве
	Value cid.Cid // Значение как CID, указывающий на данные в блочном хранилище
}

// node — внутреннее представление узла MST.
// Содержит всю информацию, необходимую для работы AVL-дерева:
// данные узла, ссылки на детей, метаданные для балансировки.
type node struct {
	Entry              // Встроенная структура с ключом и значением
	Left   cid.Cid     // CID левого дочернего узла (ключи меньше текущего)
	Right  cid.Cid     // CID правого дочернего узла (ключи больше текущего)  
	Height int         // Высота поддерева с корнем в данном узле (для AVL-балансировки)
	Hash   []byte      // Криптографический хеш узла для обеспечения целостности
}

// nodeCache кэширует узлы, считанные из blockstore, в рамках одной операции.
// Это критично для производительности, так как предотвращает множественные
// обращения к медленному блочному хранилищу для одних и тех же узлов
// во время выполнения одной операции (например, балансировки дерева).
type nodeCache map[string]*node

// NewTree создаёт пустое дерево поверх предоставленного Blockstore.
// Возвращает указатель на новую структуру Tree с неопределённым корневым CID,
// что означает пустое дерево.
func NewTree(bs blockstore.Blockstore) *Tree {
	return &Tree{
		bs: bs, // Сохраняем ссылку на блочное хранилище
		// rootCID остаётся cid.Undef (неопределённым), что означает пустое дерево
	}
}

// Root возвращает CID текущего корня (cid.Undef для пустого дерева).
// Использует блокировку только для чтения, так как не изменяет состояние.
func (t *Tree) Root() cid.Cid {
	// Получаем блокировку для чтения для безопасного доступа к rootCID
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.rootCID
}

// Load загружает дерево из Blockstore по корневому CID.
// Проверяет корректность корневого узла, но не загружает всё дерево целиком -
// узлы будут загружены по требованию при обращении к ним.
func (t *Tree) Load(ctx context.Context, root cid.Cid) error {
	// Получаем полную блокировку, так как изменяем состояние дерева
	t.mu.Lock()
	defer t.mu.Unlock()

	// Если корневой CID не определён, то дерево считается пустым
	if !root.Defined() {
		t.rootCID = cid.Undef
		return nil
	}

	// Пытаемся загрузить корневой узел для проверки его существования и корректности
	if _, err := t.loadNode(ctx, make(nodeCache), root); err != nil {
		return err
	}

	// Если узел успешно загружен, сохраняем новый корень
	t.rootCID = root

	return nil
}

// Put вставляет или обновляет значение по ключу и возвращает новый корневой CID.
// Это основная операция модификации дерева. Из-за иммутабельности узлов в IPLD,
// любое изменение создаёт новые версии всех узлов на пути от корня до изменяемого узла.
func (t *Tree) Put(ctx context.Context, key string, id cid.Cid) (cid.Cid, error) {
	// Проверяем корректность входных параметров
	if key == "" {
		return cid.Undef, errors.New("mst: empty key")
	}

	if !id.Defined() {
		return cid.Undef, errors.New("mst: undefined value CID")
	}

	// Получаем полную блокировку для модификации
	t.mu.Lock()
	defer t.mu.Unlock()

	// Создаём новый кэш для этой операции
	cache := make(nodeCache)

	// Выполняем рекурсивную вставку, начиная с корня
	newRoot, _, err := t.putNode(ctx, cache, t.rootCID, key, id)
	if err != nil {
		return cid.Undef, err
	}

	// Обновляем корень дерева на новый
	t.rootCID = newRoot

	return newRoot, nil
}

// Delete удаляет значение по ключу и возвращает новый корневой CID и признак удаления.
// Возвращает флаг removed, указывающий, был ли ключ действительно найден и удалён.
func (t *Tree) Delete(ctx context.Context, key string) (cid.Cid, bool, error) {
	// Проверяем корректность ключа
	if key == "" {
		return cid.Undef, false, errors.New("mst: empty key")
	}

	// Получаем полную блокировку для модификации
	t.mu.Lock()
	defer t.mu.Unlock()

	// Создаём новый кэш для этой операции
	cache := make(nodeCache)

	// Выполняем рекурсивное удаление
	newRoot, removed, err := t.deleteNode(ctx, cache, t.rootCID, key)
	if err != nil {
		return cid.Undef, false, err
	}
	
	// Если ключ не был найден, возвращаем текущий корень без изменений
	if !removed {
		return t.rootCID, false, nil
	}

	// Обновляем корень дерева
	t.rootCID = newRoot

	return newRoot, true, nil
}

// Get возвращает значение по ключу, признак наличия ключа и ошибку.
// Это операция только для чтения, поэтому используется только блокировка чтения.
// Поиск выполняется итеративно для оптимизации стека вызовов.
func (t *Tree) Get(ctx context.Context, key string) (cid.Cid, bool, error) {
	// Получаем снимок текущего корня под блокировкой чтения
	t.mu.RLock()
	root := t.rootCID
	t.mu.RUnlock()

	// Создаём кэш для этой операции поиска
	cache := make(nodeCache)

	// Выполняем поиск
	return t.find(ctx, cache, root, key)
}

// Range возвращает все пары ключ-значение в диапазоне [start, end].
// Выполняет обход дерева в порядке сортировки ключей (in-order traversal).
// Если start или end пустые, то соответствующая граница не учитывается.
func (t *Tree) Range(ctx context.Context, start, end string) ([]Entry, error) {
	// Получаем снимок текущего корня под блокировкой чтения
	t.mu.RLock()
	root := t.rootCID
	t.mu.RUnlock()

	// Создаём кэш для этой операции
	cache := make(nodeCache)

	// Создаём слайс для сбора результатов
	var out []Entry
	if err := t.collectRange(ctx, cache, root, start, end, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// BuildSelector строит селектор для обхода всего дерева.
// Селектор используется IPLD для определения того, какие части данных
// нужно обойти при операциях синхронизации или репликации.
func BuildSelector() (selector.Selector, error) {
	// Создаём построитель селектора
	sb := selb.NewSelectorSpecBuilder(basicnode.Prototype.Any)

	// Создаём рекурсивный селектор, который обходит все узлы дерева
	spec := sb.ExploreRecursive(selector.RecursionLimitNone(),
		sb.ExploreAll(sb.ExploreRecursiveEdge()),
	).Node()

	// Компилируем спецификацию в селектор
	return selector.CompileSelector(spec)
}

// putNode вставляет или обновляет узел в поддереве с корнем root.
// Это рекурсивная функция, которая:
// 1. Находит правильную позицию для ключа
// 2. Вставляет новый узел или обновляет существующий
// 3. Балансирует дерево на пути возврата из рекурсии
// Возвращает новый корневой CID, признак вставки нового ключа и ошибку.
func (t *Tree) putNode(ctx context.Context, cache nodeCache, root cid.Cid, key string, id cid.Cid) (cid.Cid, bool, error) {
	// Базовый случай: если поддерево пустое, создаём новый листовой узел
	if !root.Defined() {
		nd := &node{
			Entry: Entry{
				Key:   key,
				Value: id,
			},
			Left:   cid.Undef, // Новый узел не имеет детей
			Right:  cid.Undef,
			Height: 1, // Листовой узел имеет высоту 1
			Hash:   nil, // Хеш будет вычислен в storeNode
		}
		// Сохраняем новый узел и возвращаем его CID
		cidNew, _, err := t.storeNode(ctx, cache, nd)
		return cidNew, true, err
	}

	// Загружаем текущий узел из хранилища
	current, err := t.loadNode(ctx, cache, root)
	if err != nil {
		return cid.Undef, false, err
	}

	// Клонируем узел, так как узлы в IPLD иммутабельны
	cur := cloneNode(current)

	var inserted bool
	// Определяем, куда идти: влево, вправо или обновить текущий узел
	switch cmp := strings.Compare(key, cur.Key); {
	case cmp == 0:
		// Ключ уже существует - просто обновляем значение
		cur.Value = id

	case cmp < 0:
		// Ключ меньше текущего - идём в левое поддерево
		newLeft, ins, err := t.putNode(ctx, cache, cur.Left, key, id)
		if err != nil {
			return cid.Undef, false, err
		}
		cur.Left = newLeft
		inserted = ins

	default:
		// Ключ больше текущего - идём в правое поддерево
		newRight, ins, err := t.putNode(ctx, cache, cur.Right, key, id)
		if err != nil {
			return cid.Undef, false, err
		}
		cur.Right = newRight
		inserted = ins
	}

	// Балансируем узел после вставки
	// Это критично для поддержания свойств AVL-дерева
	balanced, cidNew, err := t.balanceNode(ctx, cache, cur)
	if err != nil {
		return cid.Undef, false, err
	}

	// Кэшируем сбалансированный узел
	cache[cidNew.String()] = balanced

	return cidNew, inserted, nil
}

// deleteNode удаляет узел по ключу в поддереве с корнем root.
// Реализует стандартный алгоритм удаления из BST с последующей балансировкой.
// Возвращает новый корневой CID, признак удаления ключа и ошибку.
func (t *Tree) deleteNode(ctx context.Context, cache nodeCache, root cid.Cid, key string) (cid.Cid, bool, error) {
	// Базовый случай: если поддерево пустое, ключ не найден
	if !root.Defined() {
		return cid.Undef, false, nil
	}

	// Загружаем текущий узел
	current, err := t.loadNode(ctx, cache, root)
	if err != nil {
		return cid.Undef, false, err
	}

	// Клонируем узел для модификации
	cur := cloneNode(current)

	// Определяем направление поиска или выполняем удаление
	switch cmp := strings.Compare(key, cur.Key); {
	case cmp < 0:
		// Ключ меньше текущего - ищем в левом поддереве
		newLeft, removed, err := t.deleteNode(ctx, cache, cur.Left, key)
		if err != nil {
			return cid.Undef, false, err
		}
		if !removed {
			// Если ключ не был найден, возвращаем неизменённый узел
			return root, false, nil
		}
		cur.Left = newLeft

	case cmp > 0:
		// Ключ больше текущего - ищем в правом поддереве
		newRight, removed, err := t.deleteNode(ctx, cache, cur.Right, key)
		if err != nil {
			return cid.Undef, false, err
		}
		if !removed {
			// Если ключ не был найден, возвращаем неизменённый узел
			return root, false, nil
		}
		cur.Right = newRight

	default:
		// Нашли узел для удаления - обрабатываем три случая:
		
		// Случай 1: Узел не имеет детей (лист)
		if !cur.Left.Defined() && !cur.Right.Defined() {
			return cid.Undef, true, nil
		}

		// Случай 2: Узел имеет только правого ребёнка
		if !cur.Left.Defined() {
			return cur.Right, true, nil
		}

		// Случай 3: Узел имеет только левого ребёнка
		if !cur.Right.Defined() {
			return cur.Left, true, nil
		}

		// Случай 4: Узел имеет обоих детей
		// Находим узел с минимальным ключом в правом поддереве (преемник)
		_, succNode, err := t.minNode(ctx, cache, cur.Right)
		if err != nil {
			return cid.Undef, false, err
		}

		// Заменяем ключ и значение текущего узла данными преемника
		cur.Key = succNode.Key
		cur.Value = succNode.Value

		// Удаляем преемника из правого поддерева
		newRight, _, err := t.deleteNode(ctx, cache, cur.Right, succNode.Key)
		if err != nil {
			return cid.Undef, false, err
		}

		cur.Right = newRight
	}

	// Балансируем узел после удаления
	balanced, cidNew, err := t.balanceNode(ctx, cache, cur)
	if err != nil {
		return cid.Undef, false, err
	}

	// Кэшируем результат
	cache[cidNew.String()] = balanced

	return cidNew, true, nil
}

// find ищет ключ в поддереве с корнем root.
// Использует итеративный подход для оптимизации производительности.
// Возвращает значение, признак наличия ключа и ошибку.
func (t *Tree) find(ctx context.Context, cache nodeCache, root cid.Cid, key string) (cid.Cid, bool, error) {
	// Начинаем поиск с корня
	currentCID := root

	// Итеративно спускаемся по дереву
	for currentCID.Defined() {
		// Загружаем текущий узел
		current, err := t.loadNode(ctx, cache, currentCID)
		if err != nil {
			return cid.Undef, false, err
		}

		// Сравниваем ключи и определяем следующий шаг
		switch cmp := strings.Compare(key, current.Key); {
		case cmp == 0:
			// Ключ найден
			return current.Value, true, nil
		case cmp < 0:
			// Ключ меньше текущего - идём влево
			currentCID = current.Left
		default:
			// Ключ больше текущего - идём вправо
			currentCID = current.Right
		}
	}

	// Дошли до конца и не нашли ключ
	return cid.Undef, false, nil
}

// collectRange собирает все пары ключ-значение в диапазоне [start, end] в поддереве с корнем root.
// Использует in-order traversal для получения ключей в отсортированном порядке.
// Пустые границы start или end означают отсутствие соответствующего ограничения.
func (t *Tree) collectRange(ctx context.Context, cache nodeCache, root cid.Cid, start, end string, out *[]Entry) error {
	// Базовый случай: пустое поддерево
	if !root.Defined() {
		return nil
	}

	// Загружаем текущий узел
	current, err := t.loadNode(ctx, cache, root)
	if err != nil {
		return err
	}

	// Рекурсивно обходим левое поддерево, если текущий ключ больше start
	if start == "" || strings.Compare(start, current.Key) <= 0 {
		if err := t.collectRange(ctx, cache, current.Left, start, end, out); err != nil {
			return err
		}
	}

	// Добавляем текущий узел, если он попадает в диапазон
	if (start == "" || strings.Compare(start, current.Key) <= 0) && (end == "" || strings.Compare(current.Key, end) <= 0) {
		*out = append(*out, Entry{Key: current.Key, Value: current.Value})
	}

	// Рекурсивно обходим правое поддерево, если текущий ключ меньше end
	if end == "" || strings.Compare(current.Key, end) < 0 {
		if err := t.collectRange(ctx, cache, current.Right, start, end, out); err != nil {
			return err
		}
	}

	return nil
}

// balanceNode балансирует узел и возвращает новый сбалансированный узел и его CID.
// Это ключевая функция для поддержания свойств AVL-дерева.
// Выполняет необходимые ротации, если баланс-фактор нарушен.
func (t *Tree) balanceNode(ctx context.Context, cache nodeCache, n *node) (*node, cid.Cid, error) {
	// Сначала обновляем метаданные узла (высоту и хеш)
	if err := t.updateNodeMetadata(ctx, cache, n); err != nil {
		return nil, cid.Undef, err
	}

	// Вычисляем баланс-фактор (разность высот левого и правого поддеревьев)
	balance, err := t.balanceFactor(ctx, cache, n)
	if err != nil {
		return nil, cid.Undef, err
	}

	// Случай левого дисбаланса (левое поддерево слишком высокое)
	if balance > 1 {
		// Загружаем левый узел для анализа
		leftNode, err := t.loadNode(ctx, cache, n.Left)
		if err != nil {
			return nil, cid.Undef, err
		}

		// Определяем баланс левого узла
		leftBal, err := t.balanceFactor(ctx, cache, leftNode)
		if err != nil {
			return nil, cid.Undef, err
		}

		// Случай Left-Right: сначала левый поворот вокруг левого узла
		if leftBal < 0 {
			leftClone := cloneNode(leftNode)
			rotated, rotatedCID, err := t.rotateLeft(ctx, cache, leftClone)
			if err != nil {
				return nil, cid.Undef, err
			}
			cache[rotatedCID.String()] = rotated
			n.Left = rotatedCID
		}

		// Затем правый поворот вокруг текущего узла
		rotated, rotatedCID, err := t.rotateRight(ctx, cache, n)
		if err != nil {
			return nil, cid.Undef, err
		}

		cache[rotatedCID.String()] = rotated
		return rotated, rotatedCID, nil
	}

	// Случай правого дисбаланса (правое поддерево слишком высокое)
	if balance < -1 {
		// Загружаем правый узел для анализа
		rightNode, err := t.loadNode(ctx, cache, n.Right)
		if err != nil {
			return nil, cid.Undef, err
		}

		// Определяем баланс правого узла
		rightBal, err := t.balanceFactor(ctx, cache, rightNode)
		if err != nil {
			return nil, cid.Undef, err
		}

		// Случай Right-Left: сначала правый поворот вокруг правого узла
		if rightBal > 0 {
			rightClone := cloneNode(rightNode)
			rotated, rotatedCID, err := t.rotateRight(ctx, cache, rightClone)
			if err != nil {
				return nil, cid.Undef, err
			}
			cache[rotatedCID.String()] = rotated
			n.Right = rotatedCID
		}

		// Затем левый поворот вокруг текущего узла
		rotated, rotatedCID, err := t.rotateLeft(ctx, cache, n)
		if err != nil {
			return nil, cid.Undef, err
		}

		cache[rotatedCID.String()] = rotated
		return rotated, rotatedCID, nil
	}

	// Узел уже сбалансирован - просто сохраняем его
	cidNew, stored, err := t.storeNode(ctx, cache, n)
	if err != nil {
		return nil, cid.Undef, err
	}

	cache[cidNew.String()] = stored
	return stored, cidNew, nil
}

// rotateLeft выполняет левый поворот вокруг узла x.
// Левый поворот используется для исправления правого дисбаланса в AVL-дереве.
//
//     x                y
//    / \              / \
//   A   y     =>     x   C
//      / \          / \
//     B   C        A   B
//
func (t *Tree) rotateLeft(ctx context.Context, cache nodeCache, x *node) (*node, cid.Cid, error) {
	// Проверяем, что у узла есть правый ребёнок
	if !x.Right.Defined() {
		return x, cid.Undef, errors.New("mst: rotateLeft without right child")
	}

	// Загружаем правый узел (y)
	yNode, err := t.loadNode(ctx, cache, x.Right)
	if err != nil {
		return nil, cid.Undef, err
	}

	// Клонируем узлы для модификации
	y := cloneNode(yNode)
	xClone := cloneNode(x)
	
	// Выполняем поворот: правый узел y становится новым корнем,
	// левое поддерево y (B) становится правым поддеревом x
	xClone.Right = y.Left

	// Сохраняем модифицированный узел x
	xCID, xStored, err := t.storeNode(ctx, cache, xClone)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[xCID.String()] = xStored

	// Узел x становится левым ребёнком y
	y.Left = xCID

	// Сохраняем новый корень y
	yCID, yStored, err := t.storeNode(ctx, cache, y)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[yCID.String()] = yStored

	return yStored, yCID, nil
}

// rotateRight выполняет правый поворот вокруг узла y.
// Правый поворот используется для исправления левого дисбаланса в AVL-дереве.
//
//       y              x
//      / \            / \
//     x   C    =>    A   y
//    / \                / \
//   A   B              B   C
//
func (t *Tree) rotateRight(ctx context.Context, cache nodeCache, y *node) (*node, cid.Cid, error) {
	// Проверяем, что у узла есть левый ребёнок
	if !y.Left.Defined() {
		return y, cid.Undef, errors.New("mst: rotateRight without left child")
	}

	// Загружаем левый узел (x)
	xNode, err := t.loadNode(ctx, cache, y.Left)
	if err != nil {
		return nil, cid.Undef, err
	}

	// Клонируем узлы для модификации
	x := cloneNode(xNode)
	yClone := cloneNode(y)
	
	// Выполняем поворот: левый узел x становится новым корнем,
	// правое поддерево x (B) становится левым поддеревом y
	yClone.Left = x.Right

	// Сохраняем модифицированный узел y
	yCID, yStored, err := t.storeNode(ctx, cache, yClone)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[yCID.String()] = yStored

	// Узел y становится правым ребёнком x
	x.Right = yCID

	// Сохраняем новый корень x
	xCID, xStored, err := t.storeNode(ctx, cache, x)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[xCID.String()] = xStored

	return xStored, xCID, nil
}

// balanceFactor возвращает баланс-фактор узла.
// Баланс-фактор = высота левого поддерева - высота правого поддерева.
// Для AVL-дерева этот фактор должен быть в диапазоне [-1, 0, 1].
func (t *Tree) balanceFactor(ctx context.Context, cache nodeCache, n *node) (int, error) {
	// Получаем высоты левого и правого поддеревьев
	leftHeight, err := t.childHeight(ctx, cache, n.Left)
	if err != nil {
		return 0, err
	}

	rightHeight, err := t.childHeight(ctx, cache, n.Right)
	if err != nil {
		return 0, err
	}

	// Возвращаем разность высот
	return leftHeight - rightHeight, nil
}

// childHeight возвращает высоту дочернего узла по его CID.
// Для несуществующих узлов (cid.Undef) возвращает 0.
func (t *Tree) childHeight(ctx context.Context, cache nodeCache, cid cid.Cid) (int, error) {
	// Пустое поддерево имеет высоту 0
	if !cid.Defined() {
		return 0, nil
	}

	// Загружаем узел и возвращаем его высоту
	child, err := t.loadNode(ctx, cache, cid)
	if err != nil {
		return 0, err
	}

	return child.Height, nil
}

// minNode находит узел с минимальным ключом в поддереве с корнем root.
// Это всегда самый левый узел в поддереве (нет левых детей).
// Используется при удалении узлов с двумя детьми для поиска преемника.
// Возвращает его CID, узел и ошибку.
func (t *Tree) minNode(ctx context.Context, cache nodeCache, root cid.Cid) (cid.Cid, *node, error) {
	// Проверяем, что поддерево не пустое
	if !root.Defined() {
		return cid.Undef, nil, errors.New("mst: empty subtree")
	}

	// Начинаем с корня
	currentCID := root

	// Итеративно идём влево до конца
	for {
		// Загружаем текущий узел
		current, err := t.loadNode(ctx, cache, currentCID)
		if err != nil {
			return cid.Undef, nil, err
		}

		// Если нет левого ребёнка, то это минимальный узел
		if !current.Left.Defined() {
			return currentCID, current, nil
		}

		// Иначе переходим к левому ребёнку
		currentCID = current.Left
	}
}

// loadNode загружает узел по CID, используя кэш для оптимизации.
// Сначала проверяет кэш, и только если узла там нет, обращается к blockstore.
// Это критично для производительности, так как избегает повторных дорогих операций I/O.
func (t *Tree) loadNode(ctx context.Context, cache nodeCache, id cid.Cid) (*node, error) {
	// Проверяем корректность CID
	if !id.Defined() {
		return nil, errors.New("mst: undefined cid")
	}

	// Сначала проверяем кэш
	if nd, ok := cache[id.String()]; ok {
		return nd, nil
	}

	// Если в кэше нет, загружаем из blockstore
	dm, err := t.bs.GetNode(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("mst: load node %s: %w", id, err)
	}

	// Преобразуем из IPLD datamodel в наш внутренний формат
	nd, err := t.nodeFromNode(dm)
	if err != nil {
		return nil, err
	}

	// Кэшируем загруженный узел для последующего использования
	cache[id.String()] = nd

	return nd, nil
}

// storeNode сохраняет узел в blockstore и возвращает его CID и клонированный узел.
// Перед сохранением обновляет метаданные узла (высоту и хеш).
// Из-за иммутабельности IPLD, каждое сохранение создаёт новый блок.
func (t *Tree) storeNode(ctx context.Context, cache nodeCache, n *node) (cid.Cid, *node, error) {
	// Обновляем метаданные перед сохранением
	if err := t.updateNodeMetadata(ctx, cache, n); err != nil {
		return cid.Undef, nil, err
	}

	// Преобразуем из внутреннего формата в IPLD datamodel
	dm, err := t.nodeToNode(n)
	if err != nil {
		return cid.Undef, nil, err
	}

	// Сохраняем в blockstore
	c, err := t.bs.PutNode(ctx, dm)
	if err != nil {
		return cid.Undef, nil, fmt.Errorf("mst: store node: %w", err)
	}

	// Клонируем узел для возврата
	stored := cloneNode(n)

	// Кэшируем сохранённый узел
	cache[c.String()] = stored

	return c, stored, nil
}

// updateNodeMetadata обновляет высоту и хеш узла на основе его детей.
// Высота узла = 1 + максимум высот детей (для AVL-балансировки).
// Хеш вычисляется от ключа, значения и хешей детей (для целостности Merkle-дерева).
func (t *Tree) updateNodeMetadata(ctx context.Context, cache nodeCache, n *node) error {
	// Получаем высоты и хеши левого и правого детей
	leftHeight, leftHash, err := t.childHeightAndHash(ctx, cache, n.Left)
	if err != nil {
		return err
	}

	rightHeight, rightHash, err := t.childHeightAndHash(ctx, cache, n.Right)
	if err != nil {
		return err
	}

	// Обновляем высоту: 1 + максимум высот детей
	n.Height = 1 + max(leftHeight, rightHeight)

	// Вычисляем криптографический хеш узла с использованием BLAKE3
	h := blake3.New(32, nil)
	h.Write([]byte(n.Key))          // Включаем ключ
	h.Write(n.Value.Bytes())        // Включаем байты CID значения
	if len(leftHash) > 0 {
		h.Write(leftHash)           // Включаем хеш левого ребёнка, если он есть
	}
	if len(rightHash) > 0 {
		h.Write(rightHash)          // Включаем хеш правого ребёнка, если он есть
	}

	// Сохраняем финальный хеш
	n.Hash = h.Sum(nil)

	return nil
}

// childHeightAndHash возвращает высоту и хеш дочернего узла по его CID.
// For несуществующих детей возвращает (0, nil, nil).
func (t *Tree) childHeightAndHash(ctx context.Context, cache nodeCache, id cid.Cid) (int, []byte, error) {
	// Пустой ребёнок имеет высоту 0 и не вносит вклад в хеш
	if !id.Defined() {
		return 0, nil, nil
	}

	// Загружаем узел
	nd, err := t.loadNode(ctx, cache, id)
	if err != nil {
		return 0, nil, err
	}

	// Возвращаем высоту и хеш
	return nd.Height, nd.Hash, nil
}

// nodeToNode преобразует внутреннее представление узла в datamodel.Node.
// Создаёт структуру данных, совместимую с IPLD, для сохранения в blockstore.
// Поля сериализуются в следующем формате:
// - key: строка
// - value: CID-ссылка на данные
// - height: целое число (для AVL-балансировки)
// - hash: байтовый массив (для целостности)
// - left: CID-ссылка на левого ребёнка (опционально)
// - right: CID-ссылка на правого ребёнка (опционально)
func (t *Tree) nodeToNode(n *node) (datamodel.Node, error) {
	// Вычисляем размер карты (обязательные поля + опциональные дети)
	size := int64(4) // key, value, height, hash - всегда присутствуют
	if n.Left.Defined() {
		size++
	}
	if n.Right.Defined() {
		size++
	}

	// Создаём построитель карты
	builder := basicnode.Prototype.Map.NewBuilder()
	ma, err := builder.BeginMap(size)
	if err != nil {
		return nil, err
	}

	// Добавляем ключ
	entry, err := ma.AssembleEntry("key")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignString(n.Key); err != nil {
		return nil, err
	}

	// Добавляем значение как CID-ссылку
	entry, err = ma.AssembleEntry("value")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignLink(cidlink.Link{Cid: n.Value}); err != nil {
		return nil, err
	}

	// Добавляем высоту
	entry, err = ma.AssembleEntry("height")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignInt(int64(n.Height)); err != nil {
		return nil, err
	}

	// Добавляем хеш
	entry, err = ma.AssembleEntry("hash")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignBytes(n.Hash); err != nil {
		return nil, err
	}

	// Добавляем левого ребёнка, если он есть
	if n.Left.Defined() {
		entry, err := ma.AssembleEntry("left")
		if err != nil {
			return nil, err
		}
		if err := entry.AssignLink(cidlink.Link{Cid: n.Left}); err != nil {
			return nil, err
		}
	}

	// Добавляем правого ребёнка, если он есть
	if n.Right.Defined() {
		entry, err := ma.AssembleEntry("right")
		if err != nil {
			return nil, err
		}
		if err := entry.AssignLink(cidlink.Link{Cid: n.Right}); err != nil {
			return nil, err
		}
	}

	// Завершаем построение карты
	if err := ma.Finish(); err != nil {
		return nil, err
	}

	return builder.Build(), nil
}

// nodeFromNode преобразует datamodel.Node в внутреннее представление узла.
// Парсит данные, загруженные из blockstore, в удобную для работы структуру.
// Выполняет проверку корректности всех обязательных полей.
func (t *Tree) nodeFromNode(dm datamodel.Node) (*node, error) {
	// Извлекаем ключ (обязательное поле)
	keyNode, err := dm.LookupByString("key")
	if err != nil {
		return nil, fmt.Errorf("mst: node missing key: %w", err)
	}
	key, err := keyNode.AsString()
	if err != nil {
		return nil, fmt.Errorf("mst: invalid key: %w", err)
	}

	// Извлекаем значение как CID-ссылку (обязательное поле)
	valueNode, err := dm.LookupByString("value")
	if err != nil {
		return nil, fmt.Errorf("mst: node missing value: %w", err)
	}
	link, err := valueNode.AsLink()
	if err != nil {
		return nil, fmt.Errorf("mst: invalid value link: %w", err)
	}
	valueLink, ok := link.(cidlink.Link)
	if !ok {
		return nil, errors.New("mst: unexpected link type")
	}

	// Извлекаем высоту (обязательное поле)
	heightNode, err := dm.LookupByString("height")
	if err != nil {
		return nil, fmt.Errorf("mst: node missing height: %w", err)
	}
	heightVal, err := heightNode.AsInt()
	if err != nil {
		return nil, fmt.Errorf("mst: invalid height: %w", err)
	}

	// Извлекаем хеш (обязательное поле)
	hashNode, err := dm.LookupByString("hash")
	if err != nil {
		return nil, fmt.Errorf("mst: node missing hash: %w", err)
	}
	hashBytes, err := hashNode.AsBytes()
	if err != nil {
		return nil, fmt.Errorf("mst: invalid hash: %w", err)
	}

	// Извлекаем CID левого ребёнка (опциональное поле)
	leftCID := cid.Undef
	if leftNode, err := dm.LookupByString("left"); err == nil {
		link, err := leftNode.AsLink()
		if err == nil {
			if lnk, ok := link.(cidlink.Link); ok {
				leftCID = lnk.Cid
			}
		}
	}

	// Извлекаем CID правого ребёнка (опциональное поле)
	rightCID := cid.Undef
	if rightNode, err := dm.LookupByString("right"); err == nil {
		link, err := rightNode.AsLink()
		if err == nil {
			if lnk, ok := link.(cidlink.Link); ok {
				rightCID = lnk.Cid
			}
		}
	}

	// Создаём и возвращаем узел с копией хеша
	return &node{
		Entry: Entry{
			Key:   key,
			Value: valueLink.Cid,
		},
		Left:   leftCID,
		Right:  rightCID,
		Height: int(heightVal),
		Hash:   append([]byte(nil), hashBytes...), // Создаём копию слайса
	}, nil
}

// cloneNode делает поверхностную копию узла.
// Необходимо из-за иммутабельности данных в IPLD - мы не можем изменять
// загруженные узлы напрямую, поэтому создаём их копии.
func cloneNode(n *node) *node {
	// Проверяем на nil
	if n == nil {
		return nil
	}

	// Создаём копию хеша, если он есть
	var hashCopy []byte
	if len(n.Hash) > 0 {
		hashCopy = append([]byte{}, n.Hash...)
	}

	// Создаём новый узел с теми же данными
	return &node{
		Entry:  n.Entry,  // Entry содержит простые типы, поэтому копируется по значению
		Left:   n.Left,   // CID - неизменяемый тип
		Right:  n.Right,  // CID - неизменяемый тип
		Height: n.Height, // Простое значение
		Hash:   hashCopy, // Копия слайса байт
	}
}

// max возвращает максимум из двух целых чисел.
// Используется для вычисления высоты узла в AVL-дереве.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}