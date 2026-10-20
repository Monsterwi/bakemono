package main

import (
	"fmt"
	"io"
	"log"
	rand2 "math/rand"
	"sync"
	"time"

	"github.com/Monsterwi/razor/cache"
)

const LOOP = 50000

func main() {
	// read path from args

	//_ = os.Remove("/tmp/bakemono-pressure-concurrent/cache.db")

	v, err := cache.NewCache(1, []string{"/tmp/bakemono-pressure-concurrent/cache.db"}, []int64{1024 * 512 * 100000})
	if err != nil {
		panic(err)
	}
	if err := v.Init(); err != nil {
		panic(err)
	}
	defer v.Close()

	for i := 0; i < 100000; i++ {
		log.Printf("--------------------------------- start loop #%d", i)
		wg := &sync.WaitGroup{}
		wg.Add(2)

		rw := &RW{
			v:   v,
			key: rand2.Int63n(100000),
			mp:  &sync.Map{},
		}
		go func() {
			rw.WLoop()
			wg.Done()
		}()

		//wg.Wait()

		//wg.Add(1)

		go func() {
			rw.RLoop()
			wg.Done()
		}()

		wg.Wait()
	}
}

type RW struct {
	v   *cache.Cache
	mp  *sync.Map
	key int64
}

func (rw *RW) GetContent(key int64, size int) []byte {
	seed := key + int64(size) // 设置种子
	r := rand2.New(rand2.NewSource(seed))

	// 生成固定长度的随机字节数组
	bytes := make([]byte, size)
	if _, err := r.Read(bytes); err != nil {
		panic(err)
	}

	return bytes
}

func (rw *RW) GetContentSize(key int64, serial int) int {
	seed := key + int64(serial) // 设置种子
	r := rand2.New(rand2.NewSource(seed))

	return r.Intn(511) + 1
	//return 500
}

func (rw *RW) WLoop() {
	t := time.Now()
	randomKey := rw.key

	for i := 0; i < LOOP; i++ {
		randomSize := rw.GetContentSize(randomKey, i)
		randomData := rw.GetContent(randomKey, 1024*randomSize)
		if i%10000 == 0 {
			log.Printf("++ set key-%d-%d", randomKey, i)
		}
		rw.mp.Store(fmt.Sprintf("key-%d-%d", randomKey, i), randomSize)
		writer, err := rw.v.OpenWrite([]byte(fmt.Sprintf("key-%d-%d", randomKey, i)), 0)
		if err != nil {
			panic(err)
		}
		_, err = writer.Write(randomData)
		closeErr := writer.Close()
		if closeErr != nil {
			panic(closeErr)
		}
		if err != nil {
			panic(err)
		}
	}
	log.Printf("set LOOP keys in %s", time.Since(t))
}

func (rw *RW) RLoop() {
	randomKey := rw.key
	counter := make(map[string]int)
	t := time.Now()

	for i := 0; i < LOOP*100; i++ {
		if i%100000 == 0 {
			log.Printf("get 100000 LOOP keys in %s", time.Since(t))
			log.Printf("hit: %d, miss: %d", counter["hit"], counter["miss"])
			counter = make(map[string]int)
			t = time.Now()

			log.Printf("-- get key times : %d", i)
		}

		serial := rand2.Intn(LOOP)

		reader, err := rw.v.OpenRead([]byte(fmt.Sprintf("key-%d-%d", randomKey, serial)))
		if err != nil {
			log.Printf("OpenRead: %v", err)
			counter["miss"]++
			continue
		}
		// OpenRead is lazy; loading the first fragment establishes a cache hit.
		if _, err := reader.LoadHTTPInfo(); err != nil {
			reader.Close()
			counter["miss"]++
			continue
		}
		counter["hit"]++
		randomSize := rw.GetContentSize(randomKey, serial)
		randomData := rw.GetContent(randomKey, 1024*randomSize)

		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			log.Printf("ReadAll err: %v", err)
			continue
		}

		if len(data) != 1024*randomSize {
			log.Printf("data len %v", len(data))
			log.Printf("random len %v", 1024*randomSize)
			//continue
			panic("data length is not 1024*randomSize")
		}
		dataS := string(data)
		randomS := string(randomData)
		if dataS != randomS {
			log.Printf("dataS %s", dataS)
			log.Printf("randomS %s", randomS)
			panic("data is not equal")
		}
	}
	log.Printf("get LOOP keys in %s", time.Since(t))
	log.Printf("hit: %d, miss: %d", counter["hit"], counter["miss"])
}
