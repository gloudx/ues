package mst

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/datamodel"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	selector "github.com/ipld/go-ipld-prime/traversal/selector"
	selb "github.com/ipld/go-ipld-prime/traversal/selector/builder"
	"lukechampine.com/blake3"

	"ues/blockstore"
)

// Tree реализует Merkle Search Tree поверх Blockstore.
type Tree struct {
	bs      blockstore.Blockstore
	rootCID cid.Cid
	mu      sync.RWMutex
}

// Entry описывает пару ключ-значение, возвращаемую из MST.
type Entry struct {
	Key   string
	Value cid.Cid
}

// node — внутреннее представление узла MST.
type node struct {
	Entry
	//
	Left   cid.Cid
	Right  cid.Cid
	Height int
	Hash   []byte
}

// nodeCache кэширует узлы, считанные из blockstore, в рамках одной операции.
type nodeCache map[string]*node

// NewTree создаёт пустое дерево поверх предоставленного Blockstore.
func NewTree(bs blockstore.Blockstore) *Tree {
	return &Tree{bs: bs}
}

// Root возвращает CID текущего корня (cid.Undef для пустого дерева).
func (t *Tree) Root() cid.Cid {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.rootCID
}

// Load загружает дерево из Blockstore по корневому CID.
func (t *Tree) Load(ctx context.Context, root cid.Cid) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !root.Defined() {
		t.rootCID = cid.Undef
		return nil
	}
	if _, err := t.getNode(ctx, make(nodeCache), root); err != nil {
		return err
	}
	t.rootCID = root
	return nil
}

// Put вставляет или обновляет значение по ключу и возвращает новый корневой CID.
func (t *Tree) Put(ctx context.Context, key string, value cid.Cid) (cid.Cid, error) {

	if key == "" {
		return cid.Undef, errors.New("mst: empty key")
	}

	if !value.Defined() {
		return cid.Undef, errors.New("mst: undefined value CID")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cache := make(nodeCache)

	newRoot, _, err := t.putNode(ctx, cache, t.rootCID, key, value)
	if err != nil {
		return cid.Undef, err
	}

	t.rootCID = newRoot

	return newRoot, nil
}

// Delete удаляет ключ, возвращает новый корневой CID и признак, что ключ существовал.
func (t *Tree) Delete(ctx context.Context, key string) (cid.Cid, bool, error) {
	if key == "" {
		return cid.Undef, false, errors.New("mst: empty key")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cache := make(nodeCache)
	newRoot, removed, err := t.deleteNode(ctx, cache, t.rootCID, key)
	if err != nil {
		return cid.Undef, false, err
	}
	if !removed {
		return t.rootCID, false, nil
	}
	t.rootCID = newRoot
	return newRoot, true, nil
}

// Get возвращает значение по ключу.
func (t *Tree) Get(ctx context.Context, key string) (cid.Cid, bool, error) {
	t.mu.RLock()
	root := t.rootCID
	t.mu.RUnlock()

	cache := make(nodeCache)
	return t.find(ctx, cache, root, key)
}

// Range возвращает все элементы, чей ключ лежит в [start, end].
func (t *Tree) Range(ctx context.Context, start, end string) ([]Entry, error) {
	t.mu.RLock()
	root := t.rootCID
	t.mu.RUnlock()

	cache := make(nodeCache)
	var out []Entry
	if err := t.collectRange(ctx, cache, root, start, end, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// BuildSelector возвращает селектор для обхода MST.
func BuildSelector() (selector.Selector, error) {
	sb := selb.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	spec := sb.ExploreRecursive(selector.RecursionLimitNone(),
		sb.ExploreAll(sb.ExploreRecursiveEdge()),
	).Node()
	return selector.CompileSelector(spec)
}

func (t *Tree) putNode(ctx context.Context, cache nodeCache, root cid.Cid, key string, value cid.Cid) (cid.Cid, bool, error) {
	if !root.Defined() {
		nd := &node{Key: key, Value: value}
		cidNew, _, err := t.storeNode(ctx, cache, nd)
		return cidNew, true, err
	}

	current, err := t.getNode(ctx, cache, root)
	if err != nil {
		return cid.Undef, false, err
	}
	cur := cloneNode(current)

	var inserted bool
	switch cmp := strings.Compare(key, cur.Key); {
	case cmp == 0:
		cur.Value = value
	case cmp < 0:
		newLeft, ins, err := t.putNode(ctx, cache, cur.Left, key, value)
		if err != nil {
			return cid.Undef, false, err
		}
		cur.Left = newLeft
		inserted = ins
	default:
		newRight, ins, err := t.putNode(ctx, cache, cur.Right, key, value)
		if err != nil {
			return cid.Undef, false, err
		}
		cur.Right = newRight
		inserted = ins
	}

	balanced, cidNew, err := t.balanceNode(ctx, cache, cur)
	if err != nil {
		return cid.Undef, false, err
	}
	cache[cidNew.String()] = balanced
	return cidNew, inserted, nil
}

func (t *Tree) deleteNode(ctx context.Context, cache nodeCache, root cid.Cid, key string) (cid.Cid, bool, error) {
	if !root.Defined() {
		return cid.Undef, false, nil
	}

	current, err := t.getNode(ctx, cache, root)
	if err != nil {
		return cid.Undef, false, err
	}
	cur := cloneNode(current)

	switch cmp := strings.Compare(key, cur.Key); {
	case cmp < 0:
		newLeft, removed, err := t.deleteNode(ctx, cache, cur.Left, key)
		if err != nil {
			return cid.Undef, false, err
		}
		if !removed {
			return root, false, nil
		}
		cur.Left = newLeft
	case cmp > 0:
		newRight, removed, err := t.deleteNode(ctx, cache, cur.Right, key)
		if err != nil {
			return cid.Undef, false, err
		}
		if !removed {
			return root, false, nil
		}
		cur.Right = newRight
	default:
		if !cur.Left.Defined() && !cur.Right.Defined() {
			return cid.Undef, true, nil
		}
		if !cur.Left.Defined() {
			return cur.Right, true, nil
		}
		if !cur.Right.Defined() {
			return cur.Left, true, nil
		}
		_, succNode, err := t.minNode(ctx, cache, cur.Right)
		if err != nil {
			return cid.Undef, false, err
		}
		cur.Key = succNode.Key
		cur.Value = succNode.Value
		newRight, _, err := t.deleteNode(ctx, cache, cur.Right, succNode.Key)
		if err != nil {
			return cid.Undef, false, err
		}
		cur.Right = newRight
	}

	balanced, cidNew, err := t.balanceNode(ctx, cache, cur)
	if err != nil {
		return cid.Undef, false, err
	}
	cache[cidNew.String()] = balanced
	return cidNew, true, nil
}

func (t *Tree) find(ctx context.Context, cache nodeCache, root cid.Cid, key string) (cid.Cid, bool, error) {
	currentCID := root
	for currentCID.Defined() {
		current, err := t.getNode(ctx, cache, currentCID)
		if err != nil {
			return cid.Undef, false, err
		}
		switch cmp := strings.Compare(key, current.Key); {
		case cmp == 0:
			return current.Value, true, nil
		case cmp < 0:
			currentCID = current.Left
		default:
			currentCID = current.Right
		}
	}
	return cid.Undef, false, nil
}

func (t *Tree) collectRange(ctx context.Context, cache nodeCache, root cid.Cid, start, end string, out *[]Entry) error {
	if !root.Defined() {
		return nil
	}
	current, err := t.getNode(ctx, cache, root)
	if err != nil {
		return err
	}
	if start == "" || strings.Compare(start, current.Key) <= 0 {
		if err := t.collectRange(ctx, cache, current.Left, start, end, out); err != nil {
			return err
		}
	}
	if (start == "" || strings.Compare(start, current.Key) <= 0) && (end == "" || strings.Compare(current.Key, end) <= 0) {
		*out = append(*out, Entry{Key: current.Key, Value: current.Value})
	}
	if end == "" || strings.Compare(current.Key, end) < 0 {
		if err := t.collectRange(ctx, cache, current.Right, start, end, out); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tree) balanceNode(ctx context.Context, cache nodeCache, n *node) (*node, cid.Cid, error) {
	if err := t.updateNodeMetadata(ctx, cache, n); err != nil {
		return nil, cid.Undef, err
	}

	balance, err := t.balanceFactor(ctx, cache, n)
	if err != nil {
		return nil, cid.Undef, err
	}

	if balance > 1 {
		leftNode, err := t.getNode(ctx, cache, n.Left)
		if err != nil {
			return nil, cid.Undef, err
		}
		leftBal, err := t.balanceFactor(ctx, cache, leftNode)
		if err != nil {
			return nil, cid.Undef, err
		}
		if leftBal < 0 {
			leftClone := cloneNode(leftNode)
			rotated, rotatedCID, err := t.rotateLeft(ctx, cache, leftClone)
			if err != nil {
				return nil, cid.Undef, err
			}
			cache[rotatedCID.String()] = rotated
			n.Left = rotatedCID
		}
		rotated, rotatedCID, err := t.rotateRight(ctx, cache, n)
		if err != nil {
			return nil, cid.Undef, err
		}
		cache[rotatedCID.String()] = rotated
		return rotated, rotatedCID, nil
	}

	if balance < -1 {
		rightNode, err := t.getNode(ctx, cache, n.Right)
		if err != nil {
			return nil, cid.Undef, err
		}
		rightBal, err := t.balanceFactor(ctx, cache, rightNode)
		if err != nil {
			return nil, cid.Undef, err
		}
		if rightBal > 0 {
			rightClone := cloneNode(rightNode)
			rotated, rotatedCID, err := t.rotateRight(ctx, cache, rightClone)
			if err != nil {
				return nil, cid.Undef, err
			}
			cache[rotatedCID.String()] = rotated
			n.Right = rotatedCID
		}
		rotated, rotatedCID, err := t.rotateLeft(ctx, cache, n)
		if err != nil {
			return nil, cid.Undef, err
		}
		cache[rotatedCID.String()] = rotated
		return rotated, rotatedCID, nil
	}

	cidNew, stored, err := t.storeNode(ctx, cache, n)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[cidNew.String()] = stored
	return stored, cidNew, nil
}

func (t *Tree) rotateLeft(ctx context.Context, cache nodeCache, x *node) (*node, cid.Cid, error) {
	if !x.Right.Defined() {
		return x, cid.Undef, errors.New("mst: rotateLeft without right child")
	}

	yNode, err := t.getNode(ctx, cache, x.Right)
	if err != nil {
		return nil, cid.Undef, err
	}
	y := cloneNode(yNode)

	xClone := cloneNode(x)
	xClone.Right = y.Left
	xCID, xStored, err := t.storeNode(ctx, cache, xClone)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[xCID.String()] = xStored

	y.Left = xCID
	yCID, yStored, err := t.storeNode(ctx, cache, y)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[yCID.String()] = yStored
	return yStored, yCID, nil
}

func (t *Tree) rotateRight(ctx context.Context, cache nodeCache, y *node) (*node, cid.Cid, error) {
	if !y.Left.Defined() {
		return y, cid.Undef, errors.New("mst: rotateRight without left child")
	}

	xNode, err := t.getNode(ctx, cache, y.Left)
	if err != nil {
		return nil, cid.Undef, err
	}
	x := cloneNode(xNode)

	yClone := cloneNode(y)
	yClone.Left = x.Right
	yCID, yStored, err := t.storeNode(ctx, cache, yClone)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[yCID.String()] = yStored

	x.Right = yCID
	xCID, xStored, err := t.storeNode(ctx, cache, x)
	if err != nil {
		return nil, cid.Undef, err
	}
	cache[xCID.String()] = xStored
	return xStored, xCID, nil
}

func (t *Tree) balanceFactor(ctx context.Context, cache nodeCache, n *node) (int, error) {
	leftHeight, err := t.childHeight(ctx, cache, n.Left)
	if err != nil {
		return 0, err
	}
	rightHeight, err := t.childHeight(ctx, cache, n.Right)
	if err != nil {
		return 0, err
	}
	return leftHeight - rightHeight, nil
}

func (t *Tree) childHeight(ctx context.Context, cache nodeCache, cid cid.Cid) (int, error) {
	if !cid.Defined() {
		return 0, nil
	}
	child, err := t.getNode(ctx, cache, cid)
	if err != nil {
		return 0, err
	}
	return child.Height, nil
}

func (t *Tree) minNode(ctx context.Context, cache nodeCache, root cid.Cid) (cid.Cid, *node, error) {
	if !root.Defined() {
		return cid.Undef, nil, errors.New("mst: empty subtree")
	}
	currentCID := root
	for {
		current, err := t.getNode(ctx, cache, currentCID)
		if err != nil {
			return cid.Undef, nil, err
		}
		if !current.Left.Defined() {
			return currentCID, current, nil
		}
		currentCID = current.Left
	}
}

func (t *Tree) getNode(ctx context.Context, cache nodeCache, id cid.Cid) (*node, error) {
	if !id.Defined() {
		return nil, errors.New("mst: undefined cid")
	}
	if nd, ok := cache[id.String()]; ok {
		return nd, nil
	}
	dm, err := t.bs.GetNodeAny(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("mst: load node %s: %w", id, err)
	}
	nd, err := t.nodeFromNode(dm)
	if err != nil {
		return nil, err
	}
	cache[id.String()] = nd
	return nd, nil
}

func (t *Tree) storeNode(ctx context.Context, cache nodeCache, n *node) (cid.Cid, *node, error) {
	if err := t.updateNodeMetadata(ctx, cache, n); err != nil {
		return cid.Undef, nil, err
	}
	dm, err := t.nodeToNode(n)
	if err != nil {
		return cid.Undef, nil, err
	}
	c, err := t.bs.PutNode(ctx, dm, blockstore.DefaultLP)
	if err != nil {
		return cid.Undef, nil, fmt.Errorf("mst: store node: %w", err)
	}
	stored := cloneNode(n)
	cache[c.String()] = stored
	return c, stored, nil
}

func (t *Tree) updateNodeMetadata(ctx context.Context, cache nodeCache, n *node) error {
	leftHeight, leftHash, err := t.childHeightAndHash(ctx, cache, n.Left)
	if err != nil {
		return err
	}
	rightHeight, rightHash, err := t.childHeightAndHash(ctx, cache, n.Right)
	if err != nil {
		return err
	}
	n.Height = 1 + max(leftHeight, rightHeight)

	h := blake3.New(32, nil)
	h.Write([]byte(n.Key))
	h.Write(n.Value.Bytes())
	if len(leftHash) > 0 {
		h.Write(leftHash)
	}
	if len(rightHash) > 0 {
		h.Write(rightHash)
	}
	n.Hash = h.Sum(nil)
	return nil
}

func (t *Tree) childHeightAndHash(ctx context.Context, cache nodeCache, id cid.Cid) (int, []byte, error) {
	if !id.Defined() {
		return 0, nil, nil
	}
	nd, err := t.getNode(ctx, cache, id)
	if err != nil {
		return 0, nil, err
	}
	return nd.Height, nd.Hash, nil
}

func (t *Tree) nodeToNode(n *node) (datamodel.Node, error) {
	size := int64(4)
	if n.Left.Defined() {
		size++
	}
	if n.Right.Defined() {
		size++
	}

	builder := basicnode.Prototype.Map.NewBuilder()
	ma, err := builder.BeginMap(size)
	if err != nil {
		return nil, err
	}

	entry, err := ma.AssembleEntry("key")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignString(n.Key); err != nil {
		return nil, err
	}

	entry, err = ma.AssembleEntry("value")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignLink(cidlink.Link{Cid: n.Value}); err != nil {
		return nil, err
	}

	entry, err = ma.AssembleEntry("height")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignInt(int64(n.Height)); err != nil {
		return nil, err
	}

	entry, err = ma.AssembleEntry("hash")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignBytes(n.Hash); err != nil {
		return nil, err
	}

	if n.Left.Defined() {
		entry, err := ma.AssembleEntry("left")
		if err != nil {
			return nil, err
		}
		if err := entry.AssignLink(cidlink.Link{Cid: n.Left}); err != nil {
			return nil, err
		}
	}

	if n.Right.Defined() {
		entry, err := ma.AssembleEntry("right")
		if err != nil {
			return nil, err
		}
		if err := entry.AssignLink(cidlink.Link{Cid: n.Right}); err != nil {
			return nil, err
		}
	}

	if err := ma.Finish(); err != nil {
		return nil, err
	}
	return builder.Build(), nil
}

func (t *Tree) nodeFromNode(dm datamodel.Node) (*node, error) {
	keyNode, err := dm.LookupByString("key")
	if err != nil {
		return nil, fmt.Errorf("mst: node missing key: %w", err)
	}
	key, err := keyNode.AsString()
	if err != nil {
		return nil, fmt.Errorf("mst: invalid key: %w", err)
	}

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

	heightNode, err := dm.LookupByString("height")
	if err != nil {
		return nil, fmt.Errorf("mst: node missing height: %w", err)
	}
	heightVal, err := heightNode.AsInt()
	if err != nil {
		return nil, fmt.Errorf("mst: invalid height: %w", err)
	}

	hashNode, err := dm.LookupByString("hash")
	if err != nil {
		return nil, fmt.Errorf("mst: node missing hash: %w", err)
	}
	hashBytes, err := hashNode.AsBytes()
	if err != nil {
		return nil, fmt.Errorf("mst: invalid hash: %w", err)
	}

	leftCID := cid.Undef
	if leftNode, err := dm.LookupByString("left"); err == nil {
		link, err := leftNode.AsLink()
		if err == nil {
			if lnk, ok := link.(cidlink.Link); ok {
				leftCID = lnk.Cid
			}
		}
	}

	rightCID := cid.Undef
	if rightNode, err := dm.LookupByString("right"); err == nil {
		link, err := rightNode.AsLink()
		if err == nil {
			if lnk, ok := link.(cidlink.Link); ok {
				rightCID = lnk.Cid
			}
		}
	}

	return &node{
		Key:    key,
		Value:  valueLink.Cid,
		Left:   leftCID,
		Right:  rightCID,
		Height: int(heightVal),
		Hash:   append([]byte(nil), hashBytes...),
	}, nil
}

func cloneNode(n *node) *node {
	if n == nil {
		return nil
	}
	var hashCopy []byte
	if len(n.Hash) > 0 {
		hashCopy = append([]byte{}, n.Hash...)
	}
	return &node{
		Key:    n.Key,
		Value:  n.Value,
		Left:   n.Left,
		Right:  n.Right,
		Height: n.Height,
		Hash:   hashCopy,
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
