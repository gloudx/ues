# Universal Entity Streams (UES) - Полное описание архитектуры

## 🎯 Концепция UES

**Universal Entity Streams (UES)** - это многоуровневая распределенная система для хранения, синхронизации и обработки структурированных данных с криптографическими гарантиями целостности. Система построена на принципах:

- **Content-addressed storage** - все данные адресуются по хешу содержимого
- **Криптографическая верификация** - каждая операция подписана и проверяема  
- **Decentralized sync** - P2P синхронизация без центральных серверов
- **CRDT семантика** - детерминированное разрешение конфликтов
- **Schema evolution** - безопасное изменение структур данных

## 🏗️ Архитектурные уровни

### **Уровень 1: Storage Layer (Физическое хранение)**

**Назначение:** Надежное, эффективное хранение различных типов данных с оптимизацией под разные паттерны доступа.

#### **Компоненты:**

##### **1.1 IPFS Blockstore (Content-Addressed блоки)**
- **Функция:** Хранение immutable блоков данных с CID адресацией
- **Технология:** IPFS blockstore с BadgerDB backend
- **Оптимизация:** 
  - Дедупликация через content addressing
  - Chunking больших файлов для эффективной передачи
  - Compression для экономии места
- **Паттерн использования:** Write-once, read-many для контентных данных

##### **1.2 BadgerDB KV Store (Горячие данные и кеш)**
- **Функция:** Быстрый доступ к часто используемым данным
- **Технология:** BadgerDB v4 с LSM-tree архитектурой
- **Оптимизация:**
  - In-memory bloom filters для быстрых проверок существования
  - TTL для автоматической очистки устаревших данных
  - Compression и compaction для эффективного использования места
- **Паттерн использования:** Hot path для активных пользовательских сессий

##### **1.3 SQLite (Индексы и метаданные)**
- **Функция:** Структурированные запросы и поиск
- **Технология:** SQLite 3.42+ с WAL режимом и FTS5
- **Оптимизация:**
  - JSON1 extension для работы с JSON полями
  - Partial indexes для экономии места
  - Prepared statements для производительности
- **Паттерн использования:** Complex queries, full-text search, metadata

##### **1.4 Memory Cache (Оперативный кеш)**
- **Функция:** Ultra-fast доступ к критическим данным
- **Технология:** In-process Go maps с LRU eviction
- **Оптимизация:**
  - Ring buffer для минимизации GC pressure
  - Concurrent access через sync.RWMutex
  - Memory monitoring для предотвращения OOM
- **Паттерн использования:** Sub-millisecond access для hot data

#### **Контракт Storage Layer:**
```go
type StorageLayer interface {
    // Block operations (IPFS)
    StoreBlock(ctx context.Context, cid CID, data []byte) error
    GetBlock(ctx context.Context, cid CID) ([]byte, error)
    HasBlock(ctx context.Context, cid CID) (bool, error)
    
    // Key-value operations (BadgerDB)
    Put(ctx context.Context, key []byte, value []byte, ttl time.Duration) error
    Get(ctx context.Context, key []byte) ([]byte, error)
    
    // Structured queries (SQLite)
    CreateIndex(name string, schema IndexSchema) error
    Query(index string, query Query) ([]Record, error)
    
    // Memory cache
    CacheSet(key string, value interface{}, ttl time.Duration) error
    CacheGet(key string) (interface{}, error)
}
```

**Зависимости:** Нет (базовый уровень)
**Кто зависит:** Все вышестоящие уровни

---

### **Уровень 2: Data Structure Layer (IPLD и MST структуры)**

**Назначение:** Организация простых блоков в структурированные, связанные и верифицируемые граф-структуры данных.

#### **Компоненты:**

##### **2.1 IPLD Store (Граф-ориентированные структуры)**
- **Функция:** Создание и навигация по DAG (Directed Acyclic Graph) структурам
- **Принцип работы:** 
  - Каждый узел = self-describing объект с типизированными связями
  - Связи через CID ссылки встроены в объекты
  - Schema-aware сериализация через DAG-CBOR
- **Типы структур:**
  - **Repository Objects** - корневые объекты репозиториев пользователей
  - **Collection Manifests** - описания коллекций записей  
  - **Record Objects** - отдельные записи (посты, профили)
  - **Chat Structures** - манифесты чатов, сообщения, участники
  - **Asset Registry** - определения цифровых активов

##### **2.2 MST Manager (Merkle Search Tree индексирование)**
- **Функция:** Верифицируемое индексирование с эффективным поиском
- **Принцип работы:**
  - Self-balancing binary search tree с криптографическими хешами
  - Каждый узел содержит hash всего поддерева
  - Probabilistic balancing для оптимальной производительности
- **Операции:**
  - **Put/Get/Delete** - основные операции с логарифмической сложностью
  - **Range queries** - эффективный поиск по диапазону ключей
  - **Sync protocol** - сравнение и синхронизация между узлами
  - **Merkle proofs** - криптографические доказательства включения

##### **2.3 CID Generator (Content Identifier генерация)**
- **Функция:** Создание уникальных идентификаторов для контента
- **Технологии:**
  - **BLAKE3** хеширование для высокой производительности
  - **Multihash** формат для поддержки различных алгоритмов
  - **CIDv1** с base32 encoding для URL-безопасности
- **Детерминизм:** Одинаковый контент всегда дает одинаковый CID

##### **2.4 Serialization Engine (Кодирование и декодирование)**
- **Функция:** Преобразование между Go структурами и байтовыми представлениями
- **Формат:** DAG-CBOR для компактности и IPLD совместимости
- **Особенности:**
  - Schema validation для обеспечения корректности данных
  - Canonical encoding для детерминистических хешей
  - Streaming support для больших объектов

#### **Контракт Data Structure Layer:**
```go
type DataStructureLayer interface {
    // IPLD operations
    PutIPLD(ctx context.Context, obj interface{}) (CID, error)
    GetIPLD(ctx context.Context, cid CID, out interface{}) error
    ResolveIPLD(ctx context.Context, path string) (interface{}, error)
    
    // MST operations
    MSTput(ctx context.Context, mst CID, key string, value CID) (CID, error)
    MSTGet(ctx context.Context, mst CID, key string) (CID, error)
    MSTRange(ctx context.Context, mst CID, start, end string) ([]Entry, error)
    MSTSync(ctx context.Context, local, remote CID) ([]Operation, error)
    
    // Content addressing
    ComputeCID(data []byte) CID
    ValidateCID(cid CID, data []byte) (bool, error)
}
```

**Зависимости:** Storage Layer
**Кто зависит:** Repository Layer

---

### **Уровень 3: Repository Layer (Репозитории и записи)**

**Назначение:** Управление пользовательскими репозиториями, коллекциями записей и применением лексиконов для типизации данных.

#### **Компоненты:**

##### **3.1 Repository Manager (Управление репозиториями)**
- **Функция:** Создание, управление и синхронизация пользовательских репозиториев
- **Структура репозитория:**
  - **Root object** - корневой объект с метаданными
  - **Collections** - именованные коллекции записей (posts, follows, etc.)  
  - **Commits** - снапшоты состояния репозитория с timestamp
  - **Signatures** - криптографические подписи для верификации
- **Операции:**
  - Создание нового репозитория с DID идентификацией
  - Создание коммитов при изменении данных
  - Верификация подписей и целостности истории

##### **3.2 Record Manager (Управление записями)**
- **Функция:** CRUD операции с типизированными записями
- **Типы записей:**
  - **Posts** - текстовые сообщения, медиа контент
  - **Profiles** - информация о пользователях
  - **Follows** - социальные связи
  - **Reactions** - лайки, комментарии, репосты
- **Validation:** Проверка записей против лексиконов перед сохранением

##### **3.3 Lexicon Engine (Система типов)**
- **Функция:** Определение и валидация схем данных
- **Принцип:** JSON Schema-подобные определения с расширениями для UES
- **Возможности:**
  - **Type definitions** - базовые типы (string, number, boolean, blob)
  - **Complex structures** - объекты, массивы, unions
  - **Constraints** - валидация длины, формата, диапазонов
  - **Evolution support** - backward compatible изменения схем

##### **3.4 Collection Indexer (Индексирование коллекций)**
- **Функция:** Создание и обновление индексов для быстрого поиска записей
- **Индексы:**
  - **Temporal** - по времени создания/изменения
  - **Author** - по автору записи
  - **Content** - полнотекстовый поиск
  - **Custom** - пользовательские индексы для конкретных лексиконов

#### **Контракт Repository Layer:**
```go
type RepositoryLayer interface {
    // Repository operations
    CreateRepository(ctx context.Context, did DID) (RepositoryID, error)
    GetRepository(ctx context.Context, id RepositoryID) (*Repository, error)
    UpdateRepository(ctx context.Context, id RepositoryID, changes []Change) (CommitID, error)
    
    // Record operations  
    CreateRecord(ctx context.Context, repo RepositoryID, collection string, record Record) (RecordID, error)
    GetRecord(ctx context.Context, repo RepositoryID, collection string, id RecordID) (*Record, error)
    UpdateRecord(ctx context.Context, repo RepositoryID, collection string, id RecordID, record Record) error
    DeleteRecord(ctx context.Context, repo RepositoryID, collection string, id RecordID) error
    
    // Collection queries
    ListRecords(ctx context.Context, repo RepositoryID, collection string, opts ListOptions) ([]Record, error)
    QueryRecords(ctx context.Context, repo RepositoryID, query Query) ([]Record, error)
    
    // Lexicon management
    RegisterLexicon(ctx context.Context, lexicon LexiconDefinition) error
    ValidateRecord(ctx context.Context, lexicon string, record Record) error
}
```

**Зависимости:** Data Structure Layer
**Кто зависит:** Sync Layer

---

### **Уровень 4: Sync Layer (Синхронизация и разрешение конфликтов)**

**Назначение:** Эффективная синхронизация изменений между узлами и детерминированное разрешение конфликтов в распределенной среде.

#### **Компоненты:**

##### **4.1 Operation Log (Журнал операций)**
- **Функция:** Immutable лог всех операций изменения состояния
- **Структура операции:**
  - **Operation ID** - уникальный идентификатор
  - **Type** - тип операции (CREATE, UPDATE, DELETE)
  - **Target** - идентификатор изменяемого объекта
  - **Payload** - данные операции
  - **Author** - DID автора операции
  - **Signature** - криптографическая подпись
  - **Vector Clock** - логическое время для ordering
- **MST Organization:** Операции организованы в MST для эффективного поиска и синхронизации

##### **4.2 Conflict Resolution Engine (Разрешение конфликтов)**
- **Функция:** Детерминированное разрешение конкурирующих операций
- **Алгоритмы:**
  - **Vector Clocks** - определение причинно-следственных связей
  - **Timestamp Ordering** - разрешение по времени при отсутствии causal ordering
  - **Author Priority** - fallback на DID лексикографический порядок
  - **Content-based Resolution** - для specific типов данных (например, counters)
- **Гарантии:** Все узлы приходят к одинаковому результату независимо от порядка получения операций

##### **4.3 Sync Protocol (Протокол синхронизации)**
- **Функция:** Эффективный обмен операциями между узлами
- **Этапы синхронизации:**
  1. **Head Exchange** - сравнение head commit CID между узлами
  2. **Diff Calculation** - вычисление различий через MST сравнение  
  3. **Operation Transfer** - передача недостающих операций
  4. **Application** - применение полученных операций с разрешением конфликтов
  5. **Verification** - проверка целостности результирующего состояния

##### **4.4 Vector Clock Manager (Логическое время)**
- **Функция:** Управление vector clocks для tracking причинности операций
- **Принцип:** Каждый узел поддерживает счетчики логического времени для всех известных узлов
- **Operations:**
  - **Increment** - увеличение своего счетчика при создании операции
  - **Update** - обновление при получении операции от другого узла
  - **Compare** - определение отношения happened-before между операциями

#### **Контракт Sync Layer:**
```go
type SyncLayer interface {
    // Operation log
    AppendOperation(ctx context.Context, op Operation) error
    GetOperation(ctx context.Context, id OperationID) (*Operation, error)
    GetOperationsSince(ctx context.Context, since VectorClock) ([]Operation, error)
    
    // Synchronization
    RequestSync(ctx context.Context, peer PeerID) error
    HandleSyncRequest(ctx context.Context, peer PeerID, request SyncRequest) (*SyncResponse, error)
    ApplyRemoteOperations(ctx context.Context, operations []Operation) error
    
    // Conflict resolution
    ResolveConflicts(ctx context.Context, conflicts []ConflictSet) ([]Operation, error)
    
    // Vector clocks
    GetClock(ctx context.Context) VectorClock
    UpdateClock(ctx context.Context, remoteClock VectorClock) error
}
```

**Зависимости:** Repository Layer
**Кто зависит:** Protocol Layer

---

### **Уровень 5: Protocol Layer (Сетевые протоколы)**

**Назначение:** Реализация сетевых протоколов для P2P взаимодействия, real-time обмена сообщениями и интероперабельности с AT Protocol.

#### **Компоненты:**

##### **5.1 AT Protocol Server (Совместимость с Bluesky)**
- **Функция:** Реализация AT Protocol XRPC endpoints для интероперабельности
- **Endpoints:**
  - **Repository endpoints** - `com.atproto.repo.*` для CRUD операций
  - **Sync endpoints** - `com.atproto.sync.*` для синхронизации
  - **Identity endpoints** - `com.atproto.identity.*` для DID resolution
  - **Moderation endpoints** - для контентной модерации
- **Комплаенс:** Полная совместимость с AT Protocol спецификацией

##### **5.2 P2P Network Layer (Децентрализованная сеть)**
- **Функция:** Direct peer-to-peer связь без intermediate серверов
- **Технологии:**
  - **libp2p** для транспортного уровня и peer discovery
  - **DHT** для распределенного поиска узлов
  - **NAT traversal** через hole punching для прямого соединения
- **Protocols:**
  - **Sync Protocol** - синхронизация репозиториев между пeerами
  - **Chat Protocol** - real-time обмен сообщениями
  - **Asset Transfer** - передача цифровых активов

##### **5.3 WebSocket Server (Real-time события)**
- **Функция:** Push уведомления и live updates для клиентов
- **Event types:**
  - **Repository Updates** - изменения в репозиториях
  - **New Messages** - входящие сообщения в чатах
  - **Sync Events** - статус синхронизации
  - **Peer Events** - подключение/отключение peeров
- **Features:** Connection multiplexing, automatic reconnection, backpressure handling

##### **5.4 HTTP API Gateway (REST интерфейс)**
- **Функция:** Traditional REST API для web и mobile клиентов
- **Endpoints:**
  - **Repository API** - CRUD операции с записями
  - **Chat API** - отправка и получение сообщений
  - **Search API** - поиск по контенту
  - **Assets API** - операции с цифровыми активами
- **Features:** Rate limiting, authentication, response caching

#### **Контракт Protocol Layer:**
```go
type ProtocolLayer interface {
    // AT Protocol compatibility
    HandleATProtoRequest(ctx context.Context, method string, params interface{}) (interface{}, error)
    
    // P2P networking
    ConnectToPeer(ctx context.Context, peerID PeerID) error
    BroadcastToNetwork(ctx context.Context, message Message) error
    HandlePeerMessage(ctx context.Context, from PeerID, message Message) error
    
    // WebSocket server
    RegisterWSClient(ctx context.Context, client *WSClient) error
    BroadcastToClients(ctx context.Context, event Event) error
    
    // HTTP API
    HandleHTTPRequest(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error)
}
```

**Зависимости:** Sync Layer
**Кто зависит:** Application Layer

---

### **Уровень 6: Application Layer (Приложения)**

**Назначение:** Конкретные пользовательские приложения, построенные поверх UES infrastructure.

#### **Компоненты:**

##### **6.1 Social Feed Application**
- **Функция:** Социальная сеть с лентой постов, подписками и взаимодействием
- **Features:**
  - **Timeline generation** - алгоритмическая сборка персональной ленты
  - **Social graph** - управление подписками и социальными связями  
  - **Content creation** - создание постов с медиа контентом
  - **Engagement** - лайки, комментарии, репосты, цитирование
- **Interoperability:** Полная совместимость с Bluesky ecosystem

##### **6.2 P2P Encrypted Chat System**
- **Функция:** End-to-end зашифрованные чаты с различными типами комнат
- **Криптография:**
  - **Signal Protocol** inspiration для forward secrecy
  - **Double Ratchet** для 1-on-1 чатов
  - **Sender Keys** для групповых чатов
  - **Key rotation** для post-compromise security
- **Room types:**
  - **Direct Messages** - приватные беседы между двумя участниками
  - **Group Chats** - закрытые групповые обсуждения
  - **Channels** - публичные каналы с подписчиками
  - **Broadcast Rooms** - односторонняя трансляция от админов к участникам

##### **6.3 Digital Asset Registry**
- **Функция:** Создание, торговля и управление цифровыми активами
- **Asset types:**
  - **NFT Collections** - уникальные цифровые предметы
  - **Fungible Tokens** - взаимозаменяемые токены
  - **Smart Contracts** - программируемые активы с WASM runtime
  - **Identity Credentials** - верифицируемые цифровые сертификаты
- **Features:**
  - **Marketplace** - децентрализованная торговая площадка
  - **Provenance tracking** - полная история владения
  - **Royalty distribution** - автоматические выплаты создателям

##### **6.4 Functional Lenses System**
- **Функция:** Композиция данных из различных источников в унифицированные представления
- **Lens types:**
  - **Aggregation Lenses** - сводные статистики и аналитика
  - **Filter Lenses** - фильтрация данных по критериям
  - **Transform Lenses** - преобразование структур данных
  - **Join Lenses** - объединение данных из разных репозиториев
- **Use cases:**
  - **Analytics dashboards** - бизнес-аналитика пользователей
  - **Recommendation engines** - персонализированные рекомендации
  - **Content moderation** - автоматическая модерация контента

#### **Контракт Application Layer:**
```go
type ApplicationLayer interface {
    // Social feed
    CreatePost(ctx context.Context, author DID, content PostContent) (PostID, error)
    GetFeed(ctx context.Context, user DID, algorithm FeedAlgo) ([]Post, error)
    FollowUser(ctx context.Context, follower, followee DID) error
    
    // Chat system  
    CreateRoom(ctx context.Context, creator DID, roomType RoomType) (RoomID, error)
    SendMessage(ctx context.Context, room RoomID, sender DID, message Message) error
    GetMessages(ctx context.Context, room RoomID, since Timestamp) ([]Message, error)
    
    // Asset registry
    MintAsset(ctx context.Context, creator DID, asset AssetDefinition) (AssetID, error)
    TransferAsset(ctx context.Context, asset AssetID, from, to DID) error
    ListMarketplace(ctx context.Context, filters MarketFilter) ([]Asset, error)
    
    // Functional lenses
    ApplyLens(ctx context.Context, lens LensDefinition, data interface{}) (interface{}, error)
    RegisterLens(ctx context.Context, lens LensDefinition) error
}
```

**Зависимости:** Protocol Layer
**Кто зависит:** Никто (top level)

## 📊 Диаграмма зависимостей

```
┌─────────────────────────────────────────┐
│         Application Layer               │ ← Social Feed, P2P Chat, Assets, Lenses
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐  
│         Protocol Layer                  │ ← AT Proto, P2P, WebSocket, HTTP API
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│         Sync Layer                      │ ← Operation Log, Conflict Resolution
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│         Repository Layer                │ ← Repos, Records, Lexicons, Collections
└─────────────────┬───────────────────────┘
                  │  
┌─────────────────▼───────────────────────┐
│         Data Structure Layer            │ ← IPLD, MST, CID Generation, Serialization
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│         Storage Layer                   │ ← IPFS, BadgerDB, SQLite, Memory Cache
└─────────────────────────────────────────┘
```

## 🛠️ Технологический стек

### **Основные технологии (Go-based):**
- **Go 1.21+** с generics поддержкой
- **BadgerDB v4** для LSM-tree персистентного хранения  
- **SQLite 3.42+** с WAL и FTS5 для индексов
- **IPFS ecosystem** (blockstore, bitswap, DHT)

### **Криптография:**
- **Ed25519** для цифровых подписей
- **BLAKE3** для высокопроизводительного хеширования  
- **ChaCha20-Poly1305** для симметричного шифрования
- **X25519** для key exchange в чатах

### **Сетевое взаимодействие:**
- **libp2p** для P2P networking
- **WebSocket** для real-time events
- **HTTP/2** для REST API
- **XRPC** для AT Protocol совместимости

### **Сериализация и кодирование:**
- **DAG-CBOR** для IPLD структур
- **Protobuf** для сетевых сообщений
- **JSON-LD** для linked data представления
- **CAR format** для архивирования блоков

## ⚡ Ключевые особенности архитектуры

### **1. Децентрализация без компромиссов**
- Полная функциональность без центральных серверов
- P2P синхронизация с eventual consistency гарантиями
- Offline-first дизайн с локальным хранением

### **2. Криптографические гарантии**
- Все операции подписаны и верифицируемы
- Content addressing предотвращает подделку данных
- End-to-end шифрование для приватных коммуникаций

### **3. Масштабируемая архитектура**
- Горизонтальное масштабирование через P2P репликацию
- Efficient sync protocols минимизируют network overhead
- Layered caching на каждом уровне

### **4. Интероперабельность**
- Полная совместимость с AT Protocol / Bluesky
- Стандартные интерфейсы для интеграции с внешними системами
- Cross-platform поддержка через Go compilation

### **5. Расширяемость**
- Plugin architecture для новых типов приложений  
- Lexicon system для эволюции схем данных
- Functional lenses для создания custom представлений данных

Данная архитектура обеспечивает **solid foundation** для построения децентрализованных приложений следующего поколения, сочетая производительность, безопасность и удобство разработки.