package chain

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestGuard(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name      string
		fn        func(context.Context) (int, error)
		want      int
		wantErr   error
		wantPanic bool
	}{
		{"value passes through", func(context.Context) (int, error) { return 7, nil }, 7, nil, false},
		{"error passes through", func(context.Context) (int, error) { return 1, boom }, 1, boom, false},
		{"panic becomes ErrPanic and zero value", func(context.Context) (int, error) { panic("x") }, 0, ErrPanic, true},
		{"nil panic is still caught", func(context.Context) (int, error) { panic(nil) }, 0, ErrPanic, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Guard(context.Background(), tt.fn)
			if got != tt.want || !errors.Is(err, tt.wantErr) || IsPanic(err) != tt.wantPanic {
				t.Fatalf("got %d, %v", got, err)
			}
		})
	}
}

func TestErrPanic_Is500(t *testing.T) {
	var se interface{ HTTPStatus() int }
	if !errors.As(ErrPanic, &se) || se.HTTPStatus() != 500 || ErrPanic.Error() == "" {
		t.Fatal("ErrPanic must be a 500 StatusError")
	}
}

func TestErrDeadline_Is503(t *testing.T) {
	var se interface{ HTTPStatus() int }
	if !errors.As(ErrDeadline, &se) || se.HTTPStatus() != 503 || ErrDeadline.Error() == "" {
		t.Fatal("ErrDeadline must be a 503 StatusError")
	}
}

func TestWithinLate_HandsALateResultToLate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []int
		late := func(v int, _ error) { got = append(got, v) }
		v, err := WithinLate(context.Background(), time.Second, func(context.Context) (int, error) { return 1, nil }, late)
		if v != 1 || err != nil {
			t.Fatalf("in time: v=%d err=%v", v, err)
		}
		_, err = WithinLate(context.Background(), time.Second, func(ctx context.Context) (int, error) {
			<-ctx.Done()
			time.Sleep(time.Second) // ignores its deadline, then returns a resource
			return 2, nil
		}, late)
		if !errors.Is(err, ErrDeadline) {
			t.Fatalf("err = %v, want ErrDeadline", err)
		}
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if len(got) != 1 || got[0] != 2 {
			t.Fatalf("late got %v, want only the late result 2", got)
		}
	})
}

func TestWithin_ErrorCausedByItsOwnDeadlineIsErrDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, err := Within(context.Background(), time.Second, func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err() // honours ctx and reports it: races the select
		})
		if !errors.Is(err, ErrDeadline) {
			t.Fatalf("err = %v, want ErrDeadline", err)
		}
	})
}
