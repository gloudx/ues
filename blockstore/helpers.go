package blockstore

import (
	"context"
	"errors"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	bindnode "github.com/ipld/go-ipld-prime/node/bindnode"
	"github.com/ipld/go-ipld-prime/schema"
)

// PutStruct сохраняет Go структуру как IPLD узел с type binding.
// Использует bindnode для автоматического преобразования Go типов в IPLD datamodel
// с поддержкой schema validation и type safety.
//
// Параметры:
//   - ctx: контекст операции
//   - bs: blockstore для сохранения
//   - v: указатель на Go структуру для сериализации
//   - ts: type system с определением схем данных
//   - typ: schema type для валидации и структурирования
//   - lp: link prototype для настройки CID параметров
func PutStruct[T any](ctx context.Context, bs *blockstore, v *T, ts *schema.TypeSystem, typ schema.Type, lp cidlink.LinkPrototype) (cid.Cid, error) {
	// Оборачиваем Go структуру в IPLD узел через bindnode
	n := bindnode.Wrap(v, typ)
	// Сохраняем узел через стандартный PutNode
	return bs.PutNode(ctx, n)
}

// GetStruct загружает IPLD узел и десериализует в Go структуру.
// Обеспечивает type-safe доступ к структурированным данным с автоматической
// десериализацией и валидацией схемы данных.
func GetStruct[T any](bs *blockstore, ctx context.Context, c cid.Cid, ts *schema.TypeSystem, typ schema.Type) (*T, error) {
	if bs.lsys == nil {
		return nil, errors.New("link system is nil")
	}
	var out *T
	var ok bool
	lnk := cidlink.Link{Cid: c}
	// Загружаем узел с bindnode прототипом для type binding
	n, err := bs.lsys.Load(ipld.LinkContext{Ctx: ctx}, lnk, bindnode.Prototype(out, typ))
	if err != nil {
		return nil, err
	}
	// Извлекаем Go структуру из IPLD узла
	w := bindnode.Unwrap(n)
	out, ok = w.(*T)
	if !ok {
		return nil, errors.New("bindnode: type assertion failed")
	}
	return out, nil
}
