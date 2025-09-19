package mstindex

import (
	"context"
	"fmt"
	"strings"
	"ues/blockstore"
	"ues/mst"

	"github.com/ipfs/go-cid"
)

const collectionSeparator = "\x00"

// Index wraps an MST tree to index collection records (collection + rkey -> value CID).
type Index struct {
	name string
	tree *mst.Tree
}

// NewIndex creates an index over the provided blockstore.
func NewIndex(bs blockstore.Blockstore, name string) *Index {
	return &Index{
		name: name,
		tree: mst.NewTree(bs),
	}
}

// Load sets the MST root to the provided CID (cid.Undef means empty tree).
func (i *Index) Load(ctx context.Context, root cid.Cid) error {
	return i.tree.Load(ctx, root)
}

// Root returns the current MST root CID.
func (i *Index) Root() cid.Cid {
	return i.tree.Root()
}

// Put stores a mapping (collection, rkey) -> value CID in the index and returns the new root.
func (i *Index) Put(ctx context.Context, rkey string, value cid.Cid) (cid.Cid, error) {
	key, err := keyFor(i.name, rkey)
	if err != nil {
		return cid.Undef, err
	}
	cidRoot, err := i.tree.Put(ctx, key, value)
	if err != nil {
		return cid.Undef, fmt.Errorf("index put: %w", err)
	}
	return cidRoot, nil
}

// Delete removes an entry from the index.
func (i *Index) Delete(ctx context.Context, rkey string) (cid.Cid, bool, error) {
	key, err := keyFor(i.name, rkey)
	if err != nil {
		return cid.Undef, false, err
	}
	cidRoot, removed, err := i.tree.Delete(ctx, key)
	if err != nil {
		return cid.Undef, false, fmt.Errorf("index delete: %w", err)
	}
	return cidRoot, removed, nil
}

// Get retrieves the value CID for collection+rkey.
func (i *Index) Get(ctx context.Context, rkey string) (cid.Cid, bool, error) {
	key, err := keyFor(i.name, rkey)
	if err != nil {
		return cid.Undef, false, err
	}
	value, ok, err := i.tree.Get(ctx, key)
	if err != nil {
		return cid.Undef, false, fmt.Errorf("index get: %w", err)
	}
	return value, ok, nil
}

// List returns all entries in the index for the collection.
func (i *Index) List(ctx context.Context) ([]mst.Entry, error) {
	start := i.name + collectionSeparator
	end := i.name + string([]byte{collectionSeparator[0] + 1})
	entries, err := i.tree.Range(ctx, start, end)
	if err != nil {
		return nil, fmt.Errorf("index range: %w", err)
	}
	prefix := start
	for idx := range entries {
		entries[idx].Key = strings.TrimPrefix(entries[idx].Key, prefix)
	}
	return entries, nil
}

func keyFor(collection, rkey string) (string, error) {
	if strings.Contains(collection, collectionSeparator) {
		return "", fmt.Errorf("collection name contains reserved separator")
	}
	if strings.Contains(rkey, collectionSeparator) {
		return "", fmt.Errorf("record key contains reserved separator")
	}
	return collection + collectionSeparator + rkey, nil
}
