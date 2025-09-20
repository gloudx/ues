package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"ues/datastore"

	ds "github.com/ipfs/go-datastore"
	badger4 "github.com/ipfs/go-ds-badger4"
	"github.com/urfave/cli/v2"
)

// Глобальная переменная для хранилища
var store datastore.Datastore

// Инициализация хранилища
func initStore(dbPath string) error {
	if store != nil {
		return nil
	}

	// Создаем директорию если не существует
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		return fmt.Errorf("не удалось создать директорию: %v", err)
	}

	var err error
	store, err = datastore.NewDatastorage(dbPath, &badger4.DefaultOptions)
	if err != nil {
		return fmt.Errorf("не удалось открыть хранилище: %v", err)
	}

	return nil
}

// Закрытие хранилища
func closeStore() error {
	if store != nil {
		return store.Close()
	}
	return nil
}

func main() {
	app := &cli.App{
		Name:  "datastore-cli",
		Usage: "CLI для работы с хранилищем данных на основе BadgerDB",
		Authors: []*cli.Author{
			{Name: "Developer"},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "db",
				Aliases: []string{"d"},
				Value:   ".data",
				Usage:   "Путь к директории хранилища данных",
				EnvVars: []string{"DATASTORE_PATH"},
			},
		},
		Before: func(c *cli.Context) error {
			return initStore(c.String("db"))
		},
		After: func(c *cli.Context) error {
			return closeStore()
		},
		Commands: []*cli.Command{
			{
				Name:    "put",
				Aliases: []string{"p"},
				Usage:   "Сохранить ключ-значение пару",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "key",
						Aliases:  []string{"k"},
						Usage:    "Ключ для сохранения",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "value",
						Aliases:  []string{"v"},
						Usage:    "Значение для сохранения",
						Required: true,
					},
					&cli.DurationFlag{
						Name:    "ttl",
						Aliases: []string{"t"},
						Usage:   "Время жизни (TTL) для ключа (например: 10s, 5m, 1h)",
					},
				},
				Action: putAction,
			},
			{
				Name:    "get",
				Aliases: []string{"g"},
				Usage:   "Получить значение по ключу",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "key",
						Aliases:  []string{"k"},
						Usage:    "Ключ для получения значения",
						Required: true,
					},
					&cli.BoolFlag{
						Name:    "json",
						Aliases: []string{"j"},
						Usage:   "Вывести результат в формате JSON",
					},
				},
				Action: getAction,
			},
			{
				Name:    "delete",
				Aliases: []string{"del", "rm"},
				Usage:   "Удалить ключ",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "key",
						Aliases:  []string{"k"},
						Usage:    "Ключ для удаления",
						Required: true,
					},
				},
				Action: deleteAction,
			},
			{
				Name:    "has",
				Aliases: []string{"exists"},
				Usage:   "Проверить существование ключа",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "key",
						Aliases:  []string{"k"},
						Usage:    "Ключ для проверки",
						Required: true,
					},
				},
				Action: hasAction,
			},
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Usage:   "Список ключей (опционально с префиксом)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "prefix",
						Aliases: []string{"p"},
						Usage:   "Префикс для фильтрации ключей",
						Value:   "/",
					},
					&cli.BoolFlag{
						Name:    "values",
						Aliases: []string{"v"},
						Usage:   "Показать также значения",
					},
					&cli.BoolFlag{
						Name:    "json",
						Aliases: []string{"j"},
						Usage:   "Вывести результат в формате JSON",
					},
					&cli.IntFlag{
						Name:    "limit",
						Aliases: []string{"l"},
						Usage:   "Ограничить количество результатов",
						Value:   100,
					},
				},
				Action: listAction,
			},
			{
				Name:    "clear",
				Aliases: []string{"truncate"},
				Usage:   "Очистить все данные в хранилище",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:    "force",
						Aliases: []string{"f"},
						Usage:   "Принудительная очистка без подтверждения",
					},
				},
				Action: clearAction,
			},
			{
				Name:  "ttl",
				Usage: "Управление TTL ключей",
				Subcommands: []*cli.Command{
					{
						Name:  "set",
						Usage: "Установить TTL для ключа",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "key",
								Aliases:  []string{"k"},
								Usage:    "Ключ для установки TTL",
								Required: true,
							},
							&cli.DurationFlag{
								Name:     "duration",
								Aliases:  []string{"d"},
								Usage:    "Время жизни (например: 10s, 5m, 1h)",
								Required: true,
							},
						},
						Action: setTTLAction,
					},
					{
						Name:  "get",
						Usage: "Получить время истечения TTL для ключа",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "key",
								Aliases:  []string{"k"},
								Usage:    "Ключ для проверки TTL",
								Required: true,
							},
						},
						Action: getTTLAction,
					},
				},
			},
			{
				Name:   "info",
				Usage:  "Информация о хранилище",
				Action: infoAction,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

// putAction обрабатывает команду put
func putAction(c *cli.Context) error {
	ctx := context.Background()
	key := ds.NewKey(c.String("key"))
	value := []byte(c.String("value"))
	ttl := c.Duration("ttl")

	if ttl > 0 {
		err := store.PutWithTTL(ctx, key, value, ttl)
		if err != nil {
			return fmt.Errorf("ошибка сохранения с TTL: %v", err)
		}
		fmt.Printf("✓ Сохранено '%s' с TTL %v\n", key.String(), ttl)
	} else {
		err := store.Put(ctx, key, value)
		if err != nil {
			return fmt.Errorf("ошибка сохранения: %v", err)
		}
		fmt.Printf("✓ Сохранено '%s'\n", key.String())
	}

	return nil
}

// getAction обрабатывает команду get
func getAction(c *cli.Context) error {
	ctx := context.Background()
	key := ds.NewKey(c.String("key"))
	asJSON := c.Bool("json")

	value, err := store.Get(ctx, key)
	if err != nil {
		if err == ds.ErrNotFound {
			if asJSON {
				fmt.Println(`{"found": false}`)
			} else {
				fmt.Printf("Ключ '%s' не найден\n", key.String())
			}
			return nil
		}
		return fmt.Errorf("ошибка получения значения: %v", err)
	}

	if asJSON {
		result := map[string]interface{}{
			"found": true,
			"key":   key.String(),
			"value": string(value),
		}
		jsonData, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(jsonData))
	} else {
		fmt.Printf("Ключ: %s\nЗначение: %s\n", key.String(), string(value))
	}

	return nil
}

// deleteAction обрабатывает команду delete
func deleteAction(c *cli.Context) error {
	ctx := context.Background()
	key := ds.NewKey(c.String("key"))

	// Проверяем существование ключа
	exists, err := store.Has(ctx, key)
	if err != nil {
		return fmt.Errorf("ошибка проверки существования ключа: %v", err)
	}

	if !exists {
		fmt.Printf("Ключ '%s' не существует\n", key.String())
		return nil
	}

	err = store.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("ошибка удаления: %v", err)
	}

	fmt.Printf("✓ Ключ '%s' удален\n", key.String())
	return nil
}

// hasAction обрабатывает команду has
func hasAction(c *cli.Context) error {
	ctx := context.Background()
	key := ds.NewKey(c.String("key"))

	exists, err := store.Has(ctx, key)
	if err != nil {
		return fmt.Errorf("ошибка проверки существования: %v", err)
	}

	if exists {
		fmt.Printf("✓ Ключ '%s' существует\n", key.String())
	} else {
		fmt.Printf("✗ Ключ '%s' не существует\n", key.String())
	}

	return nil
}

// listAction обрабатывает команду list
func listAction(c *cli.Context) error {
	ctx := context.Background()
	prefix := ds.NewKey(c.String("prefix"))
	showValues := c.Bool("values")
	asJSON := c.Bool("json")
	limit := c.Int("limit")

	var results []map[string]interface{}
	count := 0

	if showValues {
		// Получаем ключи и значения
		kvChan, errChan, err := store.Iterator(ctx, prefix, false)
		if err != nil {
			return fmt.Errorf("ошибка создания итератора: %v", err)
		}

		// Обрабатываем ошибки в отдельной горутине
		go func() {
			for err := range errChan {
				log.Printf("Ошибка итератора: %v", err)
			}
		}()

		for kv := range kvChan {
			if count >= limit {
				break
			}

			if asJSON {
				results = append(results, map[string]interface{}{
					"key":   kv.Key.String(),
					"value": string(kv.Value),
				})
			} else {
				fmt.Printf("%-50s | %s\n", kv.Key.String(), string(kv.Value))
			}
			count++
		}
	} else {
		// Получаем только ключи
		keysChan, errChan, err := store.Keys(ctx, prefix)
		if err != nil {
			return fmt.Errorf("ошибка получения ключей: %v", err)
		}

		// Обрабатываем ошибки в отдельной горутине
		go func() {
			for err := range errChan {
				log.Printf("Ошибка получения ключей: %v", err)
			}
		}()

		for key := range keysChan {
			if count >= limit {
				break
			}

			if asJSON {
				results = append(results, map[string]interface{}{
					"key": key.String(),
				})
			} else {
				fmt.Println(key.String())
			}
			count++
		}
	}

	if asJSON {
		output := map[string]interface{}{
			"prefix": prefix.String(),
			"count":  count,
			"items":  results,
		}
		jsonData, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(jsonData))
	} else {
		fmt.Printf("\nНайдено записей: %d (префикс: %s)\n", count, prefix.String())
	}

	return nil
}

// clearAction обрабатывает команду clear
func clearAction(c *cli.Context) error {
	ctx := context.Background()
	force := c.Bool("force")

	if !force {
		fmt.Print("Вы уверены, что хотите удалить ВСЕ данные? (yes/no): ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "yes" {
			fmt.Println("Операция отменена")
			return nil
		}
	}

	err := store.Clear(ctx)
	if err != nil {
		return fmt.Errorf("ошибка очистки хранилища: %v", err)
	}

	fmt.Println("✓ Хранилище очищено")
	return nil
}

// setTTLAction устанавливает TTL для ключа
func setTTLAction(c *cli.Context) error {
	ctx := context.Background()
	key := ds.NewKey(c.String("key"))
	duration := c.Duration("duration")

	err := store.SetTTL(ctx, key, duration)
	if err != nil {
		return fmt.Errorf("ошибка установки TTL: %v", err)
	}

	fmt.Printf("✓ TTL %v установлен для ключа '%s'\n", duration, key.String())
	return nil
}

// getTTLAction получает информацию о TTL ключа
func getTTLAction(c *cli.Context) error {
	ctx := context.Background()
	key := ds.NewKey(c.String("key"))

	expiration, err := store.GetExpiration(ctx, key)
	if err != nil {
		return fmt.Errorf("ошибка получения TTL: %v", err)
	}

	if expiration.IsZero() {
		fmt.Printf("Ключ '%s' не имеет TTL (постоянный)\n", key.String())
	} else {
		now := time.Now()
		if now.After(expiration) {
			fmt.Printf("Ключ '%s' истек %v назад (%s)\n",
				key.String(), now.Sub(expiration), expiration.Format(time.RFC3339))
		} else {
			remaining := expiration.Sub(now)
			fmt.Printf("Ключ '%s' истечет через %v (%s)\n",
				key.String(), remaining, expiration.Format(time.RFC3339))
		}
	}

	return nil
}

// infoAction показывает информацию о хранилище
func infoAction(c *cli.Context) error {
	ctx := context.Background()
	dbPath := c.String("db")

	// Получаем статистику хранилища
	keysChan, errChan, err := store.Keys(ctx, ds.NewKey("/"))
	if err != nil {
		return fmt.Errorf("ошибка получения статистики: %v", err)
	}

	// Подсчитываем количество ключей
	keyCount := 0
	go func() {
		for err := range errChan {
			log.Printf("Ошибка подсчета ключей: %v", err)
		}
	}()

	for range keysChan {
		keyCount++
	}

	// Получаем размер директории
	var dirSize int64
	err = filepath.Walk(dbPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Игнорируем ошибки доступа к файлам
		}
		if !info.IsDir() {
			dirSize += info.Size()
		}
		return nil
	})

	if err != nil {
		log.Printf("Предупреждение: не удалось рассчитать размер директории: %v", err)
	}

	fmt.Printf("Информация о хранилище:\n")
	fmt.Printf("  Путь: %s\n", dbPath)
	fmt.Printf("  Количество ключей: %d\n", keyCount)
	fmt.Printf("  Размер на диске: %s\n", formatBytes(dirSize))
	fmt.Printf("  Тип: BadgerDB v4\n")

	return nil
}

// formatBytes форматирует размер в читаемый вид
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
