package models

import "testing"

func TestWeakestFromJSON(t *testing.T) {
	blob := []byte(`{"models":[
		{"slug":"strong","visibility":"list","priority":1},
		{"slug":"hidden","visibility":"hide","priority":99},
		{"slug":"weak","visibility":"list","priority":23}
	]}`)
	got, err := WeakestFromJSON(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got != "weak" {
		t.Fatalf("got %q, want weak", got)
	}
}
