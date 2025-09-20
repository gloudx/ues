package main

import (
	"context"
	"fmt"
	"log"
	"ues/lexicon" // замените на ваш реальный путь
)

func main() {
	// Создаем реестр лексиконов
	registry := lexicon.NewRegistry("./schemas")

	// Загружаем схемы
	ctx := context.Background()
	if err := registry.LoadSchemas(ctx); err != nil {
		log.Fatalf("Failed to load schemas: %v", err)
	}

	fmt.Println("=== Список загруженных схем ===")
	schemas := registry.ListSchemas()
	for _, id := range schemas {
		fmt.Printf("- %s\n", id)
	}

	// Тестируем получение схемы
	fmt.Println("\n=== Получение схемы пользователя ===")
	userSchema, err := registry.GetSchema("com.example.user")
	if err != nil {
		log.Printf("Error getting user schema: %v", err)
	} else {
		fmt.Printf("ID: %s\n", userSchema.ID)
		fmt.Printf("Name: %s\n", userSchema.Name)
		fmt.Printf("Version: %s\n", userSchema.Version)
		fmt.Printf("Status: %s\n", userSchema.Status)
	}

	// Тестируем валидацию корректных данных
	fmt.Println("\n=== Валидация корректных данных пользователя ===")
	validUser := map[string]interface{}{
		"username": "john_doe",
		"email":    "john@example.com",
		"age":      "30",
		"active":   true,
	}

	if err := registry.ValidateData("com.example.user", validUser); err != nil {
		fmt.Printf("Validation failed: %v\n", err)
	} else {
		fmt.Println("✓ Валидация прошла успешно")
	}

	// Тестируем валидацию некорректных данных
	fmt.Println("\n=== Валидация некорректных данных пользователя ===")
	invalidUser := map[string]interface{}{
		"username": "jane_doe",
		"email":    "jane@example.com",
		// "age" отсутствует - обязательное поле
		"active": "not_boolean", // неверный тип
	}

	if err := registry.ValidateData("com.example.user", invalidUser); err != nil {
		fmt.Printf("✓ Ожидаемая ошибка валидации: %v\n", err)
	} else {
		fmt.Println("✗ Валидация прошла, но не должна была")
	}

	// Тестируем схему поста
	fmt.Println("\n=== Валидация корректного поста ===")
	validPost := map[string]interface{}{
		"title":     "Мой первый пост",
		"content":   "Содержимое поста...",
		"author":    "john_doe",
		"tags":      []interface{}{"golang", "ipld", "тест"},
		"published": true,
	}

	if err := registry.ValidateData("com.example.post", validPost); err != nil {
		fmt.Printf("Validation failed: %v\n", err)
	} else {
		fmt.Println("✓ Валидация поста прошла успешно")
	}

	// Тестируем проверку активности схем
	fmt.Println("\n=== Проверка активности схем ===")
	for _, id := range schemas {
		if registry.IsActive(id) {
			fmt.Printf("✓ %s - активная\n", id)
		} else {
			fmt.Printf("- %s - неактивная\n", id)
		}
	}

	// Тестируем компилированную схему
	fmt.Println("\n=== Получение компилированной схемы ===")
	compiledSchema, err := registry.GetCompiledSchema("com.example.user")
	if err != nil {
		fmt.Printf("Error getting compiled schema: %v\n", err)
	} else {
		fmt.Printf("✓ Схема скомпилирована, типов: %d\n", len(compiledSchema.GetTypes()))
	}

	fmt.Println("\n=== Тестирование завершено ===")
}
