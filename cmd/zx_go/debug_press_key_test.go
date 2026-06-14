package main

import (
	"reflect"
	"testing"
)

func TestParsePressKeySpec(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []pressKeyEntry
	}{
		{name: "empty", in: "", want: nil},
		{
			name: "single enter",
			in:   "enter@1500",
			want: []pressKeyEntry{{frame: 1500, row: 6, mask: 0x01, name: "enter"}},
		},
		{
			name: "case insensitive",
			in:   "ENTER@200",
			want: []pressKeyEntry{{frame: 200, row: 6, mask: 0x01, name: "enter"}},
		},
		{
			name: "multiple keys",
			in:   "space@100,a@200,5@300",
			want: []pressKeyEntry{
				{frame: 100, row: 7, mask: 0x01, name: "space"},
				{frame: 200, row: 1, mask: 0x01, name: "a"},
				{frame: 300, row: 3, mask: 0x10, name: "5"},
			},
		},
		{
			name: "row 0 caps shift",
			in:   "caps@10",
			want: []pressKeyEntry{{frame: 10, row: 0, mask: 0x01, name: "caps"}},
		},
		{
			name: "row 0 v key",
			in:   "v@10",
			want: []pressKeyEntry{{frame: 10, row: 0, mask: 0x10, name: "v"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parsePressKeySpec(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parsePressKeySpec(%q)\n got=%+v\nwant=%+v", c.in, got, c.want)
			}
		})
	}
}
