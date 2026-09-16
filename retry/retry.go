// Package retry повторяет операцию, которая может не удаться с первого раза.
//
// Без опций поведение совпадает с распространённой самописной реализацией:
// N попыток с фиксированной паузой между ними. Бэкофф, логирование и отбор
// повторяемых ошибок включаются явно — так миграция существующего кода
// не меняет поведение молча.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// Operation — повторяемая операция. Получает тот же контекст, что передан в Do.
type Operation func(ctx context.Context) error

// Sleeper ждёт d или возвращает ошибку, если контекст отменён раньше.
// Подменяется в тестах, чтобы проверки не зависели от настенных часов.
type Sleeper func(ctx context.Context, d time.Duration) error

type config struct {
	attempts int
	delay    time.Duration
	factor   float64
	maxDelay time.Duration
	logf     func(attempt int, err error)
	retryIf  func(error) bool
	sleep    Sleeper
}

// Option настраивает поведение Do.
type Option func(*config)

// WithAttempts задаёт максимальное число попыток. Значение меньше единицы
// нормализуется к единице: вызывающий просил выполнить работу, и выполнить
// её один раз ближе к намерению, чем не выполнить вовсе.
func WithAttempts(n int) Option { return func(c *config) { c.attempts = n } }

// WithDelay задаёт паузу между попытками.
func WithDelay(d time.Duration) Option { return func(c *config) { c.delay = d } }

// WithBackoff умножает паузу на factor после каждой неудачи, не превышая max.
// Потолок ограничивает и первую паузу тоже, а не только выросшие.
//
// Значения factor вне области определения — NaN, бесконечности и меньше
// единицы — трактуются как отсутствие роста. Сравнения с NaN всегда ложны,
// поэтому наивная проверка factor < 1 его бы пропустила, а умножение дало бы
// мусор. По умолчанию выключен: пауза постоянна.
func WithBackoff(factor float64, max time.Duration) Option {
	return func(c *config) {
		c.factor = factor
		c.maxDelay = max
	}
}

// WithLogger вызывается после каждой неудачной попытки с её номером (с единицы).
// Передаётся функцией, чтобы пакет не зависел от библиотеки логирования.
func WithLogger(fn func(attempt int, err error)) Option { return func(c *config) { c.logf = fn } }

// WithRetryIf отбирает повторяемые ошибки. Если предикат вернул false,
// Do прекращает попытки немедленно.
func WithRetryIf(pred func(error) bool) Option { return func(c *config) { c.retryIf = pred } }

// WithSleeper подменяет ожидание. Предназначен для тестов.
func WithSleeper(s Sleeper) Option { return func(c *config) { c.sleep = s } }

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Do выполняет op, повторяя её при ошибке.
//
// Возвращает nil при первом успехе. Иначе — последнюю ошибку, обёрнутую так,
// что errors.Is и errors.As продолжают работать. Если ожидание прервано
// отменой контекста, в ошибку вкладывается и причина отмены.
//
// Паузы после последней попытки нет: повторять уже нечего.
func Do(ctx context.Context, op Operation, opts ...Option) error {
	if op == nil {
		return errors.New("retry: операция не задана")
	}

	cfg := config{
		attempts: 3,
		delay:    100 * time.Millisecond,
		factor:   1,
		sleep:    wait,
	}
	for _, apply := range opts {
		apply(&cfg)
	}

	if cfg.attempts < 1 {
		cfg.attempts = 1
	}
	if math.IsNaN(cfg.factor) || math.IsInf(cfg.factor, 0) || cfg.factor < 1 {
		cfg.factor = 1
	}

	var lastErr error

	delay := cfg.delay
	if cfg.maxDelay > 0 && delay > cfg.maxDelay {
		delay = cfg.maxDelay
	}

	for attempt := 1; attempt <= cfg.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("retry: отменено перед попыткой %d: %w", attempt, errors.Join(lastErr, err))
		}

		lastErr = op(ctx)
		if lastErr == nil {
			return nil
		}

		if cfg.logf != nil {
			cfg.logf(attempt, lastErr)
		}

		if cfg.retryIf != nil && !cfg.retryIf(lastErr) {
			return fmt.Errorf("retry: попытка %d прервана неповторяемой ошибкой: %w", attempt, lastErr)
		}

		if attempt == cfg.attempts {
			break
		}

		if err := cfg.sleep(ctx, delay); err != nil {
			// Сбой ожидания и отмена — разные вещи. Подменённый Sleeper может
			// вернуть что угодно, и называть это отменой значит врать в сообщении.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("retry: отменено после %d попыт(ки/ок): %w", attempt, errors.Join(lastErr, ctxErr))
			}

			return fmt.Errorf("retry: ожидание после попытки %d не удалось: %w", attempt, errors.Join(lastErr, err))
		}

		if cfg.factor > 1 {
			// Умножение в float64 может перескочить диапазон Duration и дать
			// отрицательное значение, а отрицательная пауза означает повторы
			// вообще без ожидания.
			if next := float64(delay) * cfg.factor; next >= float64(math.MaxInt64) {
				delay = math.MaxInt64
			} else {
				delay = time.Duration(next)
			}

			if cfg.maxDelay > 0 && delay > cfg.maxDelay {
				delay = cfg.maxDelay
			}
		}
	}

	return fmt.Errorf("retry: все %d попыт(ка/ок) неудачны: %w", cfg.attempts, lastErr)
}
