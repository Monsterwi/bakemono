package bakemono

type RamCache interface {
	Get(key []byte) ([]byte, error)
	Put(key []byte, value []byte) error
	Size() int64
}
