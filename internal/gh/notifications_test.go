package gh

import "testing"

func TestNumberFromAPIURL(t *testing.T) {
	cases := map[string]int{
		"https://api.github.com/repos/o/r/pulls/29":  29,
		"https://api.github.com/repos/o/r/issues/327": 327,
		"https://api.github.com/repos/o/r/releases/8": 8,
		"":                          0,
		"https://api.github.com/o/r": 0, // no numeric tail
		"not a url":                 0,
	}
	for url, want := range cases {
		if got := numberFromAPIURL(url); got != want {
			t.Errorf("numberFromAPIURL(%q) = %d, want %d", url, got, want)
		}
	}
}
