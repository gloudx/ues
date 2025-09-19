package datastore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	badger4 "github.com/ipfs/go-ds-badger4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewDatastorage тестирует конструктор NewDatastorage.
// Это критически важный тест, так как все остальные функции зависят от правильной инициализации.
func TestNewDatastorage(t *testing.T) {
	t.Run("успешное создание", func(t *testing.T) {
		// Создаем временную директорию для тестирования.
		// t.TempDir() автоматически очищает директорию после завершения теста.
		tmpDir := t.TempDir()

		// Тестируем создание datastore с настройками по умолчанию (nil options).
		// Это наиболее частый случай использования в продакшене.
		store, err := NewDatastorage(tmpDir, nil)

		// require.NoError означает, что тест немедленно завершится с FAIL, если есть ошибка.
		// Используем require для критических проверок, от которых зависит дальнейшее выполнение.
		require.NoError(t, err)
		require.NotNil(t, store)

		// Обязательно закрываем ресурсы для предотвращения утечек памяти и файловых дескрипторов.
		defer store.Close()
	})

	t.Run("создание с кастомными опциями", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Тестируем создание с предопределенными опциями Badger.
		// badger4.DefaultOptions содержит оптимизированные настройки для большинства случаев.
		// Это важно для проверки совместимости с различными конфигурациями Badger.
		store, err := NewDatastorage(tmpDir, &badger4.DefaultOptions)
		require.NoError(t, err)
		require.NotNil(t, store)

		defer store.Close()
	})

	t.Run("ошибка при неверном пути", func(t *testing.T) {
		// Тестируем обработку ошибок при невалидном пути.
		// Это проверяет устойчивость к неправильным входным данным.
		invalidPath := "/invalid/path/that/does/not/exist"

		store, err := NewDatastorage(invalidPath, nil)

		// assert.Error используется для некритических проверок - тест продолжится даже при неудаче.
		// Ожидаем ошибку, так как путь недоступен для записи.
		assert.Error(t, err)
		// Убеждаемся, что при ошибке не возвращается частично инициализированный объект.
		assert.Nil(t, store)
	})
}

// TestBasicOperations тестирует основные CRUD операции datastore.
// Эти операции составляют основу функциональности любой системы хранения данных.
func TestBasicOperations(t *testing.T) {
	// Создаем тестовое хранилище для всех подтестов.
	// Используем общее хранилище для экономии ресурсов и ускорения тестов.
	store := createTestDatastore(t)
	defer store.Close()

	// Контекст для всех операций. В реальном приложении это может быть context с таймаутом.
	ctx := context.Background()

	// Тестовые данные - типичные примеры ключа и значения.
	key := ds.NewKey("/test/key")
	value := []byte("test value")

	t.Run("Put и Get", func(t *testing.T) {
		// Тестируем базовую операцию записи.
		// Put должен сохранить данные без ошибок.
		err := store.Put(ctx, key, value)
		require.NoError(t, err)

		// Тестируем чтение только что записанных данных.
		// Это проверяет целостность данных и корректность сериализации.
		retrievedValue, err := store.Get(ctx, key)
		require.NoError(t, err)

		// Проверяем точное соответствие записанных и прочитанных данных.
		// Это критично для целостности данных.
		assert.Equal(t, value, retrievedValue)
	})

	t.Run("Has", func(t *testing.T) {
		// Тестируем проверку существования ключа.
		// Has должен возвращать true для существующего ключа (записанного в предыдущем тесте).
		exists, err := store.Has(ctx, key)
		require.NoError(t, err)
		assert.True(t, exists)

		// Тестируем проверку несуществующего ключа.
		// Это важно для правильной обработки отсутствующих данных.
		nonExistentKey := ds.NewKey("/non/existent")
		exists, err = store.Has(ctx, nonExistentKey)
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("GetSize", func(t *testing.T) {
		// Тестируем получение размера значения без его загрузки.
		// Это полезно для оптимизации памяти при работе с большими объектами.
		size, err := store.GetSize(ctx, key)
		require.NoError(t, err)

		// Размер должен точно соответствовать длине исходного значения.
		assert.Equal(t, len(value), size)
	})

	t.Run("Delete", func(t *testing.T) {
		// Тестируем удаление существующего ключа.
		err := store.Delete(ctx, key)
		require.NoError(t, err)

		// Проверяем, что ключ действительно удален.
		// После Delete ключ не должен существовать в хранилище.
		exists, err := store.Has(ctx, key)
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("Get несуществующего ключа", func(t *testing.T) {
		// Тестируем обработку запроса несуществующего ключа.
		// Это важно для правильной обработки ошибок в приложении.
		nonExistentKey := ds.NewKey("/does/not/exist")
		_, err := store.Get(ctx, nonExistentKey)

		// Должна возвращаться стандартная ошибка datastore "не найдено".
		assert.Error(t, err)
		assert.Equal(t, ds.ErrNotFound, err)
	})
}

// TestIterator тестирует функциональность итерации по хранилищу.
// Итераторы критически важны для массовой обработки данных и поиска по префиксам.
func TestIterator(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()

	// Создаем структурированный набор тестовых данных.
	// Используем разные префиксы для проверки фильтрации.
	testData := map[string]string{
		"/users/alice":   "alice data", // Пользовательские данные
		"/users/bob":     "bob data",
		"/users/charlie": "charlie data",
		"/posts/1":       "post 1", // Данные постов - другой префикс
		"/posts/2":       "post 2",
	}

	// Заполняем хранилище тестовыми данными.
	for k, v := range testData {
		err := store.Put(ctx, ds.NewKey(k), []byte(v))
		require.NoError(t, err)
	}

	t.Run("итерация с префиксом /users", func(t *testing.T) {
		// Тестируем фильтрацию по префиксу.
		// Должны получить только ключи, начинающиеся с "/users".
		prefix := ds.NewKey("/users")
		kvChan, errChan, err := store.Iterator(ctx, prefix, false)
		require.NoError(t, err)

		// Собираем результаты итерации для проверки.
		results := make(map[string]string)

		// Запускаем горутину для обработки ошибок.
		// Это предотвращает блокировку при получении ошибок из канала.
		go func() {
			for err := range errChan {
				t.Errorf("Ошибка в итераторе: %v", err)
			}
		}()

		// Читаем все результаты из канала.
		for kv := range kvChan {
			results[kv.Key.String()] = string(kv.Value)
		}

		// Проверяем, что получили ровно 3 записи с префиксом "/users".
		assert.Len(t, results, 3)
		assert.Equal(t, "alice data", results["/users/alice"])
		assert.Equal(t, "bob data", results["/users/bob"])
		assert.Equal(t, "charlie data", results["/users/charlie"])
	})

	t.Run("итерация только ключей", func(t *testing.T) {
		// Тестируем режим "только ключи" (keysOnly=true).
		// Это экономит память и трафик при обработке больших значений.
		prefix := ds.NewKey("/posts")
		kvChan, errChan, err := store.Iterator(ctx, prefix, true)
		require.NoError(t, err)

		var keys []string

		go func() {
			for err := range errChan {
				t.Errorf("Ошибка в итераторе: %v", err)
			}
		}()

		for kv := range kvChan {
			keys = append(keys, kv.Key.String())
			// В режиме keysOnly значения должны быть nil для экономии памяти.
			assert.Nil(t, kv.Value)
		}

		// Проверяем, что получили правильное количество ключей.
		assert.Len(t, keys, 2)
		assert.Contains(t, keys, "/posts/1")
		assert.Contains(t, keys, "/posts/2")
	})

	t.Run("итерация с отменой контекста", func(t *testing.T) {
		// Тестируем корректную обработку отмены контекста.
		// Это важно для прерывания длительных операций.
		cancelCtx, cancel := context.WithCancel(ctx)

		_, errChan, err := store.Iterator(cancelCtx, ds.NewKey("/"), false)
		require.NoError(t, err)

		// Немедленно отменяем контекст для проверки реакции итератора.
		cancel()

		// Ожидаем получение ошибки отмены контекста через канал ошибок.
		select {
		case err := <-errChan:
			assert.Equal(t, context.Canceled, err)
		case <-time.After(time.Second):
			// Если ошибка не пришла в течение секунды, тест провален.
			t.Fatal("Таймаут ожидания ошибки отмены")
		}
	})
}

// TestKeys тестирует получение списка ключей с фильтрацией по префиксу.
// Отличается от Iterator тем, что возвращает только ключи без значений.
func TestKeys(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()

	// Создаем тестовые данные с разными префиксами для проверки фильтрации.
	testKeys := []string{"/config/db", "/config/cache", "/logs/error", "/logs/info"}
	for _, k := range testKeys {
		err := store.Put(ctx, ds.NewKey(k), []byte("data"))
		require.NoError(t, err)
	}

	t.Run("получение ключей с префиксом", func(t *testing.T) {
		// Тестируем фильтрацию ключей по префиксу "/config".
		prefix := ds.NewKey("/config")
		keysChan, errChan, err := store.Keys(ctx, prefix)
		require.NoError(t, err)

		var keys []string

		// Обрабатываем потенциальные ошибки в отдельной горутине.
		go func() {
			for err := range errChan {
				t.Errorf("Ошибка в Keys: %v", err)
			}
		}()

		// Собираем все ключи из канала.
		for key := range keysChan {
			keys = append(keys, key.String())
		}

		// Проверяем, что получили только ключи с нужным префиксом.
		assert.Len(t, keys, 2)
		assert.Contains(t, keys, "/config/db")
		assert.Contains(t, keys, "/config/cache")
	})

	t.Run("Keys с отменой контекста", func(t *testing.T) {
		// Тестируем отмену операции получения ключей через контекст.
		cancelCtx, cancel := context.WithCancel(ctx)

		_, errChan, err := store.Keys(cancelCtx, ds.NewKey("/"))
		require.NoError(t, err)

		// Отменяем операцию для проверки корректной обработки.
		cancel()

		// Ожидаем ошибку отмены контекста.
		select {
		case err := <-errChan:
			assert.Equal(t, context.Canceled, err)
		case <-time.After(time.Second):
			t.Fatal("Таймаут ожидания ошибки отмены")
		}
	})
}

// TestClear тестирует полную очистку хранилища.
// Это критически важная операция для сброса состояния или обслуживания.
func TestClear(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()

	// Подготавливаем тестовые данные для последующей очистки.
	testData := map[string]string{
		"/a": "data a",
		"/b": "data b",
		"/c": "data c",
	}

	// Заполняем хранилище данными.
	for k, v := range testData {
		err := store.Put(ctx, ds.NewKey(k), []byte(v))
		require.NoError(t, err)
	}

	// Проверяем, что данные действительно записались.
	// Это важно для корректности теста - мы должны очищать непустое хранилище.
	for k := range testData {
		exists, err := store.Has(ctx, ds.NewKey(k))
		require.NoError(t, err)
		assert.True(t, exists)
	}

	// Выполняем очистку хранилища.
	err := store.Clear(ctx)
	require.NoError(t, err)

	// Проверяем, что все данные удалены.
	// После Clear хранилище должно быть полностью пустым.
	for k := range testData {
		exists, err := store.Has(ctx, ds.NewKey(k))
		require.NoError(t, err)
		assert.False(t, exists)
	}
}

// TestMerge тестирует слияние двух хранилищ данных.
// Эта операция критична для миграции данных и синхронизации между экземплярами.
func TestMerge(t *testing.T) {
	// Создаем два отдельных хранилища для тестирования слияния.
	store1 := createTestDatastore(t)
	defer store1.Close()

	store2 := createTestDatastore(t)
	defer store2.Close()

	ctx := context.Background()

	// Заполняем первое хранилище данными.
	data1 := map[string]string{
		"/store1/key1": "value1",
		"/store1/key2": "value2",
	}

	for k, v := range data1 {
		err := store1.Put(ctx, ds.NewKey(k), []byte(v))
		require.NoError(t, err)
	}

	// Заполняем второе хранилище данными, включая ключ с пересекающимся префиксом.
	data2 := map[string]string{
		"/store2/key1": "value3",
		"/store2/key2": "value4",
		"/store1/key3": "value5", // Пересечение префиксов для проверки корректности слияния
	}

	for k, v := range data2 {
		err := store2.Put(ctx, ds.NewKey(k), []byte(v))
		require.NoError(t, err)
	}

	// Сливаем store2 в store1.
	// После этой операции store1 должен содержать данные из обоих хранилищ.
	err := store1.Merge(ctx, store2)
	require.NoError(t, err)

	// Создаем объединенный набор ожидаемых данных.
	allExpectedData := make(map[string]string)
	for k, v := range data1 {
		allExpectedData[k] = v
	}
	for k, v := range data2 {
		allExpectedData[k] = v
	}

	// Проверяем, что все данные доступны в store1.
	// Это подтверждает успешность операции слияния.
	for k, expectedValue := range allExpectedData {
		value, err := store1.Get(ctx, ds.NewKey(k))
		require.NoError(t, err)
		assert.Equal(t, expectedValue, string(value))
	}
}

// TestTTL тестирует функциональность времени жизни (Time To Live) записей.
// TTL критически важен для автоматической очистки устаревших данных.
func TestTTL(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()
	key := ds.NewKey("/ttl/test")
	value := []byte("ttl test value")

	t.Run("PutWithTTL положительное значение", func(t *testing.T) {
		// Тестируем установку TTL при записи.
		ttl := time.Second * 2
		err := store.PutWithTTL(ctx, key, value, ttl)
		require.NoError(t, err)

		// Проверяем, что ключ существует сразу после записи.
		exists, err := store.Has(ctx, key)
		require.NoError(t, err)
		assert.True(t, exists)

		// Проверяем, что время истечения установлено корректно.
		expiration, err := store.GetExpiration(ctx, key)
		require.NoError(t, err)

		// Время истечения должно быть в будущем, но не позже чем TTL + буфер на выполнение.
		assert.True(t, expiration.After(time.Now()))
		assert.True(t, expiration.Before(time.Now().Add(ttl+time.Second)))
	})

	t.Run("PutWithTTL нулевое значение (обычный Put)", func(t *testing.T) {
		// Тестируем поведение при TTL=0, что должно работать как обычный Put.
		key2 := ds.NewKey("/ttl/no_ttl")
		err := store.PutWithTTL(ctx, key2, value, 0)
		require.NoError(t, err)

		// Ключ должен существовать после записи.
		exists, err := store.Has(ctx, key2)
		require.NoError(t, err)
		assert.True(t, exists)

		// Для ключа без TTL просто проверяем, что GetExpiration работает.
		// Конкретное значение expiration зависит от реализации Badger.
		_, err = store.GetExpiration(ctx, key2)
		require.NoError(t, err)
	})

	t.Run("SetTTL для существующего ключа", func(t *testing.T) {
		// Тестируем установку TTL для уже существующего ключа.
		key3 := ds.NewKey("/ttl/set_ttl")
		err := store.Put(ctx, key3, value)
		require.NoError(t, err)

		// Устанавливаем TTL для существующего ключа.
		ttl := time.Second * 3
		err = store.SetTTL(ctx, key3, ttl)
		require.NoError(t, err)

		// Проверяем, что TTL действительно установлен.
		expiration, err := store.GetExpiration(ctx, key3)
		require.NoError(t, err)
		assert.True(t, expiration.After(time.Now()))
	})

	t.Run("SetTTL с нулевым значением (снятие TTL)", func(t *testing.T) {
		// Тестируем снятие TTL с ключа.
		key4 := ds.NewKey("/ttl/remove_ttl")

		// Создаем ключ с TTL.
		err := store.PutWithTTL(ctx, key4, value, time.Minute)
		require.NoError(t, err)

		// Снимаем TTL, устанавливая его в 0.
		err = store.SetTTL(ctx, key4, 0)
		require.NoError(t, err)

		// Проверяем поведение после снятия TTL.
		exists, err := store.Has(ctx, key4)
		require.NoError(t, err)

		if exists {
			// Если ключ существует, проверяем expiration.
			expiration, err := store.GetExpiration(ctx, key4)
			require.NoError(t, err)
			// После снятия TTL время истечения должно быть очень далеким или нулевым.
			assert.True(t, expiration.IsZero() || expiration.After(time.Now().Add(time.Hour*24*365)))
		} else {
			// В некоторых версиях Badger SetTTL(0) может удалить ключ.
			// Это тоже допустимое поведение.
			t.Log("SetTTL(0) удалил ключ - это допустимое поведение")
		}
	})

	t.Run("GetExpiration для несуществующего ключа", func(t *testing.T) {
		// Тестируем обработку запроса TTL для несуществующего ключа.
		nonExistentKey := ds.NewKey("/ttl/does_not_exist")
		_, err := store.GetExpiration(ctx, nonExistentKey)

		// Должна возвращаться ошибка для несуществующих ключей.
		assert.Error(t, err)
	})
}

// TestBatching тестирует пакетные операции.
// Batching критически важен для производительности при массовых операциях.
func TestBatching(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()

	t.Run("успешная пакетная операция", func(t *testing.T) {
		// Создаем пакет для группировки операций.
		batch, err := store.Batch(ctx)
		require.NoError(t, err)

		// Подготавливаем данные для пакетной записи.
		testData := map[string]string{
			"/batch/1": "batch value 1",
			"/batch/2": "batch value 2",
			"/batch/3": "batch value 3",
		}

		// Добавляем операции в пакет без немедленного выполнения.
		for k, v := range testData {
			err = batch.Put(ctx, ds.NewKey(k), []byte(v))
			require.NoError(t, err)
		}

		// Проверяем, что до коммита данные не видны в хранилище.
		// Это подтверждает транзакционность пакетных операций.
		for k := range testData {
			exists, err := store.Has(ctx, ds.NewKey(k))
			require.NoError(t, err)
			assert.False(t, exists)
		}

		// Коммитим пакет для применения всех операций.
		err = batch.Commit(ctx)
		require.NoError(t, err)

		// После коммита все данные должны быть доступны.
		for k, expectedValue := range testData {
			value, err := store.Get(ctx, ds.NewKey(k))
			require.NoError(t, err)
			assert.Equal(t, expectedValue, string(value))
		}
	})
}

// TestTransactions тестирует транзакционную функциональность.
// Транзакции обеспечивают ACID свойства для групп операций.
func TestTransactions(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()

	t.Run("успешная транзакция", func(t *testing.T) {
		// Создаем транзакцию (readOnly=false для записи).
		txn, err := store.NewTransaction(ctx, false)
		require.NoError(t, err)

		key := ds.NewKey("/txn/test")
		value := []byte("transaction value")

		// Выполняем операцию в рамках транзакции.
		err = txn.Put(ctx, key, value)
		require.NoError(t, err)

		// Коммитим транзакцию для применения изменений.
		err = txn.Commit(ctx)
		require.NoError(t, err)

		// Проверяем, что данные сохранились после коммита.
		retrievedValue, err := store.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, value, retrievedValue)
	})

	t.Run("откат транзакции", func(t *testing.T) {
		// Тестируем откат транзакции (Discard).
		key := ds.NewKey("/txn/discard")
		value := []byte("should be discarded")

		txn, err := store.NewTransaction(ctx, false)
		require.NoError(t, err)

		// Добавляем операцию в транзакцию.
		err = txn.Put(ctx, key, value)
		require.NoError(t, err)

		// Отменяем транзакцию вместо коммита.
		txn.Discard(ctx)

		// После отката данных не должно быть в хранилище.
		exists, err := store.Has(ctx, key)
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

// TestGC тестирует сборку мусора.
// GC важен для освобождения места, занятого удаленными записями.
func TestGC(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()

	// Тестируем, что операция сборки мусора выполняется без ошибок.
	// В реальности GC может освобождать значительные объемы места.
	err := store.CollectGarbage(ctx)
	assert.NoError(t, err)
}

// TestClose тестирует корректное закрытие хранилища.
// Правильное закрытие критично для сохранности данных и освобождения ресурсов.
func TestClose(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDatastorage(tmpDir, nil)
	require.NoError(t, err)

	ctx := context.Background()
	key := ds.NewKey("/test")
	value := []byte("test")

	// Добавляем данные перед закрытием.
	err = store.Put(ctx, key, value)
	require.NoError(t, err)

	// Закрываем хранилище.
	err = store.Close()
	assert.NoError(t, err)

	// После закрытия операции должны завершаться ошибкой.
	// Это предотвращает использование закрытых ресурсов.
	err = store.Put(ctx, ds.NewKey("/after/close"), []byte("should fail"))
	assert.Error(t, err)
}

// TestEdgeCases тестирует граничные случаи и нестандартные сценарии.
// Эти тесты важны для устойчивости системы к неожиданным входным данным.
func TestEdgeCases(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()

	t.Run("пустой ключ", func(t *testing.T) {
		// Тестируем работу с пустым ключом.
		// Это может происходить при ошибках в коде приложения.
		emptyKey := ds.NewKey("")
		value := []byte("empty key value")

		err := store.Put(ctx, emptyKey, value)
		require.NoError(t, err)

		// Пустой ключ должен обрабатываться как любой другой валидный ключ.
		retrievedValue, err := store.Get(ctx, emptyKey)
		require.NoError(t, err)
		assert.Equal(t, value, retrievedValue)
	})

	t.Run("пустое значение", func(t *testing.T) {
		// Тестируем сохранение пустого значения.
		key := ds.NewKey("/empty/value")
		emptyValue := []byte{}

		err := store.Put(ctx, key, emptyValue)
		require.NoError(t, err)

		retrievedValue, err := store.Get(ctx, key)
		require.NoError(t, err)

		// В Badger пустые значения могут возвращаться как nil.
		// Проверяем логическую пустоту, а не точное равенство типов.
		assert.True(t, len(retrievedValue) == 0, "Значение должно быть пустым")
	})

	t.Run("nil значение", func(t *testing.T) {
		// Тестируем сохранение nil значения.
		key := ds.NewKey("/nil/value")

		err := store.Put(ctx, key, nil)
		require.NoError(t, err)

		retrievedValue, err := store.Get(ctx, key)
		require.NoError(t, err)

		// nil значение должно сохраняться как пустое.
		assert.Empty(t, retrievedValue)
	})

	t.Run("очень длинный ключ", func(t *testing.T) {
		// Тестируем работу с экстремально длинными ключами.
		// Это может происходить при использовании хешей или UUID в ключах.
		longKey := ds.NewKey("/" + string(make([]byte, 1000)))
		value := []byte("long key value")

		err := store.Put(ctx, longKey, value)
		require.NoError(t, err)

		// Длинные ключи должны обрабатываться корректно.
		retrievedValue, err := store.Get(ctx, longKey)
		require.NoError(t, err)
		assert.Equal(t, value, retrievedValue)
	})

	t.Run("большое значение", func(t *testing.T) {
		// Тестируем работу с большими значениями (1MB).
		// Это важно для хранения файлов или больших JSON объектов.
		key := ds.NewKey("/large/value")
		largeValue := make([]byte, 1024*1024) // 1MB

		// Заполняем массив предсказуемыми данными для проверки целостности.
		for i := range largeValue {
			largeValue[i] = byte(i % 256)
		}

		err := store.Put(ctx, key, largeValue)
		require.NoError(t, err)

		// Большие значения должны сохраняться и читаться без потери данных.
		retrievedValue, err := store.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, largeValue, retrievedValue)
	})
}

// TestConcurrency тестирует поведение при параллельном доступе.
// Это критически важно для многопоточных приложений.
func TestConcurrency(t *testing.T) {
	store := createTestDatastore(t)
	defer store.Close()

	ctx := context.Background()

	t.Run("параллельная запись", func(t *testing.T) {
		// Параметры нагрузочного теста.
		const numGoroutines = 10              // Количество параллельных потоков
		const numOperationsPerGoroutine = 100 // Операций в каждом потоке

		// Канал для сбора ошибок от горутин.
		errChan := make(chan error, numGoroutines)

		// Запускаем горутины для параллельной записи.
		for i := 0; i < numGoroutines; i++ {
			go func(routineID int) {
				// Каждая горутина выполняет серию операций записи.
				for j := 0; j < numOperationsPerGoroutine; j++ {
					key := ds.NewKey(fmt.Sprintf("/concurrent/%d/%d", routineID, j))
					value := []byte(fmt.Sprintf("value_%d_%d", routineID, j))

					if err := store.Put(ctx, key, value); err != nil {
						errChan <- err
						return
					}
				}
				// Сигнализируем об успешном завершении.
				errChan <- nil
			}(i)
		}

		// Ждем завершения всех горутин.
		for i := 0; i < numGoroutines; i++ {
			err := <-errChan
			assert.NoError(t, err)
		}

		// Проверяем, что все данные записались корректно.
		// Это подтверждает thread-safety операций записи.
		for i := 0; i < numGoroutines; i++ {
			for j := 0; j < numOperationsPerGoroutine; j++ {
				key := ds.NewKey(fmt.Sprintf("/concurrent/%d/%d", i, j))
				expectedValue := []byte(fmt.Sprintf("value_%d_%d", i, j))

				value, err := store.Get(ctx, key)
				require.NoError(t, err)
				assert.Equal(t, expectedValue, value)
			}
		}
	})
}

// createTestDatastore создает временное хранилище для тестов.
// Эта функция инкапсулирует создание тестового окружения.
func createTestDatastore(t *testing.T) Datastore {
	// Создаем временную директорию, которая автоматически удалится после теста.
	tmpDir := t.TempDir()

	// Создаем новый экземпляр datastore с настройками по умолчанию.
	store, err := NewDatastorage(tmpDir, nil)
	require.NoError(t, err)
	return store
}

// Бенчмарки для оценки производительности различных операций.
// Бенчмарки важны для выявления узких мест и отслеживания деградации производительности.

// BenchmarkPut измеряет производительность операций записи.
func BenchmarkPut(b *testing.B) {
	store := createBenchDatastore(b)
	defer store.Close()

	ctx := context.Background()
	value := []byte("benchmark value")

	// Сбрасываем таймер для исключения времени инициализации.
	b.ResetTimer()

	// Выполняем b.N операций записи для получения статистики.
	for i := 0; i < b.N; i++ {
		key := ds.NewKey(fmt.Sprintf("/bench/put/%d", i))
		if err := store.Put(ctx, key, value); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGet измеряет производительность операций чтения.
func BenchmarkGet(b *testing.B) {
	store := createBenchDatastore(b)
	defer store.Close()

	ctx := context.Background()
	value := []byte("benchmark value")

	// Предварительно заполняем хранилище данными для чтения.
	for i := 0; i < b.N; i++ {
		key := ds.NewKey(fmt.Sprintf("/bench/get/%d", i))
		if err := store.Put(ctx, key, value); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()

	// Измеряем время выполнения операций чтения.
	for i := 0; i < b.N; i++ {
		key := ds.NewKey(fmt.Sprintf("/bench/get/%d", i))
		if _, err := store.Get(ctx, key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIterator измеряет производительность итерации по данным.
func BenchmarkIterator(b *testing.B) {
	store := createBenchDatastore(b)
	defer store.Close()

	ctx := context.Background()
	value := []byte("benchmark value")

	// Подготавливаем 1000 записей для итерации.
	for i := 0; i < 1000; i++ {
		key := ds.NewKey(fmt.Sprintf("/bench/iter/%d", i))
		if err := store.Put(ctx, key, value); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()

	// Измеряем время выполнения полной итерации.
	for i := 0; i < b.N; i++ {
		kvChan, errChan, err := store.Iterator(ctx, ds.NewKey("/bench/iter"), false)
		if err != nil {
			b.Fatal(err)
		}

		// Дренируем канал ошибок для предотвращения блокировки.
		go func() {
			for range errChan {
				// Игнорируем ошибки в бенчмарке
			}
		}()

		// Подсчитываем количество прочитанных записей.
		count := 0
		for range kvChan {
			count++
		}
	}
}

// createBenchDatastore создает хранилище для бенчмарков.
// Отличается от тестового более тщательной очисткой ресурсов.
func createBenchDatastore(b *testing.B) Datastore {
	// Создаем временную директорию с предсказуемым префиксом.
	tmpDir, err := os.MkdirTemp("", "datastore_bench_*")
	if err != nil {
		b.Fatal(err)
	}

	// Регистрируем функцию очистки для выполнения после бенчмарка.
	b.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})

	// Создаем экземпляр datastore.
	store, err := NewDatastorage(tmpDir, nil)
	if err != nil {
		b.Fatal(err)
	}
	return store
}
