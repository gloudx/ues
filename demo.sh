#!/bin/bash

# Демонстрация функциональности UES CLI

echo "🚀 Демонстрация UES CLI"
echo "========================"

echo ""
echo "📁 1. Работа с хранилищем данных (Datastore)"
echo "--------------------------------------------"

echo ""
echo "• Добавляем ключи в хранилище:"
./ues-cli ds put /config/database "postgresql://localhost:5432/mydb"
./ues-cli ds put /config/redis "redis://localhost:6379"
./ues-cli ds put /users/admin '{"name":"Administrator","role":"admin"}'

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
echo "• Список ключей с префиксом /config/:"
./ues-cli ds list /config/

echo ""
echo "• Удаляем ключ:"
./ues-cli ds delete /config/redis
echo "• Проверяем что ключ удален:"
./ues-cli ds has /config/redis

echo ""
echo "📚 2. Работа с репозиторием (Repository)"
echo "---------------------------------------"

echo ""
echo "• Создаем новый пустой репозиторий:"
./ues-cli repo create-collection posts
./ues-cli repo create-collection users

echo ""
echo "• Список коллекций:"
./ues-cli repo list-collections

echo ""
echo "• Добавляем записи в коллекцию posts:"
./ues-cli repo put posts post1 '{"title":"Hello World","content":"This is my first post","author":"admin","published":true}'
./ues-cli repo put posts post2 '{"title":"Second Post","content":"Another great post","author":"user1","published":false}'

echo ""
echo "• Добавляем записи в коллекцию users:"
./ues-cli repo put users user1 '{"username":"john_doe","email":"john@example.com","role":"user"}'
./ues-cli repo put users admin '{"username":"administrator","email":"admin@example.com","role":"admin"}'

echo ""
echo "• Получаем записи:"
./ues-cli repo get posts post1
./ues-cli repo get users admin

echo ""
echo "• Список записей в коллекциях:"
./ues-cli repo list posts
./ues-cli repo list users

echo ""
echo "• Обновляем запись:"
./ues-cli repo put posts post2 '{"title":"Updated Second Post","content":"Updated content","author":"user1","published":true}'
./ues-cli repo get posts post2

echo ""
echo "• Удаляем запись:"
./ues-cli repo delete posts post1
./ues-cli repo list posts

echo ""
echo "• Создаем коммит:"
./ues-cli repo commit

echo ""
echo "✅ Демонстрация завершена!"
echo ""
echo "📖 Для получения справки используйте: ./ues-cli help"