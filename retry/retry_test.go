package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errBoom = errors.New("boom")

// recorder подменяет ожидание, чтобы проверки были детерминированными:
// тесты на таймингах по настенным часам флакают в CI.
type recorder struct{ waits []time.Duration }

func (r *recorder) sleep(ctx context.Context, d time.Duration) error {
	r.waits = append(r.waits, d)
	return ctx.Err()
}

// Без опций поведение обязано совпадать с прежним DoWithTries: N попыток
// с фиксированной паузой. Миграция девяти точек вызова не должна ничего менять.
func TestDo_DefaultsPreserveFixedDelaySemantics(t *testing.T) {
	var r recorder
	calls := 0

	err := Do(context.Background(), func(context.Context) error {
		calls++
		return errBoom
	}, WithAttempts(5), WithDelay(5*time.Second), WithSleeper(r.sleep))

	if calls != 5 {
		t.Errorf("вызовов функции = %d, ожидалось 5", calls)
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("последняя ошибка потеряна: %v", err)
	}
	for i, w := range r.waits {
		if w != 5*time.Second {
			t.Errorf("пауза %d = %v, ожидалось 5s", i, w)
		}
	}
}

// Пауза после ПОСЛЕДНЕЙ попытки бессмысленна: повторять уже нечего.
// В прежней версии она была и стоила лишних 5 секунд на каждом отказе.
func TestDo_DoesNotWaitAfterFinalAttempt(t *testing.T) {
	var r recorder

	_ = Do(context.Background(), func(context.Context) error { return errBoom },
		WithAttempts(3), WithDelay(time.Second), WithSleeper(r.sleep))

	if len(r.waits) != 2 {
		t.Errorf("пауз = %d, ожидалось 2 (между тремя попытками, не после последней)", len(r.waits))
	}
}

// attempts <= 0 в прежней версии возвращал nil, ни разу не вызвав функцию:
// молчаливый "успех" без работы. Вызывающий просил выполнить — выполняем однажды.
func TestDo_NonPositiveAttemptsStillRunsOnce(t *testing.T) {
	for _, n := range []int{0, -3} {
		calls := 0
		err := Do(context.Background(), func(context.Context) error {
			calls++
			return nil
		}, WithAttempts(n))

		if calls != 1 {
			t.Errorf("attempts=%d: вызовов = %d, ожидался 1", n, calls)
		}
		if err != nil {
			t.Errorf("attempts=%d: неожиданная ошибка %v", n, err)
		}
	}
}

func TestDo_SucceedsWithoutRetrying(t *testing.T) {
	var r recorder
	calls := 0

	err := Do(context.Background(), func(context.Context) error {
		calls++
		return nil
	}, WithAttempts(5), WithDelay(time.Second), WithSleeper(r.sleep))

	if err != nil || calls != 1 || len(r.waits) != 0 {
		t.Errorf("успех с первой попытки: err=%v calls=%d waits=%d", err, calls, len(r.waits))
	}
}

// Прежняя версия использовала time.Sleep, который не прерывается: остановка
// сервиса игнорировалась до 25 секунд.
func TestDo_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	err := Do(ctx, func(context.Context) error {
		calls++
		cancel() // отменяем во время работы, ожидание должно прерваться
		return errBoom
	}, WithAttempts(5), WithDelay(time.Hour))

	if calls != 1 {
		t.Errorf("вызовов = %d, ожидался 1: после отмены повторять нельзя", calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("отмена не отражена в ошибке: %v", err)
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("исходная ошибка потеряна: %v", err)
	}
}

// Бэкофф ТОЛЬКО по явной просьбе: в awake есть вызов с нулевой задержкой,
// которому рост пауз молча сменил бы поведение.
func TestDo_BackoffIsOptInAndGrows(t *testing.T) {
	var off recorder
	_ = Do(context.Background(), func(context.Context) error { return errBoom },
		WithAttempts(4), WithDelay(time.Second), WithSleeper(off.sleep))
	for i, w := range off.waits {
		if w != time.Second {
			t.Fatalf("без WithBackoff пауза %d = %v, ожидалось постоянное 1s", i, w)
		}
	}

	var on recorder
	_ = Do(context.Background(), func(context.Context) error { return errBoom },
		WithAttempts(4), WithDelay(time.Second), WithBackoff(2, time.Minute), WithSleeper(on.sleep))
	for i := 1; i < len(on.waits); i++ {
		if on.waits[i] <= on.waits[i-1] {
			t.Fatalf("с WithBackoff паузы не растут: %v", on.waits)
		}
	}
}

func TestDo_BackoffRespectsCeiling(t *testing.T) {
	var r recorder
	_ = Do(context.Background(), func(context.Context) error { return errBoom },
		WithAttempts(8), WithDelay(time.Second), WithBackoff(10, 3*time.Second), WithSleeper(r.sleep))

	for i, w := range r.waits {
		if w > 3*time.Second {
			t.Errorf("пауза %d = %v превышает потолок 3s", i, w)
		}
	}
}

// Единственное расхождение между семействами проектов — логирование попыток.
// Инъекция колбэком удовлетворяет обе стороны и не тащит зависимость.
func TestDo_InvokesLoggerPerFailedAttempt(t *testing.T) {
	var seen []int
	var r recorder

	_ = Do(context.Background(), func(context.Context) error { return errBoom },
		WithAttempts(3), WithSleeper(r.sleep),
		WithLogger(func(attempt int, err error) {
			if !errors.Is(err, errBoom) {
				t.Errorf("логгеру передана чужая ошибка: %v", err)
			}
			seen = append(seen, attempt)
		}))

	want := []int{1, 2, 3}
	if len(seen) != len(want) {
		t.Fatalf("вызовов логгера = %v, ожидалось %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("номера попыток = %v, ожидалось %v", seen, want)
		}
	}
}

// Неверный пароль ретраить бессмысленно, холодный старт базы — наоборот нужно.
func TestDo_StopsOnNonRetryableError(t *testing.T) {
	fatal := errors.New("bad password")
	calls := 0

	err := Do(context.Background(), func(context.Context) error {
		calls++
		return fatal
	}, WithAttempts(5), WithRetryIf(func(e error) bool { return !errors.Is(e, fatal) }))

	if calls != 1 {
		t.Errorf("вызовов = %d, ожидался 1: ошибка не подлежит повтору", calls)
	}
	if !errors.Is(err, fatal) {
		t.Errorf("ошибка потеряна: %v", err)
	}
}

// Операция должна получать контекст, по которому её отменяют.
func TestDo_PassesContextToOperation(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "ok")

	err := Do(ctx, func(c context.Context) error {
		if c.Value(key{}) != "ok" {
			t.Error("в операцию пришёл не тот контекст")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
}
