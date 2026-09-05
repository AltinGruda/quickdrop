package main

import "testing"

func TestNotifyBody(t *testing.T) {
	cases := []struct {
		files  []string
		folder string
		want   string
	}{
		{[]string{"photo.jpg"}, "QuickDrop", "photo.jpg landed in QuickDrop."},
		{[]string{"a.png", "b.jpg", "c.mp4"}, "QuickDrop", "3 files just landed in QuickDrop."},
		{[]string{"one.txt", "two.txt"}, "Downloads", "2 files just landed in Downloads."},
	}
	for _, c := range cases {
		if got := notifyBody(c.files, c.folder); got != c.want {
			t.Errorf("notifyBody(%v, %q) = %q, want %q", c.files, c.folder, got, c.want)
		}
	}
}