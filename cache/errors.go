package cache

import "errors"

var ErrReadFromDisk = errors.New("read from disk")

// ErrNotResident means a requested fragment is absent from the directory or its ring allocation has been overwritten.
var ErrNotResident = errors.New("cache fragment not resident")

// ErrWriteInProgress means no committed reader can open while this key is being written.
var ErrWriteInProgress = errors.New("cache write in progress")
