package scout

import "testing"

func TestBestSubtitle(t *testing.T) {
	subs := []Subtitle{
		{FileID: "1", Language: "es", Downloads: 900},
		{FileID: "2", Language: "es", Downloads: 10, HashMatch: true},
		{FileID: "3", Language: "en", Downloads: 5000},
		{FileID: "4", Language: "pt-br", Downloads: 1, MachineMade: true},
	}
	if b := BestSubtitle(subs, "es"); b == nil || b.FileID != "2" {
		t.Fatalf("es: want hash match, got %+v", b)
	}
	if b := BestSubtitle(subs, "pt"); b == nil || b.FileID != "4" {
		t.Fatalf("pt: machine translation is still better than nothing, got %+v", b)
	}
	if b := BestSubtitle(subs, "fr"); b != nil {
		t.Fatalf("fr: want nil, got %+v", b)
	}
}
