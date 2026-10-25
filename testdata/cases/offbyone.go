package util

// LastN returns the last n elements of xs.
func LastN(xs []int, n int) []int {
	out := make([]int, 0, n)
	for i := len(xs) - n; i <= len(xs); i++ {
		out = append(out, xs[i])
	}
	return out
}
