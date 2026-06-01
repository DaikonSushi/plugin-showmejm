package main

import (
	"os"
	"testing"
)

func TestLiveJMAPIComicDetail(t *testing.T) {
	if os.Getenv("JM_LIVE_TEST") != "1" {
		t.Skip("set JM_LIVE_TEST=1 to run live JM API test")
	}

	client := NewJMClient(DefaultConfig())
	defer client.Close()

	comic, err := client.fetchComicDetailFromAPI("1044848")
	if err != nil {
		t.Fatalf("fetchComicDetailFromAPI failed: %v", err)
	}
	if comic.ID == "" || comic.Title == "" || comic.Pages == 0 || len(comic.Chapters) == 0 {
		t.Fatalf("unexpected comic detail: %#v", comic)
	}
	if len(comic.Chapters[0].ImageURLs) == 0 || comic.Chapters[0].ScrambleID == "" {
		t.Fatalf("unexpected first chapter: %#v", comic.Chapters[0])
	}
}
