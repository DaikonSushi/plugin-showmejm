package main

import "testing"

func TestCleanComicID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "plain", in: "114514", want: "114514", ok: true},
		{name: "jm prefix", in: "JM114514", want: "114514", ok: true},
		{name: "lower prefix", in: "jm114514", want: "114514", ok: true},
		{name: "sample placeholder", in: "jm号", ok: false},
		{name: "empty", in: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cleanComicID(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("cleanComicID(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}
