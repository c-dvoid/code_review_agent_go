package mathx

import "errors"

var ErrDivByZero = errors.New("division by zero")

// Div returns a/b, or ErrDivByZero when b is zero.
func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, ErrDivByZero
	}
	return a / b, nil
}
