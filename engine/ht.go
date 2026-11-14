package engine

import (
	"encoding/binary"
	"hash"
	"hash/fnv"

	"github.com/bits-and-blooms/bloom/v3"
)

const (
	pointerBits = 48
	pointerMask = (uint64(1) << pointerBits) - 1
	saltShift   = pointerBits
)

// Key separates a join key's semantic equality from its hash value. Equal is
// required because different keys may have the same hash.
type Key interface {
	WriteHash(hash.Hash)
	Equal(Key) bool
}

// IntKey is the current key implementation for integer CSV columns.
type IntKey int64

func (key IntKey) WriteHash(writer hash.Hash) {
	var bytes [8]byte
	binary.LittleEndian.PutUint64(bytes[:], uint64(key))
	_, _ = writer.Write(bytes[:])
}

func (key IntKey) Equal(other Key) bool {
	otherKey, ok := other.(IntKey)
	return ok && key == otherKey
}

// BucketMetadata occupies exactly 64 bits:
//
//	[ 16-bit Bloom filter salt | 48-bit pointer to the first row ]
//
// The pointer is a one-based index into HashTable.nodes, rather than a raw Go
// address. Zero means the bucket is empty. This retains a compact 48-bit
// physical layout without using unsafe pointers.
type BucketMetadata uint64

func newBucketMetadata(salt uint16, head uint64) BucketMetadata {
	if head > pointerMask {
		panic("hash-table row arena exceeds 48-bit address space")
	}
	return BucketMetadata(uint64(salt)<<saltShift | head)
}

func (metadata BucketMetadata) Salt() uint16 {
	return uint16(uint64(metadata) >> saltShift)
}

// mayContainSalt rejects keys whose Bloom bits are absent. A positive result
// still requires a full hash and key equality check.
func (metadata BucketMetadata) mayContainSalt(salt uint16) bool {
	return metadata.Salt()&salt == salt
}

func (metadata BucketMetadata) Head() uint64 {
	return uint64(metadata) & pointerMask
}

type hashEntry struct {
	hash uint64
	key  Key
	row  Row
	next uint64 // one-based index into HashTable.nodes; zero ends the duplicate chain
}

// HashTable is a linear-chained hash table. Linear probing resolves collisions
// between different keys. Each occupied bucket contains exactly one distinct
// key, and its 48-bit head points to a linked list of rows with that key.
type HashTable struct {
	buckets []BucketMetadata
	nodes   []hashEntry
	used    int // occupied buckets, not row count
}

func NewHashTable(capacity int) *HashTable {
	if capacity < 1 {
		capacity = 1
	}
	return &HashTable{buckets: make([]BucketMetadata, capacity)}
}

func (table *HashTable) Insert(key Key, row Row) {
	hash := hashKey(key)
	salt := saltFromHash(hash)

retry:
	for probes := 0; probes < len(table.buckets); probes++ {
		index := table.bucketIndex(hash, probes)
		metadata := table.buckets[index]
		head := metadata.Head()
		if head == 0 {
			if table.wouldExceedLoadFactor() {
				table.grow()
				goto retry
			}
			table.buckets[index] = newBucketMetadata(salt, table.appendNode(hash, key, row, 0))
			table.used++
			return
		}
		if !metadata.mayContainSalt(salt) {
			continue
		}
		first := table.node(head)
		if first.hash == hash && first.key.Equal(key) {
			table.buckets[index] = newBucketMetadata(salt, table.appendNode(hash, key, row, head))
			return
		}
	}
	table.grow()
	goto retry
}

func (table *HashTable) Lookup(key Key) []Row {
	hash := hashKey(key)
	salt := saltFromHash(hash)
	for probes := 0; probes < len(table.buckets); probes++ {
		metadata := table.buckets[table.bucketIndex(hash, probes)]
		head := metadata.Head()
		if head == 0 {
			return nil
		}
		if !metadata.mayContainSalt(salt) {
			continue
		}
		first := table.node(head)
		if first.hash != hash || !first.key.Equal(key) {
			continue
		}

		matches := make([]Row, 0)
		for current := head; current != 0; current = table.node(current).next {
			matches = append(matches, table.node(current).row)
		}
		return matches
	}
	return nil
}

func (table *HashTable) appendNode(hash uint64, key Key, row Row, next uint64) uint64 {
	address := uint64(len(table.nodes)) + 1
	if address > pointerMask {
		panic("hash-table row arena exceeds 48-bit address space")
	}
	table.nodes = append(table.nodes, hashEntry{hash: hash, key: key, row: row, next: next})
	return address
}

func (table *HashTable) node(address uint64) *hashEntry {
	return &table.nodes[address-1]
}

func (table *HashTable) bucketIndex(hash uint64, probes int) int {
	return int((hash + uint64(probes)) % uint64(len(table.buckets)))
}

func (table *HashTable) wouldExceedLoadFactor() bool {
	return (table.used+1)*10 > len(table.buckets)*7
}

func (table *HashTable) grow() {
	oldBuckets := table.buckets
	table.buckets = make([]BucketMetadata, len(oldBuckets)*2)
	for _, metadata := range oldBuckets {
		head := metadata.Head()
		if head == 0 {
			continue
		}
		first := table.node(head)
		for probes := 0; ; probes++ {
			index := table.bucketIndex(first.hash, probes)
			if table.buckets[index].Head() == 0 {
				table.buckets[index] = newBucketMetadata(saltFromHash(first.hash), head)
				break
			}
		}
	}
}

func hashKey(key Key) uint64 {
	hasher := fnv.New64a()
	key.WriteHash(hasher)
	return hasher.Sum64()
}

func saltFromHash(hash uint64) uint16 {
	var data [8]byte
	binary.LittleEndian.PutUint64(data[:], hash)
	// Use one backing word but restrict the filter to the 16 salt bits. Two
	// hash locations keep the single-key filter sparse without extra metadata.
	var word [1]uint64
	filter := bloom.FromWithM(word[:], 16, 2)
	filter.Add(data[:])
	return uint16(word[0])
}
