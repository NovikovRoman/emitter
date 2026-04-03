package emitter

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
)

type Event string

type HandlerFunc[T any] func(ctx context.Context, event Event, data T)

type WarnHandler func(event Event, count int)

type PanicHandler func(ctx context.Context, event Event, err any)

type handler func(ctx context.Context, event Event, data any)

type subscriptionID uint64

type Emitter struct {
	mu           sync.RWMutex
	subs         map[Event]map[subscriptionID]handler
	index        map[subscriptionID]Event
	counter      atomic.Uint64
	onPanic      []PanicHandler
	onWarn       []WarnHandler
	maxListeners int
}

type Option func(*Emitter)

// WithPanicHandler задает обработчики паник в обработчиках событий.
// Если не заданы, паника выводится в stderr.
func WithPanicHandler(fn ...PanicHandler) Option {
	return func(e *Emitter) {
		e.onPanic = append(e.onPanic, fn...)
	}
}

// WithMaxListeners задает максимальное число подписчиков на одно событие,
// при превышении которого выводится предупреждение в stderr, если не заданы warnHandler.
// Значение <= 0 игнорирует проверку.
func WithMaxListeners(n int, warnHandler ...WarnHandler) Option {
	return func(e *Emitter) {
		e.maxListeners = n
		e.onWarn = append(e.onWarn, warnHandler...)
	}
}

func New(opts ...Option) *Emitter {
	e := &Emitter{
		subs:  make(map[Event]map[subscriptionID]handler),
		index: make(map[subscriptionID]Event),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Len возвращает число обработчиков для события.
func (e *Emitter) Len(event Event) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.subs[event])
}

// Events возвращает список всех зарегистрированных событий (порядок не гарантирован).
func (e *Emitter) Events() []Event {
	e.mu.RLock()
	defer e.mu.RUnlock()
	events := make([]Event, 0, len(e.subs))
	for event := range e.subs {
		events = append(events, event)
	}
	return events
}

// Off удаляет все обработчики для события.
func (e *Emitter) Off(event Event) {
	e.mu.Lock()
	for id := range e.subs[event] {
		delete(e.index, id)
	}
	delete(e.subs, event)
	e.mu.Unlock()
}

// OffAll удаляет все обработчики для всех событий.
func (e *Emitter) OffAll() {
	e.mu.Lock()
	e.subs = make(map[Event]map[subscriptionID]handler)
	e.index = make(map[subscriptionID]Event)
	e.mu.Unlock()
}

// On регистрирует типизированный обработчик для одного или нескольких событий.
// Если data не соответствует типу T, обработчик не вызывается.
// Паникует, если не передано ни одного события.
func On[T any](e *Emitter, handler HandlerFunc[T], events ...Event) func() {
	if handler == nil {
		panic("emitter: On requires a non-nil handler")
	}
	if len(events) == 0 {
		panic("emitter: On requires at least one event")
	}
	return e.on(func(ctx context.Context, event Event, data any) {
		if v, ok := data.(T); ok {
			handler(ctx, event, v)
		}
	}, events...)
}

// Once регистрирует типизированный обработчик, который сработает только один раз для указанного события.
// При первом вызове события подписка снимается независимо от типа данных.
// Если data не соответствует типу T, обработчик не вызывается, но подписка все равно снимается.
// Для подписки "один раз суммарно на несколько событий" используйте OnceAny.
func Once[T any](e *Emitter, handler HandlerFunc[T], event Event) func() {
	if handler == nil {
		panic("emitter: Once requires a non-nil handler")
	}

	var once sync.Once
	// ID генерируется до регистрации обработчика, чтобы offFn была гарантированно
	// инициализирована до того, как другая горутина успеет вызвать событие.
	id := subscriptionID(e.counter.Add(1))
	offFn := func() { e.off(id) }

	e.addWithID(id, event, func(ctx context.Context, ev Event, data any) {
		once.Do(func() {
			offFn()
			if v, ok := data.(T); ok {
				handler(ctx, ev, v)
			}
		})
	})

	return func() { once.Do(offFn) }
}

// OnceAny регистрирует типизированный обработчик, который сработает не более одного раза
// суммарно для всех переданных событий. Как только любое событие получено, все подписки
// снимаются. Если data не соответствует типу T, обработчик не вызывается, но все подписки
// все равно снимаются.
// Паникует, если не передано ни одного события.
func OnceAny[T any](e *Emitter, handler HandlerFunc[T], events ...Event) func() {
	if handler == nil {
		panic("emitter: OnceAny requires a non-nil handler")
	}
	if len(events) == 0 {
		panic("emitter: OnceAny requires at least one event")
	}

	// ID'ы генерируются заранее, чтобы offAll была полностью инициализирована
	// до регистрации первого обработчика.
	ids := make([]subscriptionID, len(events))
	for i := range events {
		ids[i] = subscriptionID(e.counter.Add(1))
	}

	offAll := func() {
		for _, id := range ids {
			e.off(id)
		}
	}

	var once sync.Once
	for i, event := range events {
		e.addWithID(ids[i], event, func(ctx context.Context, ev Event, data any) {
			once.Do(func() {
				offAll()
				if v, ok := data.(T); ok {
					handler(ctx, ev, v)
				}
			})
		})
	}
	return func() { once.Do(offAll) }
}

// Emit синхронно вызывает все обработчики с типизированными данными.
// Возвращает количество обработчиков, завершившихся без паники.
// Порядок вызова обработчиков не гарантируется.
// Если контекст отменен до или между вызовами обработчиков, оставшиеся обработчики не вызываются.
// Паники в обработчиках перехватываются и передаются в WithPanicHandler (если задан).
func Emit[T any](ctx context.Context, e *Emitter, event Event, data T) int {
	called := 0
	for _, h := range e.collect(event) {
		if ctx.Err() != nil {
			return called
		}
		if e.safeCall(ctx, event, h, data) {
			called++
		}
	}
	return called
}

// EmitAsync асинхронно вызывает все обработчики с типизированными данными.
// Возвращает WaitGroup, позволяющий дождаться завершения всех обработчиков.
// Если контекст уже отменен до вызова, обработчики не запускаются.
// Отмена контекста после запуска горутин не прерывает уже выполняющиеся обработчики.
// Паники в обработчиках перехватываются и передаются в WithPanicHandler (если задан).
func EmitAsync[T any](ctx context.Context, e *Emitter, event Event, data T) *sync.WaitGroup {
	var wg sync.WaitGroup
	if ctx.Err() != nil {
		return &wg
	}
	handlers := e.collect(event)
	wg.Add(len(handlers))
	for _, h := range handlers {
		go func(h handler) {
			defer wg.Done()
			e.safeCall(ctx, event, h, data)
		}(h)
	}
	return &wg
}

func (e *Emitter) safeCall(ctx context.Context, event Event, h handler, data any) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if len(e.onPanic) > 0 {
				for _, fn := range e.onPanic {
					func() {
						defer func() {
							if r := recover(); r != nil {
								fmt.Fprintf(os.Stderr, "emitter: panic in panic handler for event %q: %v\n", event, r)
							}
						}()
						fn(ctx, event, r)
					}()
				}
			} else {
				fmt.Fprintf(os.Stderr, "emitter: panic in handler for event %q: %v\n", event, r)
			}
		}
	}()
	h(ctx, event, data)
	return true
}

// on регистрирует обработчик для одного или нескольких событий.
// Возвращает функцию отписки, снимающую обработчик со всех переданных событий.
func (e *Emitter) on(h handler, events ...Event) func() {
	ids := make([]subscriptionID, len(events))
	for i, event := range events {
		ids[i] = e.add(event, h)
	}
	return func() {
		for _, id := range ids {
			e.off(id)
		}
	}
}

func (e *Emitter) off(id subscriptionID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	event, ok := e.index[id]
	if !ok {
		return
	}
	delete(e.index, id)
	delete(e.subs[event], id)
	if len(e.subs[event]) == 0 {
		delete(e.subs, event)
	}
}

func (e *Emitter) add(event Event, h handler) subscriptionID {
	id := subscriptionID(e.counter.Add(1))
	e.addWithID(id, event, h)
	return id
}

// addWithID регистрирует обработчик с заранее выделенным ID.
// Используется в Once, чтобы off-функция была готова до регистрации обработчика.
func (e *Emitter) addWithID(id subscriptionID, event Event, h handler) {
	e.mu.Lock()
	if _, ok := e.subs[event]; !ok {
		e.subs[event] = make(map[subscriptionID]handler)
	}
	e.subs[event][id] = h
	e.index[id] = event
	count := len(e.subs[event])
	e.mu.Unlock()

	if e.maxListeners < 1 || count <= e.maxListeners {
		return
	}

	if len(e.onWarn) > 0 {
		for _, fn := range e.onWarn {
			fn(event, count)
		}
	} else {
		fmt.Fprintf(os.Stderr,
			"emitter: possible memory leak. Event %q has %d listeners (max %d)\n",
			event, count, e.maxListeners)
	}
}

// collect возвращает все обработчики для события.
func (e *Emitter) collect(event Event) []handler {
	e.mu.RLock()
	defer e.mu.RUnlock()

	m := e.subs[event]
	if len(m) == 0 {
		return nil
	}
	handlers := make([]handler, 0, len(m))
	for _, h := range m {
		handlers = append(handlers, h)
	}
	return handlers
}
