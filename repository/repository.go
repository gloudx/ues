package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/datamodel"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"

	"ues/blockstore"
)

// Repository manages a content-addressed collection of records grouped by collection name.
type Repository struct {
	bs    blockstore.Blockstore
	index *Index

	mu   sync.RWMutex
	head cid.Cid
	prev cid.Cid
}

// New creates an empty repository with its own index backed by the provided blockstore.
func New(bs blockstore.Blockstore) *Repository {
	return &Repository{bs: bs, index: NewIndex(bs)}
}

// LoadHead loads repository state from an existing commit CID.
func (r *Repository) LoadHead(ctx context.Context, head cid.Cid) error {
	if !head.Defined() {
		r.mu.Lock()
		r.head = cid.Undef
		r.prev = cid.Undef
		r.mu.Unlock()
		return r.index.Load(ctx, cid.Undef)
	}

	node, err := r.bs.GetNodeAny(ctx, head)
	if err != nil {
		return fmt.Errorf("load commit node: %w", err)
	}

	rootCID, prevCID, err := parseCommit(node)
	if err != nil {
		return err
	}

	if err := r.index.Load(ctx, rootCID); err != nil {
		return err
	}

	r.mu.Lock()
	r.head = head
	r.prev = prevCID
	r.mu.Unlock()
	return nil
}

// PutRecord stores a record node and indexes it under collection/rkey.
func (r *Repository) PutRecord(ctx context.Context, collection, rkey string, node datamodel.Node) (cid.Cid, error) {
	valueCID, err := r.bs.PutNode(ctx, node, blockstore.DefaultLP)
	if err != nil {
		return cid.Undef, fmt.Errorf("store record node: %w", err)
	}
	if _, err := r.index.Put(ctx, collection, rkey, valueCID); err != nil {
		return cid.Undef, err
	}
	return valueCID, nil
}

// DeleteRecord removes a mapping from the repository index.
func (r *Repository) DeleteRecord(ctx context.Context, collection, rkey string) (bool, error) {
	_, removed, err := r.index.Delete(ctx, collection, rkey)
	if err != nil {
		return false, err
	}
	return removed, nil
}

// GetRecordCID resolves the content CID for collection/rkey from the index.
func (r *Repository) GetRecordCID(ctx context.Context, collection, rkey string) (cid.Cid, bool, error) {
	return r.index.Get(ctx, collection, rkey)
}

// ListCollection returns the ordered index entries for a collection.
func (r *Repository) ListCollection(ctx context.Context, collection string) ([]cid.Cid, error) {
	entries, err := r.index.ListCollection(ctx, collection)
	if err != nil {
		return nil, err
	}
	out := make([]cid.Cid, len(entries))
	for i, entry := range entries {
		out[i] = entry.Value
	}
	return out, nil
}

// Commit persists the current index state as a new commit and returns its CID.
func (r *Repository) Commit(ctx context.Context) (cid.Cid, error) {
	r.mu.RLock()
	prev := r.head
	r.mu.RUnlock()

	commitNode, err := buildCommitNode(r.index.Root(), prev, time.Now())
	if err != nil {
		return cid.Undef, err
	}
	headCID, err := r.bs.PutNode(ctx, commitNode, blockstore.DefaultLP)
	if err != nil {
		return cid.Undef, fmt.Errorf("store commit node: %w", err)
	}

	r.mu.Lock()
	r.prev = prev
	r.head = headCID
	r.mu.Unlock()
	return headCID, nil
}

func buildCommitNode(root cid.Cid, prev cid.Cid, ts time.Time) (datamodel.Node, error) {
	builder := basicnode.Prototype.Map.NewBuilder()
	ma, err := builder.BeginMap(3)
	if err != nil {
		return nil, err
	}
	entry, err := ma.AssembleEntry("root")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignLink(cidlink.Link{Cid: root}); err != nil {
		return nil, err
	}
	entry, err = ma.AssembleEntry("prev")
	if err != nil {
		return nil, err
	}
	if prev.Defined() {
		if err := entry.AssignLink(cidlink.Link{Cid: prev}); err != nil {
			return nil, err
		}
	} else {
		if err := entry.AssignNull(); err != nil {
			return nil, err
		}
	}
	entry, err = ma.AssembleEntry("timestamp")
	if err != nil {
		return nil, err
	}
	if err := entry.AssignInt(ts.Unix()); err != nil {
		return nil, err
	}
	if err := ma.Finish(); err != nil {
		return nil, err
	}
	return builder.Build(), nil
}

func parseCommit(node datamodel.Node) (cid.Cid, cid.Cid, error) {
	rootNode, err := node.LookupByString("root")
	if err != nil {
		return cid.Undef, cid.Undef, fmt.Errorf("commit missing root: %w", err)
	}
	rootLink, err := rootNode.AsLink()
	if err != nil {
		return cid.Undef, cid.Undef, fmt.Errorf("commit root is not a link: %w", err)
	}
	rl, ok := rootLink.(cidlink.Link)
	if !ok {
		return cid.Undef, cid.Undef, fmt.Errorf("commit root link type unexpected")
	}

	prevNode, err := node.LookupByString("prev")
	if err != nil {
		return cid.Undef, cid.Undef, fmt.Errorf("commit missing prev: %w", err)
	}

	prevCID := cid.Undef
	if !prevNode.IsNull() {
		prevLink, err := prevNode.AsLink()
		if err != nil {
			return cid.Undef, cid.Undef, fmt.Errorf("commit prev is not a link: %w", err)
		}
		pl, ok := prevLink.(cidlink.Link)
		if !ok {
			return cid.Undef, cid.Undef, fmt.Errorf("commit prev link type unexpected")
		}
		prevCID = pl.Cid
	}

	return rl.Cid, prevCID, nil
}
