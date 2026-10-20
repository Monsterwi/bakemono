package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	rand2 "math/rand"
	"time"

	"github.com/Monsterwi/razor/cache"
)

const LOOP = 5000

func main() {
	//_ = os.Remove("/tmp/bakemono-pressure/cache.db")

	v, err := cache.NewCache(1, []string{"/tmp/bakemono-pressure/cache.db"}, []int64{1024 * 5120 * 1000})
	if err != nil {
		panic(err)
	}
	if err := v.Init(); err != nil {
		panic(err)
	}
	defer v.Close()

	for i := 0; i < 100000; i++ {
		log.Printf("--------------------------------- start loop #%d", i)
		CacheRWLoop(v)
	}
}

func CacheRWLoop(v *cache.Cache) {
	// pad randomData with random data
	randomKey := rand2.Int63()
	randomSize := rand2.Intn(512)
	randomData := make([]byte, 1024*randomSize)
	rand.Read(randomData)

	t := time.Now()

	for i := 0; i < LOOP; i++ {
		if i%10000 == 0 {
			log.Printf("++ set key-%d-%d", randomKey, i)
		}
		writer, err := v.OpenWrite([]byte(fmt.Sprintf("key-%d-%d", randomKey, i)), 0)
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
		//loop := v.DirCheckNextLoop()
		//if loop {
		//	panic("loop should not be true")
		//}
		//v.DirDebugPrint()

	}
	log.Printf("set LOOP keys in %s", time.Since(t))

	counter := make(map[string]int)
	t = time.Now()
	for i := 0; i < LOOP; i++ {
		if i%10000 == 0 {
			log.Printf("-- get key-%d", i)
		}
		reader, err := v.OpenRead([]byte(fmt.Sprintf("key-%d-%d", randomKey, i)))
		if err != nil {
			panic(err)
		}
		// OpenRead is lazy; loading the first fragment establishes a cache hit.
		if _, err := reader.LoadHTTPInfo(); err != nil {
			reader.Close()
			counter["miss"]++
			continue
		}
		counter["hit"]++

		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			panic(err)
		}

		if len(data) != 1024*randomSize {
			log.Printf("data len %v", len(data))
			log.Printf("random len %v", 1024*randomSize)
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
