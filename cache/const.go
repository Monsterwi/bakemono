package cache

const (
	SectorSize = 512

	// Dir Data Size Levels (based on ATS 'big' field)
	// big=0: 512 bytes units
	DirDataSizeLv0 = 512
	// big=1: 4KB units
	DirDataSizeLv1 = 4096
	// big=2: 32KB units
	DirDataSizeLv2 = 32768
	// big=3: 256KB units
	DirDataSizeLv3 = 262144

	// Max size a single Dir entry can point to (16MB)
	// 262144 * 64
	DirMaxDataSize = 16 * 1024 * 1024

	ChunkDataSize        = 0 // Placeholder
	MaxKeyLength         = 16
	BlockSize            = 4096
	MagicRazor           = 0x5F129B13
	DirDepth             = 4
	MaxBucketsPerSegment = 16384

	// Target size for cache fragments (e.g., 1MB)
	TargetFragmentSize = 1 * 1024 * 1024
)
