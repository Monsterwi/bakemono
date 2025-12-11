package cache

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Span represents a physical storage unit (file or raw device).
// Corresponding to Span in ATS.
type Span struct {
	Path         string
	Size         int64 // Configured size in bytes
	Blocks       int64 // Number of 512-byte blocks
	Offset       int64 // Offset in the file/device (usually 0)
	IsRaw        bool  // Is raw device? (Not fully supported in this Go version yet)
	FilePathname bool  // True if it's a regular file, False if raw/dir?
	DiskID       [2]uint64

	// Link to next span if multiple spans share same device?
	// ATS uses linked lists, we'll use slices in Store.
}

// Store manages a collection of Spans.
// Corresponding to Store in ATS.
type Store struct {
	Spans []*Span
	mu    sync.RWMutex
}

// BuildStripeLayout returns paths, sizes, and offsets for creating stripes.
// It aligns with ATS-style multi-volume: a single span may be split into multiple stripes.
// Stripes never cross span boundaries. The number of stripes equals nStripes.
func (s *Store) BuildStripeLayout(nStripes int) ([]string, []int64, []int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if nStripes <= 0 {
		return nil, nil, nil, fmt.Errorf("nStripes must be > 0")
	}

	if len(s.Spans) == 0 {
		return nil, nil, nil, fmt.Errorf("no spans configured")
	}
	if nStripes < len(s.Spans) {
		return nil, nil, nil, fmt.Errorf("nStripes (%d) must be >= number of spans (%d)", nStripes, len(s.Spans))
	}

	// Compute total size
	var total int64
	for _, sp := range s.Spans {
		total += sp.Size
	}
	if total == 0 {
		return nil, nil, nil, fmt.Errorf("total span size is zero")
	}

	// Allocate stripe counts per span proportionally, at least 1 each.
	alloc := make([]int, len(s.Spans))
	sumAlloc := 0
	for i, sp := range s.Spans {
		portion := int((sp.Size * int64(nStripes)) / total) // floor
		if portion == 0 {
			portion = 1
		}
		alloc[i] = portion
		sumAlloc += portion
	}

	// Adjust to match exactly nStripes
	for sumAlloc < nStripes {
		// give extra stripe to the largest span
		maxIdx := 0
		for i := 1; i < len(s.Spans); i++ {
			if s.Spans[i].Size > s.Spans[maxIdx].Size {
				maxIdx = i
			}
		}
		alloc[maxIdx]++
		sumAlloc++
	}
	for sumAlloc > nStripes {
		// remove from a span (prefer spans with alloc > 1; otherwise allow 0 to drop a small span)
		minIdx := -1
		for i := 0; i < len(alloc); i++ {
			if alloc[i] > 1 {
				minIdx = i
				break
			}
		}
		if minIdx == -1 {
			// all are 1; drop one from the smallest span
			smallIdx := 0
			for i := 1; i < len(s.Spans); i++ {
				if s.Spans[i].Size < s.Spans[smallIdx].Size {
					smallIdx = i
				}
			}
			if alloc[smallIdx] > 0 {
				alloc[smallIdx]--
				sumAlloc--
			}
		} else {
			alloc[minIdx]--
			sumAlloc--
		}
	}

	paths := make([]string, 0, nStripes)
	sizes := make([]int64, 0, nStripes)
	offsets := make([]int64, 0, nStripes)

outer:
	for i, sp := range s.Spans {
		stripesForSpan := alloc[i]
		if stripesForSpan <= 0 {
			continue
		}
		base := sp.Size / int64(stripesForSpan)
		rem := sp.Size % int64(stripesForSpan)
		var off int64
		for j := 0; j < stripesForSpan; j++ {
			chunk := base
			if int64(j) < rem {
				chunk++
			}
			if chunk == 0 {
				continue
			}
			paths = append(paths, sp.Path)
			sizes = append(sizes, chunk)
			offsets = append(offsets, off)
			off += chunk
			if len(paths) == nStripes {
				break outer
			}
		}
	}

	// If fewer stripes generated (due to zero-size spans), return error
	if len(paths) < nStripes {
		return nil, nil, nil, fmt.Errorf("insufficient non-zero spans to allocate %d stripes (got %d)", nStripes, len(paths))
	}

	return paths, sizes, offsets, nil
}

// NewStore creates a new Store.
func NewStore() *Store {
	return &Store{
		Spans: make([]*Span, 0),
	}
}

// InitSpan initializes a Span from a path and size.
// It validates the file/device and calculates geometry.
func (s *Store) InitSpan(path string, size int64) (*Span, error) {
	// Resolve absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path %s: %v", path, err)
	}

	// Check file existence and type
	fi, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		// If it doesn't exist, we might create it if it's a file
		// But usually storage.config expects pre-existing paths or directories.
		// For regular files, ATS might create them.
		// Let's assume we can create regular files.
	} else if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %v", absPath, err)
	}

	span := &Span{
		Path: absPath,
		Size: size,
	}

	// Simple logic for now: assumes regular file
	if fi != nil && fi.IsDir() {
		return nil, fmt.Errorf("directories not fully supported yet, specify a file path")
	}

	// If file exists, check size
	if fi != nil {
		if size > 0 && fi.Size() < size {
			// ATS warns and maybe fails or uses what's available.
			// We will warn but allow.
			// In a real implementation, we might extend the file.
		}
		if size <= 0 {
			// Use file size
			span.Size = fi.Size()
		}

		// Get DiskID (Device/Inode) to handle uniqueness/aliasing
		stat := fi.Sys().(*syscall.Stat_t)
		span.DiskID[0] = uint64(stat.Dev)
		span.DiskID[1] = uint64(stat.Ino)
	}

	// Calculate blocks (512 bytes per block)
	// Note: ATS Store.cc uses STORE_BLOCK_SIZE which is often 8192 or 512 depending on context,
	// but Span.blocks usually refers to 512-byte sectors or 8k blocks?
	// ATS: blocks = size / STORE_BLOCK_SIZE. STORE_BLOCK_SIZE is 8192.
	// But Stripe init uses 512-byte blocks logic?
	// Let's stick to bytes for Span.Size and convert when creating Stripes.

	return span, nil
}

// ReadConfig reads storage configuration from a file (storage.config format).
// Format: path [size] [volume=N] [id=string]
// Size can be raw number or with K/M/G/T suffixes.
func (s *Store) ReadConfig(configPath string) error {
	file, err := os.Open(configPath)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		path := fields[0]
		var size int64 = -1

		// Parse optional arguments
		for i := 1; i < len(fields); i++ {
			field := fields[i]
			if strings.Contains(field, "=") {
				// volume= or id=, ignored for now
				continue
			}
			// Parse size
			if size == -1 {
				parsedSize, err := parseSize(field)
				if err != nil {
					return fmt.Errorf("invalid size %s in %s: %v", field, configPath, err)
				}
				size = parsedSize
			}
		}

		span, err := s.InitSpan(path, size)
		if err != nil {
			return err
		}
		s.AddSpan(span)
	}

	return scanner.Err()
}

func (s *Store) AddSpan(span *Span) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Spans = append(s.Spans, span)
}

// parseSize parses a size string like "10G", "100M", "1024".
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return 0, fmt.Errorf("empty size")
	}

	multiplier := int64(1)
	lastChar := s[len(s)-1]
	if lastChar < '0' || lastChar > '9' {
		switch lastChar {
		case 'K', 'k':
			multiplier = 1024
		case 'M', 'm':
			multiplier = 1024 * 1024
		case 'G', 'g':
			multiplier = 1024 * 1024 * 1024
		case 'T', 't':
			multiplier = 1024 * 1024 * 1024 * 1024
		default:
			return 0, fmt.Errorf("unknown suffix %c", lastChar)
		}
		s = s[:len(s)-1]
	}

	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}

	return val * multiplier, nil
}
