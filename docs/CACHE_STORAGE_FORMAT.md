# Cache storage and recovery

The cache allows lost entries after a crash. It never promises database durability or recovery of an active session. Startup logs the recovery outcome (`clean` or `cold`), checkpoint version and elapsed time.

## Formats and compatibility

- The original data-area offset is unchanged. The existing 24-byte stripe-header reservation holds the header magic, marker version **2**, and a random 16-byte session identity. No existing data file is deleted or truncated during startup or upgrade.
- Document version **3** retains the 72-byte header layout. IEEE CRC32 covers the serialized header with its checksum field zeroed, followed by metadata and body. Full logical-key digests, first-key digest, writer identity, fragment position and lengths are checked independently. A non-head fragment records its logical end offset in `TotalLen`; the head records final object length.
- Checkpoint version **1** is stored at `<data path>.<Skip>.checkpoint`. One checksummed envelope contains stripe geometry, write position, phase, session identity, directory bytes, and full logical keys plus purge selectors. Directory fields are fixed little-endian; the envelope and string lengths are big-endian. Keys and selectors are limited to 16 KiB each, mappings to 65,536 entries or directory capacity (whichever is smaller), and their combined strings to 16 MiB. Encoding adds at most eight bytes per entry.
- Missing, dirty, torn, incompatible or mismatched checkpoints are cold misses. Legacy in-file indexes never establish a hit. Recovery does not scan raw old data for objects or migrate it online. Existing bytes remain until ordinary ring writes reuse space.

## Recovery and publication order

1. Obtain exclusive ownership of the underlying inode. A process may open non-overlapping split stripes in that inode; overlapping ranges and another process are rejected. The retained file lock survives closing one split stripe and is released after the last stripe closes. Separate processes cannot share even disjoint portions of one file.
2. Recover only a clean envelope matching the current in-file session identity and configured geometry. Validate lengths before allocation and directory/free-list indexes and cycles before traversal. Check every fragment and metadata block of each mapped object. Rebuild the live directory and key mapping only from complete verified objects; invalid objects become misses.
3. Before `Open` exposes access, write and sync a fresh random session identity into the in-file marker. This invalidates historical checkpoints even when another path or hardlink names the same file. Failure fails opening. Readers may cause evacuation, so read-only sessions also perform this step.
4. Running-session `Save` syncs data and publishes a dirty checkpoint. It cannot make the current session recoverable.
5. `Close` fences new work and PURGE, wakes/aborts active writers, stops evacuation and drains disk writes. It snapshots directory plus key mapping under the publication lock, syncs data, writes and syncs a temporary clean checkpoint, renames it atomically, then syncs its parent directory. Only this complete clean checkpoint can restore hits.

At open, the stripe and aggregate writer use the same process-local allocation generation, preserving the recovered phase and a representable preceding generation. This lets previous-phase objects remain eligible for invalidation, metadata refresh and evacuation after clean reopen; it does not change the checkpoint format.

Disk reads and evacuation allocate by bounded fragment length, never whole-object `TotalLen`. Readers retain already verified fragment bytes; an overwritten, replaced, corrupt or missing later fragment returns an error. Evacuation rechecks exact directory ownership, allocation generation and the PURGE generation before publication. Repeated reader registrations for the same disk offset are coalesced.

## Verified boundaries

Tests use temporary files and killed subprocesses. They cover valid graceful reopen, data/metadata/header/chain corruption, hostile lengths and directory cycles, corrupt/missing mappings, interrupted data writes and publication, PURGE followed by hard exit with or without `Save`, hardlink aliases, and inode ownership across split-stripe closes. These checks exercise process crashes and filesystem errors, not power loss on physical hardware or an adversary deliberately forging checksums.
