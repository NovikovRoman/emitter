package emitter

import (
	"context"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// captureStderr перенаправляет os.Stderr в pipe на время вызова fn,
// возвращает все, что было записано в stderr.
func captureStderr(fn func()) string {
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	buf, _ := io.ReadAll(r)
	_ = r.Close()
	return string(buf)
}

func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func eventsStrings(events []Event) []string {
	ss := make([]string, len(events))
	for i, e := range events {
		ss[i] = string(e)
	}
	sort.Strings(ss)
	return ss
}

func TestNew(t *testing.T) {
	e := New()
	if e == nil {
		t.Fatal("New returned nil")
	}
	if e.subs == nil || e.index == nil {
		t.Fatal("internal maps not initialised")
	}
}

func TestWithMaxListeners_customWarn(t *testing.T) {
	var warnCounts []int
	var warnEvent Event
	e := New(WithMaxListeners(2, func(event Event, count int) {
		warnEvent = event
		warnCounts = append(warnCounts, count)
	}))

	// Добавить 4 подписчика: warn срабатывает при каждом добавлении сверх лимита (3-й и 4-й).
	for range 4 {
		On(e, func(_ context.Context, _ Event, _ any) {}, "ev")
	}

	if len(warnCounts) != 2 {
		t.Fatalf("expected warn called twice, got %d", len(warnCounts))
	}
	if warnEvent != "ev" {
		t.Fatalf("expected warn for event 'ev', got %q", warnEvent)
	}
	if warnCounts[0] != 3 || warnCounts[1] != 4 {
		t.Fatalf("expected warnCounts=[3,4], got %v", warnCounts)
	}
}

// maxListeners <= 0 отключает проверку
// Обработчик предупреждения не должен вызываться.
func TestWithMaxListeners_zero(t *testing.T) {
	called := false
	e := New(WithMaxListeners(0, func(_ Event, _ int) { called = true }))
	for range 10 {
		On(e, func(_ context.Context, _ Event, _ any) {}, "ev")
	}
	if called {
		t.Fatal("warn handler must not be called when maxListeners == 0")
	}
}

func TestLen_noHandlers(t *testing.T) {
	e := New()
	if n := e.Len("foo"); n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestLen_exactMatch(t *testing.T) {
	e := New()
	On(e, func(_ context.Context, _ Event, _ any) {}, "a")
	On(e, func(_ context.Context, _ Event, _ any) {}, "a")
	if n := e.Len("a"); n != 2 {
		t.Fatalf("expected 2, got %d", n)
	}
}

func TestLen_afterOff(t *testing.T) {
	e := New()
	off := On(e, func(_ context.Context, _ Event, _ any) {}, "ev")
	off()
	if n := e.Len("ev"); n != 0 {
		t.Fatalf("expected 0 after off, got %d", n)
	}
}

func TestEvents_empty(t *testing.T) {
	e := New()
	if evs := e.Events(); len(evs) != 0 {
		t.Fatalf("expected empty, got %v", evs)
	}
}

func TestEvents_returnsRegisteredPatterns(t *testing.T) {
	e := New()
	On(e, func(_ context.Context, _ Event, _ any) {}, "a", "b")
	On(e, func(_ context.Context, _ Event, _ any) {}, "c")

	got := eventsStrings(e.Events())
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestEvents_removedAfterOff(t *testing.T) {
	e := New()
	off := On(e, func(_ context.Context, _ Event, _ any) {}, "x")
	off()
	if evs := e.Events(); len(evs) != 0 {
		t.Fatalf("expected empty after off, got %v", evs)
	}
}

func TestOff_exact(t *testing.T) {
	e := New()
	On(e, func(_ context.Context, _ Event, _ any) {}, "foo")
	e.Off("foo")
	if n := e.Len("foo"); n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestOff_doesNotRemoveUnrelated(t *testing.T) {
	e := New()
	On(e, func(_ context.Context, _ Event, _ any) {}, "foo")
	On(e, func(_ context.Context, _ Event, _ any) {}, "bar")
	e.Off("foo")
	if n := e.Len("bar"); n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}
}

func TestOn_calledOnEmit(t *testing.T) {
	e := New()
	var called int32
	On(e, func(_ context.Context, _ Event, _ string) {
		atomic.AddInt32(&called, 1)
	}, "ev")

	Emit(context.Background(), e, "ev", "hello")
	if atomic.LoadInt32(&called) != 1 {
		t.Fatalf("expected handler called once, got %d", called)
	}
}

func TestOn_multipleEvents(t *testing.T) {
	e := New()
	var count int32
	On(e, func(_ context.Context, _ Event, _ int) {
		atomic.AddInt32(&count, 1)
	}, "a", "b", "c")

	Emit(context.Background(), e, "a", 1)
	Emit(context.Background(), e, "b", 2)
	Emit(context.Background(), e, "c", 3)

	if atomic.LoadInt32(&count) != 3 {
		t.Fatalf("expected 3 calls, got %d", count)
	}
}

func TestOn_offUnsubscribes(t *testing.T) {
	e := New()
	var called int32
	off := On(e, func(_ context.Context, _ Event, _ any) {
		atomic.AddInt32(&called, 1)
	}, "ev")

	off()
	Emit[any](context.Background(), e, "ev", nil)
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called after off()")
	}
}

func TestOn_typeMismatch_notCalled(t *testing.T) {
	e := New()
	var called int32
	On(e, func(_ context.Context, _ Event, _ string) {
		atomic.AddInt32(&called, 1)
	}, "ev")

	// Emit[int], обработчик ожидает string. Не должен вызываться.
	Emit(context.Background(), e, "ev", 42)
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called on type mismatch")
	}
}

func TestOnce_calledOnlyOnce(t *testing.T) {
	e := New()
	var called int32
	Once(e, func(_ context.Context, _ Event, _ string) {
		atomic.AddInt32(&called, 1)
	}, "ev")

	Emit(context.Background(), e, "ev", "x")
	Emit(context.Background(), e, "ev", "x")

	if n := atomic.LoadInt32(&called); n != 1 {
		t.Fatalf("expected 1 call, got %d", n)
	}
}

// Once регистрируется отдельно для каждого события.
// Для "один раз суммарно" используется OnceAny.
func TestOnce_independentPerEvent(t *testing.T) {
	e := New()
	var count int32
	Once(e, func(_ context.Context, _ Event, _ int) { atomic.AddInt32(&count, 1) }, "a")
	Once(e, func(_ context.Context, _ Event, _ int) { atomic.AddInt32(&count, 1) }, "b")

	for i := range 3 {
		Emit(context.Background(), e, "a", i)
		Emit(context.Background(), e, "b", i)
	}

	if n := atomic.LoadInt32(&count); n != 2 {
		t.Fatalf("expected 2 calls (once per event), got %d", n)
	}
}

func TestOnce_offCancelsBeforeFire(t *testing.T) {
	e := New()
	var called int32
	off := Once(e, func(_ context.Context, _ Event, _ any) {
		atomic.AddInt32(&called, 1)
	}, "ev")

	off()
	Emit[any](context.Background(), e, "ev", nil)

	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called after off()")
	}
}

func TestOnce_typeMismatch_removesSubscription(t *testing.T) {
	e := New()
	var called int32
	Once(e, func(_ context.Context, _ Event, _ string) {
		atomic.AddInt32(&called, 1)
	}, "ev")

	// Первый Emit с неверным типом. Подписка должна быть снята, обработчик не вызван.
	Emit(context.Background(), e, "ev", 99)
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called on type mismatch")
	}
	if e.Len("ev") != 0 {
		t.Fatal("subscription must be removed even on type mismatch")
	}

	// Второй Emit с верным типом. Подписки уже нет, обработчик не вызовется.
	Emit(context.Background(), e, "ev", "ok")
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called: subscription was already removed")
	}
}

func TestEmit_passesData(t *testing.T) {
	e := New()
	var got string
	On(e, func(_ context.Context, _ Event, data string) {
		got = data
	}, "ev")
	Emit(context.Background(), e, "ev", "payload")
	if got != "payload" {
		t.Fatalf("expected %q, got %q", "payload", got)
	}
}

func TestEmit_passesEventName(t *testing.T) {
	e := New()
	var gotEvent Event
	On(e, func(_ context.Context, event Event, _ string) {
		gotEvent = event
	}, "my.event")
	Emit(context.Background(), e, "my.event", "x")
	if gotEvent != "my.event" {
		t.Fatalf("expected event 'my.event', got %q", gotEvent)
	}
}

func TestEmit_passesContext(t *testing.T) {
	e := New()
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "val")
	var gotVal any
	On(e, func(ctx context.Context, _ Event, _ string) {
		gotVal = ctx.Value(ctxKey{})
	}, "ev")
	Emit(ctx, e, "ev", "x")
	if gotVal != "val" {
		t.Fatalf("expected context value %q, got %v", "val", gotVal)
	}
}

func TestEmit_noHandlers(t *testing.T) {
	e := New()
	if n := Emit(context.Background(), e, "nobody", "data"); n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestEmit_returnsCallCount(t *testing.T) {
	e := New()
	for range 3 {
		On(e, func(_ context.Context, _ Event, _ struct{}) {}, "ev")
	}
	if n := Emit(context.Background(), e, "ev", struct{}{}); n != 3 {
		t.Fatalf("expected 3, got %d", n)
	}
}

func TestEmit_returnsZeroOnCancelledContext(t *testing.T) {
	e := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменен до Emit

	On(e, func(_ context.Context, _ Event, _ struct{}) {}, "ev")
	On(e, func(_ context.Context, _ Event, _ struct{}) {}, "ev")

	if n := Emit(ctx, e, "ev", struct{}{}); n != 0 {
		t.Fatalf("expected 0 when context pre-cancelled, got %d", n)
	}
}

func TestEmitAsync_calledHandlers(t *testing.T) {
	e := New()
	var count int32
	for range 5 {
		On(e, func(_ context.Context, _ Event, _ struct{}) {
			atomic.AddInt32(&count, 1)
		}, "ev")
	}
	wg := EmitAsync(context.Background(), e, "ev", struct{}{})
	wg.Wait()
	if n := atomic.LoadInt32(&count); n != 5 {
		t.Fatalf("expected 5, got %d", n)
	}
}

func TestEmitAsync_cancelledContext(t *testing.T) {
	e := New()
	var called int32
	On(e, func(_ context.Context, _ Event, _ struct{}) {
		atomic.AddInt32(&called, 1)
	}, "ev")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // контекст уже отменен

	wg := EmitAsync(ctx, e, "ev", struct{}{})
	wg.Wait()
	// Контекст отменен до вызова EmitAsync. Горутины не запускаются, обработчик не вызывается.
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called when context is pre-cancelled")
	}
}

func TestEmitAsync_returnsWaitGroup(t *testing.T) {
	e := New()
	done := make(chan struct{})
	On(e, func(_ context.Context, _ Event, _ struct{}) {
		time.Sleep(10 * time.Millisecond)
	}, "ev")

	wg := EmitAsync(context.Background(), e, "ev", struct{}{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitGroup never completed")
	}
}

func TestOffAll_removesEverything(t *testing.T) {
	e := New()
	On(e, func(_ context.Context, _ Event, _ any) {}, "a", "b")
	On(e, func(_ context.Context, _ Event, _ any) {}, "c")

	e.OffAll()

	if evs := e.Events(); len(evs) != 0 {
		t.Fatalf("expected no events after OffAll, got %v", evs)
	}
}

func TestPanicHandler_receivesValue(t *testing.T) {
	var panicVal any
	var panicEvent Event
	e := New(WithPanicHandler(func(_ context.Context, event Event, err any) {
		panicEvent = event
		panicVal = err
	}))
	On(e, func(_ context.Context, _ Event, _ struct{}) {
		panic(42)
	}, "boom")
	Emit(context.Background(), e, "boom", struct{}{})

	if panicEvent != "boom" {
		t.Fatalf("expected event 'boom', got %q", panicEvent)
	}
	if panicVal != 42 {
		t.Fatalf("expected panic value 42, got %v", panicVal)
	}
}

func TestPanicHandler_multipleHandlers(t *testing.T) {
	var count int32
	e := New(
		WithPanicHandler(
			func(_ context.Context, _ Event, _ any) { atomic.AddInt32(&count, 1) },
			func(_ context.Context, _ Event, _ any) { atomic.AddInt32(&count, 1) },
		),
	)
	On(e, func(_ context.Context, _ Event, _ struct{}) { panic("x") }, "ev")
	Emit(context.Background(), e, "ev", struct{}{})
	if n := atomic.LoadInt32(&count); n != 2 {
		t.Fatalf("expected 2 panic handlers called, got %d", n)
	}
}

func TestOffAll_handlersNotCalled(t *testing.T) {
	e := New()
	var called int32
	On(e, func(_ context.Context, _ Event, _ any) {
		atomic.AddInt32(&called, 1)
	}, "ev")

	e.OffAll()
	Emit[any](context.Background(), e, "ev", nil)

	if n := atomic.LoadInt32(&called); n != 0 {
		t.Fatalf("handler must not be called after OffAll, got %d calls", n)
	}
}

func TestOffAll_emitterRemainsUsable(t *testing.T) {
	e := New()
	On(e, func(_ context.Context, _ Event, _ struct{}) {}, "a")
	e.OffAll()

	var called int32
	On(e, func(_ context.Context, _ Event, _ struct{}) {
		atomic.AddInt32(&called, 1)
	}, "a")
	Emit(context.Background(), e, "a", struct{}{})

	if n := atomic.LoadInt32(&called); n != 1 {
		t.Fatalf("expected 1 call after re-subscribe, got %d", n)
	}
}

// Проверяет, что при конкурентном вызове EmitAsync сразу после Once,
// не происходит паники из-за nil off-функции.
func TestOnce_concurrentEmit_noNilPanic(t *testing.T) {
	for range 1000 {
		e := New()
		var called int32
		Once(e, func(_ context.Context, _ Event, _ string) {
			atomic.AddInt32(&called, 1)
		}, "ev")
		wg := EmitAsync(context.Background(), e, "ev", "x")
		wg.Wait()
	}
}

// Проверяет, что при двух конкурентных EmitAsync на одно событие,
// Once-обработчик вызывается ровно один раз.
func TestOnce_concurrentTwoEmitters_calledOnce(t *testing.T) {
	for range 1000 {
		e := New()
		var count int32
		Once(e, func(_ context.Context, _ Event, _ string) {
			atomic.AddInt32(&count, 1)
		}, "ev")

		wg1 := EmitAsync(context.Background(), e, "ev", "x")
		wg2 := EmitAsync(context.Background(), e, "ev", "x")
		wg1.Wait()
		wg2.Wait()

		if n := atomic.LoadInt32(&count); n != 1 {
			t.Fatalf("expected handler called once, got %d", n)
		}
	}
}

// Проверяет, что off() конкурентно с EmitAsync, не позволяет вызвать обработчик.
func TestOnce_concurrentOff_handlerCalledAtMostOnce(t *testing.T) {
	failures := 0
	for range 1000 {
		e := New()
		var count int32
		off := Once(e, func(_ context.Context, _ Event, _ string) {
			atomic.AddInt32(&count, 1)
		}, "ev")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); off() }()
		go func() { defer wg.Done(); EmitAsync(context.Background(), e, "ev", "x").Wait() }()
		wg.Wait()

		if n := atomic.LoadInt32(&count); n > 1 {
			failures++
		}
	}
	if failures > 0 {
		t.Fatalf("handler called more than once in %d/1000 runs", failures)
	}
}

func TestStderr_defaultPanicHandler(t *testing.T) {
	e := New()
	On(e, func(_ context.Context, _ Event, _ struct{}) {
		panic("boom")
	}, "ev")

	out := captureStderr(func() {
		Emit(context.Background(), e, "ev", struct{}{})
	})

	if !containsAll(out, `"ev"`, "boom", "panic in handler") {
		t.Fatalf("unexpected stderr output: %q", out)
	}
}

func TestStderr_panicInsidePanicHandler(t *testing.T) {
	e := New(WithPanicHandler(func(_ context.Context, _ Event, _ any) {
		panic("handler itself panics")
	}))
	On(e, func(_ context.Context, _ Event, _ struct{}) {
		panic("original")
	}, "ev")

	out := captureStderr(func() {
		Emit(context.Background(), e, "ev", struct{}{})
	})

	if !containsAll(out, `"ev"`, "handler itself panics", "panic in panic handler") {
		t.Fatalf("unexpected stderr output: %q", out)
	}
}

func TestStderr_defaultMaxListenersWarn(t *testing.T) {
	e := New(WithMaxListeners(2))

	out := captureStderr(func() {
		// 4 подписчика: предупреждение при каждом добавлении сверх лимита (3-й и 4-й).
		for range 4 {
			On(e, func(_ context.Context, _ Event, _ any) {}, "ev")
		}
	})

	if !containsAll(out, `"ev"`, "memory leak", "3") {
		t.Fatalf("unexpected stderr output: %q", out)
	}
	// Предупреждение должно появиться дважды (при 3 и 4 подписчике).
	if strings.Count(out, "memory leak") != 2 {
		t.Fatalf("expected 2 warnings, got: %q", out)
	}
}

func TestOnceAny_calledOnlyOnce_firstEvent(t *testing.T) {
	e := New()
	var count int32
	var gotEvent Event
	OnceAny(e, func(_ context.Context, event Event, _ string) {
		atomic.AddInt32(&count, 1)
		gotEvent = event
	}, "a", "b", "c")

	Emit(context.Background(), e, "b", "hello")
	Emit(context.Background(), e, "a", "hello")
	Emit(context.Background(), e, "c", "hello")

	if n := atomic.LoadInt32(&count); n != 1 {
		t.Fatalf("expected handler called once, got %d", n)
	}
	if gotEvent != "b" {
		t.Fatalf("expected first fired event 'b', got %q", gotEvent)
	}
}

func TestOnceAny_removesAllSubscriptions(t *testing.T) {
	e := New()
	OnceAny(e, func(_ context.Context, _ Event, _ string) {}, "a", "b")

	Emit(context.Background(), e, "a", "x")

	if n := e.Len("a"); n != 0 {
		t.Fatalf("expected 0 listeners for 'a', got %d", n)
	}
	if n := e.Len("b"); n != 0 {
		t.Fatalf("expected 0 listeners for 'b', got %d", n)
	}
}

func TestOnceAny_typeMismatch_removesAllSubscriptions(t *testing.T) {
	e := New()
	var called int32
	OnceAny(e, func(_ context.Context, _ Event, _ string) {
		atomic.AddInt32(&called, 1)
	}, "a", "b")

	// Тип не совпадает, обработчик не вызван, но все подписки сняты.
	Emit(context.Background(), e, "a", 42)

	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called on type mismatch")
	}
	if e.Len("a") != 0 || e.Len("b") != 0 {
		t.Fatal("all subscriptions must be removed even on type mismatch")
	}

	// Повторный emit с верным типом, а подписок уже нет.
	Emit(context.Background(), e, "b", "ok")
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called: all subscriptions were already removed")
	}
}

func TestOnceAny_offCancelsAll(t *testing.T) {
	e := New()
	var called int32
	off := OnceAny(e, func(_ context.Context, _ Event, _ string) {
		atomic.AddInt32(&called, 1)
	}, "a", "b")

	off()

	Emit(context.Background(), e, "a", "x")
	Emit(context.Background(), e, "b", "x")

	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler must not be called after off()")
	}
	if e.Len("a") != 0 || e.Len("b") != 0 {
		t.Fatal("all subscriptions must be removed after off()")
	}
}

func TestOnceAny_concurrentEmit_calledOnce(t *testing.T) {
	for range 1000 {
		e := New()
		var count int32
		OnceAny(e, func(_ context.Context, _ Event, _ string) {
			atomic.AddInt32(&count, 1)
		}, "a", "b")

		var wg1, wg2 *sync.WaitGroup
		wg1 = EmitAsync(context.Background(), e, "a", "x")
		wg2 = EmitAsync(context.Background(), e, "b", "x")
		wg1.Wait()
		wg2.Wait()

		if n := atomic.LoadInt32(&count); n != 1 {
			t.Fatalf("expected handler called once, got %d", n)
		}
	}
}
