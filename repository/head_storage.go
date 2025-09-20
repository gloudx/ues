package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
)

// HeadStorage управляет персистентным хранением состояния HEAD репозитория
// Обеспечивает автоматическое сохранение и восстановление состояния между перезапусками
type HeadStorage interface {
	// LoadHead загружает последнее состояние HEAD из persistent storage
	LoadHead(ctx context.Context, repoID string) (RepositoryState, error)

	// SaveHead сохраняет текущее состояние HEAD в persistent storage
	SaveHead(ctx context.Context, repoID string, state RepositoryState) error

	// WatchHead подписывается на изменения HEAD (для репликации)
	WatchHead(ctx context.Context, repoID string) (<-chan RepositoryState, error)

	// Close закрывает storage и освобождает ресурсы
	Close() error
}

// RepositoryState представляет снимок состояния репозитория
type RepositoryState struct {
	Head      cid.Cid `json:"head"`    // CID текущего HEAD коммита
	Prev      cid.Cid `json:"prev"`    // CID предыдущего коммита
	RootIndex cid.Cid `json:"root"`    // CID корневого индекса (кэш для производительности)
	Version   int     `json:"version"` // Версия формата состояния
	RepoID    string  `json:"repo_id"` // Уникальный идентификатор репозитория
}

// datastoreHeadStorage реализует HeadStorage через datastore
type datastoreHeadStorage struct {
	ds       ds.Datastore
	watchers map[string][]chan RepositoryState
	mu       sync.RWMutex
}

// NewDatastoreHeadStorage создает новый HeadStorage на основе datastore
func NewDatastoreHeadStorage(store ds.Datastore) HeadStorage {
	return &datastoreHeadStorage{
		ds:       store,
		watchers: make(map[string][]chan RepositoryState),
	}
}

// LoadHead загружает состояние репозитория из datastore
func (h *datastoreHeadStorage) LoadHead(ctx context.Context, repoID string) (RepositoryState, error) {
	key := ds.NewKey("repository").ChildString(repoID).ChildString("head")

	data, err := h.ds.Get(ctx, key)
	if err != nil {
		if err == ds.ErrNotFound {
			// Репозиторий еще не инициализирован
			return RepositoryState{
				Head:    cid.Undef,
				Prev:    cid.Undef,
				Version: 1,
				RepoID:  repoID,
			}, nil
		}
		return RepositoryState{}, fmt.Errorf("failed to load head state: %w", err)
	}

	var state RepositoryState
	if err := json.Unmarshal(data, &state); err != nil {
		return RepositoryState{}, fmt.Errorf("failed to unmarshal head state: %w", err)
	}

	return state, nil
}

// SaveHead сохраняет состояние репозитория в datastore
func (h *datastoreHeadStorage) SaveHead(ctx context.Context, repoID string, state RepositoryState) error {
	key := ds.NewKey("repository").ChildString(repoID).ChildString("head")

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal head state: %w", err)
	}

	if err := h.ds.Put(ctx, key, data); err != nil {
		return fmt.Errorf("failed to save head state: %w", err)
	}

	// Уведомляем watchers об изменении
	h.notifyWatchers(repoID, state)

	return nil
}

// WatchHead создает канал для отслеживания изменений HEAD
func (h *datastoreHeadStorage) WatchHead(ctx context.Context, repoID string) (<-chan RepositoryState, error) {
	ch := make(chan RepositoryState, 10) // Буферизованный канал

	h.mu.Lock()
	h.watchers[repoID] = append(h.watchers[repoID], ch)
	h.mu.Unlock()

	// Закрываем канал при отмене контекста
	go func() {
		<-ctx.Done()
		h.removeWatcher(repoID, ch)
		close(ch)
	}()

	return ch, nil
}

// Close закрывает storage и уведомляет всех watchers
func (h *datastoreHeadStorage) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Закрываем все watchers
	for _, watchers := range h.watchers {
		for _, ch := range watchers {
			close(ch)
		}
	}
	h.watchers = make(map[string][]chan RepositoryState)

	return nil
}

// notifyWatchers уведомляет всех подписчиков об изменении состояния
func (h *datastoreHeadStorage) notifyWatchers(repoID string, state RepositoryState) {
	h.mu.RLock()
	watchers := h.watchers[repoID]
	h.mu.RUnlock()

	for _, ch := range watchers {
		select {
		case ch <- state:
		default:
			// Если канал заблокирован, пропускаем уведомление
		}
	}
}

// removeWatcher удаляет watcher из списка
func (h *datastoreHeadStorage) removeWatcher(repoID string, target chan RepositoryState) {
	h.mu.Lock()
	defer h.mu.Unlock()

	watchers := h.watchers[repoID]
	for i, ch := range watchers {
		if ch == target {
			h.watchers[repoID] = append(watchers[:i], watchers[i+1:]...)
			break
		}
	}
}

// fileHeadStorage реализует HeadStorage через файловую систему (для простых случаев)
type fileHeadStorage struct {
	baseDir  string
	watchers map[string][]chan RepositoryState
	mu       sync.RWMutex
}

// NewFileHeadStorage создает HeadStorage на основе файловой системы
func NewFileHeadStorage(baseDir string) (HeadStorage, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	return &fileHeadStorage{
		baseDir:  baseDir,
		watchers: make(map[string][]chan RepositoryState),
	}, nil
}

// LoadHead загружает состояние из файла
func (f *fileHeadStorage) LoadHead(ctx context.Context, repoID string) (RepositoryState, error) {
	filePath := filepath.Join(f.baseDir, repoID+".json")

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Файл не существует - новый репозиторий
			return RepositoryState{
				Head:    cid.Undef,
				Prev:    cid.Undef,
				Version: 1,
				RepoID:  repoID,
			}, nil
		}
		return RepositoryState{}, fmt.Errorf("failed to read head file: %w", err)
	}

	var state RepositoryState
	if err := json.Unmarshal(data, &state); err != nil {
		return RepositoryState{}, fmt.Errorf("failed to unmarshal head state: %w", err)
	}

	return state, nil
}

// SaveHead сохраняет состояние в файл
func (f *fileHeadStorage) SaveHead(ctx context.Context, repoID string, state RepositoryState) error {
	filePath := filepath.Join(f.baseDir, repoID+".json")
	tempPath := filePath + ".tmp"

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal head state: %w", err)
	}

	// Атомарная запись через временный файл
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath) // Очищаем при ошибке
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	// Уведомляем watchers
	f.notifyWatchers(repoID, state)

	return nil
}

// WatchHead для файлового storage (упрощенная реализация)
func (f *fileHeadStorage) WatchHead(ctx context.Context, repoID string) (<-chan RepositoryState, error) {
	ch := make(chan RepositoryState, 10)

	f.mu.Lock()
	f.watchers[repoID] = append(f.watchers[repoID], ch)
	f.mu.Unlock()

	go func() {
		<-ctx.Done()
		f.removeWatcher(repoID, ch)
		close(ch)
	}()

	return ch, nil
}

// Close закрывает файловое хранилище
func (f *fileHeadStorage) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, watchers := range f.watchers {
		for _, ch := range watchers {
			close(ch)
		}
	}
	f.watchers = make(map[string][]chan RepositoryState)

	return nil
}

// notifyWatchers и removeWatcher аналогичны datastoreHeadStorage
func (f *fileHeadStorage) notifyWatchers(repoID string, state RepositoryState) {
	f.mu.RLock()
	watchers := f.watchers[repoID]
	f.mu.RUnlock()

	for _, ch := range watchers {
		select {
		case ch <- state:
		default:
		}
	}
}

func (f *fileHeadStorage) removeWatcher(repoID string, target chan RepositoryState) {
	f.mu.Lock()
	defer f.mu.Unlock()

	watchers := f.watchers[repoID]
	for i, ch := range watchers {
		if ch == target {
			f.watchers[repoID] = append(watchers[:i], watchers[i+1:]...)
			break
		}
	}
}
