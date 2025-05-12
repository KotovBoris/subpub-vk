# gRPC PubSub сервис на базе пакета subpub

Проект представляет собой gRPC сервис, реализующий систему публикации-подписки (Pub/Sub). Сервис позволяет клиентам подписываться на события по определенному ключу и публиковать события по ключу для всех активных подписчиков.

В основе лежит пакет `subpub`: эффективная in-memory Pub/Sub шина с независимой обработкой подписчиков, сохранением FIFO-порядка сообщений и автоматической очисткой ресурсов.

## Описание gRPC-сервиса

Сервис предоставляет два RPC-метода:

1. **Publish** (Unary RPC)  
   Клиент отправляет ключ (`string`) и данные (`string`), сервер передаёт сообщение всем подписчикам указанного ключа через `subpub`.

2. **Subscribe** (Server-streaming RPC)  
   Клиент указывает ключ (`string`), а сервер начинает отправлять поток событий (`Event` с полем `data: string`), публикуемых по этому ключу.

Protobuf-схема: [`pkg/grpc/proto/pubsub.proto`](https://github.com/KotovBoris/subpub-vk/blob/main/pkg/grpc/proto/pubsub.proto)
## Как это работает

### Публикация (`Publish`)

### Publish

1. Клиент вызывает `Publish(key, data)`.
2. Сервис проверяет, что `key` не пуст.
3. Вызывается `subpub.Publish(key, data)`.
4. При успехе возвращается `Empty` и `codes.OK`.
5. При ошибках — `codes.InvalidArgument` или `codes.Internal`.

### Подписка (`Subscribe`)

1.  Клиент вызывает `Subscribe(key)`.
2. Сервис проверяет `key`.
3. Вызывается `subpub.Subscribe(key, handler)`, где `handler` — callback для новых сообщений.
4. При поступлении сообщения `subpub` вызывает `handler`, который неблокирующе отправляет данные во внутренний буферизированный канал.
5. Отдельная горутина читает из этого канала и шлёт события клиенту через gRPC-стрим. Такой подход:
    - Не блокирует работу других подписчиков при медленных клиентах.
    - Не мешает механизму самоочищающейся очереди.
6. Стрим активен, пока клиент не отменит подписку или не возникнет ошибка.
7. По завершении стрима сервис вызывает `subscription.Unsubscribe()` для освобождения ресурсов.
8. Ошибки кодируются стандартными gRPC-статусами.

## Паттерны и подходы

*   **Dependency Injection:** `subpub.SubPub` и `slog.Logger` внедряются в gRPC сервис.
*   **Graceful Shutdown:** Корректное завершение gRPC сервера и системы `subpub` по сигналам ОС.
-   **Структурированное логирование** через `log/slog`.
-   **gRPC-статусы** для кодирования ошибок.

## Пакет `subpub`

Расположение: [`internal/subpub/`](https://github.com/KotovBoris/subpub-vk/tree/main/internal/subpub)

Интерфейс в [`types.go`](https://github.com/KotovBoris/subpub-vk/blob/main/internal/subpub/types.go):

```go
type MessageHandler func(msg interface{})

type Subscription interface {
	Unsubscribe()
}

type SubPub interface {
	Subscribe(subject string, cb MessageHandler) (Subscription, error)
	Publish(subject string, msg interface{}) error
	Close(ctx context.Context) error
}
```

### Архитектура `subpub`

*   **[`MySubPub`](https://github.com/KotovBoris/subpub-vk/blob/main/internal/subpub/subpub.go)**: 
    *   Хранит `topics map[string]*SelfCleaningQueue[interface{}]`, где ключ - название темы, значение - очередь сообщений для этой темы.
    *   Использует `sync.RWMutex` для безопасного доступа к хеш-таблице `topics` и флагу `closed`.
    *   Использует `sync.WaitGroup` для ожидания завершения всех горутин-подписчиков при вызове `Close`.
    *   При `Subscribe` создает итератор для соответствующей очереди и запускает горутину `subscriberLoop`, которая **проходит по очереди с помощью итератора** и вызывает `MessageHandler` для каждой вершины.
    *   При `Publish` добавляет сообщение в соответствующую очередь.
    *   При `Close` помечает сервис закрытым, закрывает все очереди и ждёт завершения горутин (или отмены контекста).
    
*   **[`SelfCleaningQueue`](https://github.com/KotovBoris/subpub-vk/blob/main/internal/subpub/internal/queue/queue.go)**:
    *   Реализована как односвязный список.
    *   Обеспечивает **lock-free чтение**: итераторы (`Iterator`) продвигаются по списку без блокировок, ожидая новые элементы с помощью канала `isLast` в последнем узле.
    *   Использует `sync.Mutex` только для операций `Push` и `Close`.
    *   **Автоматическая очистка**: Узлы, которые прошли все активные итераторы, становятся недоступными и удаляются сборщиком мусора.
    
*   **[`Iterator`](https://github.com/KotovBoris/subpub-vk/blob/main/internal/subpub/internal/queue/queue.go)**: Итератор для очереди `SelfCleaningQueue`.
    *   Хранит указатель на текущий узел `cur`.
    *   `Next()` блокируется на канале `cur.isLast`, если достигнут конец списка, и ждет либо добавления нового узла, либо закрытия очереди.

*   **[`MySubscription`](https://github.com/KotovBoris/subpub-vk/blob/main/internal/subpub/subscription.go)**:
    *   Метод `Unsubscribe()` закрывает канал `closeToUnsub`, завершая `subscriberLoop`.
### Ключевые особенности `subpub`

*   **Независимость подписчиков**: Медленная обработка сообщений одним подписчиком не влияет на скорость доставки другим подписчикам той же или других тем. Публикация не блокируется.
*   **Гарантия порядка FIFO**: Сообщения доставляются подписчикам в том же порядке, в котором они были опубликованы в рамках одной темы.
*   **Эффективность**: Lock-free чтение минимизирует конкуренцию при большом количестве подписчиков. 
*   **Корректное завершение**: `Close(ctx)` ждёт обработки всех сообщений или отмены контекста.

## Конфигурация gRPC Сервиса

Сервис настраивается через YAML-файл (по умолчанию [`configs/config.yaml`](https://github.com/KotovBoris/subpub-vk/blob/main/configs/config.yaml)). Путь можно переопределить флагом `-config`.

**Пример [`configs/config.yaml`](https://github.com/KotovBoris/subpub-vk/blob/main/configs/config.yaml):**

```yaml
grpc_server:
  port: "50051"
  shutdown_timeout: "10s"   # Таймаут для Graceful Shutdown

logger:
  level: "info"             # Уровень логирования (debug, info, warn, error)
```

## Сборка и Запуск

### Предварительные требования

*   Go (версия 1.21+).
*   `protoc` v3.
*   Go плагины `protoc-gen-go` и `protoc-gen-go-grpc`.

### Генерация gRPC кода

Из корня проекта:
```bash
protoc --go_out=. --go-grpc_out=. pkg/grpc/proto/pubsub.proto
```

### Сборка сервера

Из корня проекта:
```bash
go build -o pubsub_server ./cmd/server/main.go
```

### Запуск сервера

```bash
# С конфигом по умолчанию
./pubsub_server

# С другим конфигом
./pubsub_server -config /путь/к/config.yaml
```
Остановка: `Ctrl+C`.

## Тестирование

### Тесты пакета `subpub`

Файл: [`internal/subpub/subpub_test.go`](https://github.com/KotovBoris/subpub-vk/blob/main/internal/subpub/subpub_test.go)  
Покрытие: **96.2%**

Запуск тестов `subpub` из корня проекта:
```bash
go test ./internal/subpub -v -cover
```

### Интеграционные тесты gRPC

Файл: [`internal/service/pubsub/service_test.go`](https://github.com/KotovBoris/subpub-vk/blob/main/internal/service/pubsub/service_test.go)

Запуск тестов gRPC сервиса из корня проекта:
```bash
go test ./internal/service/pubsub/... -v
```

## Пример использования (Ручное тестирование с клиентом)

### Сборка клиента

```bash
go build -o client ./cmd/client/main.go
```

### Команды (сервер должен быть запущен)

*   **Подписка:** 
```bash
./client -action=subscribe -key=my_topic
```
*   **Публикация:**
```bash
./client -action=publish -key=my_topic -data="Hello"
```
