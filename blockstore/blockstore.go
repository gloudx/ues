package blockstore

import (
	"context"
	"errors"
	"io"
	s "pds/datastore"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ipfs/boxo/blockservice"
	bstor "github.com/ipfs/boxo/blockstore"
	chunker "github.com/ipfs/boxo/chunker"
	"github.com/ipfs/boxo/files"
	"github.com/ipfs/boxo/ipld/merkledag"
	unixfile "github.com/ipfs/boxo/ipld/unixfs/file"
	imp "github.com/ipfs/boxo/ipld/unixfs/importer"
	ufsio "github.com/ipfs/boxo/ipld/unixfs/io"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	format "github.com/ipfs/go-ipld-format"
	carv2 "github.com/ipld/go-car/v2"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/linking"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	bindnode "github.com/ipld/go-ipld-prime/node/bindnode"
	"github.com/ipld/go-ipld-prime/schema"
	"github.com/ipld/go-ipld-prime/storage/bsrvadapter"
	traversal "github.com/ipld/go-ipld-prime/traversal"
	selector "github.com/ipld/go-ipld-prime/traversal/selector"
	selb "github.com/ipld/go-ipld-prime/traversal/selector/builder"
	"github.com/multiformats/go-multihash"
)

const (
	DefaultChunkSize = 262144 // 256 KiB
	RabinMinSize     = DefaultChunkSize / 2
	RabinMaxSize     = DefaultChunkSize * 2
)

// LinkPrototype по умолчанию: CIDv1 + dag-cbor + BLAKE3
var DefaultLP = cidlink.LinkPrototype{
	Prefix: cid.Prefix{
		Version:  1,
		Codec:    uint64(cid.DagCBOR),
		MhType:   uint64(multihash.BLAKE3),
		MhLength: -1,
	},
}

type Blockstore interface {
	bstor.Blockstore
	bstor.Viewer
	io.Closer

	// PutNode сохраняет любой ipld-prime узел через LinkSystem.Store.
	PutNode(ctx context.Context, n datamodel.Node, lp cidlink.LinkPrototype) (cid.Cid, error)

	// GetNodeAny загружает узел как generic Any (basicnode).
	GetNodeAny(ctx context.Context, c cid.Cid) (datamodel.Node, error)

	// AddFile добавляет файл с использованием UnixFS
	AddFile(ctx context.Context, data io.Reader, useRabin bool) (cid.Cid, error)

	// GetFile получает файл через UnixFS
	GetFile(ctx context.Context, c cid.Cid) (files.Node, error)

	// GetReader возвращает Reader для файла (поддерживает большие чанкованные файлы)
	GetReader(ctx context.Context, c cid.Cid) (io.ReadSeekCloser, error)

	// Walk обходит весь подграф от root (как GetSubgraph, но без сбора CIDs)
	Walk(ctx context.Context, root cid.Cid, visit func(p traversal.Progress, n datamodel.Node) error) error

	// GetSubgraph обходит подграф по (ipld.Node) селектору, возвращает посещённые CIDs.
	GetSubgraph(ctx context.Context, root cid.Cid, selectorNode datamodel.Node) ([]cid.Cid, error)

	// Prefetch прогревает кэш Blockstore по подграфу, N воркеров.
	Prefetch(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, workers int) error

	// ExportCARV2 пишет CARv2 (с индексом по умолчанию) в io.Writer.
	// selectorNode — ipld.Node (узел-селектора), например из BuildSelectorNodeExploreAll().
	ExportCARV2(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, w io.Writer, opts ...carv2.WriteOption) error

	// ImportCARV2 читает CAR (v1 или v2) из r и кладёт блоки в Blockstore.
	// Возвращает корни из заголовка.
	ImportCARV2(ctx context.Context, r io.Reader, opts ...carv2.ReadOption) ([]cid.Cid, error)
}

// Blockstore реализует IPFS блокстор с MST бэкендом и индексатором
type blockstore struct {
	bstor.Blockstore
	lsys  *linking.LinkSystem
	bS    blockservice.BlockService
	dS    format.DAGService
	mu    sync.RWMutex
	cache *lru.Cache[string, blocks.Block]
}

// Проверяем что интерфейсы реализованы правильно
var _ Blockstore = (*blockstore)(nil)

func NewBlockstore(ds s.Datastore) *blockstore {

	base := bstor.NewBlockstore(ds)
	bs := &blockstore{
		Blockstore: base,
	}

	cache, _ := lru.New[string, blocks.Block](1000) // Кеш на 1000 блоков
	bs.cache = cache

	bs.mu = sync.RWMutex{}

	// Сервисы
	bs.bS = blockservice.New(bs.Blockstore, nil)
	bs.dS = merkledag.NewDAGService(bs.bS)

	// LinkSystem поверх blockservice
	adapter := &bsrvadapter.Adapter{Wrapped: bs.bS}
	lS := cidlink.DefaultLinkSystem()
	lS.SetWriteStorage(adapter)
	lS.SetReadStorage(adapter)
	bs.lsys = &lS

	return bs
}

func (bs *blockstore) cacheBlock(b blocks.Block) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.cache == nil {
		return
	}
	bs.cache.Add(b.Cid().String(), b)
}

func (bs *blockstore) cacheGet(key string) (blocks.Block, bool) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	if bs.cache == nil {
		return nil, false
	}
	return bs.cache.Get(key)
}

// Put сохраняет блок
func (bs *blockstore) Put(ctx context.Context, block blocks.Block) error {
	if err := bs.Blockstore.Put(ctx, block); err != nil {
		return err
	}
	bs.cacheBlock(block)
	return nil
}

func (bs *blockstore) PutMany(ctx context.Context, blks []blocks.Block) error {
	if err := bs.Blockstore.PutMany(ctx, blks); err != nil {
		return err
	}
	for _, b := range blks {
		bs.cacheBlock(b)
	}
	return nil
}

// PutNode сохраняет любой ipld-prime узел через LinkSystem.Store.
func (bs *blockstore) PutNode(ctx context.Context, n datamodel.Node, lp cidlink.LinkPrototype) (cid.Cid, error) {
	if bs.lsys == nil {
		return cid.Undef, errors.New("links system is nil")
	}
	lnk, err := bs.lsys.Store(ipld.LinkContext{Ctx: ctx}, lp, n)
	if err != nil {
		return cid.Undef, err
	}
	c := lnk.(cidlink.Link).Cid
	// Кэшируем сырые байты тоже, чтобы Get() быстрее отдавал.
	// Для этого перезагрузим как raw через lsys.ReadStorage? Не обязательно:
	// блок уже в нижнем storage через adapter. Кэш заполняется при обычных Put/Get.
	return c, nil
}

// Get получает блок
func (bs *blockstore) Get(ctx context.Context, c cid.Cid) (blocks.Block, error) {
	if blk, ok := bs.cacheGet(c.String()); ok {
		return blk, nil
	}
	blk, err := bs.Blockstore.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	bs.cacheBlock(blk)
	return blk, nil
}

// GetNodeAny загружает узел как generic Any (basicnode).
func (bs *blockstore) GetNodeAny(ctx context.Context, c cid.Cid) (datamodel.Node, error) {
	if bs.lsys == nil {
		return nil, errors.New("link system is nil")
	}
	lnk := cidlink.Link{Cid: c}
	return bs.lsys.Load(ipld.LinkContext{Ctx: ctx}, lnk, basicnode.Prototype.Any)
}

// DeleteBlock удаляет блок
// DeleteBlock удаляет блок
func (bs *blockstore) DeleteBlock(ctx context.Context, c cid.Cid) error {
	if err := bs.Blockstore.DeleteBlock(ctx, c); err != nil {
		return err
	}
	bs.mu.Lock()
	if bs.cache != nil {
		// В v2 есть Remove(K) bool — удаляем из кэша явно
		bs.cache.Remove(c.String())
	}
	bs.mu.Unlock()
	return nil
}

// AddFile добавляет файл с использованием UnixFS
func (bs *blockstore) AddFile(ctx context.Context, data io.Reader, useRabin bool) (cid.Cid, error) {
	var spl chunker.Splitter
	if useRabin {
		spl = chunker.NewRabinMinMax(data, RabinMinSize, DefaultChunkSize, RabinMaxSize)
	} else {
		spl = chunker.NewSizeSplitter(data, DefaultChunkSize)
	}
	nd, err := imp.BuildDagFromReader(bs.dS, spl)
	if err != nil {
		return cid.Undef, err
	}
	return nd.Cid(), nil
}

// GetFile получает файл через UnixFS
func (bs *blockstore) GetFile(ctx context.Context, c cid.Cid) (files.Node, error) {
	nd, err := bs.dS.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	return unixfile.NewUnixfsFile(ctx, bs.dS, nd)
}

// GetReader возвращает Reader для файла (поддерживает большие чанкованные файлы)
func (bs *blockstore) GetReader(ctx context.Context, c cid.Cid) (io.ReadSeekCloser, error) {
	nd, err := bs.dS.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	return ufsio.NewDagReader(ctx, nd, bs.dS)
}

// View
func (bs *blockstore) View(ctx context.Context, id cid.Cid, callback func([]byte) error) error {
	if v, ok := bs.Blockstore.(bstor.Viewer); ok {
		return v.View(ctx, id, callback)
	}
	blk, err := bs.Blockstore.Get(ctx, id)
	if err != nil {
		return err
	}
	return callback(blk.RawData())
}

// BuildSelectorNodeExploreAll — узел-селектор (ipld.Node) «обойти весь подграф».
func BuildSelectorNodeExploreAll() datamodel.Node {
	sb := selb.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return sb.
		ExploreRecursive(selector.RecursionLimitNone(),
			sb.ExploreAll(sb.ExploreRecursiveEdge()),
		).Node()
}

// CompileSelector — из узла-селектора получаем selector.Selector (для Walk/GetSubgraph).
func CompileSelector(n datamodel.Node) (selector.Selector, error) {
	return selector.CompileSelector(n)
}

// Walk — фикс, использует узел-селектор (ipld.Node → selector.Selector).
func (bs *blockstore) Walk(ctx context.Context, root cid.Cid, visit func(p traversal.Progress, n datamodel.Node) error) error {
	if bs.lsys == nil {
		return errors.New("link system is nil")
	}
	start, err := bs.lsys.Load(ipld.LinkContext{Ctx: ctx}, cidlink.Link{Cid: root}, basicnode.Prototype.Any)
	if err != nil {
		return err
	}
	spec := BuildSelectorNodeExploreAll()
	sel, err := CompileSelector(spec)
	if err != nil {
		return err
	}
	cfg := traversal.Config{
		LinkSystem: *bs.lsys,
		LinkTargetNodePrototypeChooser: func(ipld.Link, ipld.LinkContext) (datamodel.NodePrototype, error) {
			return basicnode.Prototype.Any, nil
		},
	}
	return traversal.Progress{Cfg: &cfg}.WalkMatching(start, sel, visit)
}

// Close закрывает блокстор
func (bs *blockstore) Close() error {
	// Здесь закрывать нечего: lru не требует Close, blockservice/DAGService — тоже.
	// Закрытие базового Datastore делайте на уровне, где он создан (s.Datastore.Close()).
	return nil
}

// PutStruct — ок
func PutStruct[T any](ctx context.Context, bs *blockstore, v *T, ts *schema.TypeSystem, typ schema.Type, lp cidlink.LinkPrototype) (cid.Cid, error) {
	n := bindnode.Wrap(v, typ)
	return bs.PutNode(ctx, n, lp)
}

// GetStruct — фикс
func GetStruct[T any](bs *blockstore, ctx context.Context, c cid.Cid, ts *schema.TypeSystem, typ schema.Type) (*T, error) {
	if bs.lsys == nil {
		return nil, errors.New("link system is nil")
	}
	var out *T
	var ok bool
	lnk := cidlink.Link{Cid: c}
	// Загружаем сразу в bindnode-прототип
	n, err := bs.lsys.Load(ipld.LinkContext{Ctx: ctx}, lnk, bindnode.Prototype(out, typ))
	if err != nil {
		return nil, err
	}
	w := bindnode.Unwrap(n)
	out, ok = w.(*T)
	if !ok {
		return nil, errors.New("bindnode: type assertion failed")
	}
	return out, nil
}

// BuildSelectorExploreAll: «весь подграф»
func BuildSelectorExploreAll() (selector.Selector, error) {
	ssb := selb.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	spec := ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge()),
	).Node()
	return selector.CompileSelector(spec)
}

// GetSubgraph — обходит подграф по (ipld.Node) селектору, возвращает посещённые CIDs.
func (bs *blockstore) GetSubgraph(ctx context.Context, root cid.Cid, selectorNode datamodel.Node) ([]cid.Cid, error) {
	start, err := bs.lsys.Load(ipld.LinkContext{Ctx: ctx}, cidlink.Link{Cid: root}, basicnode.Prototype.Any)
	if err != nil {
		return nil, err
	}
	sel, err := CompileSelector(selectorNode)
	if err != nil {
		return nil, err
	}

	cfg := traversal.Config{
		LinkSystem: *bs.lsys,
		LinkTargetNodePrototypeChooser: func(ipld.Link, ipld.LinkContext) (datamodel.NodePrototype, error) {
			return basicnode.Prototype.Any, nil
		},
	}
	out := make([]cid.Cid, 0, 1024)
	// включим корень явно
	out = append(out, root)
	err = traversal.Progress{Cfg: &cfg}.WalkMatching(start, sel, func(p traversal.Progress, n datamodel.Node) error {
		// Progress.LastBlock.Link заполняется, когда мы перешли по ссылке.
		if p.LastBlock.Link != nil {
			if cl, ok := p.LastBlock.Link.(cidlink.Link); ok {
				out = append(out, cl.Cid)
			}
		}
		return nil
	})
	return out, err
}

// Prefetch — прогревает кэш Blockstore по подграфу, N воркеров.
func (bs *blockstore) Prefetch(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, workers int) error {
	if workers <= 0 {
		workers = 8
	}
	cids, err := bs.GetSubgraph(ctx, root, selectorNode)
	if err != nil {
		return err
	}

	jobs := make(chan cid.Cid, workers*2)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for c := range jobs {
				_, _ = bs.Get(ctx, c) // прогрев
			}
		}()
	}
	for _, c := range cids {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- c:
		}
	}
	close(jobs)
	wg.Wait()
	return ctx.Err()
}

// ExportCARV2 пишет CARv2 (с индексом по умолчанию) в io.Writer.
// selectorNode — ipld.Node (узел-селектора), например из BuildSelectorNodeExploreAll().
func (bs *blockstore) ExportCARV2(ctx context.Context, root cid.Cid, selectorNode datamodel.Node, w io.Writer, opts ...carv2.WriteOption) error {
	if bs.lsys == nil {
		return errors.New("link system is nil")
	}
	// можно добавить: opts = append(opts, carv2.UseWholeCIDs(true)) — если нужно
	// carv2.WithIndex(carv2.DefaultIndexCodec) — явный индекс (обычно включён по умолчанию).
	// carv2.StoreIdentityCIDs(true) — если возможны identity-CID блоки.
	// carv2.UseWholeCIDs(true) — если хочешь хранить CID целиком в записи (увеличивает размер).
	writer, err := carv2.NewSelectiveWriter(ctx, bs.lsys, root, selectorNode, opts...)
	if err != nil {
		return err
	}
	_, err = writer.WriteTo(w)
	return err
}

// ImportCARV2 читает CAR (v1 или v2) из r и кладёт блоки в Blockstore.
// Возвращает корни из заголовка.
func (bs *blockstore) ImportCARV2(ctx context.Context, r io.Reader, opts ...carv2.ReadOption) ([]cid.Cid, error) {
	br, err := carv2.NewBlockReader(r, opts...)
	if err != nil {
		return nil, err
	}
	roots := br.Roots
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			blk, err := br.Next()
			if err == io.EOF {
				return roots, nil
			}
			if err != nil {
				return nil, err
			}
			if err := bs.Put(ctx, blk); err != nil {
				return nil, err
			}
		}
	}
}
