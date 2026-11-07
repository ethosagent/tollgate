package chain

import (
	"errors"
	"testing"
)

func TestGuard(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name      string
		fn        func() (int, error)
		want      int
		wantErr   error
		wantPanic bool
	}{
		{"value passes through", func() (int, error) { return 7, nil }, 7, nil, false},
		{"error passes through", func() (int, error) { return 1, boom }, 1, boom, false},
		{"panic becomes ErrPanic and zero value", func() (int, error) { panic("x") }, 0, ErrPanic, true},
		{"nil panic is still caught", func() (int, error) { panic(nil) }, 0, ErrPanic, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Guard(tt.fn)
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
