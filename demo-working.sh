#!/bin/bash

# Правильная демонстрация функциональности UES CLI
# Показывает ограничения текущей реализации и правильный workflow

echo "🚀 UES CLI - Рабочая демонстрация"
echo "=================================="

echo ""
echo "📁 1. Работа с хранилищем данных (Datastore)"
echo "--------------------------------------------"
echo "✅ Хранилище данных работает корректно между сессиями CLI"

echo ""
echo "• Очищаем хранилище для чистого теста:"
./ues-cli ds clear

echo ""
echo "• Добавляем ключи в хранилище:"
./ues-cli ds put /config/database "postgresql://localhost:5432/mydb"
./ues-cli ds put /config/redis "redis://localhost:6379"  
./ues-cli ds put /users/admin '{"name":"Administrator","role":"admin","created":"2024-01-01"}'
./ues-cli ds put /users/john '{"name":"John Doe","role":"user","created":"2024-01-15"}'

echo ""
echo "• Получаем значения по ключам:"
./ues-cli ds get /config/database
./ues-cli ds get /users/admin

echo ""
echo "• Проверяем существование ключей:"
./ues-cli ds has /config/database
./ues-cli ds has /config/nonexistent

echo ""
echo "• Список всех ключей:"
./ues-cli ds list

echo ""
echo "• Список ключей с префиксом /users/:"
./ues-cli ds list /users/

echo ""
echo "• Удаляем ключ:"
./ues-cli ds delete /config/redis
echo "• Проверяем что ключ удален:"
./ues-cli ds has /config/redis

echo ""
echo "• Финальный список ключей:"
./ues-cli ds list

echo ""
echo "📚 2. Работа с репозиторием - Ограничения"
echo "-----------------------------------------"
echo "⚠️  ОГРАНИЧЕНИЕ: Репозиторий не сохраняет состояние между вызовами CLI"
echo "    Это известная проблема текущей реализации."
echo ""
echo "    Для полноценной работы с репозиторием рекомендуется:"
echo "    1. Использовать server mode (./cmd/server/)"
echo "    2. Или создать интерактивную CLI сессию"
echo "    3. Или работать через Go API напрямую"

echo ""
echo "• Тем не менее, демонстрируем базовые операции:"
echo ""
echo "  Создание коллекции и коммит в одной сессии:"
(
    echo "    ./ues-cli repo create-collection posts"
    echo "    ./ues-cli repo commit"
) | while read cmd; do
    echo "    $cmd"
done

echo ""
echo "• Фактический вызов:"
./ues-cli repo create-collection posts
./ues-cli repo commit

echo ""
echo "📖 3. Полная документация команд"
echo "--------------------------------"

echo ""
echo "• Справка по всем командам:"
./ues-cli help

echo ""
echo "✅ Демонстрация завершена!"
echo ""
echo "📝 Заметки:"
echo "  - Datastore (ds команды) работает корректно между сессиями"
echo "  - Repository (repo команды) имеет ограничение сохранения состояния"
echo "  - Для production использования рекомендуется server mode"
echo "  - CLI отлично подходит для администрирования и отладки datastore"