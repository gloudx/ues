package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/multicodec"
	"github.com/ipld/go-ipld-prime/node/basicnode"

	"ues/blockstore"
	"ues/datastore"
	"ues/repository"
)

// DocumentServer представляет веб-сервер для работы с документами
type DocumentServer struct {
	repo           *repository.Repository
	collectionName string
}

// Document представляет структуру документа
type Document struct {
	ID       string                 `json:"id"`
	Data     map[string]interface{} `json:"data"`
	Created  string                 `json:"created"`
	Modified string                 `json:"modified"`
}

// DocumentRequest представляет запрос на создание документа
type DocumentRequest struct {
	Data map[string]interface{} `json:"data"`
}

// DocumentResponse представляет ответ с документом
type DocumentResponse struct {
	Success bool      `json:"success"`
	Message string    `json:"message,omitempty"`
	Data    *Document `json:"data,omitempty"`
}

// DocumentListResponse представляет ответ со списком документов
type DocumentListResponse struct {
	Success   bool       `json:"success"`
	Message   string     `json:"message,omitempty"`
	Documents []Document `json:"documents,omitempty"`
	Count     int        `json:"count"`
}

// NewDocumentServer создает новый экземпляр сервера документов
func NewDocumentServer(repo *repository.Repository, collectionName string) *DocumentServer {
	return &DocumentServer{
		repo:           repo,
		collectionName: collectionName,
	}
}

// setupRoutes настраивает маршруты HTTP сервера
func (ds *DocumentServer) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// API эндпоинты для документов
	mux.HandleFunc("/api/documents", ds.handleDocuments)
	mux.HandleFunc("/api/documents/", ds.handleDocumentByID)

	// Статическая страница для тестирования
	mux.HandleFunc("/", ds.handleHome)

	return mux
}

// handleHome возвращает простую HTML страницу для тестирования API
func (ds *DocumentServer) handleHome(w http.ResponseWriter, r *http.Request) {
	html := `
<!DOCTYPE html>
<html>
<head>
    <title>UES Document API</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; }
        .container { max-width: 800px; margin: 0 auto; }
        .endpoint { background: #f5f5f5; padding: 15px; margin: 10px 0; border-radius: 5px; }
        .method { font-weight: bold; color: #0066cc; }
        textarea { width: 100%; height: 100px; }
        button { background: #0066cc; color: white; padding: 10px 20px; border: none; border-radius: 3px; cursor: pointer; }
        button:hover { background: #0052a3; }
        #result { background: #f9f9f9; padding: 10px; border-radius: 3px; margin-top: 10px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>UES Document API Server</h1>
        <p>Простой веб-сервер для работы с документами в UES хранилище</p>
        
        <h2>Доступные API endpoints:</h2>
        
        <div class="endpoint">
            <span class="method">POST</span> /api/documents
            <p>Создать новый документ. Отправьте JSON с полем "data".</p>
        </div>
        
        <div class="endpoint">
            <span class="method">GET</span> /api/documents
            <p>Получить список всех документов в коллекции.</p>
        </div>
        
        <div class="endpoint">
            <span class="method">GET</span> /api/documents/{id}
            <p>Получить документ по ID.</p>
        </div>
        
        <h2>Тест API:</h2>
        <div>
            <h3>Создать документ:</h3>
            <textarea id="docData" placeholder='{"title": "Тестовый документ", "content": "Содержимое документа"}'></textarea>
            <br><br>
            <button onclick="createDocument()">Создать документ</button>
        </div>
        
        <div>
            <h3>Получить все документы:</h3>
            <button onclick="getAllDocuments()">Получить список документов</button>
        </div>
        
        <div id="result"></div>
    </div>

    <script>
        function createDocument() {
            const data = document.getElementById('docData').value;
            try {
                const jsonData = JSON.parse(data);
                fetch('/api/documents', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({data: jsonData})
                })
                .then(res => res.json())
                .then(result => {
                    document.getElementById('result').innerHTML = '<pre>' + JSON.stringify(result, null, 2) + '</pre>';
                })
                .catch(err => {
                    document.getElementById('result').innerHTML = 'Ошибка: ' + err.message;
                });
            } catch (e) {
                document.getElementById('result').innerHTML = 'Ошибка парсинга JSON: ' + e.message;
            }
        }
        
        function getAllDocuments() {
            fetch('/api/documents')
            .then(res => res.json())
            .then(result => {
                document.getElementById('result').innerHTML = '<pre>' + JSON.stringify(result, null, 2) + '</pre>';
            })
            .catch(err => {
                document.getElementById('result').innerHTML = 'Ошибка: ' + err.message;
            });
        }
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// handleDocuments обрабатывает запросы к /api/documents
func (ds *DocumentServer) handleDocuments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "POST":
		ds.createDocument(w, r)
	case "GET":
		ds.listDocuments(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Метод не поддерживается",
		})
	}
}

// handleDocumentByID обрабатывает запросы к /api/documents/{id}
func (ds *DocumentServer) handleDocumentByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Извлекаем ID документа из URL
	path := strings.TrimPrefix(r.URL.Path, "/api/documents/")
	if path == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "ID документа не указан",
		})
		return
	}

	switch r.Method {
	case "GET":
		ds.getDocument(w, r, path)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Метод не поддерживается",
		})
	}
}

// createDocument создает новый документ в коллекции
func (ds *DocumentServer) createDocument(w http.ResponseWriter, r *http.Request) {
	var req DocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Неверный формат JSON: " + err.Error(),
		})
		return
	}

	if req.Data == nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Поле 'data' обязательно",
		})
		return
	}

	// Создаем документ с метаданными
	now := time.Now().Format(time.RFC3339)
	docData := map[string]interface{}{
		"data":     req.Data,
		"created":  now,
		"modified": now,
	}

	// Преобразуем в IPLD узел
	node, err := toIPLDNode(docData)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Ошибка создания IPLD узла: " + err.Error(),
		})
		return
	}

	// Генерируем уникальный ключ (можно использовать UUID, но для простоты используем timestamp)
	rkey := fmt.Sprintf("doc_%d", time.Now().UnixNano())

	// Сохраняем документ в репозиторий
	ctx := context.Background()
	_, err = ds.repo.PutRecord(ctx, ds.collectionName, rkey, node)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Ошибка сохранения документа: " + err.Error(),
		})
		return
	}

	// Возвращаем успешный ответ
	doc := Document{
		ID:       rkey,
		Data:     req.Data,
		Created:  now,
		Modified: now,
	}

	json.NewEncoder(w).Encode(DocumentResponse{
		Success: true,
		Message: "Документ успешно создан",
		Data:    &doc,
	})
}

// listDocuments возвращает список всех документов в коллекции
func (ds *DocumentServer) listDocuments(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// Получаем все записи из коллекции
	records, err := ds.repo.ListRecords(ctx, ds.collectionName)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(DocumentListResponse{
			Success: false,
			Message: "Ошибка получения списка документов: " + err.Error(),
		})
		return
	}

	var documents []Document
	for _, record := range records {
		// Для каждой записи нужно получить содержимое из blockstore
		node, found, err := ds.repo.GetRecord(ctx, ds.collectionName, record.Key)
		if err != nil || !found {
			log.Printf("Ошибка получения записи %s: %v", record.Key, err)
			continue
		}

		doc, err := recordToDocument(record.Key, node)
		if err != nil {
			log.Printf("Ошибка преобразования записи %s: %v", record.Key, err)
			continue
		}
		documents = append(documents, doc)
	}

	json.NewEncoder(w).Encode(DocumentListResponse{
		Success:   true,
		Documents: documents,
		Count:     len(documents),
	})
}

// getDocument возвращает документ по ID
func (ds *DocumentServer) getDocument(w http.ResponseWriter, r *http.Request, docID string) {
	ctx := context.Background()

	// Получаем документ из репозитория
	node, found, err := ds.repo.GetRecord(ctx, ds.collectionName, docID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Ошибка получения документа: " + err.Error(),
		})
		return
	}

	if !found {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Документ не найден",
		})
		return
	}

	doc, err := recordToDocument(docID, node)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(DocumentResponse{
			Success: false,
			Message: "Ошибка обработки документа: " + err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(DocumentResponse{
		Success: true,
		Data:    &doc,
	})
}

// toIPLDNode преобразует map в IPLD узел
func toIPLDNode(data map[string]interface{}) (datamodel.Node, error) {
	nb := basicnode.Prototype.Map.NewBuilder()
	ma, err := nb.BeginMap(int64(len(data)))
	if err != nil {
		return nil, err
	}

	for key, value := range data {
		if err := ma.AssembleKey().AssignString(key); err != nil {
			return nil, err
		}

		switch v := value.(type) {
		case string:
			if err := ma.AssembleValue().AssignString(v); err != nil {
				return nil, err
			}
		case map[string]interface{}:
			// Рекурсивно обрабатываем вложенные объекты
			nested, err := toIPLDNode(v)
			if err != nil {
				return nil, err
			}
			if err := ma.AssembleValue().AssignNode(nested); err != nil {
				return nil, err
			}
		default:
			// Для других типов преобразуем в строку
			if err := ma.AssembleValue().AssignString(fmt.Sprintf("%v", v)); err != nil {
				return nil, err
			}
		}
	}

	if err := ma.Finish(); err != nil {
		return nil, err
	}

	return nb.Build(), nil
}

// recordToDocument преобразует запись репозитория в Document
func recordToDocument(rkey string, node datamodel.Node) (Document, error) {
	doc := Document{
		ID: rkey,
	}

	// Извлекаем данные из IPLD узла
	if node != nil && node.Kind() == datamodel.Kind_Map {
		iter := node.MapIterator()
		for !iter.Done() {
			key, value, err := iter.Next()
			if err != nil {
				return doc, err
			}

			keyStr, err := key.AsString()
			if err != nil {
				continue
			}

			switch keyStr {
			case "data":
				if value.Kind() == datamodel.Kind_Map {
					docData := make(map[string]interface{})
					dataIter := value.MapIterator()
					for !dataIter.Done() {
						k, v, err := dataIter.Next()
						if err != nil {
							break
						}
						kStr, _ := k.AsString()
						vStr, _ := v.AsString()
						docData[kStr] = vStr
					}
					doc.Data = docData
				}
			case "created":
				if valueStr, err := value.AsString(); err == nil {
					doc.Created = valueStr
				}
			case "modified":
				if valueStr, err := value.AsString(); err == nil {
					doc.Modified = valueStr
				}
			}
		}
	}

	return doc, nil
}

func main() {
	// Регистрируем кодировщики IPLD
	multicodec.RegisterEncoder(0x71, dagcbor.Encode)
	multicodec.RegisterDecoder(0x71, dagcbor.Decode)

	// Инициализация компонентов хранилища
	ctx := context.Background()

	// Создаем datastore
	dsPath := "./data"
	if err := os.MkdirAll(dsPath, 0755); err != nil {
		log.Fatalf("Не удалось создать директорию данных: %v", err)
	}

	ds, err := datastore.NewDatastorage(dsPath, nil)
	if err != nil {
		log.Fatalf("Не удалось создать datastore: %v", err)
	}
	defer ds.Close()

	// Создаем blockstore
	bs := blockstore.NewBlockstore(ds)

	// Создаем репозиторий
	repo := repository.New(bs)

	// Имя коллекции для документов
	collectionName := "documents"

	// Создаем коллекцию документов, если она не существует
	_, err = repo.CreateCollection(ctx, collectionName)
	if err != nil {
		// Коллекция может уже существовать, это нормально
		log.Printf("Коллекция %s возможно уже существует: %v", collectionName, err)
	}

	// Создаем сервер документов
	docServer := NewDocumentServer(repo, collectionName)

	// Настраиваем роуты
	handler := docServer.setupRoutes()

	// Запускаем сервер
	port := "8080"
	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	fmt.Printf("🚀 UES Document Server запущен на порту %s\n", port)
	fmt.Printf("📄 Откройте http://localhost:%s для тестирования API\n", port)
	fmt.Printf("📂 Коллекция документов: %s\n", collectionName)
	fmt.Printf("💾 Данные сохраняются в: %s\n", dsPath)

	log.Fatal(http.ListenAndServe(":"+port, handler))
}
