package cache

import (
	"reflect"
	"strings"
	"testing"
)

func TestIndexRestoresDistinctKeysAndSelectorsWithinBounds(t *testing.T) {
	index := NewDirTrie(2)
	if err := index.SetIdentity([]byte("variant-a"), []byte("https://host/a")); err != nil {
		t.Fatal(err)
	}
	if err := index.SetIdentity([]byte("variant-b"), []byte("https://host/a")); err != nil {
		t.Fatal(err)
	}
	if got := index.KeysForSelector([]byte("https://host/a")); !reflect.DeepEqual(got, []string{"variant-a", "variant-b"}) {
		t.Fatalf("selector keys = %v", got)
	}
	restored := NewDirTrie(2)
	if err := restored.Restore(index.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if got := restored.Get([]byte("https://host/")); !reflect.DeepEqual(got, []string{"variant-a", "variant-b"}) {
		t.Fatalf("prefix keys = %v", got)
	}
	if err := restored.SetIdentity([]byte("variant-c"), []byte("https://host/b")); err == nil {
		t.Fatal("accepted entry beyond capacity")
	}
	if err := restored.Restore([]IndexEntry{{Key: strings.Repeat("x", MaxIndexKeyBytes+1), Selector: "s"}}); err == nil {
		t.Fatal("accepted oversized key")
	}
	if !reflect.DeepEqual(index.Snapshot(), restored.Snapshot()) {
		t.Fatal("failed restore changed existing entries")
	}
	restored.Delete([]byte("variant-a"))
	if err := restored.SetIdentity([]byte("variant-c"), []byte("https://host/b")); err != nil {
		t.Fatal(err)
	}
}
