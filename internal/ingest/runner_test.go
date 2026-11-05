package ingest

import "testing"

func TestResolveStart(t *testing.T) {
	cases := []struct {
		expr    string
		want    uint32
		wantErr bool
	}{
		{"", 1000, false},
		{"latest", 1000, false},
		{"oldest", 500, false},
		{"-100", 900, false},
		{"750", 750, false},
		{"100", 0, true}, // before retention
		{"-600", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := ResolveStart(c.expr, 500, 1000)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%q: got %d, %v", c.expr, got, err)
		}
	}
}
