package img

import "testing"

func TestSpilt(t *testing.T) {
	err := Split4("./test.webp", []string{})
	if err != nil {
		panic(err)
	}
}
