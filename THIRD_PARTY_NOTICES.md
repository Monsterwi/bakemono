# Third party notices

## S3-FIFO RAM policy

`cache/ram_s3fifo.go` is a Go adaptation of the queue/eviction design in
Apache Traffic Server 10.2.0 `src/iocore/cache/RamCacheS3FIFO.cc`, fixed at
[`b1df7334b39a878971c376da849e8da4984918b6`](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/iocore/cache/RamCacheS3FIFO.cc).
The original is licensed under Apache License 2.0 (see [LICENSE](LICENSE)).
The Go adaptation, concurrency, payload accounting, bounds, API and tests were changed for Razor.
No unlicensed SOSP artifact source was copied.

Relevant upstream NOTICE attribution:

> Apache Traffic Server
> Copyright 2014 The Apache Software Foundation
>
> This product includes software developed at The Apache Software Foundation
> (http://www.apache.org/).
>
> S3-FIFO: A simple, scalable FIFO-based algorithm with three static queues
> Copyright 2023, Carnegie Mellon University (Apache-2.0)
>
> Website: https://s3fifo.com/
> Paper: http://dx.doi.org/10.1145/3600006.3613147

Dependency notices remain with their respective modules in `go.mod` and `go.sum`.
