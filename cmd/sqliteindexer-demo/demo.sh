#!/bin/bash

# Демонстрационный скрипт для тестирования SQLiteIndexer
# 
# Этот скрипт компилирует и запускает демонстрационное приложение
# в различных режимах для полного тестирования функциональности

set -e

echo "🚀 SQLiteIndexer Demo Launcher"
echo "=============================="

# Переход в директорию приложения
cd "$(dirname "$0")"

echo "📦 Компилируем демо-приложение..."
go build -o sqliteindexer-demo main.go

if [ $? -ne 0 ]; then
    echo "❌ Ошибка компиляции!"
    exit 1
fi

echo "✅ Компиляция успешна!"
echo ""

# Функция для показа меню
show_menu() {
    echo "Выберите режим демонстрации:"
    echo "1. Автоматическая демонстрация (быстрая)"
    echo "2. Интерактивный CLI режим"
    echo "3. Быстрый тест основных функций"
    echo "4. Выход"
    echo ""
    read -p "Ваш выбор (1-4): " choice
}

# Функция для автоматической демонстрации
auto_demo() {
    echo "🎯 Запускаем автоматическую демонстрацию..."
    echo "============================================="
    ./sqliteindexer-demo
}

# Функция для интерактивного режима
interactive_mode() {
    echo "🎯 Запускаем интерактивный CLI режим..."
    echo "======================================"
    echo "💡 Подсказка: начните с команды 'setup', затем 'help'"
    echo ""
    ./sqliteindexer-demo cli
}

# Функция для быстрого теста
quick_test() {
    echo "🎯 Запускаем быстрый тест функций..."
    echo "===================================="
    
    # Автоматические команды для быстрого теста
    cat << 'EOF' | ./sqliteindexer-demo cli
setup
stats
search collection posts
search text технология
search author alice
search active
help
exit
EOF
    
    echo ""
    echo "✅ Быстрый тест завершен!"
}

# Основной цикл меню
while true; do
    show_menu
    
    case $choice in
        1)
            auto_demo
            ;;
        2)
            interactive_mode
            ;;
        3)
            quick_test
            ;;
        4)
            echo "👋 До свидания!"
            exit 0
            ;;
        *)
            echo "❌ Неверный выбор. Попробуйте снова."
            ;;
    esac
    
    echo ""
    echo "Нажмите Enter для продолжения..."
    read
    echo ""
done