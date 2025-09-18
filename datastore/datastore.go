package datastore

import (
	"context"

	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
	badger4 "github.com/ipfs/go-ds-badger4"
	bds "github.com/ipfs/go-ds-badger4"
)

// Datastore ...
type Datastore interface {

	// Встраивание методов базового хранилища данных и его возможностей.
	ds.Datastore
	ds.BatchingFeature
	ds.TxnFeature
	ds.GCFeature
	ds.PersistentFeature
	ds.TTL

	// Iterator — итератор по ключам с префиксом.
	// Возвращает канал пар ключ-значение и канал ошибок (сигнализирует
	// ошибки из Query/Next/ctx.Done()).
	Iterator(ctx context.Context, prefix ds.Key, keysOnly bool) (<-chan KeyValue, <-chan error, error)

	// Merge ...
	Merge(ctx context.Context, other Datastore) error

	// Clear ...
	Clear(ctx context.Context) error

	//Keys ...
	Keys(ctx context.Context, prefix ds.Key) (<-chan ds.Key, <-chan error, error)
}

// KeyValue - это простая структура для хранения пары ключ-значение.
type KeyValue struct {
	Key   ds.Key
	Value []byte
}

var _ ds.Datastore = (*datastorage)(nil)
var _ ds.PersistentDatastore = (*datastorage)(nil)
var _ ds.TxnDatastore = (*datastorage)(nil)
var _ ds.TTLDatastore = (*datastorage)(nil)
var _ ds.GCDatastore = (*datastorage)(nil)
var _ ds.Batching = (*datastorage)(nil)

type datastorage struct {
	*bds.Datastore
}

// NewDatastorage ...
func NewDatastorage(path string, opts *badger4.Options) (Datastore, error) {
	badgerDS, err := bds.NewDatastore(path, opts)
	if err != nil {
		return nil, err
	}
	return &datastorage{Datastore: badgerDS}, nil
}

// Iterator ...
func (s *datastorage) Iterator(ctx context.Context, prefix ds.Key, keysOnly bool) (<-chan KeyValue, <-chan error, error) {
	q := query.Query{
		Prefix:   prefix.String(),
		KeysOnly: keysOnly,
	}

	result, err := s.Datastore.Query(ctx, q)
	if err != nil {
		return nil, nil, err
	}

	out := make(chan KeyValue)
	errc := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errc)
		defer result.Close()

		for {
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case res, ok := <-result.Next():
				if !ok {
					return
				}
				if res.Error != nil {
					errc <- res.Error
					return
				}
				out <- KeyValue{Key: ds.NewKey(res.Key), Value: res.Value}
			}
		}
	}()

	return out, errc, nil
}

// Merge ...
func (s *datastorage) Merge(ctx context.Context, other Datastore) error {
	batch, err := s.Batch(ctx)
	if err != nil {
		return err
	}

	it, errc, err := other.Iterator(ctx, ds.NewKey("/"), false)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-errc:
			if ok && e != nil {
				return e
			}
			// канал ошибок закрылся — продолжаем дренировать it до закрытия
			errc = nil
		case kv, ok := <-it:
			if !ok {
				return batch.Commit(ctx)
			}
			if err := batch.Put(ctx, kv.Key, kv.Value); err != nil {
				return err
			}
		}
	}
}

// Clear ...
func (s *datastorage) Clear(ctx context.Context) error {
	q, err := s.Query(ctx, query.Query{
		KeysOnly: true,
	})
	if err != nil {
		return err
	}
	defer q.Close()

	b, err := s.Batch(ctx)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res, ok := <-q.Next():
			if !ok {
				return b.Commit(ctx)
			}
			if res.Error != nil {
				return res.Error
			}
			if err := b.Delete(ctx, ds.NewKey(res.Key)); err != nil {
				return err
			}
		}
	}
}

// Keys ...
func (s *datastorage) Keys(ctx context.Context, prefix ds.Key) (<-chan ds.Key, <-chan error, error) {
	q := query.Query{
		Prefix:   prefix.String(),
		KeysOnly: true,
	}

	result, err := s.Datastore.Query(ctx, q)
	if err != nil {
		return nil, nil, err
	}

	out := make(chan ds.Key)
	errc := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errc)
		defer result.Close()

		for {
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case res, ok := <-result.Next():
				if !ok {
					return
				}
				if res.Error != nil {
					errc <- res.Error
					return
				}
				out <- ds.NewKey(res.Key)
			}
		}
	}()

	return out, errc, nil
}

func (s *datastorage) Close() error {
	// bds.Datastore реализует io.Closer
	return s.Datastore.Close()
}
