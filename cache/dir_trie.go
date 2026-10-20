package cache

import "github.com/derekparker/trie"

type DirTrie struct {
	trie *trie.Trie
}

func NewDirTrie() *DirTrie {
	return &DirTrie{
		trie: trie.New(),
	}
}

func (dt *DirTrie) Set(key []byte) {
	dt.trie.Add(string(key), nil)
}

func (dt *DirTrie) Get(prefix []byte) []string {
	return dt.trie.PrefixSearch(string(prefix))
}

func (dt *DirTrie) Delete(key []byte) {
	dt.trie.Remove(string(key))
}
