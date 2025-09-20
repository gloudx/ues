package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"ues/blockstore"
	"ues/datastore"
	"ues/repository"

	"github.com/google/uuid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
)

// Demo демонстрирует использование всех трёх новых компонентов UES:
// 1. BlobStore - хранение файлов и бинарных объектов
// 2. ChunkedNodeStore - обработка больших IPLD узлов
// 3. OperationLog - запись всех операций
func main() {
	ctx := context.Background()

	// Настройка базовых компонентов
	ds, err := datastore.NewMemoryDatastore()
	if err != nil {
		log.Fatal("Failed to create datastore:", err)
	}

	bs := blockstore.NewBasicBlockstore(ds)

	fmt.Println("=== UES Architecture Extensions Demo ===\n")

	// 1. ДЕМОНСТРАЦИЯ BLOB STORAGE
	fmt.Println("1. BLOB STORAGE - хранение файлов и бинарных объектов")
	fmt.Println("=====================================================")

	demoBlobStorage(ctx, bs)
	fmt.Println()

	// 2. ДЕМОНСТРАЦИЯ CHUNKED NODE STORE
	fmt.Println("2. CHUNKED NODE STORE - обработка больших IPLD узлов")
	fmt.Println("===================================================")

	demoChunkedNodeStore(ctx, bs)
	fmt.Println()

	// 3. ДЕМОНСТРАЦИЯ OPERATION LOG
	fmt.Println("3. OPERATION LOG - запись всех операций")
	fmt.Println("======================================")

	demoOperationLog(ctx, bs)
	fmt.Println()

	// 4. ИНТЕГРИРОВАННЫЙ ПРИМЕР
	fmt.Println("4. ИНТЕГРИРОВАННОЕ ИСПОЛЬЗОВАНИЕ")
	fmt.Println("===============================")

	demoIntegratedUsage(ctx, bs)
}

// demoBlobStorage демонстрирует работу с файлами и бинарными объектами
func demoBlobStorage(ctx context.Context, bs blockstore.Blockstore) {
	// Создаем пример Repository (упрощенная версия)
	repo := &repository.Repository{} // Simplified for demo

	// Создаем BlobStore
	blobStore := repository.NewBlobStore(bs, repo, nil)

	fmt.Println("Сохранение небольшого файла:")

	// Сохраняем небольшой файл (< 256KB)
	smallFile := strings.NewReader("Hello, this is a small text file content!")
	contentCID, metadata, err := blobStore.PutBlob(ctx, "hello.txt", smallFile)
	if err != nil {
		fmt.Printf("Error storing small file: %v\n", err)
		return
	}

	fmt.Printf("✓ Small file stored\n")
	fmt.Printf("  CID: %s\n", contentCID)
	fmt.Printf("  Name: %s\n", metadata.Name)
	fmt.Printf("  Size: %d bytes\n", metadata.Size)
	fmt.Printf("  MIME: %s\n", metadata.MimeType)
	fmt.Printf("  Created: %s\n", metadata.CreatedAt.Format(time.RFC3339))

	// Получаем файл обратно
	data, retrievedMeta, err := blobStore.GetBlob(ctx, contentCID)
	if err != nil {
		fmt.Printf("Error retrieving file: %v\n", err)
		return
	}

	fmt.Printf("✓ File retrieved successfully\n")
	fmt.Printf("  Content: %s\n", string(data))
	if retrievedMeta != nil {
		fmt.Printf("  Original name: %s\n", retrievedMeta.Name)
	}

	fmt.Println("\nСохранение большого файла (демонстрация chunking):")

	// Создаем большой файл (> 256KB) для демонстрации chunking
	largeContent := strings.Repeat("This is a large file content that will be chunked. ", 10000)
	largeFile := strings.NewReader(largeContent)

	largeCID, largeMeta, err := blobStore.PutBlob(ctx, "large_file.txt", largeFile)
	if err != nil {
		fmt.Printf("Error storing large file: %v\n", err)
		return
	}

	fmt.Printf("✓ Large file stored with automatic chunking\n")
	fmt.Printf("  CID: %s\n", largeCID)
	fmt.Printf("  Size: %d bytes\n", largeMeta.Size)
	fmt.Printf("  Would be chunked: %s\n", func() string {
		if largeMeta.Size > 256*1024 {
			return "Yes"
		}
		return "No"
	}())
}

// demoChunkedNodeStore демонстрирует работу с большими IPLD узлами
func demoChunkedNodeStore(ctx context.Context, bs blockstore.Blockstore) {
	// Создаем ChunkedNodeStore
	chunkedStore := blockstore.NewChunkedNodeStore(bs, nil)

	fmt.Println("Сохранение небольшого IPLD узла:")

	// Небольшой узел
	smallNode := basicnode.NewString("Small IPLD node content")
	smallCID, err := chunkedStore.PutNode(ctx, smallNode)
	if err != nil {
		fmt.Printf("Error storing small node: %v\n", err)
		return
	}

	fmt.Printf("✓ Small IPLD node stored\n")
	fmt.Printf("  CID: %s\n", smallCID)

	// Проверяем, является ли узел chunked
	isChunked, err := chunkedStore.IsChunked(ctx, smallCID)
	if err != nil {
		fmt.Printf("Error checking if chunked: %v\n", err)
		return
	}
	fmt.Printf("  Is chunked: %v\n", isChunked)

	fmt.Println("\nСохранение большого binary объекта (демонстрация chunking):")

	// Большие binary данные (> 256KB)
	largeBinaryData := make([]byte, 500*1024) // 500KB
	for i := range largeBinaryData {
		largeBinaryData[i] = byte(i % 256)
	}

	largeBinaryCID, err := chunkedStore.PutLargeBytes(ctx, largeBinaryData)
	if err != nil {
		fmt.Printf("Error storing large binary data: %v\n", err)
		return
	}

	fmt.Printf("✓ Large binary data stored with chunking\n")
	fmt.Printf("  CID: %s\n", largeBinaryCID)

	// Проверяем chunk информацию
	chunkInfo, err := chunkedStore.GetChunkInfo(ctx, largeBinaryCID)
	if err != nil {
		fmt.Printf("Error getting chunk info: %v\n", err)
		return
	}

	fmt.Printf("✓ Chunk information:\n")
	fmt.Printf("  Original size: %d bytes\n", chunkInfo.OriginalSize)
	fmt.Printf("  Chunk size: %d bytes\n", chunkInfo.ChunkSize)
	fmt.Printf("  Number of chunks: %d\n", chunkInfo.ChunkCount)
	fmt.Printf("  Content type: %s\n", chunkInfo.ContentType)

	// Получаем узел обратно (автоматическая сборка)
	retrievedNode, err := chunkedStore.GetNode(ctx, largeBinaryCID)
	if err != nil {
		fmt.Printf("Error retrieving chunked node: %v\n", err)
		return
	}

	retrievedData, err := retrievedNode.AsBytes()
	if err != nil {
		fmt.Printf("Error extracting bytes from retrieved node: %v\n", err)
		return
	}

	fmt.Printf("✓ Chunked node assembled successfully\n")
	fmt.Printf("  Retrieved size: %d bytes\n", len(retrievedData))
	fmt.Printf("  Data integrity: %s\n", func() string {
		if bytes.Equal(largeBinaryData, retrievedData) {
			return "✓ VERIFIED"
		}
		return "✗ CORRUPTED"
	}())
}

// demoOperationLog демонстрирует систему записи операций
func demoOperationLog(ctx context.Context, bs blockstore.Blockstore) {
	// Создаем OperationLog
	opLog := repository.NewOperationLog(bs, nil)

	fmt.Println("Запись различных типов операций:")

	// Операция 1: PutRecord
	entry1 := &repository.LogEntry{
		OperationID:   uuid.New().String(),
		Timestamp:     time.Now(),
		OperationType: repository.OpPutRecord,
		Actor:         "did:plc:example123",
		Collection:    "app.bsky.feed.post",
		RKey:          "post_" + time.Now().Format("20060102150405"),
		Result:        "success",
		Duration:      50 * time.Millisecond,
		Metadata: map[string]interface{}{
			"client": "UES Demo",
			"ip":     "127.0.0.1",
		},
	}

	err := opLog.LogOperation(ctx, entry1)
	if err != nil {
		fmt.Printf("Error logging operation 1: %v\n", err)
		return
	}
	fmt.Printf("✓ Logged PutRecord operation: %s\n", entry1.OperationID)

	// Операция 2: DeleteRecord
	entry2 := &repository.LogEntry{
		OperationID:   uuid.New().String(),
		Timestamp:     time.Now(),
		OperationType: repository.OpDeleteRecord,
		Actor:         "did:plc:example123",
		Collection:    "app.bsky.feed.post",
		RKey:          "old_post_123",
		Result:        "success",
		Duration:      25 * time.Millisecond,
	}

	err = opLog.LogOperation(ctx, entry2)
	if err != nil {
		fmt.Printf("Error logging operation 2: %v\n", err)
		return
	}
	fmt.Printf("✓ Logged DeleteRecord operation: %s\n", entry2.OperationID)

	// Операция 3: Ошибка
	entry3 := &repository.LogEntry{
		OperationID:   uuid.New().String(),
		Timestamp:     time.Now(),
		OperationType: repository.OpPutRecord,
		Actor:         "did:plc:example456",
		Collection:    "app.bsky.feed.post",
		RKey:          "invalid_post",
		Result:        "error",
		Error:         "validation failed: missing required field 'text'",
		Duration:      10 * time.Millisecond,
	}

	err = opLog.LogOperation(ctx, entry3)
	if err != nil {
		fmt.Printf("Error logging operation 3: %v\n", err)
		return
	}
	fmt.Printf("✓ Logged failed operation: %s\n", entry3.OperationID)

	fmt.Println("\nOperation log записи созданы успешно!")
	fmt.Println("В production версии доступны:")
	fmt.Println("- Query операций по времени, типу, пользователю")
	fmt.Println("- Real-time streaming новых операций")
	fmt.Println("- Automatic cleanup старых записей")
	fmt.Println("- Batch processing для performance")
}

// demoIntegratedUsage демонстрирует интегрированное использование всех компонентов
func demoIntegratedUsage(ctx context.Context, bs blockstore.Blockstore) {
	fmt.Println("Сценарий: Пользователь загружает изображение в social media пост")

	// 1. Создаем все компоненты
	chunkedStore := blockstore.NewChunkedNodeStore(bs, nil)
	opLog := repository.NewOperationLog(bs, nil)
	repo := &repository.Repository{} // Simplified
	blobStore := repository.NewBlobStore(bs, repo, nil)

	// 2. Симулируем загрузку изображения
	fmt.Println("\n1. Загрузка изображения:")

	// Создаем "изображение" (большой binary файл)
	imageData := make([]byte, 800*1024) // 800KB изображение
	for i := range imageData {
		imageData[i] = byte(i % 256) // Псевдо-изображение
	}

	// Логируем начало операции
	startTime := time.Now()
	opID := uuid.New().String()

	// Сохраняем изображение
	imageCID, imageMeta, err := blobStore.PutBlob(ctx, "photo.jpg", bytes.NewReader(imageData))
	if err != nil {
		fmt.Printf("Error uploading image: %v\n", err)
		return
	}

	fmt.Printf("✓ Image uploaded successfully\n")
	fmt.Printf("  CID: %s\n", imageCID)
	fmt.Printf("  Size: %d KB\n", imageMeta.Size/1024)

	// 3. Создаем пост с ссылкой на изображение
	fmt.Println("\n2. Создание поста:")

	// Пост как IPLD узел
	postBuilder := basicnode.Prototype.Map.NewBuilder()
	ma, _ := postBuilder.BeginMap(4)

	// Добавляем поля поста
	ma.AssembleEntry("$type").AssignString("app.bsky.feed.post")
	ma.AssembleEntry("text").AssignString("Check out this amazing photo!")
	ma.AssembleEntry("createdAt").AssignString(time.Now().Format(time.RFC3339))

	// Добавляем ссылку на изображение
	embedEntry, _ := ma.AssembleEntry("embed")
	embedMap, _ := embedEntry.BeginMap(2)
	embedMap.AssembleEntry("$type").AssignString("app.bsky.embed.images")

	imagesEntry, _ := embedMap.AssembleEntry("images")
	imagesList, _ := imagesEntry.BeginList(1)

	imageItem := imagesList.AssembleValue()
	imageItemMap, _ := imageItem.BeginMap(2)
	imageItemMap.AssembleEntry("image").AssignString(imageCID.String())
	imageItemMap.AssembleEntry("alt").AssignString("A beautiful photo")
	imageItemMap.Finish()

	imagesList.Finish()
	embedMap.Finish()
	ma.Finish()

	postNode := postBuilder.Build()

	// Сохраняем пост (может потребовать chunking если много изображений)
	postCID, err := chunkedStore.PutNode(ctx, postNode)
	if err != nil {
		fmt.Printf("Error storing post: %v\n", err)
		return
	}

	fmt.Printf("✓ Post created successfully\n")
	fmt.Printf("  Post CID: %s\n", postCID)

	// 4. Логируем всю операцию
	fmt.Println("\n3. Запись в operation log:")

	logEntry := &repository.LogEntry{
		OperationID:   opID,
		Timestamp:     startTime,
		OperationType: repository.OpPutRecord,
		Actor:         "did:plc:user123",
		Collection:    "app.bsky.feed.post",
		RKey:          "post_" + time.Now().Format("20060102150405"),
		RecordCID:     &postCID,
		Result:        "success",
		Duration:      time.Since(startTime),
		Metadata: map[string]interface{}{
			"has_images":  true,
			"image_count": 1,
			"image_size":  imageMeta.Size,
			"image_cid":   imageCID.String(),
			"client_type": "web",
		},
	}

	err = opLog.LogOperation(ctx, logEntry)
	if err != nil {
		fmt.Printf("Error logging operation: %v\n", err)
		return
	}

	fmt.Printf("✓ Operation logged successfully\n")
	fmt.Printf("  Operation ID: %s\n", logEntry.OperationID)
	fmt.Printf("  Total duration: %v\n", logEntry.Duration)

	// 5. Демонстрируем получение данных
	fmt.Println("\n4. Получение данных:")

	// Получаем пост
	retrievedPost, err := chunkedStore.GetNode(ctx, postCID)
	if err != nil {
		fmt.Printf("Error retrieving post: %v\n", err)
		return
	}

	// Извлекаем CID изображения из поста
	embedNode, _ := retrievedPost.LookupByString("embed")
	imagesNode, _ := embedNode.LookupByString("images")
	imagesIter := imagesNode.ListIterator()
	_, firstImage, _ := imagesIter.Next()
	imageCIDNode, _ := firstImage.LookupByString("image")
	imageCIDStr, _ := imageCIDNode.AsString()

	fmt.Printf("✓ Post retrieved\n")
	fmt.Printf("  Contains image CID: %s\n", imageCIDStr)

	// Получаем изображение
	retrievedImageData, _, err := blobStore.GetBlob(ctx, imageCID)
	if err != nil {
		fmt.Printf("Error retrieving image: %v\n", err)
		return
	}

	fmt.Printf("✓ Image retrieved\n")
	fmt.Printf("  Image size: %d KB\n", len(retrievedImageData)/1024)
	fmt.Printf("  Data integrity: %s\n", func() string {
		if bytes.Equal(imageData, retrievedImageData) {
			return "✓ VERIFIED"
		}
		return "✗ CORRUPTED"
	}())

	fmt.Println("\n=== ИТОГИ ДЕМОНСТРАЦИИ ===")
	fmt.Println("✓ Blob Storage: Файлы сохранены с automatic chunking")
	fmt.Println("✓ Chunked Node Store: Большие IPLD узлы обработаны")
	fmt.Println("✓ Operation Log: Все операции записаны для аудита")
	fmt.Println("✓ Интеграция: Компоненты работают совместно")
	fmt.Println("\nUES теперь поддерживает:")
	fmt.Println("- Загрузку файлов любого размера")
	fmt.Println("- Обработку больших IPLD структур")
	fmt.Println("- Полный аудит всех операций")
}
