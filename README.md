# Emitter

[![Go Reference](https://pkg.go.dev/badge/github.com/NovikovRoman/emitter.svg)](https://pkg.go.dev/github.com/NovikovRoman/emitter)
[![Go Report Card](https://goreportcard.com/badge/github.com/NovikovRoman/emitter)](https://goreportcard.com/report/github.com/NovikovRoman/emitter)

Типобезопасный event emitter.

## Установка

```bash
go get github.com/NovikovRoman/emitter
```

## Возможности

- Типобезопасные обработчики через дженерики
- Синхронный (`Emit`) и асинхронный (`EmitAsync`) вызов обработчиков
- Одноразовые подписки (`Once`, `OnceAny`)
- Подписка на несколько событий одним вызовом
- Перехват паник в обработчиках
- Предупреждение при превышении лимита подписчиков
- Отписка через возвращаемую функцию

## Быстрый старт

```go
package main

import (
    "context"
    "fmt"

    "github.com/NovikovRoman/emitter"
)

func main() {
    e := emitter.New()

    // Подписаться на событие
    off := emitter.On(e, func(ctx context.Context, event emitter.Event, data string) {
        fmt.Printf("событие %s: %s\n", event, data)
    }, "hello")

    ctx := context.Background()
    // Генерация события
    emitter.Emit(ctx, e, "hello", "мир")
    // Вывод: событие hello: мир

    // Отписаться
    off()

    // После отписки обработчик не вызывается
    emitter.Emit(ctx, e, "hello", "снова")
}
```

## Использование

### Создание emitter

```go
e := emitter.New()
```

### Подписка `On`

`On` регистрирует обработчик для одного или нескольких событий. Возвращает функцию отписки.

```go
// Подписка на одно событие
off := emitter.On(e, func(ctx context.Context, event emitter.Event, data int) {
    fmt.Println("получено:", data)
}, "counter.inc")

// Подписка на несколько событий одним вызовом
off := emitter.On(e, func(ctx context.Context, event emitter.Event, data string) {
    fmt.Printf("[%s] %s\n", event, data)
}, "user.created", "user.updated", "user.deleted")

// Отписка (снимает обработчик со всех событий, переданных при регистрации)
off()
```

### Одноразовая подписка `Once`

Обработчик вызывается только один раз для каждого события, после чего подписка снимается автоматически.

```go
emitter.Once(e, func(ctx context.Context, event emitter.Event, data string) {
    fmt.Println("первый логин:", data)
}, "user.login")

ctx := context.Background()
emitter.Emit(ctx, e, "user.login", "alice") // вызовется
emitter.Emit(ctx, e, "user.login", "bob")   // не вызовется
```

Подписка снимается при первом же вызове события, независимо от типа данных.
Если тип не совпадает, обработчик не вызывается, но подписка все равно удаляется:

```go
emitter.Once(e, func(ctx context.Context, event emitter.Event, data string) {
    fmt.Println(data)
}, "ev")

ctx := context.Background()
emitter.Emit(ctx, e, "ev", 42)     // тип int: обработчик не вызывается, подписка снята
emitter.Emit(ctx, e, "ev", "text") // подписки уже нет, обработчик не вызовется
```

### Одноразовая подписка на первое из событий `OnceAny`

Обработчик вызывается не более одного раза суммарно для всех переданных событий.
Как только срабатывает любое из них, все подписки снимаются автоматически.

```go
emitter.OnceAny(e, func(ctx context.Context, event emitter.Event, data string) {
    fmt.Printf("первое событие: %s - %s\n", event, data)
}, "user.login", "user.register")

ctx := context.Background()
emitter.Emit(ctx, e, "user.register", "alice") // вызовется, обе подписки сняты
emitter.Emit(ctx, e, "user.login", "alice")    // не вызовется
```

Отличие от `Once`: при `Once` обработчик срабатывает **один раз на каждое событие**, при `OnceAny` **один раз на все события вместе**.

Если тип данных не совпадает, обработчик не вызывается, но все подписки все равно снимаются:

```go
emitter.OnceAny(e, func(ctx context.Context, event emitter.Event, data string) {
    fmt.Println(data)
}, "a", "b")

ctx := context.Background()
emitter.Emit(ctx, e, "a", 42)     // тип int: обработчик не вызывается, все подписки сняты
emitter.Emit(ctx, e, "b", "text") // подписок уже нет, обработчик не вызовется
```

Возвращаемая функция снимает все подписки сразу:

```go
off := emitter.OnceAny(e, func(ctx context.Context, event emitter.Event, data string) {
    fmt.Println("сработало:", data)
}, "x", "y", "z")

off() // отписаться до того, как любое событие сработало
```

### Генерация событий `Emit`

Синхронно вызывает все обработчики в текущей горутине. Возвращает количество реально вызванных обработчиков. Если контекст отменен до или между вызовами обработчиков, оставшиеся обработчики не вызываются.

```go
type UserEvent struct {
    Name string
    Age  int
}

emitter.On(e, func(ctx context.Context, event emitter.Event, data UserEvent) {
    fmt.Printf("новый пользователь: %s (%d лет)\n", data.Name, data.Age)
}, "user.created")

n := emitter.Emit(context.Background(), e, "user.created", UserEvent{Name: "Иван", Age: 30})
fmt.Println("вызвано обработчиков:", n)
```

### Асинхронная генерация `EmitAsync`

Каждый обработчик вызывается в отдельной горутине. Возвращает `*sync.WaitGroup` для ожидания завершения.

```go
emitter.On(e, func(ctx context.Context, event emitter.Event, data string) {
    // выполняется асинхронно
    fmt.Println("обработано:", data)
}, "task")

wg := emitter.EmitAsync(context.Background(), e, "task", "payload")
wg.Wait() // дождаться завершения всех обработчиков
```

Отмена контекста после запуска горутин не прерывает уже выполняющиеся обработчики.

```go
ctx, cancel := context.WithCancel(context.Background())
cancel()

wg := emitter.EmitAsync(ctx, e, "task", "payload")
wg.Wait() // немедленно, обработчики не вызваны
```

### Контекст

Контекст передается в каждый обработчик и может нести дополнительные данные или сигнал отмены.

```go
type ctxKey struct{}

ctx := context.WithValue(context.Background(), ctxKey{}, "request-id-123")

emitter.On(e, func(ctx context.Context, event emitter.Event, data string) {
    reqID := ctx.Value(ctxKey{})
    fmt.Printf("requestID=%v, data=%s\n", reqID, data)
}, "request")

emitter.Emit(ctx, e, "request", "payload")
```

### Опции

#### `WithPanicHandler`

По умолчанию паника в обработчике выводится в stderr. Можно задать собственный обработчик:

```go
e := emitter.New(
    emitter.WithPanicHandler(func(ctx context.Context, event emitter.Event, err any) {
        log.Printf("паника в обработчике события %s: %v", event, err)
    }),
)

emitter.On(e, func(ctx context.Context, event emitter.Event, data string) {
    panic("что-то пошло не так")
}, "ev")

emitter.Emit(context.Background(), e, "ev", "x") // паника перехвачена, программа продолжает работу
```

#### `WithMaxListeners`

Предупреждение при превышении лимита подписчиков на одно событие. Значение `<= 0` отключает проверку.

```go
e := emitter.New(
    emitter.WithMaxListeners(10, func(event emitter.Event, count int) {
        log.Printf("предупреждение: событие %s имеет %d подписчиков", event, count)
    }),
)

// При регистрации 11-го обработчика сработает warnHandler
for i := range 11 {
    emitter.On(e, func(ctx context.Context, event emitter.Event, data any) {}, "ev")
}
```

### Вспомогательные методы

```go
// Количество обработчиков для события
n := e.Len("user.created")

// Список всех зарегистрированных событий
events := e.Events()

// Удалить все обработчики для конкретного события
e.Off("user.created")

// Удалить все обработчики для всех событий
e.OffAll()
```

## Лицензия

MIT
